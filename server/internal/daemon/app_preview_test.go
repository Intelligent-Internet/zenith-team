package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/pkg/agent"
)

func TestAppPreviewConcurrentViewersShareActivation(t *testing.T) {
	t.Parallel()
	backend := newFakeAppPreviewBackend()
	service := newTestAppPreviewService(t, backend)
	manifest := testAppPreviewManifest(t, 4173)
	if err := service.Register(manifest); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	first := attachTestAppPreview(t, service, manifest)
	second := attachTestAppPreview(t, service, manifest)
	if _, err := service.Ensure(first.LeaseID, manifest.identity()); err != nil {
		t.Fatalf("Ensure(first) error = %v", err)
	}
	if _, err := service.Ensure(second.LeaseID, manifest.identity()); err != nil {
		t.Fatalf("Ensure(second) error = %v", err)
	}
	waitForAppPreview(t, service, first.LeaseID, manifest.identity(), appPreviewLifecycleRunning)
	if got := backend.launchCount(); got != 1 {
		t.Fatalf("launch count = %d, want 1", got)
	}
	if _, err := service.Release(first.LeaseID, manifest.identity()); err != nil {
		t.Fatalf("Release(first) error = %v", err)
	}
	time.Sleep(2 * testAppPreviewSettings().gracePeriod)
	if got := backend.stopCount(); got != 0 {
		t.Fatalf("stop count with second viewer = %d, want 0", got)
	}
	if _, err := service.Release(second.LeaseID, manifest.identity()); err != nil {
		t.Fatalf("Release(second) error = %v", err)
	}
	waitForBackendStops(t, backend, 1)
}

func TestAppPreviewEnsureReplayDoesNotRelaunchFailure(t *testing.T) {
	t.Parallel()
	backend := newFakeAppPreviewBackend()
	service := newTestAppPreviewService(t, backend)
	manifest := testAppPreviewManifest(t, 4174)
	if err := service.Register(manifest); err != nil {
		t.Fatal(err)
	}
	lease := attachTestAppPreview(t, service, manifest)
	if _, err := service.Ensure(lease.LeaseID, manifest.identity()); err != nil {
		t.Fatal(err)
	}
	waitForAppPreview(t, service, lease.LeaseID, manifest.identity(), appPreviewLifecycleRunning)
	backend.exitRoot(errors.New("root exited"))
	waitForAppPreview(t, service, lease.LeaseID, manifest.identity(), appPreviewLifecycleFailed)
	if _, err := service.Ensure(lease.LeaseID, manifest.identity()); err != nil {
		t.Fatalf("Ensure(replay) error = %v", err)
	}
	if got := backend.launchCount(); got != 1 {
		t.Fatalf("launch count after replay = %d, want 1", got)
	}
}

func TestAppPreviewFailurePublishesStoppingBeforeCleanup(t *testing.T) {
	t.Parallel()
	backend := newFakeAppPreviewBackend()
	stopStarted := make(chan struct{}, 1)
	stopRelease := make(chan struct{})
	backend.stopStarted = stopStarted
	backend.stopRelease = stopRelease
	defer func() {
		select {
		case <-stopRelease:
		default:
			close(stopRelease)
		}
	}()
	service := newTestAppPreviewService(t, backend)
	manifest := testAppPreviewManifest(t, 4191)
	if err := service.Register(manifest); err != nil {
		t.Fatal(err)
	}
	lease := attachTestAppPreview(t, service, manifest)
	if _, err := service.Ensure(lease.LeaseID, manifest.identity()); err != nil {
		t.Fatal(err)
	}
	running := waitForAppPreview(
		t, service, lease.LeaseID, manifest.identity(), appPreviewLifecycleRunning,
	)
	backend.exitRoot(errors.New("root exited"))
	select {
	case <-stopStarted:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not start")
	}

	result, err := service.Renew(lease.LeaseID, manifest.identity())
	if err != nil {
		t.Fatalf("Renew() error = %v", err)
	}
	if result.Snapshot.Lifecycle != appPreviewLifecycleStopping {
		t.Fatalf(
			"lifecycle during cleanup = %s, want stopping",
			result.Snapshot.Lifecycle,
		)
	}
	if result.Snapshot.Availability != appPreviewAvailabilityUnavailable {
		t.Fatalf(
			"availability during cleanup = %s, want unavailable",
			result.Snapshot.Availability,
		)
	}
	if result.Snapshot.URL != "" {
		t.Fatalf("URL during cleanup = %q, want empty", result.Snapshot.URL)
	}
	if _, err := service.Restart(
		lease.LeaseID,
		running.ActivationID,
		manifest.identity(),
	); !errors.Is(err, errAppPreviewBusy) {
		t.Fatalf("Restart() during cleanup error = %v, want busy", err)
	}

	close(stopRelease)
	waitForAppPreview(
		t, service, lease.LeaseID, manifest.identity(), appPreviewLifecycleFailed,
	)
}

