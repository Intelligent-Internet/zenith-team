package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
)

type appPreviewOwnership string

const (
	appPreviewOwnershipAbsent  appPreviewOwnership = "absent"
	appPreviewOwnershipOwned   appPreviewOwnership = "owned"
	appPreviewOwnershipForeign appPreviewOwnership = "foreign"
	appPreviewOwnershipUnknown appPreviewOwnership = "unknown"
)

type appPreviewEndpointObservation struct {
	Ownership appPreviewOwnership
	HTTP      bool
}

type appPreviewTree interface {
	RootExit() <-chan error
}

type appPreviewBackend interface {
	CheckSupported() error
	// Launch starts each command in its working directory, inside sandbox
	// when it is not nil.
	Launch(context.Context, appPreviewManifest, []string, *agent.Sandbox) ([]appPreviewTree, error)
	Observe(context.Context, int, appPreviewTree) appPreviewEndpointObservation
	Stop(context.Context, []appPreviewTree) error
}

func (s *appPreviewService) runAppPreviewLifecycle(
	attemptCtx context.Context,
	entry *appPreviewEntry,
	attemptID uuid.UUID,
) {
	defer s.wg.Done()
	for {
		trees, failureReason, release := s.startAndSuperviseAppPreview(
			attemptCtx, entry, attemptID,
		)
		s.markAppPreviewStopping(entry, attemptID, trees, failureReason)
		if err := s.cleanupAppPreview(entry, trees); err != nil {
			// The daemon is shutting down with processes it may not have
			// stopped, so an unwrapped launch keeps its hold on the report.
			entry.mu.Lock()
			entry.pendingReplacement = false
			entry.lifecycleRunning = false
			entry.cancelAttempt = nil
			entry.mu.Unlock()
			s.recordAppPreviewCloseError(err)
			return
		}
		release()

		entry.mu.Lock()
		if failureReason == "" && entry.reason == "app_preview_cleanup_blocked" {
			failureReason = "app_preview_cleanup_blocked"
		}
		if entry.stopRequested || !entry.pendingReplacement ||
			entry.activeLeaseCountLocked(time.Now()) == 0 {
			entry.pendingReplacement = false
			entry.lifecycleRunning = false
			entry.cancelAttempt = nil
			entry.availability = appPreviewAvailabilityUnavailable
			if entry.stopRequested || entry.activeLeaseCountLocked(time.Now()) == 0 {
				entry.lifecycle = appPreviewLifecycleStopped
				entry.reason = ""
			} else {
				entry.lifecycle = appPreviewLifecycleFailed
				entry.reason = failureReason
			}
			s.releasePortsLocked(entry)
			entry.mu.Unlock()
			return
		}

		attemptID = entry.activationID
		attemptCtx, entry.cancelAttempt = context.WithCancel(s.ctx)
		entry.pendingReplacement = false
		entry.lifecycle = appPreviewLifecycleStarting
		entry.availability = appPreviewAvailabilityChecking
		entry.reason = ""
		entry.lastObservedAt = time.Time{}
		entry.mu.Unlock()
	}
}

