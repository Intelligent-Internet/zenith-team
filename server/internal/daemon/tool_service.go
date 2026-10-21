package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

type localToolService struct {
	studioOrigin   string
	root           string
	mu             sync.Mutex
	installed      map[string]toolInstallation
	states         map[string]protocol.ToolObservation
	accounts       map[toolAccount]string
	checkedAt      map[toolAccount]string
	logins         map[toolAccount]context.CancelFunc
	pending        map[toolJob]bool
	cancelled      map[toolJob]bool
	jobs           chan toolJob
	loginJobs      chan toolJob
	changed        chan struct{}
	started        bool
	authorizeLogin func(context.Context, toolAuthScope) bool
}

// Tool authentication is private to one Company and Agent on this machine.
// Runtime/provider and Run IDs do not change the persistent account profile.
type (
	toolAuthScope struct{ CompanyID, AgentID string }
	toolAccount   struct {
		toolAuthScope
		Key string
	}
)
type toolJob struct{ CompanyID, AgentID, Key, Action string }

func (j toolJob) scope() toolAuthScope { return toolAuthScope{j.CompanyID, j.AgentID} }
func (j toolJob) account() toolAccount { return toolAccount{j.scope(), j.Key} }

func newLocalToolService(root string) *localToolService {
	return &localToolService{
		root:      root,
		installed: make(map[string]toolInstallation),
		states:    make(map[string]protocol.ToolObservation),
		accounts:  make(map[toolAccount]string),
		checkedAt: make(map[toolAccount]string),
		logins:    make(map[toolAccount]context.CancelFunc),
		pending:   make(map[toolJob]bool),
		cancelled: make(map[toolJob]bool),
		jobs:      make(chan toolJob, 32),
		loginJobs: make(chan toolJob, 1),
		changed:   make(chan struct{}, 1),
	}
}

// Every authenticated tool must use this resolver for login, probing and
// execution. Never inherit an ambient or legacy Company-wide credential home.
func (s *localToolService) authHome(scope toolAuthScope, release toolRelease) (string, error) {
	company, companyErr := uuid.Parse(scope.CompanyID)
	agent, agentErr := uuid.Parse(scope.AgentID)
	if companyErr != nil || agentErr != nil || company == uuid.Nil || agent == uuid.Nil || release.HomeEnv == "" {
		return "", errors.New("tool authentication requires a Company and Agent")
	}
	digest := sha256.Sum256([]byte(s.apiOrigin(release)))
	return filepath.Join(
		s.root,
		"accounts",
		"companies",
		company.String(),
		"agents",
		agent.String(),
		"tools",
		release.Key,
		"origins",
		hex.EncodeToString(digest[:]),
	), nil
}

func (s *localToolService) observations(companyID, agentID string) []protocol.ToolObservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]protocol.ToolObservation, 0, len(reviewedToolReleases()))
	for _, release := range reviewedToolReleases() {
		value, ok := s.states[release.Key]
		if !ok {
			value = protocol.ToolObservation{
				Key:        release.Key,
				Version:    release.Version,
				State:      "pending",
				ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
			}
		}
		if origin := s.apiOrigin(release); origin != "" {
			value.APIOrigin = origin
		}
		key := toolAccount{toolAuthScope{companyID, agentID}, release.Key}
		value.AuthState = s.accounts[key]
		if release.HomeEnv != "" {
			value.AgentID = agentID
		}
		if release.HomeEnv == "" {
			value.AuthState = "not_required"
		}
		checked, _ := time.Parse(time.RFC3339Nano, s.checkedAt[key])
		observed, _ := time.Parse(time.RFC3339Nano, value.ObservedAt)
		if checked.After(observed) {
			value.ObservedAt = checked.Format(time.RFC3339Nano)
		}
		if value.AuthState == "" {
			value.AuthState = "unknown"
		}
		result = append(result, value)
	}
	return result
}

func (s *localToolService) notify() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *localToolService) schedule(job toolJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, known := findToolRelease(job.Key)
	if !known {
		return errors.New("unknown tool")
	}
	if release.HomeEnv != "" && (job.CompanyID != "" || job.AgentID != "" || isToolLogin(job.Action)) {
		if _, err := s.authHome(job.scope(), release); err != nil {
			return err
		}
	}
	if !s.started {
		return errors.New("tool setup has not started")
	}
	if s.pending[job] || (isToolLogin(job.Action) && s.loginPending(job.scope(), job.Key)) {
		if isToolLogin(job.Action) {
			return errors.New("sign-in is already pending")
		}
		return nil
	}
	queue := s.jobs
	if isToolLogin(job.Action) {
		queue = s.loginJobs
	}
	select {
	case queue <- job:
		s.pending[job] = true
		if isToolLogin(job.Action) {
			s.accounts[job.account()] = "connecting"
			s.checkedAt[job.account()] = time.Now().UTC().Format(time.RFC3339Nano)
			s.notify()
		}
		return nil
	default:
		return errors.New("tool operation queue is full")
	}
}

