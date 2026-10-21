package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
)

const (
	appPreviewManifestSchemaVersion = 2
	appPreviewMaximumCommands       = 8
	appPreviewMaximumArguments      = 64
	appPreviewShutdownCleanupWindow = 30 * time.Second
)

var (
	errAppPreviewBusy                = errors.New("app preview is busy")
	errAppPreviewChanged             = errors.New("app preview state changed")
	errAppPreviewCleanupBlocked      = errors.New("app preview cleanup is blocked")
	errAppPreviewConflict            = errors.New("app preview registration conflicts with the existing Artifact")
	errAppPreviewInvalid             = errors.New("app preview specification is invalid")
	errAppPreviewLeaseInvalid        = errors.New("app preview lease is invalid")
	errAppPreviewNotFound            = errors.New("app preview registration was not found")
	errAppPreviewPlatformUnsupported = errors.New("app preview platform is unsupported")
	errAppPreviewPortUnavailable     = errors.New("app preview port is unavailable")
	errAppPreviewRuntimeMismatch     = errors.New("app preview identity does not match")
	errAppPreviewUnavailable         = errors.New("app preview is unavailable")
)

type appPreviewCommand struct {
	Command          string   `json:"command"`
	Args             []string `json:"args,omitempty"`
	WorkingDirectory string   `json:"workingDirectory,omitempty"`
	Port             int      `json:"port"`
}

type appPreviewManifest struct {
	SchemaVersion int                 `json:"schemaVersion"`
	CompanyID     uuid.UUID           `json:"companyId"`
	OutputID      uuid.UUID           `json:"outputId"`
	RunID         uuid.UUID           `json:"runId"`
	RuntimeID     uuid.UUID           `json:"runtimeId"`
	WorkDir       string              `json:"workDir"`
	Commands      []appPreviewCommand `json:"commands"`
	// Hidden are the folders the presenting Run's sandbox hid: a Run tagged
	// in a chat hides its person's private folders, and so does every launch
	// of its app. Empty for every other Run, and in a
	// registration written before this field existed.
	Hidden []string `json:"hidden,omitempty"`
}

type appPreviewLegacyManifest struct {
	SchemaVersion int                 `json:"schemaVersion"`
	OutputID      uuid.UUID           `json:"outputId"`
	RunID         uuid.UUID           `json:"runId"`
	RuntimeID     uuid.UUID           `json:"runtimeId"`
	WorkDir       string              `json:"workDir"`
	Commands      []appPreviewCommand `json:"commands"`
}

type appPreviewIdentity struct {
	CompanyID uuid.UUID
	OutputID  uuid.UUID
	RunID     uuid.UUID
	RuntimeID uuid.UUID
}

func (m appPreviewManifest) identity() appPreviewIdentity {
	return appPreviewIdentity{
		CompanyID: m.CompanyID, OutputID: m.OutputID, RunID: m.RunID, RuntimeID: m.RuntimeID,
	}
}

type appPreviewLifecycle string

const (
	appPreviewLifecycleStopped  appPreviewLifecycle = "stopped"
	appPreviewLifecycleStarting appPreviewLifecycle = "starting"
	appPreviewLifecycleRunning  appPreviewLifecycle = "running"
	appPreviewLifecycleStopping appPreviewLifecycle = "stopping"
	appPreviewLifecycleFailed   appPreviewLifecycle = "failed"
)

type appPreviewAvailability string

const (
	appPreviewAvailabilityChecking    appPreviewAvailability = "checking"
	appPreviewAvailabilityAvailable   appPreviewAvailability = "available"
	appPreviewAvailabilityUnavailable appPreviewAvailability = "unavailable"
)

type appPreviewSnapshot struct {
	CompanyID      uuid.UUID
	OutputID       uuid.UUID
	RunID          uuid.UUID
	RuntimeID      uuid.UUID
	ActivationID   uuid.UUID
	Lifecycle      appPreviewLifecycle
	Availability   appPreviewAvailability
	Reason         string
	URL            string
	Ports          []int
	LastObservedAt time.Time
}

type appPreviewOperationResult struct {
	Snapshot     appPreviewSnapshot
	StopToken    string
	LeaseID      uuid.UUID
	LeaseExpires time.Time
	LeaseTTL     time.Duration
	RenewAfter   time.Duration
}

type appPreviewLease struct {
	expiresAt time.Time
	active    bool
	ensured   bool
}

