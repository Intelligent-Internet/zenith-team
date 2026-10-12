package workspace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/daemon/repocache"
)

func TestMainMutationGuardWaitRespectsContext(t *testing.T) {
	t.Parallel()

	manager, _ := newMainMutationTestManagers(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- manager.withMainMutation(context.Background(), func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := manager.withMainMutation(ctx, func(context.Context) error {
		t.Fatal("canceled waiter entered the mutation section")
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("withMainMutation() error = %v, want context canceled", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("holder error = %v", err)
	}
}

func TestMainMutationGuardCoordinatesManagerInstances(t *testing.T) {
	t.Parallel()

	first, second := newMainMutationTestManagers(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- first.withMainMutation(context.Background(), func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	waitCtx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := second.withMainMutation(waitCtx, func(context.Context) error {
		t.Fatal("second Manager entered while the first held the guard")
		return nil
	}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second withMainMutation() error = %v, want deadline exceeded", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("holder error = %v", err)
	}
	if err := second.withMainMutation(t.Context(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("second withMainMutation() after release = %v", err)
	}
}

func TestMainMutationOperationsRejectCanceledContext(t *testing.T) {
	t.Parallel()

	manager, _ := newMainMutationTestManagers(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := manager.AcquireScopedScratch(
		ctx,
		taskMergeTestCompanyID,
		"engineering",
		taskMergeTestConversationID,
		uuid.MustParse("00000000-0000-4000-8000-000000001501"),
		0,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("AcquireScopedScratch(, 0) error = %v, want context canceled", err)
	}
	if _, err := manager.GC(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("GC() error = %v, want context canceled", err)
	}
	if err := manager.Reconcile(ctx, map[string]struct{}{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Reconcile() error = %v, want context canceled", err)
	}
	if _, err := manager.ReconcileAndGC(ctx, map[string]struct{}{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReconcileAndGC() error = %v, want context canceled", err)
	}
}

func TestMainMutationGuardCoordinatesProcesses(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "workspaces")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	marker := filepath.Join(dataRoot, "lock-held")
	manager := newMainMutationTestManager(t, root, cacheRoot)

	command := exec.CommandContext(
		t.Context(),
		os.Args[0],
		"-test.run=^TestMainMutationGuardHelperProcess$",
	)
	command.Env = append(
		os.Environ(),
		"BOARDLY_TEST_MAIN_MUTATION_HELPER=1",
		"BOARDLY_TEST_MAIN_MUTATION_ROOT="+root,
		"BOARDLY_TEST_MAIN_MUTATION_CACHE_ROOT="+cacheRoot,
		"BOARDLY_TEST_MAIN_MUTATION_MARKER="+marker,
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waitForMainMutationMarker(t, marker, command, &output)

	waitCtx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	if err := manager.withMainMutation(waitCtx, func(context.Context) error {
		t.Fatal("parent entered while helper process held the guard")
		return nil
	}); !errors.Is(err, context.DeadlineExceeded) {
		cancel()
		_ = stdin.Close()
		_ = command.Wait()
		t.Fatalf("withMainMutation() error = %v, want deadline exceeded", err)
	}
	cancel()

	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("helper process: %v: %s", err, output.String())
	}
	if err := manager.withMainMutation(t.Context(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("withMainMutation() after helper exit = %v", err)
	}
}

func TestMainMutationGuardHelperProcess(t *testing.T) {
	if os.Getenv("BOARDLY_TEST_MAIN_MUTATION_HELPER") != "1" {
		return
	}
	manager := newMainMutationTestManager(
		t,
		os.Getenv("BOARDLY_TEST_MAIN_MUTATION_ROOT"),
		os.Getenv("BOARDLY_TEST_MAIN_MUTATION_CACHE_ROOT"),
	)
	if err := manager.withMainMutation(t.Context(), func(context.Context) error {
		if err := os.WriteFile(
			os.Getenv("BOARDLY_TEST_MAIN_MUTATION_MARKER"),
			[]byte("held"),
			0o600,
		); err != nil {
			return err
		}
		_, err := io.Copy(io.Discard, os.Stdin)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func newMainMutationTestManagers(t *testing.T) (*Manager, *Manager) {
	t.Helper()
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "workspaces")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	return newMainMutationTestManager(t, root, cacheRoot), newMainMutationTestManager(t, root, cacheRoot)
}

func newMainMutationTestManager(t *testing.T, root, cacheRoot string) *Manager {
	t.Helper()
	manager, err := NewManager(
		root,
		cacheRoot,
		testMetadataKey,
		repocache.OSRunner{},
		Retention{Completed: time.Hour, Orphaned: 2 * time.Hour},
		time.Now,
	)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return manager
}

func waitForMainMutationMarker(t *testing.T, marker string, command *exec.Cmd, output *bytes.Buffer) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatalf("helper did not acquire lock: %s", output.String())
		case <-ticker.C:
		}
	}
}