func (s *appPreviewService) markAppPreviewStopping(
	entry *appPreviewEntry,
	attemptID uuid.UUID,
	trees []appPreviewTree,
	failureReason string,
) {
	if len(trees) == 0 || failureReason == "" {
		return
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.activationID != attemptID {
		return
	}
	entry.lifecycle = appPreviewLifecycleStopping
	entry.availability = appPreviewAvailabilityUnavailable
	if !entry.stopRequested {
		entry.reason = failureReason
	}
}

func (s *appPreviewService) startAndSuperviseAppPreview(
	attemptCtx context.Context,
	entry *appPreviewEntry,
	attemptID uuid.UUID,
) ([]appPreviewTree, string, func()) {
	if err := s.backend.CheckSupported(); err != nil {
		return nil, "app_preview_platform_unsupported", func() {}
	}
	entry.mu.Lock()
	manifest := entry.manifest
	entry.mu.Unlock()
	workingDirectories := make([]string, len(manifest.Commands))
	for index, command := range manifest.Commands {
		workingDirectory, err := resolveAppPreviewWorkingDirectory(
			manifest.WorkDir, command.WorkingDirectory,
		)
		if err != nil {
			return nil, "app_preview_workspace_unavailable", func() {}
		}
		workingDirectories[index] = workingDirectory
	}
	for _, command := range manifest.Commands {
		observation := s.backend.Observe(attemptCtx, command.Port, nil)
		if observation.Ownership != appPreviewOwnershipAbsent {
			return nil, "app_preview_port_unavailable", func() {}
		}
	}
	sandbox, release := s.isolate(append([]string{manifest.WorkDir}, workingDirectories...), manifest.Hidden)
	trees, err := s.backend.Launch(attemptCtx, manifest, workingDirectories, sandbox)
	if err != nil || len(trees) != len(manifest.Commands) {
		return trees, "app_preview_start_failed", release
	}
	return trees, s.superviseAppPreview(attemptCtx, entry, attemptID, manifest, trees), release
}

// superviseAppPreview waits for a launched preview to answer and then
// watches it. It returns why the preview failed, or "" when the attempt was
// cancelled.
func (s *appPreviewService) superviseAppPreview(
	attemptCtx context.Context,
	entry *appPreviewEntry,
	attemptID uuid.UUID,
	manifest appPreviewManifest,
	trees []appPreviewTree,
) string {
	startupTimer := time.NewTimer(s.settings.startupDeadline)
	defer startupTimer.Stop()
	startupTicker := time.NewTicker(min(100*time.Millisecond, s.settings.healthInterval))
	defer startupTicker.Stop()
	for {
		if reason := appPreviewRootExitReason(trees); reason != "" {
			return reason
		}
		observations := s.observeAppPreviewEndpoints(attemptCtx, manifest, trees)
		if attemptCtx.Err() != nil {
			return ""
		}
		if reason := appPreviewRootExitReason(trees); reason != "" {
			return reason
		}
		now := time.Now()
		foreign, available := appPreviewObservationState(observations)
		entry.mu.Lock()
		if entry.activationID == attemptID {
			entry.lastObservedAt = now
			if available {
				entry.lifecycle = appPreviewLifecycleRunning
				entry.availability = appPreviewAvailabilityAvailable
				entry.reason = ""
			} else {
				entry.availability = appPreviewAvailabilityChecking
			}
		}
		entry.mu.Unlock()
		if foreign {
			return "app_preview_foreign_listener"
		}
		if available {
			break
		}
		select {
		case <-attemptCtx.Done():
			return ""
		case <-startupTimer.C:
			return "app_preview_readiness_failed"
		case <-startupTicker.C:
		}
	}

	healthTicker := time.NewTicker(s.settings.healthInterval)
	defer healthTicker.Stop()
	for {
		select {
		case <-attemptCtx.Done():
			return ""
		case <-healthTicker.C:
			if reason := appPreviewRootExitReason(trees); reason != "" {
				return reason
			}
			observations := s.observeAppPreviewEndpoints(attemptCtx, manifest, trees)
			if attemptCtx.Err() != nil {
				return ""
			}
			if reason := appPreviewRootExitReason(trees); reason != "" {
				return reason
			}
			foreign, available := appPreviewObservationState(observations)
			entry.mu.Lock()
			if entry.activationID == attemptID {
				entry.lastObservedAt = time.Now()
				if available {
					entry.availability = appPreviewAvailabilityAvailable
					entry.reason = ""
				} else {
					entry.availability = appPreviewAvailabilityUnavailable
					entry.reason = "app_preview_http_unavailable"
				}
			}
			entry.mu.Unlock()
			if foreign {
				return "app_preview_foreign_listener"
			}
		}
	}
}

func (s *appPreviewService) observeAppPreviewEndpoints(
	ctx context.Context,
	manifest appPreviewManifest,
	trees []appPreviewTree,
) []appPreviewEndpointObservation {
	observations := make([]appPreviewEndpointObservation, len(manifest.Commands))
	var wait sync.WaitGroup
	for index, command := range manifest.Commands {
		wait.Add(1)
		go func() {
			defer wait.Done()
			observations[index] = s.backend.Observe(ctx, command.Port, trees[index])
		}()
	}
	wait.Wait()
	return observations
}

func appPreviewObservationState(observations []appPreviewEndpointObservation) (bool, bool) {
	available := len(observations) > 0
	for _, observation := range observations {
		if observation.Ownership == appPreviewOwnershipForeign {
			return true, false
		}
		if observation.Ownership != appPreviewOwnershipOwned || !observation.HTTP {
			available = false
		}
	}
	return false, available
}

func appPreviewRootExitReason(trees []appPreviewTree) string {
	for _, tree := range trees {
		select {
		case <-tree.RootExit():
			return "app_preview_command_exited"
		default:
		}
	}
	return ""
}

func (s *appPreviewService) cleanupAppPreview(
	entry *appPreviewEntry,
	trees []appPreviewTree,
) error {
	if len(trees) == 0 {
		entry.mu.Lock()
		entry.cleanupBlocked = false
		entry.mu.Unlock()
		return nil
	}
	for {
		cleanupCtx, cancel := context.WithTimeout(s.cleanupCtx, s.settings.cleanupTimeout)
		err := s.backend.Stop(cleanupCtx, trees)
		cancel()
		if err == nil {
			entry.mu.Lock()
			entry.cleanupBlocked = false
			entry.mu.Unlock()
			return nil
		}
		entry.mu.Lock()
		entry.cleanupBlocked = true
		entry.pendingReplacement = false
		entry.lifecycle = appPreviewLifecycleStopping
		entry.availability = appPreviewAvailabilityUnavailable
		entry.reason = "app_preview_cleanup_blocked"
		outputID := entry.manifest.OutputID
		activationID := entry.activationID
		entry.mu.Unlock()
		s.logger.Warn(
			"app preview cleanup blocked",
			"output_id", outputID,
			"activation_id", activationID,
			"error", err,
		)
		if s.cleanupCtx.Err() != nil {
			return fmt.Errorf(
				"%w before daemon shutdown deadline: %w",
				errAppPreviewCleanupBlocked,
				err,
			)
		}
		timer := time.NewTimer(s.settings.cleanupRetry)
		select {
		case <-timer.C:
		case <-s.cleanupCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return fmt.Errorf(
				"%w before daemon shutdown deadline: %w",
				errAppPreviewCleanupBlocked,
				err,
			)
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

func (s *appPreviewService) recordAppPreviewCloseError(err error) {
	s.mu.Lock()
	s.closeErr = errors.Join(s.closeErr, err)
	s.mu.Unlock()
}
