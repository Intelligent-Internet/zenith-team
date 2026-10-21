package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestRunIdentityMigrationPreservesWorkAndResumesAfterBranchRename(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "interrupted"}[interrupted], func(t *testing.T) {
			manager, _ := newTaskMergeTestManager(t)
			runID := uuid.New()
			acquired, err := manager.AcquireScopedScratch(
				t.Context(),
				taskMergeTestCompanyID,
				"engineering",
				taskMergeTestConversationID,
				runID,
				0,
			)
			if err != nil {
				t.Fatal(err)
			}
			delete(manager.inUse, taskMergeTestConversationID) // Simulate a stopped daemon.
			entry, err := manager.readMetadata(acquired.Root)
			if err != nil {
				t.Fatal(err)
			}
			entry.LastRunID = "1207"
			entry.Branch = "zenith-team/task-1207"
			if err := manager.writeMetadata(acquired.Root, entry); err != nil {
				t.Fatal(err)
			}
			if !interrupted {
				runTaskMergeTestGit(t, acquired.WorkDir, "branch", "-m", entry.Branch)
			}
			content := filepath.Join(acquired.WorkDir, "unmerged.txt")
			if err := os.WriteFile(content, []byte("preserve my work\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := strings.TrimSpace(runTaskMergeTestGit(t, acquired.WorkDir, "rev-parse", "HEAD"))
			if _, err := manager.MigrateRunIdentities(
				t.Context(),
				map[string]uuid.UUID{},
				true,
			); err == nil {
				t.Fatal("missing mapping accepted")
			}
			changes, err := manager.MigrateRunIdentities(
				t.Context(),
				map[string]uuid.UUID{"1207": runID},
				false,
			)
			if err != nil || len(changes) != 1 {
				t.Fatalf("preview = %v, %v", changes, err)
			}
			unchanged, err := manager.readMetadata(acquired.Root)
			if err != nil || unchanged.LastRunID != "1207" {
				t.Fatalf("preview modified metadata: %+v, %v", unchanged, err)
			}
			changes, err = manager.MigrateRunIdentities(
				t.Context(),
				map[string]uuid.UUID{"1207": runID},
				true,
			)
			if err != nil || len(changes) != 1 {
				t.Fatalf("apply = %v, %v", changes, err)
			}
			got, err := manager.ResolveRunWorkDir(t.Context(), runID)
			if err != nil || got != acquired.WorkDir {
				t.Fatalf("resolved path = %s, %v", got, err)
			}
			if head := strings.TrimSpace(runTaskMergeTestGit(t, got, "rev-parse", "HEAD")); head != before {
				t.Fatal("migration changed commit")
			}
			if branch := strings.TrimSpace(
				runTaskMergeTestGit(t, got, "symbolic-ref", "--short", "HEAD"),
			); branch != taskBranch(
				runID,
			) {
				t.Fatalf("branch = %s", branch)
			}
			body, err := os.ReadFile(content)
			if err != nil || string(body) != "preserve my work\n" {
				t.Fatalf("unmerged content = %q, %v", body, err)
			}
			changes, err = manager.MigrateRunIdentities(
				t.Context(),
				map[string]uuid.UUID{"1207": runID},
				true,
			)
			if err != nil || len(changes) != 0 {
				t.Fatalf("replay = %v, %v", changes, err)
			}
		})
	}
}

func TestRunIdentityMigrationRetainsCompletedMergeProof(t *testing.T) {
	manager, root := newTaskMergeTestManager(t)
	runID := uuid.New()
	acquired, err := manager.AcquireScopedScratch(
		t.Context(),
		taskMergeTestCompanyID,
		"engineering",
		taskMergeTestConversationID,
		runID,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	delete(manager.inUse, taskMergeTestConversationID)
	entry, err := manager.readMetadata(acquired.Root)
	if err != nil {
		t.Fatal(err)
	}
	entry.LastRunID, entry.Branch = "1207", "zenith-team/task-1207"
	if err := manager.writeMetadata(acquired.Root, entry); err != nil {
		t.Fatal(err)
	}
	runTaskMergeTestGit(t, acquired.WorkDir, "branch", "-m", entry.Branch)
	mainDir := filepath.Join(root, taskMergeTestCompanyID, "engineering", "main")
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
		"Merge Zenith Team task run 1207",
	)
	before := strings.TrimSpace(runTaskMergeTestGit(t, mainDir, "rev-parse", "HEAD"))
	if _, err := manager.MigrateRunIdentities(t.Context(), map[string]uuid.UUID{"1207": runID}, true); err != nil {
		t.Fatal(err)
	}
	review, err := manager.ReviewTask(t.Context(), runID)
	if err != nil || review.Status != TaskReviewMerged {
		t.Fatalf("converted merge review = %+v, %v", review, err)
	}
	if head := strings.TrimSpace(runTaskMergeTestGit(t, mainDir, "rev-parse", "HEAD")); head != before {
		t.Fatal("migration rewrote main")
	}
}