type appPreviewEntry struct {
	mu sync.Mutex

	manifest  appPreviewManifest
	stopToken string
	leases    map[uuid.UUID]*appPreviewLease

	lifecycle      appPreviewLifecycle
	availability   appPreviewAvailability
	reason         string
	activationID   uuid.UUID
	lastObservedAt time.Time
	graceDeadline  time.Time

	lifecycleRunning   bool
	pendingReplacement bool
	stopRequested      bool
	cleanupBlocked     bool
	cancelAttempt      context.CancelFunc
}

type appPreviewSettings struct {
	leaseTTL               time.Duration
	renewAfter             time.Duration
	gracePeriod            time.Duration
	sweepInterval          time.Duration
	startupDeadline        time.Duration
	healthInterval         time.Duration
	observationMaxAge      time.Duration
	cleanupRetry           time.Duration
	cleanupTimeout         time.Duration
	shutdownCleanupTimeout time.Duration
}

func defaultAppPreviewSettings() appPreviewSettings {
	return appPreviewSettings{
		leaseTTL:               60 * time.Second,
		renewAfter:             5 * time.Second,
		gracePeriod:            5 * time.Minute,
		sweepInterval:          time.Second,
		startupDeadline:        30 * time.Second,
		healthInterval:         5 * time.Second,
		observationMaxAge:      10 * time.Second,
		cleanupRetry:           5 * time.Second,
		cleanupTimeout:         8 * time.Second,
		shutdownCleanupTimeout: appPreviewShutdownCleanupWindow,
	}
}

// appPreviewIsolation decides the sandbox for one launch of a preview's
// commands: the sandbox to start them in, or nil to start
// them without one, and release, which the supervisor calls once every
// process the launch started has stopped. hidden are the folders the
// presenting Run's sandbox hid. A launch without the sandbox holds the
// computer's report at not_enforced until release
// (Daemon.appPreviewIsolation).
type appPreviewIsolation func(workingDirectories, hidden []string) (sandbox *agent.Sandbox, release func())

type appPreviewService struct {
	stateDir         string
	logger           *slog.Logger
	backend          appPreviewBackend
	isolate          appPreviewIsolation
	settings         appPreviewSettings
	ctx              context.Context
	cancel           context.CancelFunc
	cleanupCtx       context.Context
	cancelCleanup    context.CancelFunc
	wg               sync.WaitGroup
	readManifestFile func(string) ([]byte, error)

	mu           sync.Mutex
	closed       bool
	sweepStarted bool
	entries      map[uuid.UUID]*appPreviewEntry
	loading      map[uuid.UUID]chan struct{}
	reservations map[int]uuid.UUID
	closeDone    chan struct{}
	closeErr     error
}

func newAppPreviewService(
	stateDir string,
	logger *slog.Logger,
	isolate appPreviewIsolation,
) (*appPreviewService, error) {
	return newAppPreviewServiceWithBackend(
		stateDir, logger, newPlatformAppPreviewBackend(), isolate, defaultAppPreviewSettings(),
	)
}