func (s *localToolService) start(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	stopJobs := s.startJobWorkers(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Setup must not hold up sign-in or status requests.
		// Bounded retries belong to startup reconciliation, never a heartbeat
		// or Run preparation lease. Repair can request another attempt later.
		for _, release := range reviewedToolReleases() {
			for _, delay := range []time.Duration{0, 15 * time.Second, time.Minute} {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				if s.install(ctx, release) == nil {
					break
				}
			}
		}
	}()
	return func() {
		cancel()
		stopJobs()
		<-done
	}
}

func (s *localToolService) startJobWorkers(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	var workers sync.WaitGroup
	// One browser sign-in at a time, with one queued request. Status and
	// repair operations continue while the user completes the browser flow.
	for _, queue := range []<-chan toolJob{s.jobs, s.loginJobs} {
		workers.Go(func() { s.processJobs(ctx, queue) })
	}
	return func() {
		s.mu.Lock()
		s.started = false
		s.mu.Unlock()
		cancel()
		workers.Wait()
	}
}

func (s *localToolService) processJobs(ctx context.Context, queue <-chan toolJob) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-queue:
			s.mu.Lock()
			pending := s.pending[job] && !s.cancelled[job]
			s.mu.Unlock()
			release, ok := findToolRelease(job.Key)
			if ok && pending {
				if job.Action == "repair" {
					_ = s.install(ctx, release)
				}
				if release.HomeEnv == "" || job.CompanyID != "" {
					s.probeAuth(ctx, job, release)
				}
			}
			s.mu.Lock()
			if isToolLogin(job.Action) && s.accounts[job.account()] == "connecting" {
				s.accounts[job.account()] = "error"
				s.checkedAt[job.account()] = time.Now().UTC().Format(time.RFC3339Nano)
				s.notify()
			}
			delete(s.pending, job)
			wasCancelled := s.cancelled[job]
			delete(s.cancelled, job)
			s.mu.Unlock()
			if ok && wasCancelled && isToolLogin(job.Action) {
				job.Action = "status"
				s.probeAuth(ctx, job, release)
			}
		}
	}
}

func (s *localToolService) install(ctx context.Context, release toolRelease) error {
	s.setState(release, "installing", "")
	installCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	installed, err := installToolRelease(installCtx, filepath.Join(s.root, "npm"), release)
	if err == nil {
		s.mu.Lock()
		s.installed[release.ID()] = installed
		s.mu.Unlock()
	} else {
		s.mu.Lock()
		delete(s.installed, release.ID())
		s.mu.Unlock()
	}
	if err == nil && release.Key == "hyperframes" {
		err = prepareHyperframes(installCtx, installed)
	}
	if err != nil {
		reason := "setup_failed"
		if errors.Is(err, errToolPrerequisites) {
			reason = "prerequisites_missing"
		}
		s.setState(release, "error", reason)
		return err
	}
	s.mu.Lock()
	s.installed[release.ID()] = installed
	s.mu.Unlock()
	s.setState(release, "installed", "")
	return nil
}