func TestAppPreviewPortReservationsAreAtomicAndNeverPreempt(t *testing.T) {
	t.Parallel()
	backend := newFakeAppPreviewBackend()
	service := newTestAppPreviewService(t, backend)
	first := testAppPreviewManifest(t, 4175, 4176)
	second := testAppPreviewManifest(t, 4176, 4177)
	if err := service.Register(first); err != nil {
		t.Fatal(err)
	}
	if err := service.Register(second); err != nil {
		t.Fatal(err)
	}
	firstLease := attachTestAppPreview(t, service, first)
	if _, err := service.Ensure(firstLease.LeaseID, first.identity()); err != nil {
		t.Fatal(err)
	}
	waitForAppPreview(t, service, firstLease.LeaseID, first.identity(), appPreviewLifecycleRunning)
	secondLease := attachTestAppPreview(t, service, second)
	if _, err := service.Ensure(
		secondLease.LeaseID,
		second.identity(),
	); !errors.Is(
		err,
		errAppPreviewPortUnavailable,
	) {
		t.Fatalf("Ensure(conflict) error = %v, want port unavailable", err)
	}
	if got := backend.launchCount(); got != 1 {
		t.Fatalf("launch count = %d, want 1", got)
	}
	if owner := service.reservationOwner(4177); owner != uuid.Nil {
		t.Fatalf("non-conflicting port reserved by failed start: %s", owner)
	}
}

func TestAppPreviewStopDefeatsDelayedOperations(t *testing.T) {
	t.Parallel()
	backend := newFakeAppPreviewBackend()
	service := newTestAppPreviewService(t, backend)
	manifest := testAppPreviewManifest(t, 4178)
	if err := service.Register(manifest); err != nil {
		t.Fatal(err)
	}
	inspection, err := service.Inspect(manifest.identity())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := service.Attach(inspection.StopToken, manifest.identity())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Ensure(lease.LeaseID, manifest.identity()); err != nil {
		t.Fatal(err)
	}
	waitForAppPreview(t, service, lease.LeaseID, manifest.identity(), appPreviewLifecycleRunning)
	stopped, err := service.Stop(inspection.StopToken, manifest.identity())
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if stopped.StopToken == inspection.StopToken {
		t.Fatal("Stop() did not rotate the stop token")
	}
	if _, err := service.Renew(lease.LeaseID, manifest.identity()); !errors.Is(err, errAppPreviewLeaseInvalid) {
		t.Fatalf("Renew(old lease) error = %v, want invalid lease", err)
	}
	if _, err := service.Attach(inspection.StopToken, manifest.identity()); !errors.Is(err, errAppPreviewChanged) {
		t.Fatalf("Attach(old token) error = %v, want changed", err)
	}
	if _, err := service.Stop(inspection.StopToken, manifest.identity()); !errors.Is(err, errAppPreviewChanged) {
		t.Fatalf("Stop(old token) error = %v, want changed", err)
	}
	waitForBackendStops(t, backend, 1)
}