func newAppPreviewServiceWithBackend(
	stateDir string,
	logger *slog.Logger,
	backend appPreviewBackend,
	isolate appPreviewIsolation,
	settings appPreviewSettings,
) (*appPreviewService, error) {
	stateDir = filepath.Clean(strings.TrimSpace(stateDir))
	if !filepath.IsAbs(stateDir) || logger == nil || backend == nil || isolate == nil ||
		settings.leaseTTL <= 0 || settings.renewAfter <= 0 || settings.gracePeriod <= 0 ||
		settings.sweepInterval <= 0 || settings.startupDeadline <= 0 ||
		settings.healthInterval <= 0 || settings.cleanupRetry <= 0 || settings.cleanupTimeout <= 0 ||
		settings.shutdownCleanupTimeout <= 0 {
		return nil, errAppPreviewInvalid
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create app preview state directory: %w", err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure app preview state directory: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cleanupCtx, cancelCleanup := context.WithCancel(context.Background())
	service := &appPreviewService{
		stateDir: stateDir, logger: logger, backend: backend, isolate: isolate, settings: settings,
		ctx: ctx, cancel: cancel, cleanupCtx: cleanupCtx, cancelCleanup: cancelCleanup,
		readManifestFile: os.ReadFile, entries: make(map[uuid.UUID]*appPreviewEntry),
		loading: make(map[uuid.UUID]chan struct{}), reservations: make(map[int]uuid.UUID),
		closeDone: make(chan struct{}),
	}
	return service, nil
}

func (s *appPreviewService) Register(manifest appPreviewManifest) error {
	if s == nil {
		return errAppPreviewUnavailable
	}
	normalized, err := normalizeAppPreviewManifest(manifest)
	if err != nil {
		return err
	}
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return errAppPreviewUnavailable
		}
		if entry := s.entries[normalized.OutputID]; entry != nil {
			s.mu.Unlock()
			entry.mu.Lock()
			matches := reflect.DeepEqual(entry.manifest, normalized)
			entry.mu.Unlock()
			if !matches {
				return errAppPreviewConflict
			}
			return nil
		}
		if done := s.loading[normalized.OutputID]; done != nil {
			s.mu.Unlock()
			<-done
			continue
		}
		done := make(chan struct{})
		s.loading[normalized.OutputID] = done
		s.wg.Add(1)
		s.mu.Unlock()

		stored, legacy, loadErr := s.loadStoredManifest(normalized.OutputID)
		switch {
		case loadErr == nil && !reflect.DeepEqual(stored, normalized):
			err = errAppPreviewConflict
		case loadErr == nil:
			normalized = stored
		case legacy != nil && legacyMatchesManifest(*legacy, normalized):
			err = s.writeManifest(normalized)
		case legacy != nil:
			err = errAppPreviewConflict
		case errors.Is(loadErr, errAppPreviewNotFound):
			err = s.writeManifest(normalized)
		default:
			err = loadErr
		}

		s.mu.Lock()
		if err == nil && !s.closed {
			s.entries[normalized.OutputID] = newAppPreviewEntry(normalized)
		} else if err == nil {
			err = errAppPreviewUnavailable
		}
		delete(s.loading, normalized.OutputID)
		close(done)
		s.mu.Unlock()
		s.wg.Done()
		return err
	}
}

func newAppPreviewEntry(manifest appPreviewManifest) *appPreviewEntry {
	return &appPreviewEntry{
		manifest: manifest, stopToken: newAppPreviewToken(),
		leases:    make(map[uuid.UUID]*appPreviewLease),
		lifecycle: appPreviewLifecycleStopped, availability: appPreviewAvailabilityUnavailable,
	}
}