func (s *localToolService) setState(release toolRelease, state, reason string) {
	if current, ok := findToolRelease(release.Key); !ok || current.Version != release.Version {
		return
	}
	s.mu.Lock()
	s.states[release.Key] = protocol.ToolObservation{
		Key: release.Key, Version: release.Version, State: state,
		ReasonCode: reason, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	s.mu.Unlock()
	s.notify()
}

func (s *localToolService) probeAuth(ctx context.Context, job toolJob, release toolRelease) {
	if release.HomeEnv == "" {
		s.probeLocalTool(ctx, release)
		return
	}
	key := job.account()
	if release.Key == "zenith-studio" && s.apiOrigin(release) == "" {
		// Studio is off: never sign in to or probe a Studio API.
		if isToolLogin(job.Action) {
			s.mu.Lock()
			s.accounts[key] = "error"
			s.checkedAt[key] = time.Now().UTC().Format(time.RFC3339Nano)
			s.mu.Unlock()
			s.notify()
		}
		return
	}
	s.mu.Lock()
	if !isToolLogin(job.Action) && s.loginPending(job.scope(), job.Key) {
		s.mu.Unlock()
		return
	}
	checkedAt := s.checkedAt[key]
	installed, ok := s.installed[release.ID()]
	s.mu.Unlock()
	if !ok {
		return
	}
	if _, err := os.Stat(installed.Entrypoint); err != nil {
		s.mu.Lock()
		delete(s.installed, release.ID())
		s.mu.Unlock()
		s.setState(release, "error", "setup_failed")
		return
	}
	state := "error"
	args := []string{installed.Entrypoint, "--json", "auth", "status"}
	timeout := 15 * time.Second
	if isToolLogin(job.Action) {
		if s.authorizeLogin == nil || !s.authorizeLogin(ctx, job.scope()) {
			s.mu.Lock()
			s.accounts[key] = "error"
			s.checkedAt[key] = time.Now().UTC().Format(time.RFC3339Nano)
			s.mu.Unlock()
			s.notify()
			return
		}
		timeout = 5 * time.Minute
		args[len(args)-1] = "login"

	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if isToolLogin(job.Action) {
		s.mu.Lock()
		if !s.pending[job] || s.cancelled[job] {
			s.mu.Unlock()
			return
		}
		s.logins[key] = cancel
		s.accounts[key] = "connecting"
		s.checkedAt[key] = time.Now().UTC().Format(time.RFC3339Nano)
		s.mu.Unlock()
		s.notify()
		defer func() { s.mu.Lock(); delete(s.logins, key); s.mu.Unlock() }()
	}
	home, homeErr := s.authHome(job.scope(), release)
	if homeErr != nil || os.MkdirAll(home, 0o700) != nil {
		s.mu.Lock()
		s.accounts[key] = "error"
		s.mu.Unlock()
		s.notify()
		return
	}
	process := toolProcess{
		Executable: installed.Node,
		Args:       args,
		Env: map[string]string{
			release.HomeEnv:                home,
			"ZENITH_STUDIO_CLI_SKIP_SETUP": "1",
			"SSH_CONNECTION":               "",
			"SSH_TTY":                      "",
			release.APIURLEnv:              s.apiOrigin(release),
			"PATH":                         pathWithRunCLI(os.Getenv("PATH"), filepath.Dir(installed.Node)),
		},
	}
	output, err := runToolProcess(probeCtx, process)
	if err == nil {
		var result struct {
			OK   bool `json:"ok"`
			Data struct {
				Authenticated bool `json:"authenticated"`
			} `json:"data"`
		}
		if json.Unmarshal(output, &result) == nil && result.OK {
			state = "needs_auth"
			if result.Data.Authenticated {
				state = "authenticated"
				if release.Key == "zenith-studio" {
					// Auth status only proves a local credential exists. A read-only
					// remote request verifies refresh/revocation before resuming work.
					process.Args = []string{installed.Entrypoint, "--json", "models", "list"}
					remoteCtx, remoteCancel := context.WithTimeout(probeCtx, 15*time.Second)
					_, remoteErr := runToolProcess(remoteCtx, process)
					remoteCancel()
					if remoteErr != nil {
						state = "error"
						var exit *exec.ExitError
						if errors.As(remoteErr, &exit) && exit.ExitCode() == 3 {
							state = "needs_auth"
						}
					}
				}
			}
		}
	} else {
		state = "error"
	}
	s.mu.Lock()
	// A status probe may have started just before the login was queued.
	// Its older result must not replace that flow's connecting or final state.
	if !isToolLogin(job.Action) && s.checkedAt[key] != checkedAt {
		s.mu.Unlock()
		return
	}
	s.accounts[key] = state
	s.checkedAt[key] = time.Now().UTC().Format(time.RFC3339Nano)
	s.mu.Unlock()
	s.notify()
}

func (s *localToolService) probeLocalTool(ctx context.Context, release toolRelease) {
	s.mu.Lock()
	installed, ok := s.installed[release.ID()]
	s.mu.Unlock()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := inspectToolInstallation(ctx, installed.Prefix, installed.Node, release)
	if err == nil {
		s.mu.Lock()
		s.installed[release.ID()] = installed
		s.mu.Unlock()
	} else {
		s.mu.Lock()
		delete(s.installed, release.ID())
		s.mu.Unlock()
	}
	if err == nil && release.Key == "hyperframes" {
		err = checkHyperframes(ctx, installed)
	}
	if err != nil {
		reason := "setup_failed"
		if errors.Is(err, errToolPrerequisites) {
			reason = "prerequisites_missing"
		}
		s.setState(release, "error", reason)
		return
	}
	s.setState(release, "installed", "")
}

func (s *localToolService) cancelLogin(scope toolAuthScope, toolKey string) {
	s.mu.Lock()
	for _, action := range []string{"connect"} {
		job := toolJob{CompanyID: scope.CompanyID, AgentID: scope.AgentID, Key: toolKey, Action: action}
		if s.pending[job] {
			s.cancelled[job] = true
			s.accounts[toolAccount{scope, toolKey}] = "unknown"
			s.checkedAt[toolAccount{scope, toolKey}] = time.Now().UTC().Format(time.RFC3339Nano)
			s.notify()
		}
	}
	if cancel := s.logins[toolAccount{scope, toolKey}]; cancel != nil {
		cancel()
	}
	s.mu.Unlock()
}

// apiOrigin is the API a tool talks to. Zenith Studio uses only the origin
// the server configured: empty means Studio is off on this server, and
// nothing may contact any Studio API, including the release's default one.
func (s *localToolService) apiOrigin(release toolRelease) string {
	if release.Key == "zenith-studio" {
		return s.studioOrigin
	}
	return release.APIURL
}

func isToolLogin(action string) bool { return action == "connect" }

// Caller holds mu; both consent choices share one login slot and cancellation.
func (s *localToolService) loginPending(scope toolAuthScope, key string) bool {
	for _, action := range []string{"connect"} {
		job := toolJob{CompanyID: scope.CompanyID, AgentID: scope.AgentID, Key: key, Action: action}
		if s.pending[job] && !s.cancelled[job] {
			return true
		}
	}
	return false
}
