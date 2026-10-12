package daemon

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

func TestUnixAppPreviewBackendLaunchHonorsCanceledContext(t *testing.T) {
	backend := newPlatformAppPreviewBackend()
	if err := backend.CheckSupported(); err != nil {
		t.Skipf("platform backend unavailable: %v", err)
	}
	manifest := testAppPreviewManifest(t, 4192, 4193)
	for index := range manifest.Commands {
		manifest.Commands[index].Command = "/usr/bin/true"
	}
	workingDirectories := []string{t.TempDir(), t.TempDir()}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	trees, err := backend.Launch(ctx, manifest, workingDirectories, nil)
	if len(trees) > 0 {
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			_ = backend.Stop(cleanupCtx, trees)
		})
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Launch() error = %v, want context canceled", err)
	}
	if len(trees) != 0 {
		t.Fatalf("Launch() trees = %d, want 0", len(trees))
	}
}

func TestUnixAppPreviewBackendLaunchStopsBeforeNextCommandAfterCancellation(t *testing.T) {
	backend := newPlatformAppPreviewBackend()
	if err := backend.CheckSupported(); err != nil {
		t.Skipf("platform backend unavailable: %v", err)
	}
	manifest := testAppPreviewManifest(t, 4195, 4196)
	for index := range manifest.Commands {
		manifest.Commands[index].Command = "/usr/bin/true"
	}
	workingDirectories := []string{t.TempDir(), t.TempDir()}
	ctx := newCancelAfterErrChecksContext(t.Context(), 3)

	trees, err := backend.Launch(ctx, manifest, workingDirectories, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Launch() error = %v, want context canceled", err)
	}
	if len(trees) != 1 {
		t.Fatalf("Launch() trees = %d, want only the first command", len(trees))
	}
	alive, aliveErr := appPreviewAnchorAlive(trees[0].(*unixAppPreviewTree))
	if aliveErr != nil {
		t.Fatalf("first command identity after Launch() error = %v", aliveErr)
	}
	if alive {
		t.Fatal("first command survived canceled launch cleanup")
	}
}

func TestUnixAppPreviewBackendStopCleansVerifiedSiblingAfterLostAnchor(t *testing.T) {
	backend := newPlatformAppPreviewBackend()
	if err := backend.CheckSupported(); err != nil {
		t.Skipf("platform backend unavailable: %v", err)
	}
	manifest := testAppPreviewManifest(t, 4194)
	manifest.Commands[0].Command = "/usr/bin/sleep"
	manifest.Commands[0].Args = []string{"60"}
	trees, err := backend.Launch(t.Context(), manifest, []string{t.TempDir()}, nil)
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	sibling := trees[0].(*unixAppPreviewTree)
	cleaned := false
	t.Cleanup(func() {
		if cleaned {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = backend.Stop(cleanupCtx, trees)
	})
	uncertain := &unixAppPreviewTree{
		process: &os.Process{Pid: os.Getpid()},
		groupID: -1,
	}
	stopCtx, stopCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer stopCancel()

	err = backend.Stop(stopCtx, []appPreviewTree{uncertain, sibling})
	if !errors.Is(err, errAppPreviewCleanupBlocked) {
		t.Fatalf("Stop() error = %v, want cleanup blocked", err)
	}
	alive, aliveErr := appPreviewAnchorAlive(sibling)
	if aliveErr != nil {
		t.Fatalf("sibling identity after Stop() error = %v", aliveErr)
	}
	if alive {
		t.Fatal("verified sibling survived cleanup")
	}
	cleaned = true
}

type cancelAfterErrChecksContext struct {
	context.Context

	mu       sync.Mutex
	checks   int
	cancelAt int
	canceled bool
	done     chan struct{}
}

func newCancelAfterErrChecksContext(
	parent context.Context,
	cancelAt int,
) *cancelAfterErrChecksContext {
	return &cancelAfterErrChecksContext{
		Context:  parent,
		cancelAt: cancelAt,
		done:     make(chan struct{}),
	}
}

func (c *cancelAfterErrChecksContext) Done() <-chan struct{} {
	return c.done
}

func (c *cancelAfterErrChecksContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.canceled {
		return context.Canceled
	}
	c.checks++
	if c.checks < c.cancelAt {
		return nil
	}
	c.canceled = true
	close(c.done)
	return context.Canceled
}