func (s *appPreviewService) Inspect(identity appPreviewIdentity) (appPreviewOperationResult, error) {
	entry, err := s.entry(identity.OutputID)
	if err != nil {
		return appPreviewOperationResult{}, err
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if !entry.matches(identity) {
		return appPreviewOperationResult{}, errAppPreviewRuntimeMismatch
	}
	return s.resultLocked(entry), nil
}

func (s *appPreviewService) Attach(
	stopToken string,
	identity appPreviewIdentity,
) (appPreviewOperationResult, error) {
	entry, err := s.entry(identity.OutputID)
	if err != nil {
		return appPreviewOperationResult{}, err
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if !entry.matches(identity) {
		return s.resultLocked(entry), errAppPreviewRuntimeMismatch
	}
	if stopToken == "" || stopToken != entry.stopToken {
		return s.resultLocked(entry), errAppPreviewChanged
	}
	if err := s.startLeaseSweep(); err != nil {
		return s.resultLocked(entry), err
	}
	leaseID := uuid.New()
	expiresAt := time.Now().Add(s.settings.leaseTTL)
	entry.leases[leaseID] = &appPreviewLease{expiresAt: expiresAt}
	result := s.resultLocked(entry)
	result.LeaseID = leaseID
	result.LeaseExpires = expiresAt
	return result, nil
}

func (s *appPreviewService) Ensure(
	leaseID uuid.UUID,
	identity appPreviewIdentity,
) (appPreviewOperationResult, error) {
	entry, err := s.entry(identity.OutputID)
	if err != nil {
		return appPreviewOperationResult{}, err
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	lease, err := s.liveLeaseLocked(entry, leaseID, identity)
	if err != nil {
		return s.resultLocked(entry), err
	}
	if lease.ensured {
		return s.leaseResultLocked(entry, leaseID, lease), nil
	}
	if entry.cleanupBlocked {
		return s.leaseResultLocked(entry, leaseID, lease), errAppPreviewCleanupBlocked
	}
	if entry.lifecycle == appPreviewLifecycleStopping && !entry.pendingReplacement {
		return s.leaseResultLocked(entry, leaseID, lease), errAppPreviewBusy
	}
	if (entry.lifecycle == appPreviewLifecycleStopped || entry.lifecycle == appPreviewLifecycleFailed) &&
		s.backend.CheckSupported() != nil {
		lease.ensured = true
		lease.active = true
		entry.lifecycle = appPreviewLifecycleFailed
		entry.availability = appPreviewAvailabilityUnavailable
		entry.reason = "app_preview_platform_unsupported"
		return s.leaseResultLocked(entry, leaseID, lease), errAppPreviewPlatformUnsupported
	}
	lease.ensured = true
	lease.active = true
	entry.graceDeadline = time.Time{}
	if entry.lifecycle == appPreviewLifecycleStopped || entry.lifecycle == appPreviewLifecycleFailed {
		if err := s.reservePortsLocked(entry); err != nil {
			entry.lifecycle = appPreviewLifecycleFailed
			entry.availability = appPreviewAvailabilityUnavailable
			entry.reason = "app_preview_port_unavailable"
			return s.leaseResultLocked(entry, leaseID, lease), err
		}
		if err := s.startLifecycleLocked(entry, uuid.New()); err != nil {
			s.releasePortsLocked(entry)
			return s.leaseResultLocked(entry, leaseID, lease), err
		}
	}
	return s.leaseResultLocked(entry, leaseID, lease), nil
}

func (s *appPreviewService) Renew(
	leaseID uuid.UUID,
	identity appPreviewIdentity,
) (appPreviewOperationResult, error) {
	entry, err := s.entry(identity.OutputID)
	if err != nil {
		return appPreviewOperationResult{}, err
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	lease, err := s.liveLeaseLocked(entry, leaseID, identity)
	if err != nil {
		return s.resultLocked(entry), err
	}
	lease.expiresAt = time.Now().Add(s.settings.leaseTTL)
	return s.leaseResultLocked(entry, leaseID, lease), nil
}

func (s *appPreviewService) Release(
	leaseID uuid.UUID,
	identity appPreviewIdentity,
) (appPreviewOperationResult, error) {
	entry, err := s.entry(identity.OutputID)
	if err != nil {
		return appPreviewOperationResult{}, err
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if !entry.matches(identity) {
		return s.resultLocked(entry), errAppPreviewRuntimeMismatch
	}
	lease := entry.leases[leaseID]
	delete(entry.leases, leaseID)
	if lease != nil && lease.active {
		s.setGraceDeadlineLocked(entry, time.Now())
	}
	return s.resultLocked(entry), nil
}

func (s *appPreviewService) Restart(
	leaseID, activationID uuid.UUID,
	identity appPreviewIdentity,
) (appPreviewOperationResult, error) {
	entry, err := s.entry(identity.OutputID)
	if err != nil {
		return appPreviewOperationResult{}, err
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	lease, err := s.liveLeaseLocked(entry, leaseID, identity)
	if err != nil || !lease.active {
		if err == nil {
			err = errAppPreviewLeaseInvalid
		}
		return s.resultLocked(entry), err
	}
	if activationID == uuid.Nil || activationID != entry.activationID {
		return s.leaseResultLocked(entry, leaseID, lease), errAppPreviewChanged
	}
	if entry.cleanupBlocked {
		return s.leaseResultLocked(entry, leaseID, lease), errAppPreviewCleanupBlocked
	}
	if entry.lifecycle == appPreviewLifecycleStopping {
		return s.leaseResultLocked(entry, leaseID, lease), errAppPreviewBusy
	}
	if entry.lifecycle == appPreviewLifecycleFailed {
		if err := s.reservePortsLocked(entry); err != nil {
			return s.leaseResultLocked(entry, leaseID, lease), err
		}
		if err := s.startLifecycleLocked(entry, uuid.New()); err != nil {
			s.releasePortsLocked(entry)
			return s.leaseResultLocked(entry, leaseID, lease), err
		}
		return s.leaseResultLocked(entry, leaseID, lease), nil
	}
	if !entry.lifecycleRunning {
		return s.leaseResultLocked(entry, leaseID, lease), errAppPreviewBusy
	}
	entry.activationID = uuid.New()
	entry.pendingReplacement = true
	entry.stopRequested = false
	entry.lifecycle = appPreviewLifecycleStopping
	entry.availability = appPreviewAvailabilityUnavailable
	entry.reason = ""
	entry.cancelAttempt()
	return s.leaseResultLocked(entry, leaseID, lease), nil
}

func (s *appPreviewService) Stop(
	stopToken string,
	identity appPreviewIdentity,
) (appPreviewOperationResult, error) {
	entry, err := s.entry(identity.OutputID)
	if err != nil {
		return appPreviewOperationResult{}, err
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if !entry.matches(identity) {
		return s.resultLocked(entry), errAppPreviewRuntimeMismatch
	}
	if stopToken == "" || stopToken != entry.stopToken {
		return s.resultLocked(entry), errAppPreviewChanged
	}
	entry.stopToken = newAppPreviewToken()
	clear(entry.leases)
	entry.pendingReplacement = false
	entry.stopRequested = true
	entry.graceDeadline = time.Time{}
	entry.availability = appPreviewAvailabilityUnavailable
	entry.reason = ""
	if entry.lifecycleRunning {
		entry.lifecycle = appPreviewLifecycleStopping
		entry.cancelAttempt()
	} else {
		entry.lifecycle = appPreviewLifecycleStopped
		s.releasePortsLocked(entry)
	}
	return s.resultLocked(entry), nil
}

func (s *appPreviewService) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		done := s.closeDone
		s.mu.Unlock()
		<-done
		s.mu.Lock()
		err := s.closeErr
		s.mu.Unlock()
		return err
	}
	s.closed = true
	entries := make([]*appPreviewEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		entries = append(entries, entry)
	}
	s.mu.Unlock()
	s.cancel()
	cleanupTimer := time.AfterFunc(s.settings.shutdownCleanupTimeout, s.cancelCleanup)
	for _, entry := range entries {
		entry.mu.Lock()
		entry.stopRequested = true
		entry.pendingReplacement = false
		if entry.lifecycleRunning {
			entry.lifecycle = appPreviewLifecycleStopping
			entry.cancelAttempt()
		}
		entry.mu.Unlock()
	}
	s.wg.Wait()
	cleanupTimer.Stop()
	s.cancelCleanup()
	s.mu.Lock()
	close(s.closeDone)
	err := s.closeErr
	s.mu.Unlock()
	return err
}

func (s *appPreviewService) entry(outputID uuid.UUID) (*appPreviewEntry, error) {
	if s == nil || outputID == uuid.Nil {
		return nil, errAppPreviewInvalid
	}
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, errAppPreviewUnavailable
		}
		if entry := s.entries[outputID]; entry != nil {
			s.mu.Unlock()
			return entry, nil
		}
		if done := s.loading[outputID]; done != nil {
			s.mu.Unlock()
			<-done
			continue
		}
		done := make(chan struct{})
		s.loading[outputID] = done
		s.wg.Add(1)
		s.mu.Unlock()
		manifest, legacy, err := s.loadStoredManifest(outputID)
		if legacy != nil {
			err = errAppPreviewUnavailable
		}
		s.mu.Lock()
		if err == nil && !s.closed {
			s.entries[outputID] = newAppPreviewEntry(manifest)
		} else if err == nil {
			err = errAppPreviewUnavailable
		}
		delete(s.loading, outputID)
		close(done)
		entry := s.entries[outputID]
		s.mu.Unlock()
		s.wg.Done()
		if entry != nil {
			return entry, nil
		}
		return nil, err
	}
}

func (s *appPreviewService) startLifecycleLocked(entry *appPreviewEntry, activationID uuid.UUID) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errAppPreviewUnavailable
	}
	ctx, cancel := context.WithCancel(s.ctx)
	entry.cancelAttempt = cancel
	entry.activationID = activationID
	entry.lifecycle = appPreviewLifecycleStarting
	entry.availability = appPreviewAvailabilityChecking
	entry.reason = ""
	entry.lastObservedAt = time.Time{}
	entry.lifecycleRunning = true
	entry.pendingReplacement = false
	entry.stopRequested = false
	entry.cleanupBlocked = false
	s.wg.Add(1)
	s.mu.Unlock()
	go s.runAppPreviewLifecycle(ctx, entry, activationID)
	return nil
}

func (s *appPreviewService) reservePortsLocked(entry *appPreviewEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errAppPreviewUnavailable
	}
	for _, command := range entry.manifest.Commands {
		if owner := s.reservations[command.Port]; owner != uuid.Nil && owner != entry.manifest.OutputID {
			return fmt.Errorf("%w: %d", errAppPreviewPortUnavailable, command.Port)
		}
	}
	for _, command := range entry.manifest.Commands {
		s.reservations[command.Port] = entry.manifest.OutputID
	}
	return nil
}

