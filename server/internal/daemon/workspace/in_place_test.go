package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A Run that executed in place — in the directory a Company Resource binds
// to this daemon — leaves a signed receipt of the commits it moved HEAD
// across, so Task Review can show exactly the Run's own changes and file
// access still resolves the Run after the daemon restarts. The receipt lives
// in the Run's environment root, not in the person's repository.
func TestInPlaceRunReviewShowsTheRunsOwnCommits(t *testing.T) {
	t.Parallel()
	manager, _ := newTaskMergeTestManager(t)
	localDir := newInPlaceTestRepository(t)
	runID := uuid.MustParse("00000000-0000-4000-8000-000000001501")
	identity := InPlaceRunIdentity{
		CompanyID: taskMergeTestCompanyID, ConversationID: taskMergeTestConversationID,
		TaskID: "00000000-0000-4000-8000-0000000015aa",
	}
	base := strings.TrimSpace(runTaskMergeTestGit(t, localDir, "rev-parse", "HEAD"))

	if err := manager.RecordInPlaceRunStart(t.Context(), runID, identity, localDir); err != nil {
		t.Fatalf("RecordInPlaceRunStart() error = %v", err)
	}
	if got, err := manager.ResolveRunWorkDir(t.Context(), runID); err != nil || got != localDir {
		t.Fatalf("ResolveRunWorkDir(in place) = %q, %v; want %q", got, err, localDir)
	}
	receiptPath := filepath.Join(manager.root, LocalScratchDirectoryName, runID.String(), inPlaceReceiptFileName)
	if _, err := os.Stat(receiptPath); err != nil {
		t.Fatalf("receipt in the Run environment: %v", err)
	}
	if entries, err := os.ReadDir(localDir); err != nil || len(entries) != 2 {
		t.Fatalf("the bound directory must hold only .git and README.md, got %v (%v)", entries, err)
	}

	// The Run commits its work, then ends.
	if err := os.WriteFile(filepath.Join(localDir, "feature.txt"), []byte("built\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commitTaskMergeTestFiles(t, localDir, "Build the feature")
	head := strings.TrimSpace(runTaskMergeTestGit(t, localDir, "rev-parse", "HEAD"))
	if err := manager.CompleteInPlaceRun(t.Context(), runID); err != nil {
		t.Fatalf("CompleteInPlaceRun() error = %v", err)
	}

	review, err := manager.ReviewTask(t.Context(), runID)
	if err != nil {
		t.Fatalf("ReviewTask(in place) error = %v", err)
	}
	if review.Status != TaskReviewReady || review.RunID != runID ||
		review.BaseRef != base[:12] || review.HeadRef != head[:12] ||
		!strings.Contains(review.Diff, "+++ b/feature.txt") || !strings.Contains(review.Diff, "+built") ||
		!strings.Contains(review.Message, "committed in place") {
		t.Fatalf("ReviewTask(in place) = %+v", review)
	}

	// Later work on top of the Run's commit leaves the Run's own diff intact.
	if err := os.WriteFile(filepath.Join(localDir, "later.txt"), []byte("later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commitTaskMergeTestFiles(t, localDir, "Later work")
	later, err := manager.ReviewTask(t.Context(), runID)
	if err != nil || later.Status != TaskReviewReady || later.Diff != review.Diff {
		t.Fatalf("ReviewTask(after later commit) = %+v, %v; want the Run's own diff", later, err)
	}

	// Uncommitted changes block review exactly as they block acceptance.
	if err := os.WriteFile(filepath.Join(localDir, "later.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty, err := manager.ReviewTask(t.Context(), runID)
	if err != nil || dirty.Status != TaskReviewMainDirty || dirty.Diff != "" {
		t.Fatalf("ReviewTask(dirty) = %+v, %v", dirty, err)
	}
	runTaskMergeTestGit(t, localDir, "checkout", "--", "later.txt")

	// Rewritten history means the Run's commit is no longer in the tree: the
	// exact retained identity cannot be proven, so review is unavailable.
	runTaskMergeTestGit(t, localDir, "reset", "--hard", base)
	if _, err := manager.ReviewTask(t.Context(), runID); !errors.Is(err, ErrTaskReviewUnavailable) {
		t.Fatalf("ReviewTask(rewritten history) error = %v, want %v", err, ErrTaskReviewUnavailable)
	}
}

func TestInPlaceRunReviewWithoutCommitsAndWithoutReceipt(t *testing.T) {
	t.Parallel()
	manager, _ := newTaskMergeTestManager(t)
	localDir := newInPlaceTestRepository(t)
	runID := uuid.MustParse("00000000-0000-4000-8000-000000001502")
	identity := InPlaceRunIdentity{
		CompanyID: taskMergeTestCompanyID, ConversationID: taskMergeTestConversationID,
	}

	if _, err := manager.ReviewTask(t.Context(), runID); !errors.Is(err, ErrTaskReviewUnavailable) {
		t.Fatalf("ReviewTask(unknown Run) error = %v, want %v", err, ErrTaskReviewUnavailable)
	}
	if err := manager.RecordInPlaceRunStart(t.Context(), runID, identity, localDir); err != nil {
		t.Fatalf("RecordInPlaceRunStart() error = %v", err)
	}
	if err := manager.CompleteInPlaceRun(t.Context(), runID); err != nil {
		t.Fatalf("CompleteInPlaceRun() error = %v", err)
	}
	review, err := manager.ReviewTask(t.Context(), runID)
	if err != nil || review.Status != TaskReviewReady || review.Diff != "" ||
		review.Message != "No file changes were found." {
		t.Fatalf("ReviewTask(no commits) = %+v, %v", review, err)
	}

	// A receipt that fails authentication is unusable, never trusted.
	receiptPath := filepath.Join(manager.root, LocalScratchDirectoryName, runID.String(), inPlaceReceiptFileName)
	contents, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(contents), localDir, t.TempDir(), 1)
	if tampered == string(contents) {
		t.Fatal("receipt does not name the local directory")
	}
	if err := os.WriteFile(receiptPath, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ReviewTask(t.Context(), runID); !errors.Is(err, ErrTaskReviewUnavailable) {
		t.Fatalf("ReviewTask(tampered receipt) error = %v, want %v", err, ErrTaskReviewUnavailable)
	}
	if _, err := manager.ResolveRunWorkDir(t.Context(), runID); err == nil {
		t.Fatal("ResolveRunWorkDir(tampered receipt) resolved a directory it cannot vouch for")
	}
}

// A managed worktree is collected on a clock, so its record lapses with
// it. An in-place Run's commits stay in the person's repository for good,
// and a review may come days after the Run — the receipt outlives the
// worktree retention window.
func TestInPlaceRunReceiptOutlivesWorktreeRetention(t *testing.T) {
	t.Parallel()
	current := time.Date(2026, time.September, 17, 9, 0, 0, 0, time.UTC)
	manager := newWorkspaceAccessTestManager(t, func() time.Time { return current })
	localDir := newInPlaceTestRepository(t)
	runID := uuid.MustParse("00000000-0000-4000-8000-000000001503")
	if err := manager.RecordInPlaceRunStart(t.Context(), runID, InPlaceRunIdentity{
		CompanyID: taskMergeTestCompanyID, ConversationID: taskMergeTestConversationID,
	}, localDir); err != nil {
		t.Fatalf("RecordInPlaceRunStart() error = %v", err)
	}
	if err := manager.CompleteInPlaceRun(t.Context(), runID); err != nil {
		t.Fatalf("CompleteInPlaceRun() error = %v", err)
	}
	current = current.Add(30 * 24 * time.Hour)
	if _, err := manager.ReviewTask(t.Context(), runID); err != nil {
		t.Fatalf("ReviewTask(a month later) error = %v", err)
	}
	if got, err := manager.ResolveRunWorkDir(t.Context(), runID); err != nil || got != localDir {
		t.Fatalf("ResolveRunWorkDir(a month later) = %q, %v", got, err)
	}
	// A directory that is gone is the one thing that ends the receipt.
	if err := os.RemoveAll(localDir); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ReviewTask(t.Context(), runID); !errors.Is(err, ErrTaskReviewUnavailable) {
		t.Fatalf("ReviewTask(directory gone) error = %v, want %v", err, ErrTaskReviewUnavailable)
	}
}

func newInPlaceTestRepository(t *testing.T) string {
	t.Helper()
	localDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runTaskMergeTestGit(t, localDir, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commitTaskMergeTestFiles(t, localDir, "Initial commit")
	return localDir
}
