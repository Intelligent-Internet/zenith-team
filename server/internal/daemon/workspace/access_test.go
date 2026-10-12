package workspace

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/daemon/repocache"
)

func TestManagerResolveRunWorkDirUsesAuthenticatedLatestRun(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	manager := newWorkspaceAccessTestManager(t, func() time.Time { return now })
	conversationID := "01900000-0000-4000-8000-0000000000e1"
	first, err := manager.AcquireScratch(conversationID, uuid.MustParse("00000000-0000-4000-8000-000000001401"))
	if err != nil {
		t.Fatalf("AcquireScratch() error = %v", err)
	}
	if err := manager.MarkCompleted(first.Root); err != nil {
		t.Fatalf("MarkCompleted() error = %v", err)
	}

	got, err := manager.ResolveRunWorkDir(t.Context(), uuid.MustParse("00000000-0000-4000-8000-000000001401"))
	if err != nil {
		t.Fatalf("ResolveRunWorkDir() error = %v", err)
	}
	if got != first.WorkDir {
		t.Fatalf("ResolveRunWorkDir() = %q, want %q", got, first.WorkDir)
	}

	now = now.Add(time.Hour)
	second, err := manager.AcquireScratch(conversationID, uuid.MustParse("00000000-0000-4000-8000-000000001402"))
	if err != nil {
		t.Fatalf("AcquireScratch(reuse) error = %v", err)
	}
	if _, err := manager.ResolveRunWorkDir(t.Context(), uuid.MustParse("00000000-0000-4000-8000-000000001401")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ResolveRunWorkDir(previous) error = %v, want fs.ErrNotExist", err)
	}
	if got, err := manager.ResolveRunWorkDir(t.Context(), uuid.MustParse("00000000-0000-4000-8000-000000001402")); err != nil || got != second.WorkDir {
		t.Fatalf("ResolveRunWorkDir(latest) = %q, %v, want %q", got, err, second.WorkDir)
	}
}

func TestManagerResolveRunWorkDirHonorsRetention(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	manager := newWorkspaceAccessTestManager(t, func() time.Time { return now })
	acquired, err := manager.AcquireScratch(
		"01900000-0000-4000-8000-0000000000e2",
		uuid.MustParse("00000000-0000-4000-8000-000000001403"),
	)
	if err != nil {
		t.Fatalf("AcquireScratch() error = %v", err)
	}
	if err := manager.MarkCompleted(acquired.Root); err != nil {
		t.Fatalf("MarkCompleted() error = %v", err)
	}
	now = now.Add(24 * time.Hour)

	if _, err := manager.ResolveRunWorkDir(t.Context(), uuid.MustParse("00000000-0000-4000-8000-000000001403")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ResolveRunWorkDir(expired) error = %v, want fs.ErrNotExist", err)
	}
}