func (s *appPreviewService) releasePortsLocked(entry *appPreviewEntry) {
	s.mu.Lock()
	for _, command := range entry.manifest.Commands {
		if s.reservations[command.Port] == entry.manifest.OutputID {
			delete(s.reservations, command.Port)
		}
	}
	s.mu.Unlock()
}

func (s *appPreviewService) reservationOwner(port int) uuid.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reservations[port]
}

func (s *appPreviewService) liveLeaseLocked(
	entry *appPreviewEntry,
	leaseID uuid.UUID,
	identity appPreviewIdentity,
) (*appPreviewLease, error) {
	if !entry.matches(identity) {
		return nil, errAppPreviewRuntimeMismatch
	}
	lease := entry.leases[leaseID]
	if lease == nil || !time.Now().Before(lease.expiresAt) {
		if lease != nil {
			delete(entry.leases, leaseID)
			if lease.active {
				s.setGraceDeadlineLocked(entry, lease.expiresAt)
			}
		}
		return nil, errAppPreviewLeaseInvalid
	}
	return lease, nil
}

func (s *appPreviewService) setGraceDeadlineLocked(entry *appPreviewEntry, lostAt time.Time) {
	if entry.activeLeaseCountLocked(time.Now()) != 0 || !entry.graceDeadline.IsZero() {
		return
	}
	entry.graceDeadline = lostAt.Add(s.settings.gracePeriod)
}

