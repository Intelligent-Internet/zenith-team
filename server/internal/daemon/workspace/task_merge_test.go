package workspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon/repocache"
)

const (
	taskMergeTestCompanyID      = "01900000-0000-4000-8000-0000000000d1"
	taskMergeTestConversationID = "01900000-0000-4000-8000-0000000000d2"
)

func TestManagerReviewAndMergeTaskWithLargeBinaryFiles(t *testing.T) {
	t.Parallel()

	image := make([]byte, 5<<20)
	if _, err := rand.Read(image); err != nil {
		t.Fatal(err)
	}
	image[0] = 0 // Git treats the incompressible payload as binary.
	base := []byte("original\x00image")
	concurrent := []byte("concurrent\x00image")

	tests := []struct {
		name       string
		base       []byte
		updated    []byte
		path       string
		concurrent []byte
		wantStatus TaskReviewStatus
	}{
		{name: "add", updated: image, path: "brand.png", wantStatus: TaskReviewReady},
		{name: "modify", base: base, updated: image, path: "brand.png", wantStatus: TaskReviewReady},
		{name: "delete", base: image, path: "brand.png", wantStatus: TaskReviewReady},
		{name: "rename", base: image, updated: image, path: "renamed.png", wantStatus: TaskReviewReady},
		{
			name:       "conflict",
			base:       base,
			updated:    image,
			path:       "brand.png",
			concurrent: concurrent,
			wantStatus: TaskReviewConflict,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, root := newTaskMergeTestManager(t)
			seed, err := manager.AcquireScopedScratch(
				t.Context(),
				taskMergeTestCompanyID,
				"product",
				"01900000-0000-4000-8000-0000000000d3",
				uuid.MustParse("00000000-0000-4000-8000-000000001600"),
				0,
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.MarkCompleted(seed.Root); err != nil {
				t.Fatal(err)
			}
			mainDir := filepath.Join(root, taskMergeTestCompanyID, "product", mainDirectoryName)
			mainImage := filepath.Join(mainDir, "brand.png")
			if tt.base != nil {
				if err := os.WriteFile(mainImage, tt.base, 0o600); err != nil {
					t.Fatal(err)
				}
				commitTaskMergeTestFiles(t, mainDir, "Add original image")
			}

			runID := uuid.MustParse("00000000-0000-4000-8000-000000001601")
			acquired, err := manager.AcquireScopedScratch(
				t.Context(), taskMergeTestCompanyID, "product", taskMergeTestConversationID, runID,
				0,
			)
			if err != nil {
				t.Fatal(err)
			}
			if tt.base != nil && (tt.updated == nil || tt.path != "brand.png") {
				if err := os.Remove(filepath.Join(acquired.WorkDir, "brand.png")); err != nil {
					t.Fatal(err)
				}
			}
			if tt.updated != nil {
				if err := os.WriteFile(
					filepath.Join(acquired.WorkDir, tt.path),
					tt.updated,
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			}
			brief := []byte("Updated brand brief\n")
			if err := os.WriteFile(filepath.Join(acquired.WorkDir, "brief.md"), brief, 0o600); err != nil {
				t.Fatal(err)
			}
			wantBefore := tt.base
			if tt.concurrent != nil {
				if err := os.WriteFile(mainImage, tt.concurrent, 0o600); err != nil {
					t.Fatal(err)
				}
				commitTaskMergeTestFiles(t, mainDir, "Update image on main")
				wantBefore = tt.concurrent
			}
			if err := manager.MarkCompleted(acquired.Root); err != nil {
				t.Fatal(err)
			}
			beforeHead := runTaskMergeTestGit(t, mainDir, "rev-parse", "HEAD")

			review, err := manager.ReviewTask(t.Context(), runID)
			if err != nil {
				t.Fatalf("ReviewTask() error = %v", err)
			}
			if review.Status != tt.wantStatus {
				t.Fatalf("ReviewTask() status = %q, want %q", review.Status, tt.wantStatus)
			}
			if tt.wantStatus == TaskReviewReady {
				if !strings.Contains(review.Diff, tt.path) ||
					!strings.Contains(review.Diff, "+Updated brand brief") {
					t.Fatalf("review is missing image or text changes: %s", review.Diff)
				}
				if len(review.Diff) > 4096 || strings.Contains(review.Diff, "GIT binary patch") {
					t.Fatalf("review embeds binary contents: %d bytes", len(review.Diff))
				}
			}
			assertTaskMergeTestFile(t, mainImage, wantBefore)
			assertTaskMergeTestFile(t, filepath.Join(mainDir, "brief.md"), nil)
			if got := runTaskMergeTestGit(t, mainDir, "rev-parse", "HEAD"); got != beforeHead {
				t.Fatalf("main HEAD after review = %q, want %q", got, beforeHead)
			}
			if got := strings.TrimSpace(
				runTaskMergeTestGit(t, mainDir, "status", "--porcelain"),
			); got != "" {
				t.Fatalf("main status after review = %q, want clean", got)
			}

			result, err := manager.MergeTask(t.Context(), TaskMergeInput{
				CompanyID:      taskMergeTestCompanyID,
				Scope:          "product",
				ConversationID: taskMergeTestConversationID,
				RunID:          runID,
				LeaseFence:     allowTaskMerge,
			})
			if tt.wantStatus == TaskReviewConflict {
				if err == nil || result.ResultCode != "conflict" || result.MergedCommit != "" {
					t.Fatalf("MergeTask() = %+v, %v, want conflict", result, err)
				}
				assertTaskMergeTestFile(t, mainImage, wantBefore)
				assertTaskMergeTestFile(t, filepath.Join(mainDir, "brief.md"), nil)
				if got := runTaskMergeTestGit(t, mainDir, "rev-parse", "HEAD"); got != beforeHead {
					t.Fatalf("main HEAD after conflict = %q, want %q", got, beforeHead)
				}
			} else {
				if err != nil || result.ResultCode != "" || !validCommitID(result.MergedCommit) {
					t.Fatalf("MergeTask() = %+v, %v", result, err)
				}
				assertTaskMergeTestFile(t, filepath.Join(mainDir, tt.path), tt.updated)
				assertTaskMergeTestFile(t, filepath.Join(mainDir, "brief.md"), brief)
				if tt.path != "brand.png" {
					assertTaskMergeTestFile(t, mainImage, nil)
				}
			}
			if got := strings.TrimSpace(
				runTaskMergeTestGit(t, mainDir, "status", "--porcelain"),
			); got != "" {
				t.Fatalf("main status after merge = %q, want clean", got)
			}
		})
	}
}

func TestManagerMergeTaskUsesSignedScopedIdentityAndIsIdempotent(t *testing.T) {
	t.Parallel()

	manager, root := newTaskMergeTestManager(t)
	runID := uuid.MustParse("00000000-0000-4000-8000-000000001207")
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
	wantRoot := filepath.Join(
		root,
		taskMergeTestCompanyID,
		"engineering",
		"worktrees",
		taskMergeTestConversationID,
	)
	if acquired.Root != wantRoot || acquired.WorkDir != filepath.Join(wantRoot, workdirDirectoryName) {
		t.Fatalf("AcquireScopedScratch(, 0) = %+v, want root %q", acquired, wantRoot)
	}
	entry, err := manager.readMetadata(acquired.Root)
	if err != nil {
		t.Fatalf("readMetadata() error = %v", err)
	}
	if entry.Version != metadataVersion || entry.CompanyID != taskMergeTestCompanyID ||
		entry.Scope != "engineering" || entry.ConversationID != taskMergeTestConversationID ||
		entry.LastRunID != runID.String() || entry.Branch != taskBranch(runID) ||
		entry.State != workspaceActive || entry.Signature == "" {
		t.Fatalf("scoped metadata = %+v", entry)
	}

	if err := os.WriteFile(
		filepath.Join(acquired.WorkDir, "result.txt"),
		[]byte("task result\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkCompleted(acquired.Root); err != nil {
		t.Fatalf("MarkCompleted() error = %v", err)
	}

	mainDir := filepath.Join(root, taskMergeTestCompanyID, "engineering", mainDirectoryName)
	runTaskMergeTestGit(
		t,
		mainDir,
		"-c",
		"user.name=Test",
		"-c",
		"user.email=test@zenith-team.local",
		"commit",
		"--allow-empty",
		"-m",
		"Merge Zenith Team task run 12070",
	)

	input := TaskMergeInput{
		CompanyID:      taskMergeTestCompanyID,
		Scope:          "engineering",
		ConversationID: taskMergeTestConversationID,
		RunID:          runID,
		LeaseFence:     allowTaskMerge,
	}
	result, err := manager.MergeTask(t.Context(), input)
	if err != nil {
		t.Fatalf("MergeTask() error = %v", err)
	}
	if result.ResultCode != "" || !validCommitID(result.MergedCommit) {
		t.Fatalf("MergeTask() result = %+v", result)
	}
	merged, err := os.ReadFile(filepath.Join(mainDir, "result.txt"))
	if err != nil || string(merged) != "task result\n" {
		t.Fatalf("merged result = %q, %v", merged, err)
	}
	if got := strings.TrimSpace(
		runTaskMergeTestGit(t, mainDir, "show", "-s", "--format=%s", result.MergedCommit),
	); got != "Merge Zenith Team task run "+input.RunID.String() {
		t.Fatalf("merge subject = %q, want exact Run identity", got)
	}
	beforeReplay := strings.TrimSpace(runTaskMergeTestGit(t, mainDir, "rev-list", "--count", "main"))
	replay, err := manager.MergeTask(t.Context(), input)
	if err != nil {
		t.Fatalf("MergeTask() replay error = %v", err)
	}
	afterReplay := strings.TrimSpace(runTaskMergeTestGit(t, mainDir, "rev-list", "--count", "main"))
	if replay.MergedCommit != result.MergedCommit || afterReplay != beforeReplay {
		t.Fatalf(
			"MergeTask() replay = %+v, commits %s -> %s, want the original commit",
			replay,
			beforeReplay,
			afterReplay,
		)
	}

	wrongRun := input
	wrongRun.RunID = uuid.New()
	blocked, err := manager.MergeTask(t.Context(), wrongRun)
	if err == nil || blocked.ResultCode != "workspace_unavailable" || blocked.MergedCommit != "" {
		t.Fatalf("MergeTask() wrong Run = %+v, %v, want unavailable", blocked, err)
	}
}

func TestManagerMergeTaskLeavesMainCleanOnConflict(t *testing.T) {
	t.Parallel()

	manager, root := newTaskMergeTestManager(t)
	seedConversationID := "01900000-0000-4000-8000-0000000000d3"
	seed, err := manager.AcquireScopedScratch(
		t.Context(),
		taskMergeTestCompanyID,
		"product",
		seedConversationID,
		uuid.MustParse("00000000-0000-4000-8000-000000001300"),
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkCompleted(seed.Root); err != nil {
		t.Fatal(err)
	}
	mainDir := filepath.Join(root, taskMergeTestCompanyID, "product", mainDirectoryName)
	if err := os.WriteFile(filepath.Join(mainDir, "shared.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commitTaskMergeTestFiles(t, mainDir, "Add shared file")

	conversationID := "01900000-0000-4000-8000-0000000000d4"
	runID := uuid.MustParse("00000000-0000-4000-8000-000000001301")
	acquired, err := manager.AcquireScopedScratch(
		t.Context(),
		taskMergeTestCompanyID,
		"product",
		conversationID,
		runID,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(acquired.WorkDir, "shared.txt"), []byte("task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainDir, "shared.txt"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commitTaskMergeTestFiles(t, mainDir, "Move main")
	if err := manager.MarkCompleted(acquired.Root); err != nil {
		t.Fatal(err)
	}

	result, err := manager.MergeTask(t.Context(), TaskMergeInput{
		CompanyID:      taskMergeTestCompanyID,
		Scope:          "product",
		ConversationID: conversationID,
		RunID:          runID,
		LeaseFence:     allowTaskMerge,
	})
	if err == nil || result.ResultCode != "conflict" || result.MergedCommit != "" {
		t.Fatalf("MergeTask() conflict = %+v, %v", result, err)
	}
	content, readErr := os.ReadFile(filepath.Join(mainDir, "shared.txt"))
	if readErr != nil || string(content) != "main\n" {
		t.Fatalf("main content after conflict = %q, %v", content, readErr)
	}
	if got := strings.TrimSpace(runTaskMergeTestGit(t, mainDir, "status", "--porcelain")); got != "" {
		t.Fatalf("main status after conflict = %q, want clean", got)
	}
}

func TestManagerMergeTaskVerifiesLocalDirectoryHead(t *testing.T) {
	t.Parallel()

	manager, _ := newTaskMergeTestManager(t)
	localDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runTaskMergeTestGit(t, localDir, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commitTaskMergeTestFiles(t, localDir, "Initial commit")
	head := strings.TrimSpace(runTaskMergeTestGit(t, localDir, "rev-parse", "HEAD"))

	input := TaskMergeInput{
		CompanyID:      taskMergeTestCompanyID,
		Scope:          "engineering",
		ConversationID: taskMergeTestConversationID,
		RunID:          uuid.MustParse("00000000-0000-4000-8000-000000001401"),
		LocalPath:      localDir,
		LeaseFence:     allowTaskMerge,
	}
	result, err := manager.MergeTask(t.Context(), input)
	if err != nil || result.MergedCommit != head || result.ResultCode != "" {
		t.Fatalf("MergeTask(local) = %+v, %v, want HEAD %q", result, err, head)
	}
	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	blocked, err := manager.MergeTask(t.Context(), input)
	if err == nil || blocked.ResultCode != "main_dirty" || blocked.MergedCommit != "" {
		t.Fatalf("MergeTask(dirty local) = %+v, %v", blocked, err)
	}
}

func TestManagerMergeTaskFencesLocalDirectoryBeforeGitInspection(t *testing.T) {
	t.Parallel()

	manager, _ := newTaskMergeTestManager(t)
	wantErr := errors.New("lease is no longer owned")
	result, err := manager.MergeTask(t.Context(), TaskMergeInput{
		CompanyID:      taskMergeTestCompanyID,
		Scope:          "engineering",
		ConversationID: taskMergeTestConversationID,
		RunID:          uuid.MustParse("00000000-0000-4000-8000-000000001402"),
		LocalPath:      filepath.Join(t.TempDir(), "not-a-repository"),
		LeaseFence: func(context.Context) error {
			return wantErr
		},
	})
	if !errors.Is(err, wantErr) || result != (TaskMergeResult{}) {
		t.Fatalf("MergeTask(local) = %+v, %v, want empty result and lease error", result, err)
	}
}

func TestManagerMergeTaskRollsBackWhenLeaseFenceFailsAfterApply(t *testing.T) {
	t.Parallel()

	manager, root := newTaskMergeTestManager(t)
	runID := uuid.MustParse("00000000-0000-4000-8000-000000001403")
	conversationID := "01900000-0000-4000-8000-0000000000d5"
	acquired, err := manager.AcquireScopedScratch(
		t.Context(),
		taskMergeTestCompanyID,
		"engineering",
		conversationID,
		runID,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(acquired.WorkDir, "leased.txt"), []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkCompleted(acquired.Root); err != nil {
		t.Fatal(err)
	}

	mainDir := filepath.Join(root, taskMergeTestCompanyID, "engineering", mainDirectoryName)
	originalHead := strings.TrimSpace(runTaskMergeTestGit(t, mainDir, "rev-parse", "HEAD"))
	wantErr := errors.New("lease expired after apply")
	result, err := manager.MergeTask(t.Context(), TaskMergeInput{
		CompanyID:      taskMergeTestCompanyID,
		Scope:          "engineering",
		ConversationID: conversationID,
		RunID:          runID,
		LeaseFence: func(ctx context.Context) error {
			status, statusErr := gitOutput(ctx, mainDir, "status", "--porcelain")
			if statusErr != nil {
				return statusErr
			}
			if strings.TrimSpace(status) != "" {
				return wantErr
			}
			return nil
		},
	})
	if !errors.Is(err, wantErr) || result != (TaskMergeResult{}) {
		t.Fatalf("MergeTask() = %+v, %v, want empty result and lease error", result, err)
	}
	if head := strings.TrimSpace(runTaskMergeTestGit(t, mainDir, "rev-parse", "HEAD")); head != originalHead {
		t.Fatalf("main HEAD = %q, want rollback to %q", head, originalHead)
	}
	if status := strings.TrimSpace(runTaskMergeTestGit(t, mainDir, "status", "--porcelain")); status != "" {
		t.Fatalf("main status after lease loss = %q, want clean", status)
	}
	if _, statErr := os.Stat(filepath.Join(mainDir, "leased.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stale task file remains after rollback: %v", statErr)
	}
}

func TestManagerMergeTaskRollsBackWhenLeaseFenceFailsAfterCommit(t *testing.T) {
	t.Parallel()

	manager, root := newTaskMergeTestManager(t)
	runID := uuid.MustParse("00000000-0000-4000-8000-000000001405")
	conversationID := "01900000-0000-4000-8000-0000000000d8"
	acquired, err := manager.AcquireScopedScratch(
		t.Context(),
		taskMergeTestCompanyID,
		"engineering",
		conversationID,
		runID,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(acquired.WorkDir, "committed.txt"), []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkCompleted(acquired.Root); err != nil {
		t.Fatal(err)
	}

	mainDir := filepath.Join(root, taskMergeTestCompanyID, "engineering", mainDirectoryName)
	originalHead := strings.TrimSpace(runTaskMergeTestGit(t, mainDir, "rev-parse", "HEAD"))
	wantErr := errors.New("lease expired after commit")
	result, err := manager.MergeTask(t.Context(), TaskMergeInput{
		CompanyID:      taskMergeTestCompanyID,
		Scope:          "engineering",
		ConversationID: conversationID,
		RunID:          runID,
		LeaseFence: func(ctx context.Context) error {
			subject, subjectErr := gitOutput(ctx, mainDir, "show", "-s", "--format=%s", "HEAD")
			if subjectErr != nil {
				return subjectErr
			}
			if strings.TrimSpace(subject) == taskMergeCommitSubject(runID) {
				return wantErr
			}
			return nil
		},
	})
	if !errors.Is(err, wantErr) || result != (TaskMergeResult{}) {
		t.Fatalf("MergeTask() = %+v, %v, want empty result and lease error", result, err)
	}
	if head := strings.TrimSpace(runTaskMergeTestGit(t, mainDir, "rev-parse", "HEAD")); head != originalHead {
		t.Fatalf("main HEAD = %q, want rollback to %q", head, originalHead)
	}
	if status := strings.TrimSpace(runTaskMergeTestGit(t, mainDir, "status", "--porcelain")); status != "" {
		t.Fatalf("main status after lease loss = %q, want clean", status)
	}
}

func TestManagerMergeTaskIgnoresSigningHooksAndInheritedGitControl(t *testing.T) {
	manager, root := newTaskMergeTestManager(t)
	runID := uuid.MustParse("00000000-0000-4000-8000-000000001404")
	conversationID := "01900000-0000-4000-8000-0000000000d6"
	acquired, err := manager.AcquireScopedScratch(
		t.Context(),
		taskMergeTestCompanyID,
		"engineering",
		conversationID,
		runID,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(acquired.WorkDir, "safe.txt"), []byte("merged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkCompleted(acquired.Root); err != nil {
		t.Fatal(err)
	}

	mainDir := filepath.Join(root, taskMergeTestCompanyID, "engineering", mainDirectoryName)
	hooksDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(hooksDir, "pre-commit"),
		[]byte("#!/bin/sh\nexit 91\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	runTaskMergeTestGit(t, mainDir, "config", "--local", "core.hooksPath", hooksDir)
	runTaskMergeTestGit(t, mainDir, "config", "--local", "commit.gpgSign", "true")
	runTaskMergeTestGit(t, mainDir, "config", "--local", "user.signingKey", "missing-zenith-team-test-key")

	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "attacker.git"))
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "attacker.index"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "attacker.gitconfig"))

	result, err := manager.MergeTask(t.Context(), TaskMergeInput{
		CompanyID:      taskMergeTestCompanyID,
		Scope:          "engineering",
		ConversationID: conversationID,
		RunID:          runID,
		LeaseFence:     allowTaskMerge,
	})
	if err != nil || !validCommitID(result.MergedCommit) || result.ResultCode != "" {
		t.Fatalf("MergeTask() = %+v, %v", result, err)
	}
	if content, readErr := os.ReadFile(
		filepath.Join(mainDir, "safe.txt"),
	); readErr != nil ||
		string(content) != "merged\n" {
		t.Fatalf("merged file = %q, %v", content, readErr)
	}

	secondConversationID := "01900000-0000-4000-8000-0000000000d7"
	second, err := manager.AcquireScopedScratch(
		t.Context(),
		taskMergeTestCompanyID,
		"product",
		secondConversationID,
		uuid.New(),
		0,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(, 0) with inherited Git control = %v", err)
	}
	if second.WorkDir == "" {
		t.Fatal("AcquireScopedScratch(, 0) returned an empty workdir")
	}
}

func newTaskMergeTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "workspaces")
	manager, err := NewManager(
		root,
		filepath.Join(dataRoot, "repositories"),
		testMetadataKey,
		repocache.OSRunner{},
		Retention{Completed: 24 * time.Hour, Orphaned: 72 * time.Hour},
		time.Now,
	)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return manager, root
}

func commitTaskMergeTestFiles(t *testing.T, dir, message string) {
	t.Helper()
	runTaskMergeTestGit(t, dir, "add", "-A")
	runTaskMergeTestGit(
		t,
		dir,
		"-c",
		"user.name=Test",
		"-c",
		"user.email=test@zenith-team.local",
		"commit",
		"-m",
		message,
	)
}

func assertTaskMergeTestFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if want == nil {
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ReadFile(%q) error = %v, want file absent", path, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("ReadFile(%q) = %d bytes, want original %d bytes", path, len(got), len(want))
	}
}

func runTaskMergeTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(context.Background(), "git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func allowTaskMerge(context.Context) error {
	return nil
}