// A superseded root has no retention clock, and the files a Run left in it
// stay readable for as long as it is kept. Review is never offered for a
// generation a handoff ended.
func TestManagerResolveRunWorkDirReadsASupersededRootButNeverReviewsIt(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	manager := newWorkspaceAccessTestManager(t, func() time.Time { return now })
	conversationID := "01900000-0000-4000-8000-0000000000e7"
	runID := uuid.MustParse("00000000-0000-4000-8000-000000001411")
	first, err := manager.AcquireScopedScratch(
		t.Context(), taskMergeTestCompanyID, "engineering", conversationID, runID, 0,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(generation 0) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(first.WorkDir, "report.md"), []byte("generated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkSuperseded(first.Root); err != nil {
		t.Fatalf("MarkSuperseded() error = %v", err)
	}
	now = now.Add(30 * 24 * time.Hour)

	if got, err := manager.ResolveRunWorkDir(t.Context(), runID); err != nil || got != first.WorkDir {
		t.Fatalf("ResolveRunWorkDir(superseded) = %q, %v, want %q", got, err, first.WorkDir)
	}
	if _, err := manager.ReviewTask(t.Context(), runID); !errors.Is(err, ErrTaskReviewUnavailable) {
		t.Fatalf("ReviewTask(superseded) error = %v, want ErrTaskReviewUnavailable", err)
	}
}

func TestManagerResolveMainReturnsOnlyManagedRealDirectory(t *testing.T) {
	t.Parallel()

	manager := newWorkspaceAccessTestManager(t, time.Now)
	acquired, err := manager.AcquireScopedScratch(
		t.Context(),
		taskMergeTestCompanyID,
		"engineering",
		taskMergeTestConversationID,
		uuid.MustParse("00000000-0000-4000-8000-000000001404"),
		0,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(, 0) error = %v", err)
	}
	want := filepath.Join(filepath.Dir(filepath.Dir(acquired.Root)), mainDirectoryName)
	got, err := manager.ResolveMain(t.Context(), taskMergeTestCompanyID, "engineering")
	if err != nil {
		t.Fatalf("ResolveMain() error = %v", err)
	}
	if got != want {
		t.Fatalf("ResolveMain() = %q, want %q", got, want)
	}
	if _, err := manager.ResolveMain(t.Context(), taskMergeTestCompanyID, "../engineering"); err == nil {
		t.Fatal("ResolveMain(invalid scope) error = nil, want error")
	}
}

func TestManagerReviewTaskIsReadOnlyAndUsesMergeSemantics(t *testing.T) {
	t.Parallel()

	manager, root := newTaskMergeTestManager(t)
	runID := uuid.MustParse("00000000-0000-4000-8000-000000001405")
	acquired, err := manager.AcquireScopedScratch(
		t.Context(),
		taskMergeTestCompanyID,
		"engineering",
		taskMergeTestConversationID,
		runID,
		0,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(, 0) error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(acquired.WorkDir, "result.txt"),
		[]byte("review me\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkCompleted(acquired.Root); err != nil {
		t.Fatalf("MarkCompleted() error = %v", err)
	}

	review, err := manager.ReviewTask(t.Context(), runID)
	if err != nil {
		t.Fatalf("ReviewTask() error = %v", err)
	}
	if review.Status != TaskReviewReady || review.BaseRef != "main" ||
		review.HeadRef != taskBranch(runID) || !strings.Contains(review.Diff, "review me") {
		t.Fatalf("ReviewTask() = %+v", review)
	}
	mainDir := filepath.Join(root, taskMergeTestCompanyID, "engineering", mainDirectoryName)
	if _, err := os.Stat(filepath.Join(mainDir, "result.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReviewTask() mutated main: stat error = %v", err)
	}
	if dirty, err := gitDirty(t.Context(), mainDir); err != nil || dirty {
		t.Fatalf("ReviewTask() left main dirty = %v, %v", dirty, err)
	}
}

func TestManagerMergeTaskMergesIdempotently(t *testing.T) {
	t.Parallel()

	manager, root := newTaskMergeTestManager(t)
	runID := uuid.MustParse("00000000-0000-4000-8000-000000001406")
	conversationID := "01900000-0000-4000-8000-0000000000e3"
	acquired, err := manager.AcquireScopedScratch(
		t.Context(),
		taskMergeTestCompanyID,
		"engineering",
		conversationID,
		runID,
		0,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(, 0) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(acquired.WorkDir, "manual.txt"), []byte("accepted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkCompleted(acquired.Root); err != nil {
		t.Fatalf("MarkCompleted() error = %v", err)
	}

	input := TaskMergeInput{
		CompanyID: taskMergeTestCompanyID, Scope: "engineering",
		ConversationID: conversationID, RunID: runID,
		LeaseFence: func(context.Context) error { return nil },
	}
	first, err := manager.MergeTask(t.Context(), input)
	if err != nil || first.ResultCode != "" || !validCommitID(first.MergedCommit) {
		t.Fatalf("AcceptTask() = %+v, %v", first, err)
	}
	mainDir := filepath.Join(root, taskMergeTestCompanyID, "engineering", mainDirectoryName)
	wantCount := strings.TrimSpace(runTaskMergeTestGit(t, mainDir, "rev-list", "--count", "main"))
	second, err := manager.MergeTask(t.Context(), input)
	if err != nil || second != first {
		t.Fatalf("AcceptTask(replay) = %+v, %v, want %+v", second, err, first)
	}
	if got := strings.TrimSpace(runTaskMergeTestGit(t, mainDir, "rev-list", "--count", "main")); got != wantCount {
		t.Fatalf("AcceptTask(replay) commit count = %s, want %s", got, wantCount)
	}
}

func TestManagerMergeTaskSerializesConcurrentClaims(t *testing.T) {
	t.Parallel()

	manager, root := newTaskMergeTestManager(t)
	runID := uuid.MustParse("00000000-0000-4000-8000-000000001407")
	conversationID := "01900000-0000-4000-8000-0000000000e4"
	acquired, err := manager.AcquireScopedScratch(
		t.Context(),
		taskMergeTestCompanyID,
		"product",
		conversationID,
		runID,
		0,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(, 0) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(acquired.WorkDir, "serialized.txt"), []byte("once\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkCompleted(acquired.Root); err != nil {
		t.Fatalf("MarkCompleted() error = %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	proactiveResult := make(chan TaskMergeResult, 1)
	proactiveErr := make(chan error, 1)
	var fenceCalls int
	go func() {
		result, mergeErr := manager.MergeTask(context.Background(), TaskMergeInput{
			CompanyID:      taskMergeTestCompanyID,
			Scope:          "product",
			ConversationID: conversationID,
			RunID:          runID,
			LeaseFence: func(context.Context) error {
				fenceCalls++
				if fenceCalls == 1 {
					close(entered)
					<-release
				}
				return nil
			},
		})
		proactiveResult <- result
		proactiveErr <- mergeErr
	}()
	<-entered
	manualResult := make(chan TaskMergeResult, 1)
	manualErr := make(chan error, 1)
	go func() {
		result, acceptErr := manager.MergeTask(context.Background(), TaskMergeInput{
			CompanyID: taskMergeTestCompanyID, Scope: "product",
			ConversationID: conversationID, RunID: runID,
			LeaseFence: func(context.Context) error { return nil },
		})
		manualResult <- result
		manualErr <- acceptErr
	}()
	close(release)
	first := <-proactiveResult
	if err := <-proactiveErr; err != nil {
		t.Fatalf("MergeTask() error = %v", err)
	}
	second := <-manualResult
	if err := <-manualErr; err != nil {
		t.Fatalf("AcceptTask() error = %v", err)
	}
	if first != second || !validCommitID(first.MergedCommit) {
		t.Fatalf("serialized results = %+v and %+v", first, second)
	}
	mainDir := filepath.Join(root, taskMergeTestCompanyID, "product", mainDirectoryName)
	if got := strings.TrimSpace(runTaskMergeTestGit(
		t,
		mainDir,
		"log",
		"--format=%s",
		"--grep=^"+taskMergeCommitSubject(runID)+"$",
	)); got != taskMergeCommitSubject(runID) {
		t.Fatalf("serialized merge subjects = %q, want one exact commit", got)
	}
}

func newWorkspaceAccessTestManager(t *testing.T, now func() time.Time) *Manager {
	t.Helper()
	dataRoot := t.TempDir()
	manager, err := NewManager(
		filepath.Join(dataRoot, "workspaces"),
		filepath.Join(dataRoot, "repositories"),
		testMetadataKey,
		repocache.OSRunner{},
		Retention{Completed: 24 * time.Hour, Orphaned: 72 * time.Hour},
		now,
	)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return manager
}
