package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon/repocache"

	"github.com/google/uuid"
)

var testMetadataKey = []byte("zenith-team-workspace-test-authentication-key-v1")

type fakeGitRunner struct {
	mu    sync.Mutex
	calls [][]string
}

func (r *fakeGitRunner) Run(_ context.Context, _ string, _ []string, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string(nil), args...))
	return nil
}

func (r *fakeGitRunner) joined() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	joined := ""
	for _, call := range r.calls {
		joined += strings.Join(call, " ") + "\n"
	}
	return joined
}

func TestScratchWorkspaceUsesSignedLifecycleAndGCWithoutGit(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &fakeGitRunner{}
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	manager, err := NewManager(root, cacheRoot, testMetadataKey, runner, Retention{
		Completed: 24 * time.Hour, Orphaned: 72 * time.Hour,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	conversationID := "01900000-0000-7000-8000-0000000000a3"
	runID := uuid.MustParse("00000000-0000-4000-8000-000000001067")
	acquired, err := manager.AcquireScratch(conversationID, runID)
	if err != nil {
		t.Fatal(err)
	}
	if acquired.Reused || acquired.Root != filepath.Join(root, conversationID) ||
		acquired.WorkDir != filepath.Join(acquired.Root, workdirDirectoryName) {
		t.Fatalf("acquired scratch = %+v", acquired)
	}
	entry, err := manager.readMetadata(acquired.Root)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Kind != metadataKindManagedScratch || entry.State != workspaceActive ||
		entry.ConversationID != conversationID ||
		entry.LastRunID != runID.String() ||
		entry.CacheName != "" || entry.Branch != "" || entry.Signature == "" {
		t.Fatalf("scratch metadata = %+v", entry)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("scratch creation invoked Git: %+v", runner.calls)
	}
	if err := manager.MarkCompleted(acquired.Root); err != nil {
		t.Fatal(err)
	}
	// Completion keeps the conversation directory for reuse until retention.
	if removed, err := manager.GC(context.Background()); err != nil || len(removed) != 0 {
		t.Fatalf("completed conversation removed before retention: %q / %v", removed, err)
	}
	if _, err := os.Stat(acquired.WorkDir); err != nil {
		t.Fatalf("completed conversation workdir disappeared: %v", err)
	}
	now = now.Add(24 * time.Hour)
	removed, err := manager.GC(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != acquired.Root {
		t.Fatalf("scratch removal = %q, want %q", removed, acquired.Root)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("scratch GC invoked Git: %+v", runner.calls)
	}
}

func TestScratchConversationWorkspaceIsReusedAcrossRuns(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	manager, err := NewManager(root, cacheRoot, testMetadataKey, &fakeGitRunner{}, Retention{
		Completed: 24 * time.Hour, Orphaned: 72 * time.Hour,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	conversationID := "01900000-0000-7000-8000-0000000000a4"
	firstRunID, secondRunID := uuid.MustParse(
		"00000000-0000-4000-8000-000000001071",
	), uuid.MustParse(
		"00000000-0000-4000-8000-000000001072",
	)
	first, err := manager.AcquireScratch(conversationID, firstRunID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Reused {
		t.Fatalf("first acquire reported reuse: %+v", first)
	}
	noteFile := filepath.Join(first.WorkDir, "notes.txt")
	if err := os.WriteFile(noteFile, []byte("conversation context"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkCompleted(first.Root); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	second, err := manager.AcquireScratch(conversationID, secondRunID)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Reused || second.Root != first.Root || second.WorkDir != first.WorkDir {
		t.Fatalf("second acquire = %+v, want reuse of %+v", second, first)
	}
	body, err := os.ReadFile(noteFile)
	if err != nil || string(body) != "conversation context" {
		t.Fatalf("conversation files were not preserved: %q / %v", body, err)
	}
	entry, err := manager.readMetadata(second.Root)
	if err != nil {
		t.Fatal(err)
	}
	if entry.LastRunID != secondRunID.String() || entry.State != workspaceActive ||
		entry.CompletedAt != nil || entry.OrphanedAt != nil {
		t.Fatalf("reused metadata = %+v", entry)
	}
	// The environment survives even a very old prior completion because the
	// active state resets the lifecycle.
	now = now.Add(365 * 24 * time.Hour)
	if err := manager.MarkCompleted(second.Root); err != nil {
		t.Fatal(err)
	}
	if removed, err := manager.GC(context.Background()); err != nil || len(removed) != 0 {
		t.Fatalf("recompleted conversation collected immediately: %q / %v", removed, err)
	}
}

func TestScopedScratchRefreshPreservesPreviousRunBranchAndFiles(t *testing.T) {
	t.Parallel()

	manager, root := newTaskMergeTestManager(t)
	conversationID := "01900000-0000-4000-8000-0000000000e1"
	firstRunID, secondRunID := uuid.MustParse(
		"00000000-0000-4000-8000-000000001601",
	), uuid.MustParse(
		"00000000-0000-4000-8000-000000001602",
	)
	first, err := manager.AcquireScopedScratch(
		t.Context(),
		taskMergeTestCompanyID,
		"engineering",
		conversationID,
		firstRunID,
		0,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(first, 0) error = %v", err)
	}
	persistedPath := filepath.Join(first.WorkDir, "persisted.txt")
	if err := os.WriteFile(persistedPath, []byte("committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commitTaskMergeTestFiles(t, first.WorkDir, "Persist first Run work")
	firstHead := strings.TrimSpace(runTaskMergeTestGit(t, first.WorkDir, "rev-parse", "HEAD"))
	if err := os.WriteFile(persistedPath, []byte("continued\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	continuedPath := filepath.Join(first.WorkDir, "continued.txt")
	if err := os.WriteFile(continuedPath, []byte("uncommitted work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkCompleted(first.Root); err != nil {
		t.Fatalf("MarkCompleted(first) error = %v", err)
	}

	second, err := manager.AcquireScopedScratch(
		t.Context(),
		taskMergeTestCompanyID,
		"engineering",
		conversationID,
		secondRunID,
		0,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(second, 0) error = %v", err)
	}
	if !second.Reused || second.Branch != taskBranch(secondRunID) {
		t.Fatalf("AcquireScopedScratch(second, 0) = %+v", second)
	}
	content, err := os.ReadFile(persistedPath)
	if err != nil || string(content) != "continued\n" {
		t.Fatalf("persisted file = %q, %v", content, err)
	}
	content, err = os.ReadFile(continuedPath)
	if err != nil || string(content) != "uncommitted work\n" {
		t.Fatalf("untracked previous Run file = %q, %v", content, err)
	}
	if got := strings.TrimSpace(runTaskMergeTestGit(t, second.WorkDir, "rev-parse", "HEAD")); got != firstHead {
		t.Fatalf("second Run HEAD = %q, want previous Run HEAD %q", got, firstHead)
	}
	if got := strings.TrimSpace(
		runTaskMergeTestGit(t, second.WorkDir, "branch", "--show-current"),
	); got != taskBranch(secondRunID) {
		t.Fatalf("second Run branch = %q, want %q", got, taskBranch(secondRunID))
	}
	mainDir := filepath.Join(root, taskMergeTestCompanyID, "engineering", mainDirectoryName)
	if got := strings.TrimSpace(
		runTaskMergeTestGit(t, mainDir, "branch", "--list", taskBranch(firstRunID)),
	); got != "" {
		t.Fatalf("previous Run branch remains after refresh: %q", got)
	}
}

func TestScopedScratchRefreshFallsBackToMainWhenPreviousBranchIsMissing(t *testing.T) {
	t.Parallel()

	manager, root := newTaskMergeTestManager(t)
	conversationID := "01900000-0000-4000-8000-0000000000e2"
	firstRunID, secondRunID := uuid.MustParse(
		"00000000-0000-4000-8000-000000001611",
	), uuid.MustParse(
		"00000000-0000-4000-8000-000000001612",
	)
	first, err := manager.AcquireScopedScratch(
		t.Context(),
		taskMergeTestCompanyID,
		"product",
		conversationID,
		firstRunID,
		0,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(first, 0) error = %v", err)
	}
	firstOnlyPath := filepath.Join(first.WorkDir, "first-only.txt")
	if err := os.WriteFile(firstOnlyPath, []byte("first Run\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commitTaskMergeTestFiles(t, first.WorkDir, "Persist first-only work")
	if err := manager.MarkCompleted(first.Root); err != nil {
		t.Fatalf("MarkCompleted(first) error = %v", err)
	}
	mainDir := filepath.Join(root, taskMergeTestCompanyID, "product", mainDirectoryName)
	mainHead := strings.TrimSpace(runTaskMergeTestGit(t, mainDir, "rev-parse", "main"))
	runTaskMergeTestGit(t, first.WorkDir, "checkout", "--detach", "main")
	runTaskMergeTestGit(
		t,
		mainDir,
		"update-ref",
		"-d",
		"refs/heads/"+taskBranch(firstRunID),
	)
	stalePath := filepath.Join(first.WorkDir, "stale.txt")
	if err := os.WriteFile(stalePath, []byte("remove me\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	second, err := manager.AcquireScopedScratch(
		t.Context(),
		taskMergeTestCompanyID,
		"product",
		conversationID,
		secondRunID,
		0,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(second, 0) error = %v", err)
	}
	if got := strings.TrimSpace(runTaskMergeTestGit(t, second.WorkDir, "rev-parse", "HEAD")); got != mainHead {
		t.Fatalf("fallback HEAD = %q, want main HEAD %q", got, mainHead)
	}
	if _, err := os.Stat(firstOnlyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing previous branch leaked its file into fallback: %v", err)
	}
	if _, err := os.Stat(stalePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("untracked file survived fallback refresh: %v", err)
	}
}

func TestIsolatedScratchUsesOneWorkspacePerRun(t *testing.T) {
	t.Parallel()

	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(root, cacheRoot, testMetadataKey, &fakeGitRunner{}, Retention{
		Completed: 24 * time.Hour, Orphaned: 72 * time.Hour,
	}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	conversationID := "01900000-0000-7000-8000-0000000000a5"
	first, err := manager.AcquireIsolatedScratch(
		conversationID,
		uuid.MustParse("00000000-0000-4000-8000-000000001201"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.WorkDir, "private.txt"), []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkCompleted(first.Root); err != nil {
		t.Fatal(err)
	}
	if reused, err := manager.AcquireIsolatedScratch(
		conversationID,
		uuid.MustParse("00000000-0000-4000-8000-000000001201"),
	); err == nil {
		_ = manager.MarkOrphaned(reused.Root)
		t.Fatalf("same scheduled Run reacquired its prior workspace: %+v", reused)
	}
	second, err := manager.AcquireIsolatedScratch(
		conversationID,
		uuid.MustParse("00000000-0000-4000-8000-000000001202"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.Reused || second.Reused || first.Root == second.Root {
		t.Fatalf("isolated workspaces = %+v / %+v", first, second)
	}
	if _, err := os.Stat(filepath.Join(second.WorkDir, "private.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second isolated Run inherited first Run files: %v", err)
	}
	if err := manager.MarkCompleted(second.Root); err != nil {
		t.Fatal(err)
	}
}

func TestCompanyScopedMetadataCannotMoveAcrossCompanyNamespace(t *testing.T) {
	t.Parallel()

	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "workspaces")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(
		root,
		cacheRoot,
		testMetadataKey,
		&fakeGitRunner{},
		Retention{Completed: 24 * time.Hour, Orphaned: 72 * time.Hour},
		time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	companyID := "01900000-0000-7000-8000-0000000000ca"
	otherCompanyID := "01900000-0000-7000-8000-0000000000cb"
	conversationID := "01900000-0000-7000-8000-0000000000cc"
	source := filepath.Join(root, companyID, "engineering", "worktrees", conversationID)
	if err := os.MkdirAll(filepath.Join(source, workdirDirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := manager.writeMetadata(source, metadata{
		Version: metadataVersion, Kind: metadataKindManagedScratch,
		CompanyID: companyID, Scope: "engineering",
		ConversationID: conversationID, LastRunID: "01900000-0000-7000-8000-0000000000cd",
		State: workspaceActive, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.readMetadata(source); err != nil {
		t.Fatalf("scoped metadata unreadable in its own namespace: %v", err)
	}
	destination := filepath.Join(root, otherCompanyID, "engineering", "worktrees", conversationID)
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source, destination); err != nil {
		t.Fatal(err)
	}
	sourceMetadata, err := manager.metadataPath(source)
	if err != nil {
		t.Fatal(err)
	}
	destinationMetadata, err := manager.metadataPath(destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destinationMetadata), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(sourceMetadata, destinationMetadata); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.readMetadata(destination); !errors.Is(err, errMetadataUnusable) {
		t.Fatalf("moved metadata error = %v, want errMetadataUnusable", err)
	}
}

func TestAcquireRejectsConversationAlreadyInUse(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(root, cacheRoot, testMetadataKey, &fakeGitRunner{}, Retention{
		Completed: time.Hour, Orphaned: 2 * time.Hour,
	}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	conversationID := "01900000-0000-7000-8000-0000000000a6"
	acquired, err := manager.AcquireScratch(conversationID, uuid.MustParse("00000000-0000-4000-8000-000000001117"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AcquireScratch(
		conversationID,
		uuid.MustParse("00000000-0000-4000-8000-000000001118"),
	); err == nil {
		t.Fatal("concurrent acquire of an in-use conversation succeeded")
	}
	if err := manager.MarkOrphaned(acquired.Root); err != nil {
		t.Fatal(err)
	}
	released, err := manager.AcquireScratch(conversationID, uuid.MustParse("00000000-0000-4000-8000-000000001118"))
	if err != nil {
		t.Fatalf("acquire after release error = %v", err)
	}
	if !released.Reused {
		t.Fatalf("orphaned-but-intact conversation was not reattached: %+v", released)
	}
}

func TestAcquireScratchReplacesUnusableConversationEnvironment(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(root, cacheRoot, testMetadataKey, &fakeGitRunner{}, Retention{
		Completed: time.Hour, Orphaned: 2 * time.Hour,
	}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	conversationID := "01900000-0000-7000-8000-0000000000a7"
	envRoot := filepath.Join(root, conversationID)
	if err := os.MkdirAll(filepath.Join(envRoot, workdirDirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(envRoot, workdirDirectoryName, "stale.txt")
	if err := os.WriteFile(stale, []byte("previous build"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, metadataDirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	// A legacy per-Run schema sidecar fails verification under the v2 schema.
	legacy := []byte(
		`{"version":1,"kind":"managed_scratch","runId":"` + conversationID + `","cacheName":"","branch":"","state":"active","createdAt":"2026-07-13T00:00:00Z","signature":"AAAA"}`,
	)
	if err := os.WriteFile(
		filepath.Join(root, metadataDirectoryName, conversationID+metadataFileSuffix),
		legacy,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	acquired, err := manager.AcquireScratch(conversationID, uuid.MustParse("00000000-0000-4000-8000-000000001119"))
	if err != nil {
		t.Fatal(err)
	}
	if acquired.Reused {
		t.Fatalf("unusable environment was attached: %+v", acquired)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("unusable environment content survived: %v", err)
	}
	entry, err := manager.readMetadata(acquired.Root)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Version != legacyMetadataVersion || entry.ConversationID != conversationID ||
		entry.State != workspaceActive {
		t.Fatalf("replacement metadata = %+v", entry)
	}
}

func TestScratchWorkspaceAbortUsesSignedOrphanRetention(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &fakeGitRunner{}
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	manager, err := NewManager(root, cacheRoot, testMetadataKey, runner, Retention{
		Completed: 24 * time.Hour, Orphaned: 72 * time.Hour,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := manager.AcquireScratch(
		"01900000-0000-7000-8000-0000000000a8",
		uuid.MustParse("00000000-0000-4000-8000-000000001104"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkOrphaned(acquired.Root); err != nil {
		t.Fatal(err)
	}
	entry, err := manager.readMetadata(acquired.Root)
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != workspaceOrphaned || entry.OrphanedAt == nil || entry.CompletedAt != nil {
		t.Fatalf("aborted scratch metadata = %+v", entry)
	}
	now = now.Add(24 * time.Hour)
	if removed, err := manager.GC(context.Background()); err != nil || len(removed) != 0 {
		t.Fatalf("orphan removed at completed retention: %q / %v", removed, err)
	}
	now = now.Add(48 * time.Hour)
	removed, err := manager.GC(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != acquired.Root {
		t.Fatalf("orphan removal = %q, want %q", removed, acquired.Root)
	}
}

func TestWorktreeManagerRejectsFilesystemRoot(t *testing.T) {
	if _, err := NewManager(
		string(filepath.Separator),
		filepath.Join(t.TempDir(), "repositories"),
		testMetadataKey,
		&fakeGitRunner{},
		Retention{Completed: time.Hour, Orphaned: 2 * time.Hour},
		time.Now,
	); err == nil {
		t.Fatal("filesystem root was accepted as worktree root")
	}
}

func TestWorktreeManagerRejectsExistingNonRealManagementRoots(t *testing.T) {
	dataRoot := t.TempDir()
	cacheRoot := filepath.Join(dataRoot, "repositories")
	if err := os.Mkdir(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	regularFile := filepath.Join(dataRoot, "worktrees-file")
	if err := os.WriteFile(regularFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(regularFile, cacheRoot, testMetadataKey, &fakeGitRunner{}, Retention{
		Completed: time.Hour, Orphaned: 2 * time.Hour,
	}, time.Now); err == nil {
		t.Fatal("regular file was accepted as the worktree root")
	}

	root := filepath.Join(dataRoot, "worktrees")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dataRoot, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, metadataDirectoryName)); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(root, cacheRoot, testMetadataKey, &fakeGitRunner{}, Retention{
		Completed: time.Hour, Orphaned: 2 * time.Hour,
	}, time.Now); err == nil {
		t.Fatal("symlink was accepted as the worktree metadata root")
	}

	rootWithFileMetadata := filepath.Join(dataRoot, "worktrees-with-file-metadata")
	if err := os.Mkdir(rootWithFileMetadata, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(rootWithFileMetadata, metadataDirectoryName),
		[]byte("not a directory"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(rootWithFileMetadata, cacheRoot, testMetadataKey, &fakeGitRunner{}, Retention{
		Completed: time.Hour, Orphaned: 2 * time.Hour,
	}, time.Now); err == nil {
		t.Fatal("regular file was accepted as the worktree metadata root")
	}
}

func TestAcquireScratchRejectsRootSwapWithoutWritingThroughIt(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(root, cacheRoot, testMetadataKey, &fakeGitRunner{}, Retention{
		Completed: time.Hour, Orphaned: 2 * time.Hour,
	}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dataRoot, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.AcquireScratch(
		"01900000-0000-7000-8000-0000000000a9",
		uuid.MustParse("00000000-0000-4000-8000-000000001097"),
	); err == nil {
		t.Fatal("symlinked worktree root was accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("symlink target was mutated: %+v", entries)
	}
}

func TestAcquireScratchRejectsMetadataRootSwapWithoutWritingThroughIt(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(root, cacheRoot, testMetadataKey, &fakeGitRunner{}, Retention{
		Completed: time.Hour, Orphaned: 2 * time.Hour,
	}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dataRoot, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, metadataDirectoryName)); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.AcquireScratch(
		"01900000-0000-7000-8000-0000000000aa",
		uuid.MustParse("00000000-0000-4000-8000-000000001098"),
	); err == nil {
		t.Fatal("symlinked metadata root was accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("metadata symlink target was mutated: %+v", entries)
	}
}

func TestWorktreeGCUsesCompletedAndOrphanRetention(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	manager, err := NewManager(
		root,
		cacheRoot,
		testMetadataKey,
		&fakeGitRunner{},
		Retention{Completed: 24 * time.Hour, Orphaned: 72 * time.Hour},
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := manager.AcquireScratch(
		"01900000-0000-7000-8000-0000000000b1",
		uuid.MustParse("00000000-0000-4000-8000-000000001081"),
	)
	if err != nil {
		t.Fatal(err)
	}
	orphaned, err := manager.AcquireScratch(
		"01900000-0000-7000-8000-0000000000b2",
		uuid.MustParse("00000000-0000-4000-8000-000000001082"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkCompleted(completed.Root); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkOrphaned(orphaned.Root); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(context.Background(), map[string]struct{}{}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	removed, err := manager.GC(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != completed.Root {
		t.Fatalf("24h removed = %q, want completed", removed)
	}
	if _, err := os.Stat(orphaned.Root); err != nil {
		t.Fatalf("orphan removed before 72h: %v", err)
	}
	now = now.Add(48 * time.Hour)
	removed, err = manager.GC(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != orphaned.Root {
		t.Fatalf("72h removed = %q, want orphaned", removed)
	}
}

func TestWorktreeGCRemovesCrashOrphanWithoutMetadataAfterRetention(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	runner := &fakeGitRunner{}
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	manager, err := NewManager(
		root,
		cacheRoot,
		testMetadataKey,
		runner,
		Retention{Completed: 24 * time.Hour, Orphaned: 72 * time.Hour},
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	crashOrphan := filepath.Join(root, "01900000-0000-7000-8000-000000000053")
	if err := os.MkdirAll(filepath.Join(crashOrphan, workdirDirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	forgedOld := now.Add(-365 * 24 * time.Hour)
	if err := os.Chtimes(crashOrphan, forgedOld, forgedOld); err != nil {
		t.Fatal(err)
	}
	if removed, err := manager.GC(context.Background()); err != nil || len(removed) != 0 {
		t.Fatalf("unsigned crash orphan was deleted from forged mtime: %q / %v", removed, err)
	}
	if err := manager.Reconcile(context.Background(), map[string]struct{}{}); err != nil {
		t.Fatal(err)
	}
	if removed, err := manager.GC(context.Background()); err != nil || len(removed) != 0 {
		t.Fatalf("freshly signed crash orphan skipped retention: %q / %v", removed, err)
	}
	now = now.Add(72 * time.Hour)
	removed, err := manager.GC(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != crashOrphan {
		t.Fatalf("crash orphan removal = %q", removed)
	}
}

// TestWorktreeReconcileMigratesLegacyPerRunDirectoryThroughCrashOrphan proves
// the migration path for pre-conversation builds: a per-Run directory whose
// legacy sidecar no longer verifies is re-marked as a freshly signed crash
// orphan and removed after the orphan retention.
func TestWorktreeReconcileMigratesLegacyPerRunDirectoryThroughCrashOrphan(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	manager, err := NewManager(root, cacheRoot, testMetadataKey, &fakeGitRunner{}, Retention{
		Completed: 24 * time.Hour, Orphaned: 72 * time.Hour,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	legacyRunID := "01900000-0000-7000-8000-000000000054"
	legacyPath := filepath.Join(root, legacyRunID)
	if err := os.MkdirAll(legacyPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, metadataDirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	legacySidecar := []byte(
		`{"version":1,"kind":"managed_scratch","runId":"` + legacyRunID + `","cacheName":"","branch":"","state":"completed","createdAt":"2026-07-01T00:00:00Z","completedAt":"2026-07-01T01:00:00Z","signature":"AAAA"}`,
	)
	if err := os.WriteFile(
		filepath.Join(root, metadataDirectoryName, legacyRunID+metadataFileSuffix),
		legacySidecar,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ReconcileAndGC(context.Background(), map[string]struct{}{}); err != nil {
		t.Fatal(err)
	}
	entry, err := manager.readMetadata(legacyPath)
	if err != nil {
		t.Fatalf("migrated crash orphan metadata: %v", err)
	}
	if entry.Kind != metadataKindCrashOrphan || entry.State != workspaceOrphaned ||
		entry.ConversationID != legacyRunID {
		t.Fatalf("migrated metadata = %+v", entry)
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("legacy directory removed before retention: %v", err)
	}
	now = now.Add(72 * time.Hour)
	removed, err := manager.ReconcileAndGC(context.Background(), map[string]struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != legacyPath {
		t.Fatalf("legacy directory removal = %q", removed)
	}
}

func TestWorktreeReconcileNeverQuarantinesAnActiveConversationWithMissingMetadata(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	manager, err := NewManager(root, cacheRoot, testMetadataKey, &fakeGitRunner{}, Retention{
		Completed: 24 * time.Hour, Orphaned: 72 * time.Hour,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	conversationID := "01900000-0000-7000-8000-000000000056"
	activePath := filepath.Join(root, conversationID)
	if err := os.MkdirAll(activePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(context.Background(), map[string]struct{}{conversationID: {}}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(365 * 24 * time.Hour)
	if removed, err := manager.GC(context.Background()); err != nil || len(removed) != 0 {
		t.Fatalf("active workspace without metadata was collected: %q / %v", removed, err)
	}
	if _, err := os.Stat(activePath); err != nil {
		t.Fatalf("active workspace disappeared: %v", err)
	}
}

func TestWorktreeGCFailsClosedWhenProviderEditsSignedLifecycleMetadata(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &fakeGitRunner{}
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	manager, err := NewManager(root, cacheRoot, testMetadataKey, runner, Retention{
		Completed: 24 * time.Hour, Orphaned: 72 * time.Hour,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := manager.AcquireScratch(
		"01900000-0000-7000-8000-0000000000ac",
		uuid.MustParse("00000000-0000-4000-8000-000000001085"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkCompleted(acquired.Root); err != nil {
		t.Fatal(err)
	}
	metadataPath, err := manager.metadataPath(acquired.Root)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	var entry metadata
	if err := json.Unmarshal(body, &entry); err != nil {
		t.Fatal(err)
	}
	forged := now.Add(-365 * 24 * time.Hour)
	entry.State = workspaceCompleted
	entry.CompletedAt = &forged
	forgedBody, err := json.Marshal(entry) // Deliberately retain the old MAC.
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, forgedBody, 0o600); err != nil {
		t.Fatal(err)
	}
	now = now.Add(365 * 24 * time.Hour)
	if removed, err := manager.GC(context.Background()); err == nil || len(removed) != 0 {
		t.Fatalf("tampered lifecycle metadata did not fail closed: %q / %v", removed, err)
	}
	if _, err := os.Stat(acquired.Root); err != nil {
		t.Fatalf("tampered workspace was deleted: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("tampered metadata triggered git removal: %+v", runner.calls)
	}
}

func TestWorktreeGCContinuesPastCorruptMetadataWithoutDeletingThatWorkspace(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	manager, err := NewManager(root, cacheRoot, testMetadataKey, &fakeGitRunner{}, Retention{
		Completed: 24 * time.Hour, Orphaned: 72 * time.Hour,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	corrupt, err := manager.AcquireScratch(
		"01900000-0000-7000-8000-0000000000ad",
		uuid.MustParse("00000000-0000-4000-8000-000000001099"),
	)
	if err != nil {
		t.Fatal(err)
	}
	safe, err := manager.AcquireScratch(
		"01900000-0000-7000-8000-0000000000ae",
		uuid.MustParse("00000000-0000-4000-8000-000000001100"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkCompleted(safe.Root); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkOrphaned(corrupt.Root); err != nil {
		t.Fatal(err)
	}
	metadataPath, err := manager.metadataPath(corrupt.Root)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	var entry metadata
	if err := json.Unmarshal(body, &entry); err != nil {
		t.Fatal(err)
	}
	entry.State = workspaceCompleted // Retain the orphaned-state MAC deliberately.
	forged := now.Add(-365 * 24 * time.Hour)
	entry.CompletedAt = &forged
	entry.OrphanedAt = nil
	forgedBody, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, forgedBody, 0o600); err != nil {
		t.Fatal(err)
	}
	now = now.Add(365 * 24 * time.Hour)

	removed, err := manager.GC(context.Background())
	if err == nil {
		t.Fatal("corrupt metadata was not reported")
	}
	if len(removed) != 1 || removed[0] != safe.Root {
		t.Fatalf("safe cleanup after corrupt entry = %q", removed)
	}
	if _, err := os.Stat(corrupt.Root); err != nil {
		t.Fatalf("corrupt workspace was deleted: %v", err)
	}
	if _, err := os.Stat(safe.Root); !os.IsNotExist(err) {
		t.Fatalf("safe expired workspace remains: %v", err)
	}
}

func TestWorktreeReconcilePreservesInterruptedGitIdentityForSafeCleanup(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	cachePath := filepath.Join(cacheRoot, "interrupted.git")
	mustCreateCache(t, cachePath)
	runner := &fakeGitRunner{}
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	manager, err := NewManager(root, cacheRoot, testMetadataKey, runner, Retention{
		Completed: 24 * time.Hour, Orphaned: 72 * time.Hour,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, metadataDirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	conversationID := "01900000-0000-7000-8000-0000000000af"
	path := filepath.Join(root, conversationID)
	workDir := filepath.Join(path, workdirDirectoryName)
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := metadata{
		Version: legacyMetadataVersion, Kind: metadataKindManaged,
		ConversationID: conversationID, LastRunID: "01900000-0000-7000-8000-000000000065",
		CacheName: filepath.Base(cachePath), Branch: "zenith-team/interrupted",
		State: workspacePreparing, CreatedAt: now,
	}
	if err := manager.writeMetadata(path, entry); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(context.Background(), map[string]struct{}{}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(72 * time.Hour)
	removed, err := manager.GC(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != path {
		t.Fatalf("interrupted workspace removal = %q", removed)
	}
	joined := runner.joined()
	for _, want := range []string{
		"worktree remove --force " + workDir,
		"worktree prune --expire now",
		"update-ref -d refs/heads/zenith-team/interrupted",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("Git cleanup missing %q from:\n%s", want, joined)
		}
	}
}

func TestWorktreeReconcileCleansMetadataOnlyInterruptedGitIdentity(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	cachePath := filepath.Join(cacheRoot, "interrupted.git")
	mustCreateCache(t, cachePath)
	runner := &fakeGitRunner{}
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	manager, err := NewManager(root, cacheRoot, testMetadataKey, runner, Retention{
		Completed: 24 * time.Hour, Orphaned: 72 * time.Hour,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, metadataDirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	conversationID := "01900000-0000-7000-8000-0000000000b0"
	path := filepath.Join(root, conversationID)
	if err := manager.writeMetadata(path, metadata{
		Version: legacyMetadataVersion, Kind: metadataKindManaged,
		ConversationID: conversationID, LastRunID: "01900000-0000-7000-8000-000000000067",
		CacheName: filepath.Base(cachePath), Branch: "zenith-team/interrupted-before-add",
		State: workspacePreparing, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(context.Background(), map[string]struct{}{}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(72 * time.Hour)
	removed, err := manager.GC(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != path {
		t.Fatalf("metadata-only interrupted workspace removal = %q", removed)
	}
	joined := runner.joined()
	if strings.Contains(joined, "worktree remove") {
		t.Fatalf("missing worktree triggered a remove command: %s", joined)
	}
	for _, want := range []string{
		"worktree prune --expire now",
		"update-ref -d refs/heads/zenith-team/interrupted-before-add",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("Git cleanup missing %q from:\n%s", want, joined)
		}
	}
}

func TestWorktreeReconcileAndGCUsesExactActiveConversationSetAsDeletionFence(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	manager, err := NewManager(root, cacheRoot, testMetadataKey, &fakeGitRunner{}, Retention{
		Completed: 24 * time.Hour, Orphaned: 72 * time.Hour,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	conversationID := "01900000-0000-7000-8000-0000000000b3"
	acquired, err := manager.AcquireScratch(
		conversationID,
		uuid.MustParse("00000000-0000-4000-8000-000000001104"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkOrphaned(acquired.Root); err != nil {
		t.Fatal(err)
	}
	now = now.Add(72 * time.Hour)
	removed, err := manager.ReconcileAndGC(context.Background(), map[string]struct{}{conversationID: {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("active exact-set workspace was removed: %q", removed)
	}
	if _, err := os.Stat(acquired.Root); err != nil {
		t.Fatalf("active exact-set workspace disappeared: %v", err)
	}
	removed, err = manager.ReconcileAndGC(context.Background(), map[string]struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != acquired.Root {
		t.Fatalf("inactive expired workspace removal = %q", removed)
	}
}

func TestWorktreeGCSkipsConversationEnvironmentInLocalUse(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	manager, err := NewManager(root, cacheRoot, testMetadataKey, &fakeGitRunner{}, Retention{
		Completed: 24 * time.Hour, Orphaned: 72 * time.Hour,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := manager.AcquireScratch(
		"01900000-0000-7000-8000-0000000000b4",
		uuid.MustParse("00000000-0000-4000-8000-000000001105"),
	)
	if err != nil {
		t.Fatal(err)
	}
	// Even a stale empty active fence must not orphan or collect a workspace
	// a local Run is still executing in.
	now = now.Add(365 * 24 * time.Hour)
	if removed, err := manager.ReconcileAndGC(
		context.Background(),
		map[string]struct{}{},
	); err != nil ||
		len(removed) != 0 {
		t.Fatalf("locally in-use workspace was maintained away: %q / %v", removed, err)
	}
	entry, err := manager.readMetadata(acquired.Root)
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != workspaceActive {
		t.Fatalf("locally in-use workspace state = %+v", entry)
	}
}

func TestWorktreeGCFailsClosedAfterManagementRootSwap(t *testing.T) {
	for _, test := range []struct {
		name string
		swap func(*testing.T, string)
	}{
		{
			name: "worktree root",
			swap: func(t *testing.T, root string) {
				t.Helper()
				saved := root + "-saved"
				if err := os.Rename(root, saved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(saved, root); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "metadata root",
			swap: func(t *testing.T, root string) {
				t.Helper()
				metadataRoot := filepath.Join(root, metadataDirectoryName)
				saved := filepath.Join(filepath.Dir(root), "metadata-saved")
				if err := os.Rename(metadataRoot, saved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(saved, metadataRoot); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataRoot := t.TempDir()
			root := filepath.Join(dataRoot, "worktrees")
			cacheRoot := filepath.Join(dataRoot, "repositories")
			if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
			manager, err := NewManager(root, cacheRoot, testMetadataKey, &fakeGitRunner{}, Retention{
				Completed: 24 * time.Hour, Orphaned: 72 * time.Hour,
			}, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			acquired, err := manager.AcquireScratch(
				"01900000-0000-7000-8000-0000000000b5",
				uuid.MustParse("00000000-0000-4000-8000-000000001105"),
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.MarkCompleted(acquired.Root); err != nil {
				t.Fatal(err)
			}
			now = now.Add(24 * time.Hour)
			test.swap(t, root)

			removed, err := manager.GC(context.Background())
			if err == nil || len(removed) != 0 {
				t.Fatalf("swapped management root did not fail closed: %q / %v", removed, err)
			}
		})
	}
}

func mustCreateCache(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureMainCreatesDurableScopedWorkspace(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	mustCreateCache(t, cacheRoot)
	runner := &fakeGitRunner{}
	manager, err := NewManager(root, cacheRoot, testMetadataKey, runner, Retention{
		Completed: 24 * time.Hour, Orphaned: 72 * time.Hour,
	}, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	companyID := "01900000-0000-7000-8000-0000000000c1"
	mainPath, err := manager.EnsureMain(context.Background(), companyID, "engineering")
	if err != nil {
		t.Fatalf("EnsureMain() error = %v", err)
	}
	if want := filepath.Join(root, companyID, "engineering", mainDirectoryName); mainPath != want {
		t.Fatalf("EnsureMain() = %q, want %q", mainPath, want)
	}
	memoryPath := filepath.Join(mainPath, memoryDirectoryName)
	if info, err := os.Lstat(memoryPath); err != nil || !info.IsDir() {
		t.Fatalf("main workspace memory directory missing: %v", err)
	}
	if joined := runner.joined(); !strings.Contains(joined, "rev-parse --is-inside-work-tree") {
		t.Fatalf("EnsureMain() did not initialize the Git repository: %q", joined)
	}

	again, err := manager.EnsureMain(context.Background(), companyID, "engineering")
	if err != nil || again != mainPath {
		t.Fatalf("EnsureMain() second call = %q / %v, want %q", again, err, mainPath)
	}

	companyMain, err := manager.EnsureMain(context.Background(), companyID, "company")
	if err != nil {
		t.Fatalf("EnsureMain(company) error = %v", err)
	}
	if want := filepath.Join(root, companyID, "company", mainDirectoryName); companyMain != want {
		t.Fatalf("EnsureMain(company) = %q, want %q", companyMain, want)
	}

	if _, err := manager.EnsureMain(context.Background(), companyID, "../escape"); err == nil {
		t.Fatal("EnsureMain() accepted an invalid scope")
	}
	if _, err := manager.EnsureMain(context.Background(), "not-a-workspace-id", "engineering"); err == nil {
		t.Fatal("EnsureMain() accepted an invalid company id")
	}
}

func TestScopedScratchCreatesMemoryDirectories(t *testing.T) {
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "worktrees")
	cacheRoot := filepath.Join(dataRoot, "repositories")
	mustCreateCache(t, cacheRoot)
	manager, err := NewManager(root, cacheRoot, testMetadataKey, &fakeGitRunner{}, Retention{
		Completed: 24 * time.Hour, Orphaned: 72 * time.Hour,
	}, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	companyID := "01900000-0000-7000-8000-0000000000c2"
	conversationID := "01900000-0000-7000-8000-0000000000c3"
	acquired, err := manager.AcquireScopedScratch(
		context.Background(),
		companyID,
		"engineering",
		conversationID,
		uuid.MustParse("00000000-0000-4000-8000-000000001201"),
		0,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(, 0) error = %v", err)
	}
	mainMemory := filepath.Join(root, companyID, "engineering", mainDirectoryName, memoryDirectoryName)
	if info, err := os.Lstat(mainMemory); err != nil || !info.IsDir() {
		t.Fatalf("scoped main memory directory missing: %v", err)
	}
	workMemory := filepath.Join(acquired.WorkDir, memoryDirectoryName)
	if info, err := os.Lstat(workMemory); err != nil || !info.IsDir() {
		t.Fatalf("scoped workdir memory directory missing: %v", err)
	}
	if err := manager.MarkCompleted(acquired.Root); err != nil {
		t.Fatal(err)
	}
}

func TestProjectWorkspaceIsolation(t *testing.T) {
	root := t.TempDir()
	manager, err := NewManager(
		root,
		t.TempDir(),
		testMetadataKey,
		repocache.OSRunner{},
		Retention{Completed: 24 * time.Hour, Orphaned: 72 * time.Hour},
		time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	companyID := uuid.NewString()
	projects := []string{uuid.NewString(), uuid.NewString()}
	for index, id := range projects {
		scope := "projects/" + id
		conversationID, runID := uuid.NewString(), uuid.New()
		acquired, err := manager.AcquireScopedScratch(
			context.Background(),
			companyID,
			scope,
			conversationID,
			runID,
			0,
		)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(
			acquired.WorkDir,
			filepath.Join(root, companyID, "projects", id)+string(filepath.Separator),
		) {
			t.Fatalf("wrong project workdir: %+v", acquired)
		}
		main, err := manager.ResolveMain(context.Background(), companyID, scope)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(main, ".git")); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			if err := os.WriteFile(
				filepath.Join(acquired.WorkDir, "website.txt"),
				[]byte("website"),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			if err := manager.MarkCompleted(acquired.Root); err != nil {
				t.Fatal(err)
			}
			result, err := manager.MergeTask(
				context.Background(),
				TaskMergeInput{
					CompanyID:      companyID,
					Scope:          scope,
					ConversationID: conversationID,
					RunID:          runID,
					LeaseFence:     func(context.Context) error { return nil },
				},
			)
			if err != nil || result.ResultCode != "" {
				t.Fatalf("Project merge=%+v,%v", result, err)
			}
			if _, err := os.Stat(filepath.Join(main, "website.txt")); err != nil {
				t.Fatal(err)
			}
		} else if _, err := os.Stat(filepath.Join(main, "website.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Project inherited another Project files: %v", err)
		}

	}
	records, err := manager.records()
	if err != nil || len(records) != 2 {
		t.Fatalf("records = %v, %v", records, err)
	}
}

// A Task handed to another member arrives with a new workspace generation.
// The Run that follows must not continue the files the previous assignee
// left behind, so it gets a root of its own, prepared from the machine's
// Project main — and the old root is retained exactly as it was, because the
// work in it was never merged (ADR 0014). The server's first generation is
// 0 and its first handoff makes it 1, so the first handoff that keeps the
// Task on this machine is exactly the case that must not reuse the root.
func TestScopedScratchPreparesAFreshRootForANewWorkspaceGeneration(t *testing.T) {
	t.Parallel()

	manager, root := newTaskMergeTestManager(t)
	conversationID := "01900000-0000-4000-8000-0000000000f1"
	firstRunID := uuid.MustParse("00000000-0000-4000-8000-000000001701")
	secondRunID := uuid.MustParse("00000000-0000-4000-8000-000000001702")
	first, err := manager.AcquireScopedScratch(
		t.Context(), taskMergeTestCompanyID, "engineering", conversationID, firstRunID, 0,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(generation 0) error = %v", err)
	}
	worktrees := filepath.Join(root, taskMergeTestCompanyID, "engineering", "worktrees")
	if first.Root != filepath.Join(worktrees, conversationID) {
		t.Fatalf("generation 0 root = %s, want the plain Conversation root", first.Root)
	}
	unmergedPath := filepath.Join(first.WorkDir, "unmerged.txt")
	if err := os.WriteFile(unmergedPath, []byte("the previous assignee's work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkCompleted(first.Root); err != nil {
		t.Fatalf("MarkCompleted(generation 0) error = %v", err)
	}

	second, err := manager.AcquireScopedScratch(
		t.Context(), taskMergeTestCompanyID, "engineering", conversationID, secondRunID, 1,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(generation 1) error = %v", err)
	}
	if second.Reused || second.Root == first.Root {
		t.Fatalf("AcquireScopedScratch(generation 1) = %+v, want a fresh root beside %s",
			second, first.Root)
	}
	if wantRoot := filepath.Join(worktrees, conversationID+"-g1"); second.Root != wantRoot {
		t.Fatalf("generation 1 root = %s, want %s", second.Root, wantRoot)
	}
	if _, err := os.Stat(filepath.Join(second.WorkDir, "unmerged.txt")); !os.IsNotExist(err) {
		t.Fatalf("the new generation inherited the previous assignee's file: %v", err)
	}
	// The previous generation keeps its files: nothing moves them anywhere,
	// and its root says it was superseded rather than merely completed.
	content, err := os.ReadFile(unmergedPath)
	if err != nil || string(content) != "the previous assignee's work\n" {
		t.Fatalf("retained generation 0 file = %q, %v", content, err)
	}
	retired, err := manager.readMetadata(first.Root)
	if err != nil {
		t.Fatalf("readMetadata(generation 0) error = %v", err)
	}
	if retired.State != workspaceSuperseded || retired.SupersededAt == nil ||
		retired.CompletedAt != nil || retired.WorkspaceGeneration != 0 {
		t.Fatalf("generation 0 sidecar = %+v, want superseded", retired)
	}

	// Its Runs keep landing in the new root while the generation stands.
	thirdRunID := uuid.MustParse("00000000-0000-4000-8000-000000001703")
	if err := manager.MarkCompleted(second.Root); err != nil {
		t.Fatalf("MarkCompleted(generation 1) error = %v", err)
	}
	third, err := manager.AcquireScopedScratch(
		t.Context(), taskMergeTestCompanyID, "engineering", conversationID, thirdRunID, 1,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(generation 1 again) error = %v", err)
	}
	if !third.Reused || third.Root != second.Root {
		t.Fatalf("second Run of generation 1 = %+v, want the same root reused", third)
	}

	// A superseded root is never continued, not even by a Run that still
	// carries its generation: it is refused, and the root stays as it was.
	if err := manager.MarkCompleted(third.Root); err != nil {
		t.Fatalf("MarkCompleted(generation 1 again) error = %v", err)
	}
	staleRunID := uuid.MustParse("00000000-0000-4000-8000-000000001704")
	if stale, err := manager.AcquireScopedScratch(
		t.Context(), taskMergeTestCompanyID, "engineering", conversationID, staleRunID, 0,
	); err == nil {
		t.Fatalf("AcquireScopedScratch(superseded generation 0) = %+v, want refused", stale)
	}
	content, err = os.ReadFile(unmergedPath)
	if err != nil || string(content) != "the previous assignee's work\n" {
		t.Fatalf("superseded file after a stale Run = %q, %v", content, err)
	}
	if entry, err := manager.readMetadata(first.Root); err != nil ||
		entry.State != workspaceSuperseded || entry.LastRunID != firstRunID.String() {
		t.Fatalf("superseded sidecar after a stale Run = %+v, %v", entry, err)
	}
}

// GC never collects a superseded root: its files are the only copy of work
// nobody merged, and nothing has recorded what should happen to them.
// Neither the completed nor the orphan retention
// clock applies, and reconciliation does not orphan it.
func TestWorktreeGCKeepsSupersededGenerationPastRetention(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	dataRoot := t.TempDir()
	root := filepath.Join(dataRoot, "workspaces")
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	manager, err := NewManager(
		root, filepath.Join(dataRoot, "repositories"), testMetadataKey, repocache.OSRunner{},
		Retention{Completed: 24 * time.Hour, Orphaned: 72 * time.Hour},
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	conversationID := "01900000-0000-4000-8000-0000000000f3"
	first, err := manager.AcquireScopedScratch(
		t.Context(), taskMergeTestCompanyID, "engineering", conversationID,
		uuid.MustParse("00000000-0000-4000-8000-000000001901"), 0,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(generation 0) error = %v", err)
	}
	unmergedPath := filepath.Join(first.WorkDir, "unmerged.txt")
	if err := os.WriteFile(unmergedPath, []byte("never merged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkCompleted(first.Root); err != nil {
		t.Fatal(err)
	}
	second, err := manager.AcquireScopedScratch(
		t.Context(), taskMergeTestCompanyID, "engineering", conversationID,
		uuid.MustParse("00000000-0000-4000-8000-000000001902"), 1,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(generation 1) error = %v", err)
	}
	if err := manager.MarkCompleted(second.Root); err != nil {
		t.Fatal(err)
	}

	now = now.Add(30 * 24 * time.Hour)
	removed, err := manager.ReconcileAndGC(t.Context(), map[string]struct{}{})
	if err != nil {
		t.Fatalf("ReconcileAndGC() error = %v", err)
	}
	if len(removed) != 1 || removed[0] != second.Root {
		t.Fatalf("removed = %q, want only the completed generation 1 root", removed)
	}
	if removed, err := manager.GC(t.Context()); err != nil || len(removed) != 0 {
		t.Fatalf("GC() = %q, %v, want the superseded root kept", removed, err)
	}
	content, err := os.ReadFile(unmergedPath)
	if err != nil || string(content) != "never merged\n" {
		t.Fatalf("superseded file after GC = %q, %v", content, err)
	}
	if entry, err := manager.readMetadata(first.Root); err != nil || entry.State != workspaceSuperseded {
		t.Fatalf("superseded sidecar after GC = %+v, %v", entry, err)
	}
}

// The daemon learns that a confirmed handoff cancelled a Run while it runs.
// Its root is superseded on the spot instead of being recorded as a normally
// completed root that GC would collect a day later.
func TestMarkSupersededRetiresTheRootOfACancelledRun(t *testing.T) {
	t.Parallel()

	manager, _ := newTaskMergeTestManager(t)
	conversationID := "01900000-0000-4000-8000-0000000000f4"
	acquired, err := manager.AcquireScopedScratch(
		t.Context(), taskMergeTestCompanyID, "engineering", conversationID,
		uuid.MustParse("00000000-0000-4000-8000-000000002001"), 0,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch() error = %v", err)
	}
	if err := manager.MarkSuperseded(acquired.Root); err != nil {
		t.Fatalf("MarkSuperseded() error = %v", err)
	}
	entry, err := manager.readMetadata(acquired.Root)
	if err != nil || entry.State != workspaceSuperseded || entry.SupersededAt == nil {
		t.Fatalf("sidecar = %+v, %v, want superseded", entry, err)
	}
	// Retiring the root ends its Run's local use, as completing it would.
	manager.mu.Lock()
	_, busy := manager.inUse[conversationID]
	manager.mu.Unlock()
	if busy {
		t.Fatal("MarkSuperseded() left the Conversation in local use")
	}
	if err := manager.MarkSuperseded(acquired.Root); err != nil {
		t.Fatalf("MarkSuperseded() again error = %v", err)
	}
	if err := manager.MarkCompleted(acquired.Root); err == nil {
		t.Fatal("MarkCompleted() turned a superseded root back into a completed one")
	}
	// The next generation of the Task on this machine starts beside it.
	next, err := manager.AcquireScopedScratch(
		t.Context(), taskMergeTestCompanyID, "engineering", conversationID,
		uuid.MustParse("00000000-0000-4000-8000-000000002002"), 1,
	)
	if err != nil || next.Reused || next.Root == acquired.Root {
		t.Fatalf("AcquireScopedScratch(generation 1) = %+v, %v, want a fresh root", next, err)
	}
}

// The daemon can learn of a handoff only after the Run finished and completed
// its root. SupersedeFinished retires that root without touching the
// Conversation's in-use fence, which a Run of the newer generation may hold
// by then, and never retires a root a Run is using.
func TestSupersedeFinishedRetiresACompletedRootAndLeavesTheFence(t *testing.T) {
	t.Parallel()

	manager, _ := newTaskMergeTestManager(t)
	conversationID := "01900000-0000-4000-8000-0000000000f6"
	first, err := manager.AcquireScopedScratch(
		t.Context(), taskMergeTestCompanyID, "engineering", conversationID,
		uuid.MustParse("00000000-0000-4000-8000-000000002101"), 0,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(generation 0) error = %v", err)
	}
	if err := manager.SupersedeFinished(first.Root); err == nil {
		t.Fatal("SupersedeFinished() retired a root its Run is still using")
	}
	if err := manager.MarkCompleted(first.Root); err != nil {
		t.Fatal(err)
	}
	if err := manager.SupersedeFinished(first.Root); err != nil {
		t.Fatalf("SupersedeFinished() error = %v", err)
	}
	entry, err := manager.readMetadata(first.Root)
	if err != nil || entry.State != workspaceSuperseded || entry.SupersededAt == nil || entry.CompletedAt != nil {
		t.Fatalf("sidecar = %+v, %v, want superseded", entry, err)
	}

	next, err := manager.AcquireScopedScratch(
		t.Context(), taskMergeTestCompanyID, "engineering", conversationID,
		uuid.MustParse("00000000-0000-4000-8000-000000002102"), 1,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(generation 1) error = %v", err)
	}
	if err := manager.SupersedeFinished(first.Root); err != nil {
		t.Fatalf("SupersedeFinished() again error = %v", err)
	}
	manager.mu.Lock()
	_, busy := manager.inUse[conversationID]
	manager.mu.Unlock()
	if !busy {
		t.Fatal("SupersedeFinished() released the fence the generation 1 Run holds")
	}
	if err := manager.MarkCompleted(next.Root); err != nil {
		t.Fatalf("MarkCompleted(generation 1) error = %v", err)
	}
}

// Roots prepared before this build carry no generation, or carry the first
// generation as 1 because this branch's first daemon numbered from there.
// Both are the Task's pre-handoff generation, 0: a Run of it continues them,
// and the first handoff supersedes them. A "-g<N>" root that build prepared
// already names the server's generation and is continued unchanged.
func TestScopedScratchReadsEarlierRootsAsTheirServerGeneration(t *testing.T) {
	t.Parallel()

	for _, earlier := range []struct {
		name       string
		version    int
		generation int64
	}{
		{name: "no generation recorded", version: generationlessMetadataVersion},
		{name: "first generation recorded as 1", version: metadataVersion, generation: 1},
	} {
		t.Run(earlier.name, func(t *testing.T) {
			t.Parallel()
			manager, _ := newTaskMergeTestManager(t)
			conversationID := "01900000-0000-4000-8000-0000000000f5"
			rewriteAsEarlier := func(root string) {
				t.Helper()
				entry, err := manager.readMetadata(root)
				if err != nil {
					t.Fatal(err)
				}
				entry.Version, entry.WorkspaceGeneration = earlier.version, earlier.generation
				if err := manager.writeMetadata(root, entry); err != nil {
					t.Fatal(err)
				}
			}
			seed, err := manager.AcquireScopedScratch(
				t.Context(), taskMergeTestCompanyID, "engineering", conversationID,
				uuid.MustParse("00000000-0000-4000-8000-000000002101"), 0,
			)
			if err != nil {
				t.Fatalf("AcquireScopedScratch(seed) error = %v", err)
			}
			if err := manager.MarkCompleted(seed.Root); err != nil {
				t.Fatal(err)
			}
			rewriteAsEarlier(seed.Root)
			if err := os.WriteFile(
				filepath.Join(seed.WorkDir, "earlier.txt"),
				[]byte("kept\n"),
				0o600,
			); err != nil {
				t.Fatal(err)
			}

			continued, err := manager.AcquireScopedScratch(
				t.Context(), taskMergeTestCompanyID, "engineering", conversationID,
				uuid.MustParse("00000000-0000-4000-8000-000000002102"), 0,
			)
			if err != nil || !continued.Reused || continued.Root != seed.Root {
				t.Fatalf(
					"AcquireScopedScratch(generation 0) = %+v, %v, want the earlier root",
					continued,
					err,
				)
			}
			if err := manager.MarkCompleted(continued.Root); err != nil {
				t.Fatal(err)
			}
			rewriteAsEarlier(seed.Root)

			handedOff, err := manager.AcquireScopedScratch(
				t.Context(), taskMergeTestCompanyID, "engineering", conversationID,
				uuid.MustParse("00000000-0000-4000-8000-000000002103"), 1,
			)
			if err != nil || handedOff.Reused || handedOff.Root == seed.Root {
				t.Fatalf(
					"AcquireScopedScratch(generation 1) = %+v, %v, want a fresh root",
					handedOff,
					err,
				)
			}
			if _, err := os.Stat(filepath.Join(handedOff.WorkDir, "earlier.txt")); !os.IsNotExist(err) {
				t.Fatalf("generation 1 continued the earlier root's file: %v", err)
			}
			retired, err := manager.readMetadata(seed.Root)
			if err != nil || retired.State != workspaceSuperseded || retired.WorkspaceGeneration != 0 {
				t.Fatalf("earlier root = %+v, %v, want superseded generation 0", retired, err)
			}
		})
	}

	t.Run("a later generation root keeps its number", func(t *testing.T) {
		t.Parallel()
		manager, root := newTaskMergeTestManager(t)
		conversationID := "01900000-0000-4000-8000-0000000000f6"
		seed, err := manager.AcquireScopedScratch(
			t.Context(), taskMergeTestCompanyID, "engineering", conversationID,
			uuid.MustParse("00000000-0000-4000-8000-000000002201"), 2,
		)
		if err != nil {
			t.Fatalf("AcquireScopedScratch(generation 2) error = %v", err)
		}
		want := filepath.Join(root, taskMergeTestCompanyID, "engineering", "worktrees", conversationID+"-g2")
		if seed.Root != want {
			t.Fatalf("generation 2 root = %s, want %s", seed.Root, want)
		}
		if err := manager.MarkCompleted(seed.Root); err != nil {
			t.Fatal(err)
		}
		continued, err := manager.AcquireScopedScratch(
			t.Context(), taskMergeTestCompanyID, "engineering", conversationID,
			uuid.MustParse("00000000-0000-4000-8000-000000002202"), 2,
		)
		if err != nil || !continued.Reused || continued.Root != seed.Root {
			t.Fatalf("AcquireScopedScratch(generation 2 again) = %+v, %v", continued, err)
		}
	})
}

// The generation lives in the signed sidecar as well as the directory name,
// so renaming a root to impersonate another generation invalidates it.
func TestScopedScratchRejectsARootRenamedToAnotherGeneration(t *testing.T) {
	t.Parallel()

	manager, root := newTaskMergeTestManager(t)
	conversationID := "01900000-0000-4000-8000-0000000000f2"
	runID := uuid.MustParse("00000000-0000-4000-8000-000000001801")
	acquired, err := manager.AcquireScopedScratch(
		t.Context(), taskMergeTestCompanyID, "engineering", conversationID, runID, 2,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(generation 2) error = %v", err)
	}
	if err := manager.MarkCompleted(acquired.Root); err != nil {
		t.Fatalf("MarkCompleted() error = %v", err)
	}
	worktrees := filepath.Join(root, taskMergeTestCompanyID, "engineering", "worktrees")
	renamed := filepath.Join(worktrees, conversationID+"-g3")
	if err := os.Rename(acquired.Root, renamed); err != nil {
		t.Fatalf("rename the workspace root: %v", err)
	}
	metadataPath, err := manager.metadataPath(acquired.Root)
	if err != nil {
		t.Fatalf("metadataPath() error = %v", err)
	}
	renamedMetadataPath, err := manager.metadataPath(renamed)
	if err != nil {
		t.Fatalf("metadataPath(renamed) error = %v", err)
	}
	if err := os.Rename(metadataPath, renamedMetadataPath); err != nil {
		t.Fatalf("rename the workspace sidecar: %v", err)
	}
	if _, err := manager.readMetadata(renamed); !errors.Is(err, errMetadataUnusable) {
		t.Fatalf("readMetadata(renamed root) error = %v, want errMetadataUnusable", err)
	}
}

// A Task worktree's own folder in its Project main's repository (its HEAD,
// index and their logs) is the one place there its Run may write. The
// manager names it on every acquire, as Git itself finds it.
// The worktree's .git file is the Run's to change, so the folder is checked
// both ways: a .git file pointed at another worktree's folder, at the main's
// own repository, or made a link names nothing, and so does a folder whose
// gitdir file does not name the worktree back.
func TestScopedScratchNamesTheWorktreesOwnGitFolder(t *testing.T) {
	t.Parallel()
	manager, root := newTaskMergeTestManager(t)
	acquire := func(conversationID string, runID uuid.UUID) Acquired {
		t.Helper()
		acquired, err := manager.AcquireScopedScratch(
			t.Context(), taskMergeTestCompanyID, "engineering", conversationID, runID, 0,
		)
		if err != nil {
			t.Fatalf("AcquireScopedScratch(%s) error = %v", conversationID, err)
		}
		return acquired
	}
	const firstConversation, secondConversation = "01900000-0000-4000-8000-0000000000e1",
		"01900000-0000-4000-8000-0000000000e2"
	first := acquire(firstConversation, uuid.MustParse("00000000-0000-4000-8000-000000001701"))
	second := acquire(secondConversation, uuid.MustParse("00000000-0000-4000-8000-000000001702"))
	mainDir := filepath.Join(root, taskMergeTestCompanyID, "engineering", mainDirectoryName)
	worktrees := filepath.Join(mainDir, ".git", "worktrees")
	for _, acquired := range []Acquired{first, second} {
		if filepath.Dir(acquired.GitDir) != worktrees {
			t.Fatalf("GitDir = %q, want a folder directly in %s", acquired.GitDir, worktrees)
		}
		want, err := filepath.EvalSymlinks(acquired.GitDir)
		if err != nil {
			t.Fatal(err)
		}
		got, err := filepath.EvalSymlinks(strings.TrimSpace(
			runTaskMergeTestGit(t, acquired.WorkDir, "rev-parse", "--absolute-git-dir"),
		))
		if err != nil || got != want {
			t.Fatalf("Git finds the worktree's folder at %q (%v), the manager names %q", got, err, want)
		}
	}
	if first.GitDir == second.GitDir {
		t.Fatalf("two worktrees share the Git folder %s", first.GitDir)
	}
	if err := manager.MarkCompleted(first.Root); err != nil {
		t.Fatal(err)
	}
	again := acquire(firstConversation, uuid.MustParse("00000000-0000-4000-8000-000000001703"))
	if !again.Reused || again.GitDir != first.GitDir {
		t.Fatalf("reacquired worktree = %+v, want the same Git folder %s", again, first.GitDir)
	}

	gitFile := filepath.Join(first.WorkDir, ".git")
	original, err := os.ReadFile(gitFile)
	if err != nil {
		t.Fatal(err)
	}
	backFile := filepath.Join(first.GitDir, "gitdir")
	back, err := os.ReadFile(backFile)
	if err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "git")
	writeTestFile(t, linked, string(original))
	for _, step := range []struct {
		name          string
		gitFile, back string // the .git file's content ("" for a link) and the folder's gitdir
	}{
		{"points at another worktree's folder", "gitdir: " + second.GitDir + "\n", string(back)},
		{"points at the main's own repository", "gitdir: " + filepath.Join(mainDir, ".git") + "\n", string(back)},
		{"is a link", "", string(back)},
		{"is not named back by the folder", string(original), filepath.Join(second.WorkDir, ".git") + "\n"},
	} {
		if got := worktreeGitDir(mainDir, first.WorkDir); got != first.GitDir {
			t.Fatalf("worktreeGitDir before %q = %q, want %q", step.name, got, first.GitDir)
		}
		if err := os.Remove(gitFile); err != nil {
			t.Fatal(err)
		}
		if step.gitFile == "" {
			if err := os.Symlink(linked, gitFile); err != nil {
				t.Fatal(err)
			}
		} else {
			writeTestFile(t, gitFile, step.gitFile)
		}
		writeTestFile(t, backFile, step.back)
		if got := worktreeGitDir(mainDir, first.WorkDir); got != "" {
			t.Errorf("worktreeGitDir when the .git file %s = %q, want none", step.name, got)
		}
		if err := os.Remove(gitFile); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, gitFile, string(original))
		writeTestFile(t, backFile, string(back))
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