func (entry *appPreviewEntry) activeLeaseCountLocked(now time.Time) int {
	count := 0
	for _, lease := range entry.leases {
		if lease.active && now.Before(lease.expiresAt) {
			count++
		}
	}
	return count
}

func (s *appPreviewService) sweepLeases() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.settings.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-ticker.C:
			s.sweepLeaseExpiry(now)
		}
	}
}

func (s *appPreviewService) startLeaseSweep() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errAppPreviewUnavailable
	}
	if s.sweepStarted {
		return nil
	}
	s.sweepStarted = true
	s.wg.Add(1)
	go s.sweepLeases()
	return nil
}

func (s *appPreviewService) sweepLeaseExpiry(now time.Time) {
	s.mu.Lock()
	entries := make([]*appPreviewEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		entries = append(entries, entry)
	}
	s.mu.Unlock()
	for _, entry := range entries {
		entry.mu.Lock()
		var lastActiveExpiry time.Time
		for leaseID, lease := range entry.leases {
			if !now.Before(lease.expiresAt) {
				delete(entry.leases, leaseID)
				if lease.active && lease.expiresAt.After(lastActiveExpiry) {
					lastActiveExpiry = lease.expiresAt
				}
			}
		}
		if !lastActiveExpiry.IsZero() {
			s.setGraceDeadlineLocked(entry, lastActiveExpiry)
		}
		if !entry.graceDeadline.IsZero() && !now.Before(entry.graceDeadline) &&
			entry.activeLeaseCountLocked(now) == 0 {
			entry.graceDeadline = time.Time{}
			entry.stopRequested = true
			entry.pendingReplacement = false
			entry.availability = appPreviewAvailabilityUnavailable
			entry.reason = ""
			if entry.lifecycleRunning {
				entry.lifecycle = appPreviewLifecycleStopping
				entry.cancelAttempt()
			} else {
				entry.lifecycle = appPreviewLifecycleStopped
				s.releasePortsLocked(entry)
			}
		}
		entry.mu.Unlock()
	}
}

func (s *appPreviewService) resultLocked(entry *appPreviewEntry) appPreviewOperationResult {
	return appPreviewOperationResult{
		Snapshot: s.snapshotLocked(entry), StopToken: entry.stopToken,
		LeaseTTL: s.settings.leaseTTL, RenewAfter: s.settings.renewAfter,
	}
}