func TestAppPreviewCloseBoundsBlockedCleanup(t *testing.T) {
	t.Parallel()
	backend := newFakeAppPreviewBackend()
	backend.stopAlwaysFails = true
	settings := testAppPreviewSettings()
	settings.cleanupRetry = time.Second
	settings.shutdownCleanupTimeout = 30 * time.Millisecond
	service, err := newAppPreviewServiceWithBackend(
		t.TempDir(), slog.New(slog.NewTextHandler(testDiscardWriter{}, nil)), backend,
		launchAppPreviewUnsandboxed, settings,
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest := testAppPreviewManifest(t, 4192)
	if err := service.Register(manifest); err != nil {
		t.Fatal(err)
	}
	lease := attachTestAppPreview(t, service, manifest)
	if _, err := service.Ensure(lease.LeaseID, manifest.identity()); err != nil {
		t.Fatal(err)
	}
	waitForAppPreview(t, service, lease.LeaseID, manifest.identity(), appPreviewLifecycleRunning)
	entry, err := service.entry(manifest.OutputID)
	if err != nil {
		t.Fatal(err)
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- service.Close() }()
	select {
	case err := <-closeDone:
		if !errors.Is(err, errAppPreviewCleanupBlocked) {
			t.Fatalf("Close() error = %v, want cleanup blocked", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close() did not honor the cleanup shutdown deadline")
	}

	entry.mu.Lock()
	lifecycle := entry.lifecycle
	reason := entry.reason
	cleanupBlocked := entry.cleanupBlocked
	entry.mu.Unlock()
	if lifecycle != appPreviewLifecycleStopping || reason != "app_preview_cleanup_blocked" ||
		!cleanupBlocked {
		t.Fatalf(
			"state after Close() = (%s, %q, %t), want stopping cleanup-blocked state",
			lifecycle,
			reason,
			cleanupBlocked,
		)
	}
	if owner := service.reservationOwner(4192); owner != manifest.OutputID {
		t.Fatalf("reservation owner after Close() = %s, want %s", owner, manifest.OutputID)
	}
}

func TestAppPreviewRestartKeepsLeasesAndReplacesActivation(t *testing.T) {
	t.Parallel()
	backend := newFakeAppPreviewBackend()
	service := newTestAppPreviewService(t, backend)
	manifest := testAppPreviewManifest(t, 4179)
	if err := service.Register(manifest); err != nil {
		t.Fatal(err)
	}
	lease := attachTestAppPreview(t, service, manifest)
	started, err := service.Ensure(lease.LeaseID, manifest.identity())
	if err != nil {
		t.Fatal(err)
	}
	running := waitForAppPreview(t, service, lease.LeaseID, manifest.identity(), appPreviewLifecycleRunning)
	if running.ActivationID != started.Snapshot.ActivationID {
		t.Fatalf("running activation = %s, want %s", running.ActivationID, started.Snapshot.ActivationID)
	}
	restarting, err := service.Restart(lease.LeaseID, running.ActivationID, manifest.identity())
	if err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	if restarting.Snapshot.ActivationID == running.ActivationID {
		t.Fatal("Restart() retained the activation id")
	}
	replacement := waitForAppPreviewActivation(
		t, service, lease.LeaseID, manifest.identity(), restarting.Snapshot.ActivationID,
		appPreviewLifecycleRunning,
	)
	if replacement.Availability != appPreviewAvailabilityAvailable {
		t.Fatalf("replacement availability = %s, want available", replacement.Availability)
	}
	if got := backend.launchCount(); got != 2 {
		t.Fatalf("launch count = %d, want 2", got)
	}
	if _, err := service.Restart(
		lease.LeaseID,
		running.ActivationID,
		manifest.identity(),
	); !errors.Is(
		err,
		errAppPreviewChanged,
	) {
		t.Fatalf("Restart(old activation) error = %v, want changed", err)
	}
}

func TestAppPreviewRegistrationPersistsCompanyAndRejectsChangedRecipe(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	backend := newFakeAppPreviewBackend()
	service, err := newAppPreviewServiceWithBackend(
		stateDir, slog.New(slog.NewTextHandler(testDiscardWriter{}, nil)), backend, launchAppPreviewUnsandboxed,
		testAppPreviewSettings(),
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest := testAppPreviewManifest(t, 4180)
	if err := service.Register(manifest); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newAppPreviewServiceWithBackend(
		stateDir, slog.New(slog.NewTextHandler(testDiscardWriter{}, nil)), backend, launchAppPreviewUnsandboxed,
		testAppPreviewSettings(),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reloaded.Close() })
	inspection, err := reloaded.Inspect(manifest.identity())
	if err != nil {
		t.Fatalf("Inspect(reloaded) error = %v", err)
	}
	if inspection.Snapshot.CompanyID != manifest.CompanyID {
		t.Fatalf("company id = %s, want %s", inspection.Snapshot.CompanyID, manifest.CompanyID)
	}
	changed := manifest
	changed.Commands = []appPreviewCommand{{Command: "pnpm", Args: []string{"dev"}, Port: 4180}}
	if err := reloaded.Register(changed); !errors.Is(err, errAppPreviewConflict) {
		t.Fatalf("Register(changed) error = %v, want conflict", err)
	}
}

func TestAppPreviewLazyLoadLosingShutdownReturnsUnavailable(t *testing.T) {
	t.Parallel()
	service, err := newAppPreviewServiceWithBackend(
		t.TempDir(), slog.New(slog.NewTextHandler(testDiscardWriter{}, nil)),
		newFakeAppPreviewBackend(), launchAppPreviewUnsandboxed, testAppPreviewSettings(),
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest := testAppPreviewManifest(t, 4193)
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	loadStarted := make(chan struct{})
	releaseLoad := make(chan struct{})
	service.readManifestFile = func(string) ([]byte, error) {
		close(loadStarted)
		<-releaseLoad
		return encoded, nil
	}

	inspectDone := make(chan error, 1)
	go func() {
		var inspectErr error
		defer func() {
			if recovered := recover(); recovered != nil {
				inspectErr = fmt.Errorf("Inspect() panicked: %v", recovered)
			}
			inspectDone <- inspectErr
		}()
		_, inspectErr = service.Inspect(manifest.identity())
	}()
	<-loadStarted
	closeDone := make(chan error, 1)
	go func() { closeDone <- service.Close() }()
	deadline := time.Now().Add(time.Second)
	for {
		service.mu.Lock()
		closed := service.closed
		service.mu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Close() did not close the service")
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseLoad)
	if err := <-inspectDone; !errors.Is(err, errAppPreviewUnavailable) {
		t.Fatalf("Inspect() error = %v, want unavailable", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestAppPreviewHealthCanBecomeUnavailableAndRecover(t *testing.T) {
	t.Parallel()
	backend := newFakeAppPreviewBackend()
	service := newTestAppPreviewService(t, backend)
	manifest := testAppPreviewManifest(t, 4181)
	if err := service.Register(manifest); err != nil {
		t.Fatal(err)
	}
	lease := attachTestAppPreview(t, service, manifest)
	if _, err := service.Ensure(lease.LeaseID, manifest.identity()); err != nil {
		t.Fatal(err)
	}
	waitForAppPreview(t, service, lease.LeaseID, manifest.identity(), appPreviewLifecycleRunning)
	backend.setObservation(appPreviewEndpointObservation{
		Ownership: appPreviewOwnershipOwned, HTTP: false,
	})
	waitForAppPreviewAvailability(
		t, service, lease.LeaseID, manifest.identity(), appPreviewAvailabilityUnavailable,
	)
	backend.setObservation(appPreviewEndpointObservation{
		Ownership: appPreviewOwnershipOwned, HTTP: true,
	})
	waitForAppPreviewAvailability(
		t, service, lease.LeaseID, manifest.identity(), appPreviewAvailabilityAvailable,
	)
	if got := backend.launchCount(); got != 1 {
		t.Fatalf("launch count after health recovery = %d, want 1", got)
	}
}

func TestAppPreviewStaleObservationWithholdsAvailability(t *testing.T) {
	t.Parallel()
	service := newTestAppPreviewService(t, newFakeAppPreviewBackend())
	entry := newAppPreviewEntry(testAppPreviewManifest(t, 4190))
	entry.lifecycle = appPreviewLifecycleRunning
	entry.availability = appPreviewAvailabilityAvailable
	entry.lastObservedAt = time.Now().Add(-2 * service.settings.observationMaxAge)

	entry.mu.Lock()
	snapshot := service.snapshotLocked(entry)
	entry.mu.Unlock()
	if snapshot.Availability != appPreviewAvailabilityUnavailable {
		t.Fatalf("availability = %s, want unavailable", snapshot.Availability)
	}
	if snapshot.Reason != "app_preview_observation_stale" {
		t.Fatalf("reason = %q, want stale observation", snapshot.Reason)
	}
	if snapshot.URL != "" {
		t.Fatalf("URL = %q, want empty", snapshot.URL)
	}
}

func TestAppPreviewExternalListenerFailsWithoutLaunching(t *testing.T) {
	t.Parallel()
	backend := newFakeAppPreviewBackend()
	backend.externalListener = true
	service := newTestAppPreviewService(t, backend)
	manifest := testAppPreviewManifest(t, 4182)
	if err := service.Register(manifest); err != nil {
		t.Fatal(err)
	}
	lease := attachTestAppPreview(t, service, manifest)
	if _, err := service.Ensure(lease.LeaseID, manifest.identity()); err != nil {
		t.Fatal(err)
	}
	failed := waitForAppPreview(
		t, service, lease.LeaseID, manifest.identity(), appPreviewLifecycleFailed,
	)
	if failed.Reason != "app_preview_port_unavailable" {
		t.Fatalf("failure reason = %q, want port unavailable", failed.Reason)
	}
	if got := backend.launchCount(); got != 0 {
		t.Fatalf("launch count = %d, want 0", got)
	}
}

func TestAppPreviewBlockedCleanupCancelsReplacement(t *testing.T) {
	t.Parallel()
	backend := newFakeAppPreviewBackend()
	backend.stopFailures = 1
	service := newTestAppPreviewService(t, backend)
	manifest := testAppPreviewManifest(t, 4183)
	if err := service.Register(manifest); err != nil {
		t.Fatal(err)
	}
	lease := attachTestAppPreview(t, service, manifest)
	if _, err := service.Ensure(lease.LeaseID, manifest.identity()); err != nil {
		t.Fatal(err)
	}
	running := waitForAppPreview(
		t, service, lease.LeaseID, manifest.identity(), appPreviewLifecycleRunning,
	)
	if _, err := service.Restart(lease.LeaseID, running.ActivationID, manifest.identity()); err != nil {
		t.Fatal(err)
	}
	failed := waitForAppPreview(
		t, service, lease.LeaseID, manifest.identity(), appPreviewLifecycleFailed,
	)
	if failed.Reason != "app_preview_cleanup_blocked" {
		t.Fatalf("failure reason = %q, want cleanup blocked", failed.Reason)
	}
	if got := backend.launchCount(); got != 1 {
		t.Fatalf("launch count after blocked restart = %d, want 1", got)
	}
	if owner := service.reservationOwner(4183); owner != uuid.Nil {
		t.Fatalf("reservation retained after recovered cleanup: %s", owner)
	}
}

func TestAppPreviewEnsureReportsBlockedCleanup(t *testing.T) {
	t.Parallel()
	backend := newFakeAppPreviewBackend()
	backend.stopFailures = 1
	service := newTestAppPreviewService(t, backend)
	service.settings.cleanupRetry = time.Second
	service.settings.shutdownCleanupTimeout = 30 * time.Millisecond
	manifest := testAppPreviewManifest(t, 4194)
	if err := service.Register(manifest); err != nil {
		t.Fatal(err)
	}
	lease := attachTestAppPreview(t, service, manifest)
	if _, err := service.Ensure(lease.LeaseID, manifest.identity()); err != nil {
		t.Fatal(err)
	}
	running := waitForAppPreview(
		t, service, lease.LeaseID, manifest.identity(), appPreviewLifecycleRunning,
	)
	if _, err := service.Restart(lease.LeaseID, running.ActivationID, manifest.identity()); err != nil {
		t.Fatal(err)
	}
	entry, err := service.entry(manifest.OutputID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		entry.mu.Lock()
		blocked := entry.cleanupBlocked
		entry.mu.Unlock()
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cleanup did not become blocked")
		}
		time.Sleep(time.Millisecond)
	}
	inspection, err := service.Inspect(manifest.identity())
	if err != nil {
		t.Fatal(err)
	}
	newLease, err := service.Attach(inspection.StopToken, manifest.identity())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Ensure(
		newLease.LeaseID,
		manifest.identity(),
	); !errors.Is(err, errAppPreviewCleanupBlocked) {
		t.Fatalf("Ensure() error = %v, want cleanup blocked", err)
	}
}

func TestAppPreviewPlatformBackendOwnsListenerAndCleansTree(t *testing.T) {
	backend := newPlatformAppPreviewBackend()
	if err := backend.CheckSupported(); err != nil {
		t.Skipf("platform backend unavailable: %v", err)
	}
	settings := testAppPreviewSettings()
	settings.startupDeadline = 5 * time.Second
	settings.healthInterval = 50 * time.Millisecond
	settings.cleanupTimeout = 5 * time.Second
	service, err := newAppPreviewServiceWithBackend(
		t.TempDir(), slog.New(slog.NewTextHandler(testDiscardWriter{}, nil)), backend,
		launchAppPreviewUnsandboxed, settings,
	)
	if err != nil {
		t.Fatal(err)
	}
	port := availableAppPreviewPort(t)
	manifest := testAppPreviewManifest(t, port)
	manifest.Commands[0] = appPreviewCommand{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestAppPreviewHelperProcess", "--", "serve", strconv.Itoa(port)},
		Port:    port,
	}
	if err := service.Register(manifest); err != nil {
		t.Fatal(err)
	}
	lease := attachTestAppPreview(t, service, manifest)
	if _, err := service.Ensure(lease.LeaseID, manifest.identity()); err != nil {
		t.Fatal(err)
	}
	waitForAppPreview(t, service, lease.LeaseID, manifest.identity(), appPreviewLifecycleRunning)
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if connection, err := net.DialTimeout(
		"tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond,
	); err == nil {
		connection.Close()
		t.Fatal("listener survived supervisor cleanup")
	}
}

func TestAppPreviewPlatformBackendCleansDescendantAfterRootExit(t *testing.T) {
	backend := newPlatformAppPreviewBackend()
	if err := backend.CheckSupported(); err != nil {
		t.Skipf("platform backend unavailable: %v", err)
	}
	settings := testAppPreviewSettings()
	settings.startupDeadline = 5 * time.Second
	settings.cleanupTimeout = 5 * time.Second
	service, err := newAppPreviewServiceWithBackend(
		t.TempDir(), slog.New(slog.NewTextHandler(testDiscardWriter{}, nil)), backend,
		launchAppPreviewUnsandboxed, settings,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	port := availableAppPreviewPort(t)
	manifest := testAppPreviewManifest(t, port)
	manifest.Commands[0] = appPreviewCommand{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestAppPreviewHelperProcess", "--", "spawn", strconv.Itoa(port)},
		Port:    port,
	}
	if err := service.Register(manifest); err != nil {
		t.Fatal(err)
	}
	lease := attachTestAppPreview(t, service, manifest)
	if _, err := service.Ensure(lease.LeaseID, manifest.identity()); err != nil {
		t.Fatal(err)
	}
	waitForAppPreview(t, service, lease.LeaseID, manifest.identity(), appPreviewLifecycleFailed)
	if connection, err := net.DialTimeout(
		"tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond,
	); err == nil {
		connection.Close()
		t.Fatal("descendant listener survived root-exit cleanup")
	}
}

func TestAppPreviewHelperProcess(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+2 >= len(os.Args) {
		return
	}
	mode := os.Args[separator+1]
	port, err := strconv.Atoi(os.Args[separator+2])
	if err != nil {
		t.Fatal(err)
	}
	if mode == "spawn" {
		child := exec.Command(
			os.Args[0], "-test.run=TestAppPreviewHelperProcess", "--", "serve", strconv.Itoa(port),
		)
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			connection, dialErr := net.DialTimeout(
				"tcp", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond,
			)
			if dialErr == nil {
				connection.Close()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("descendant did not start")
	}
	server := &http.Server{
		Addr: fmt.Sprintf("127.0.0.1:%d", port), ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

func availableAppPreviewPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

type fakeAppPreviewBackend struct {
	mu               sync.Mutex
	launches         int
	sandboxes        []*agent.Sandbox
	stops            int
	rootExits        []chan error
	observation      appPreviewEndpointObservation
	externalListener bool
	stopFailures     int
	stopAlwaysFails  bool
	stopStarted      chan<- struct{}
	stopRelease      <-chan struct{}
}

func newFakeAppPreviewBackend() *fakeAppPreviewBackend {
	return &fakeAppPreviewBackend{observation: appPreviewEndpointObservation{
		Ownership: appPreviewOwnershipOwned, HTTP: true,
	}}
}

func (b *fakeAppPreviewBackend) CheckSupported() error { return nil }

func (b *fakeAppPreviewBackend) Launch(
	_ context.Context, manifest appPreviewManifest, _ []string, sandbox *agent.Sandbox,
) ([]appPreviewTree, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.launches++
	b.sandboxes = append(b.sandboxes, sandbox)
	trees := make([]appPreviewTree, len(manifest.Commands))
	for index := range trees {
		rootExit := make(chan error, 1)
		b.rootExits = append(b.rootExits, rootExit)
		trees[index] = &fakeAppPreviewTree{rootExit: rootExit}
	}
	return trees, nil
}

func (b *fakeAppPreviewBackend) Observe(
	_ context.Context, _ int, tree appPreviewTree,
) appPreviewEndpointObservation {
	if tree == nil {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.externalListener {
			return appPreviewEndpointObservation{Ownership: appPreviewOwnershipForeign}
		}
		return appPreviewEndpointObservation{Ownership: appPreviewOwnershipAbsent}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.observation
}

func (b *fakeAppPreviewBackend) Stop(context.Context, []appPreviewTree) error {
	b.mu.Lock()
	b.stops++
	stopStarted := b.stopStarted
	stopRelease := b.stopRelease
	failed := b.stopAlwaysFails || b.stopFailures > 0
	if b.stopFailures > 0 {
		b.stopFailures--
	}
	b.mu.Unlock()
	if stopStarted != nil {
		select {
		case stopStarted <- struct{}{}:
		default:
		}
	}
	if stopRelease != nil {
		<-stopRelease
	}
	if failed {
		return errAppPreviewCleanupBlocked
	}
	return nil
}

func (b *fakeAppPreviewBackend) setObservation(observation appPreviewEndpointObservation) {
	b.mu.Lock()
	b.observation = observation
	b.mu.Unlock()
}

func (b *fakeAppPreviewBackend) launchCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.launches
}

// launchSandboxes is the sandbox each launch was given, nil for none.
func (b *fakeAppPreviewBackend) launchSandboxes() []*agent.Sandbox {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]*agent.Sandbox(nil), b.sandboxes...)
}

func (b *fakeAppPreviewBackend) stopCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stops
}

func (b *fakeAppPreviewBackend) exitRoot(err error) {
	b.mu.Lock()
	rootExit := b.rootExits[len(b.rootExits)-1]
	b.mu.Unlock()
	rootExit <- err
}

type fakeAppPreviewTree struct{ rootExit <-chan error }

func (t *fakeAppPreviewTree) RootExit() <-chan error { return t.rootExit }

func newTestAppPreviewService(t *testing.T, backend appPreviewBackend) *appPreviewService {
	t.Helper()
	service, err := newAppPreviewServiceWithBackend(
		t.TempDir(), slog.New(slog.NewTextHandler(testDiscardWriter{}, nil)), backend,
		launchAppPreviewUnsandboxed, testAppPreviewSettings(),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func testAppPreviewSettings() appPreviewSettings {
	return appPreviewSettings{
		leaseTTL:               500 * time.Millisecond,
		renewAfter:             20 * time.Millisecond,
		gracePeriod:            20 * time.Millisecond,
		sweepInterval:          5 * time.Millisecond,
		startupDeadline:        time.Second,
		healthInterval:         10 * time.Millisecond,
		observationMaxAge:      50 * time.Millisecond,
		cleanupRetry:           10 * time.Millisecond,
		cleanupTimeout:         time.Second,
		shutdownCleanupTimeout: 6 * time.Second,
	}
}

func testAppPreviewManifest(t *testing.T, ports ...int) appPreviewManifest {
	t.Helper()
	commands := make([]appPreviewCommand, len(ports))
	for index, port := range ports {
		commands[index] = appPreviewCommand{Command: "test-preview", Port: port}
	}
	return appPreviewManifest{
		SchemaVersion: appPreviewManifestSchemaVersion,
		CompanyID:     uuid.New(), OutputID: uuid.New(), RunID: uuid.New(), RuntimeID: uuid.New(),
		WorkDir: t.TempDir(), Commands: commands,
	}
}

func attachTestAppPreview(
	t *testing.T, service *appPreviewService, manifest appPreviewManifest,
) appPreviewOperationResult {
	t.Helper()
	inspection, err := service.Inspect(manifest.identity())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := service.Attach(inspection.StopToken, manifest.identity())
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func waitForAppPreview(
	t *testing.T, service *appPreviewService, leaseID uuid.UUID,
	identity appPreviewIdentity, want appPreviewLifecycle,
) appPreviewSnapshot {
	t.Helper()
	return waitForAppPreviewActivation(t, service, leaseID, identity, uuid.Nil, want)
}

func waitForAppPreviewActivation(
	t *testing.T, service *appPreviewService, leaseID uuid.UUID,
	identity appPreviewIdentity, activationID uuid.UUID, want appPreviewLifecycle,
) appPreviewSnapshot {
	t.Helper()
	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		result, err := service.Renew(leaseID, identity)
		if err == nil && result.Snapshot.Lifecycle == want &&
			(activationID == uuid.Nil || result.Snapshot.ActivationID == activationID) {
			return result.Snapshot
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("preview did not reach %s", want)
	return appPreviewSnapshot{}
}

func waitForAppPreviewAvailability(
	t *testing.T,
	service *appPreviewService,
	leaseID uuid.UUID,
	identity appPreviewIdentity,
	want appPreviewAvailability,
) appPreviewSnapshot {
	t.Helper()
	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		result, err := service.Renew(leaseID, identity)
		if err == nil && result.Snapshot.Availability == want {
			return result.Snapshot
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("preview did not reach availability %s", want)
	return appPreviewSnapshot{}
}

func waitForBackendStops(t *testing.T, backend *fakeAppPreviewBackend, want int) {
	t.Helper()
	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		if backend.stopCount() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("stop count = %d, want at least %d", backend.stopCount(), want)
}

// launchAppPreviewUnsandboxed starts a preview without the sandbox and holds
// nothing: the supervisor tests do not need the Daemon's verdict.
func launchAppPreviewUnsandboxed([]string, []string) (*agent.Sandbox, func()) { return nil, func() {} }

type testDiscardWriter struct{}

func (testDiscardWriter) Write(value []byte) (int, error) { return len(value), nil }