func (s *appPreviewService) leaseResultLocked(
	entry *appPreviewEntry,
	leaseID uuid.UUID,
	lease *appPreviewLease,
) appPreviewOperationResult {
	result := s.resultLocked(entry)
	result.LeaseID = leaseID
	result.LeaseExpires = lease.expiresAt
	return result
}

func (s *appPreviewService) snapshotLocked(entry *appPreviewEntry) appPreviewSnapshot {
	ports := make([]int, len(entry.manifest.Commands))
	for index, command := range entry.manifest.Commands {
		ports[index] = command.Port
	}
	snapshot := appPreviewSnapshot{
		CompanyID: entry.manifest.CompanyID, OutputID: entry.manifest.OutputID,
		RunID: entry.manifest.RunID, RuntimeID: entry.manifest.RuntimeID,
		ActivationID: entry.activationID, Lifecycle: entry.lifecycle,
		Availability: entry.availability, Reason: entry.reason, Ports: ports,
		LastObservedAt: entry.lastObservedAt,
	}
	if entry.lifecycle == appPreviewLifecycleRunning &&
		entry.availability == appPreviewAvailabilityAvailable {
		if !entry.lastObservedAt.IsZero() &&
			time.Since(entry.lastObservedAt) <= s.settings.observationMaxAge {
			snapshot.URL = fmt.Sprintf("http://127.0.0.1:%d", ports[0])
		} else {
			snapshot.Availability = appPreviewAvailabilityUnavailable
			snapshot.Reason = "app_preview_observation_stale"
		}
	}
	return snapshot
}

func (entry *appPreviewEntry) matches(identity appPreviewIdentity) bool {
	return identity.CompanyID != uuid.Nil && identity.CompanyID == entry.manifest.CompanyID &&
		identity.OutputID == entry.manifest.OutputID && identity.RunID == entry.manifest.RunID &&
		identity.RuntimeID == entry.manifest.RuntimeID
}

func (s *appPreviewService) manifestPath(outputID uuid.UUID) string {
	return filepath.Join(s.stateDir, outputID.String()+".json")
}

func (s *appPreviewService) loadStoredManifest(
	outputID uuid.UUID,
) (appPreviewManifest, *appPreviewLegacyManifest, error) {
	data, err := s.readManifestFile(s.manifestPath(outputID))
	if errors.Is(err, os.ErrNotExist) {
		return appPreviewManifest{}, nil, errAppPreviewNotFound
	}
	if err != nil {
		return appPreviewManifest{}, nil, fmt.Errorf("open app preview registration: %w", err)
	}
	if len(data) > 64<<10 {
		return appPreviewManifest{}, nil, errAppPreviewInvalid
	}
	var version struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &version); err != nil {
		return appPreviewManifest{}, nil, fmt.Errorf("decode app preview registration: %w", err)
	}
	if version.SchemaVersion == 1 {
		var legacy appPreviewLegacyManifest
		if err := decodeStrictAppPreviewJSON(data, &legacy); err != nil || legacy.OutputID != outputID {
			return appPreviewManifest{}, nil, errAppPreviewInvalid
		}
		return appPreviewManifest{}, &legacy, errAppPreviewUnavailable
	}
	var manifest appPreviewManifest
	if err := decodeStrictAppPreviewJSON(data, &manifest); err != nil {
		return appPreviewManifest{}, nil, err
	}
	manifest, err = normalizeAppPreviewManifest(manifest)
	if err != nil || manifest.OutputID != outputID {
		return appPreviewManifest{}, nil, errAppPreviewInvalid
	}
	return manifest, nil, nil
}

func decodeStrictAppPreviewJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode app preview registration: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errAppPreviewInvalid
	}
	return nil
}

func legacyMatchesManifest(legacy appPreviewLegacyManifest, manifest appPreviewManifest) bool {
	return legacy.SchemaVersion == 1 && legacy.OutputID == manifest.OutputID &&
		legacy.RunID == manifest.RunID && legacy.RuntimeID == manifest.RuntimeID &&
		legacy.WorkDir == manifest.WorkDir && reflect.DeepEqual(legacy.Commands, manifest.Commands)
}

func (s *appPreviewService) writeManifest(manifest appPreviewManifest) error {
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode app preview registration: %w", err)
	}
	temporary, err := os.CreateTemp(s.stateDir, ".app-preview-*.tmp")
	if err != nil {
		return fmt.Errorf("create app preview registration: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure app preview registration: %w", err)
	}
	if _, err := temporary.Write(append(encoded, '\n')); err != nil {
		temporary.Close()
		return fmt.Errorf("write app preview registration: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync app preview registration: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close app preview registration: %w", err)
	}
	if err := os.Rename(temporaryPath, s.manifestPath(manifest.OutputID)); err != nil {
		return fmt.Errorf("install app preview registration: %w", err)
	}
	return nil
}

func normalizeAppPreviewManifest(manifest appPreviewManifest) (appPreviewManifest, error) {
	if manifest.SchemaVersion != appPreviewManifestSchemaVersion || manifest.CompanyID == uuid.Nil ||
		manifest.OutputID == uuid.Nil || manifest.RunID == uuid.Nil || manifest.RuntimeID == uuid.Nil ||
		len(manifest.Commands) < 1 || len(manifest.Commands) > appPreviewMaximumCommands ||
		!filepath.IsAbs(manifest.WorkDir) {
		return appPreviewManifest{}, errAppPreviewInvalid
	}
	workDir, err := filepath.EvalSymlinks(filepath.Clean(manifest.WorkDir))
	if err != nil {
		return appPreviewManifest{}, errAppPreviewInvalid
	}
	info, err := os.Stat(workDir)
	if err != nil || !info.IsDir() {
		return appPreviewManifest{}, errAppPreviewInvalid
	}
	manifest.WorkDir = workDir
	commands := make([]appPreviewCommand, len(manifest.Commands))
	ports := make(map[int]struct{}, len(manifest.Commands))
	for index, command := range manifest.Commands {
		normalized, err := normalizeAppPreviewCommand(command)
		if err != nil {
			return appPreviewManifest{}, err
		}
		if _, duplicate := ports[normalized.Port]; duplicate {
			return appPreviewManifest{}, errAppPreviewInvalid
		}
		ports[normalized.Port] = struct{}{}
		commands[index] = normalized
	}
	manifest.Commands = commands
	var hidden []string
	for _, folder := range manifest.Hidden {
		if !filepath.IsAbs(folder) {
			return appPreviewManifest{}, errAppPreviewInvalid
		}
		hidden = append(hidden, filepath.Clean(folder))
	}
	manifest.Hidden = hidden
	return manifest, nil
}

func normalizeAppPreviewCommand(command appPreviewCommand) (appPreviewCommand, error) {
	if !validAppPreviewCommandValue(command.Command, 4096) ||
		strings.IndexFunc(command.Command, func(r rune) bool { return r == ' ' || r == '\t' }) >= 0 ||
		command.Port < 1024 || command.Port > 65535 || len(command.Args) > appPreviewMaximumArguments {
		return appPreviewCommand{}, errAppPreviewInvalid
	}
	for _, argument := range command.Args {
		if !validAppPreviewArgument(argument) {
			return appPreviewCommand{}, errAppPreviewInvalid
		}
	}
	workingDirectory := command.WorkingDirectory
	if workingDirectory == "" {
		workingDirectory = "."
	}
	if filepath.IsAbs(workingDirectory) || filepath.Clean(workingDirectory) != workingDirectory ||
		(workingDirectory != "." && (workingDirectory == ".." ||
			strings.HasPrefix(workingDirectory, ".."+string(filepath.Separator)))) {
		return appPreviewCommand{}, errAppPreviewInvalid
	}
	command.WorkingDirectory = workingDirectory
	command.Args = append([]string(nil), command.Args...)
	return command, nil
}

func validAppPreviewCommandValue(value string, maximumBytes int) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) &&
		len(value) <= maximumBytes && strings.IndexFunc(value, func(r rune) bool {
		return r == 0 || r < 0x20 || r == 0x7f
	}) < 0
}

func validAppPreviewArgument(value string) bool {
	return utf8.ValidString(value) && len(value) <= 4096 &&
		strings.IndexFunc(value, func(r rune) bool { return r == 0 || r == '\r' || r == '\n' }) < 0
}

func resolveAppPreviewWorkingDirectory(root, relative string) (string, error) {
	candidate := filepath.Join(root, relative)
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("%w: working directory is unavailable", errAppPreviewInvalid)
	}
	inside, err := filepath.Rel(root, resolved)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", errAppPreviewInvalid
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errAppPreviewInvalid
	}
	return resolved, nil
}

func newAppPreviewToken() string {
	var value [24]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(value[:])
}
