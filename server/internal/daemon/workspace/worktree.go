// Package workspace manages per-Run worktrees and directory locks.
package workspace

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon/repocache"
)

const (
	metadataDirectoryName = ".metadata"
	metadataFileSuffix    = ".json"
	mainDirectoryName     = "main"
	// workdirDirectoryName is the agent working directory inside a
	// conversation environment root. Keeping the cwd one level below the
	// lifecycle root leaves room for sibling artifacts (output, logs) without
	// moving the agent cwd — and the cwd must never move, because provider
	// CLIs key their resumable session stores to the exact working directory.
	workdirDirectoryName = "workdir"
	// memoryDirectoryName is the durable Markdown memory directory workspace
	// preparation creates in every main workspace and Task workdir.
	memoryDirectoryName   = ".memory"
	legacyMetadataVersion = 2
	// generationlessMetadataVersion is the scoped record this build reads but
	// no longer writes: a Task worktree from before workspace generations
	// (ADR 0014). Such a root is the Task's first generation.
	generationlessMetadataVersion = 3
	metadataVersion               = 4
)

type Retention struct {
	Completed time.Duration
	Orphaned  time.Duration
}

// Acquired describes an attached-or-created conversation workspace. Root is
// the lifecycle handle (MarkCompleted/MarkOrphaned); WorkDir is the agent cwd.
// Reused reports that an existing conversation environment was re-entered,
// which is the precondition for provider session resume.
type Acquired struct {
	Root    string
	WorkDir string
	Branch  string
	Reused  bool
	// GitDir is the Task worktree's own folder in its Project main's
	// repository (.git/worktrees/<name>: its HEAD, index and their logs),
	// the one place there a Run in the worktree may write. Empty for a scratch
	// workspace, and when the worktree's .git file
	// and that folder do not name each other.
	GitDir string
}

type Manager struct {
	mu                   sync.Mutex
	mainMutation         *mainMutationGate
	mainMutationLockPath string
	root                 string
	cacheRoot            string
	metaRoot             string
	metadataKey          []byte
	runner               repocache.GitRunner
	retention            Retention
	now                  func() time.Time
	// inUse fences conversation environments between AcquireScratch and
	// MarkCompleted/MarkSuperseded/MarkOrphaned so a second Run of the same
	// conversation can never refresh or collect a workdir another local Run
	// is still executing in. Runs of one conversation are serialized
	// cloud-side; this is the local belt-and-suspenders.
	inUse map[string]struct{}
}

func NewManager(
	root, cacheRoot string,
	metadataKey []byte,
	runner repocache.GitRunner,
	retention Retention,
	now func() time.Time,
) (*Manager, error) {
	root = filepath.Clean(root)
	cacheRoot = filepath.Clean(cacheRoot)
	metaRoot := filepath.Join(root, metadataDirectoryName)
	if strings.TrimSpace(root) == "" || strings.TrimSpace(cacheRoot) == "" ||
		!filepath.IsAbs(root) || !filepath.IsAbs(cacheRoot) || volumeRoot(root) || volumeRoot(cacheRoot) ||
		pathsOverlap(root, cacheRoot) || len(metadataKey) < 32 || runner == nil ||
		retention.Completed <= 0 || retention.Orphaned <= retention.Completed {
		return nil, errors.New("invalid worktree manager configuration")
	}
	for _, path := range []string{root, metaRoot, cacheRoot} {
		if err := requireRealDirectory(path, true); err != nil {
			return nil, errors.New("worktree management roots must be real directories")
		}
	}
	if now == nil {
		now = time.Now
	}
	mainMutationLockPath := filepath.Join(metaRoot, mainMutationLockFileName)
	return &Manager{
		mainMutation: sharedMainMutationGate(mainMutationLockPath), mainMutationLockPath: mainMutationLockPath,
		root: root, cacheRoot: cacheRoot, metaRoot: metaRoot,
		metadataKey: append([]byte(nil), metadataKey...), runner: runner, retention: retention, now: now,
		inUse: make(map[string]struct{}),
	}, nil
}

func requireRealDirectory(path string, allowMissing bool) error {
	info, err := os.Lstat(path)
	if allowMissing && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("path is not a real directory")
	}
	return nil
}

func (m *Manager) ensureWritableRoots() error {
	if err := os.MkdirAll(m.root, 0o700); err != nil {
		return fmt.Errorf("create worktree root: %w", err)
	}
	if err := requireRealDirectory(m.root, false); err != nil {
		return errors.New("worktree root is not a real directory")
	}
	if err := os.MkdirAll(m.metaRoot, 0o700); err != nil {
		return fmt.Errorf("create worktree metadata root: %w", err)
	}
	if err := requireRealDirectory(m.metaRoot, false); err != nil {
		return errors.New("worktree metadata root is not a real directory")
	}
	return nil
}

func (m *Manager) verifyReadableRoots() (bool, error) {
	if err := requireRealDirectory(m.root, false); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, errors.New("worktree root is not a real directory")
	}
	if err := requireRealDirectory(m.metaRoot, true); err != nil {
		return false, errors.New("worktree metadata root is not a real directory")
	}
	return true, nil
}

func pathsOverlap(left, right string) bool {
	for _, pair := range [][2]string{{left, right}, {right, left}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil &&
			(relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

type workspaceState string

const (
	workspacePreparing workspaceState = "preparing"
	workspaceActive    workspaceState = "active"
	workspaceCompleted workspaceState = "completed"
	workspaceOrphaned  workspaceState = "orphaned"
	// workspaceSuperseded is a Task root whose workspace generation a
	// confirmed handoff advanced past (ADR 0014). No Run continues it, and
	// because the work in it was never merged, GC keeps it until a
	// disposition is recorded.
	workspaceSuperseded workspaceState = "superseded"
)

type metadata struct {
	Version        int    `json:"version"`
	Kind           string `json:"kind"`
	CompanyID      string `json:"companyId,omitempty"`
	Scope          string `json:"scope,omitempty"`
	ConversationID string `json:"conversationId"`
	// LastRunID is the Run UUID as text. Retained decimal identities are
	// accepted for offline migration. It is a diagnostic label the
	// manager never resolves, and it stays text so the v2 authentication body
	// below keeps signing the same bytes it always did.
	LastRunID string `json:"lastRunId,omitempty"`
	// WorkspaceGeneration is the identity of the Task workspace this root
	// holds (ADR 0014): the server's tasks.workspace_generation, exactly.
	// Only a confirmed handoff advances it, and a root prepared under an
	// older generation is never continued: the new generation gets a root of
	// its own, prepared from the machine's Project main, and the old one is
	// superseded and retained with whatever was left in it. It is part of the
	// signed body, and it agrees with the directory name.
	WorkspaceGeneration int64          `json:"workspaceGeneration,omitempty"`
	CacheName           string         `json:"cacheName"`
	Branch              string         `json:"branch"`
	State               workspaceState `json:"state"`
	CreatedAt           time.Time      `json:"createdAt"`
	CompletedAt         *time.Time     `json:"completedAt,omitempty"`
	OrphanedAt          *time.Time     `json:"orphanedAt,omitempty"`
	SupersededAt        *time.Time     `json:"supersededAt,omitempty"`
	Signature           string         `json:"signature"`
}

// scopedRootName is the directory a Task's worktree lives in: the
// Conversation id, and past the first generation a "-g<N>" suffix naming the
// server's generation. The first generation keeps the plain name, so every
// root prepared before generations existed is already correctly named.
func scopedRootName(conversationID string, generation int64) string {
	if generation <= firstWorkspaceGeneration {
		return conversationID
	}
	return conversationID + workspaceGenerationSeparator + strconv.FormatInt(generation, 10)
}

// splitScopedRootName reads a workspace directory name back into the
// Conversation it belongs to and the generation it holds. A name without a
// suffix is the first generation; a suffix scopedRootName would not write
// ("-g0", "-g01") leaves the name whole, so it names no Conversation.
func splitScopedRootName(name string) (string, int64) {
	index := strings.LastIndex(name, workspaceGenerationSeparator)
	if index <= 0 {
		return name, firstWorkspaceGeneration
	}
	suffix := name[index+len(workspaceGenerationSeparator):]
	generation, err := strconv.ParseInt(suffix, 10, 64)
	if err != nil || generation <= firstWorkspaceGeneration || strconv.FormatInt(generation, 10) != suffix {
		return name, firstWorkspaceGeneration
	}
	return name[:index], generation
}

// normalizeWorkspaceGeneration maps a value a server sends onto a real
// generation. The server's first generation is 0, which is also what a build
// from before ADR 0014 sends by omitting the field; its first handoff makes
// it 1. Every distinct value is a distinct root.
func normalizeWorkspaceGeneration(generation int64) int64 {
	if generation < firstWorkspaceGeneration {
		return firstWorkspaceGeneration
	}
	return generation
}

const (
	firstWorkspaceGeneration     = 0
	workspaceGenerationSeparator = "-g"
)

const (
	metadataKindManaged        = "managed"
	metadataKindManagedScratch = "managed_scratch"
	metadataKindCrashOrphan    = "crash_orphan"
)

var workspaceScopePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// errMetadataUnusable classifies a sidecar that exists but cannot be trusted:
// legacy schema versions (pre-conversation per-Run records), failed HMAC
// verification, or semantic invalidity. Reconciliation treats such records
// exactly like missing ones so previous-build directories migrate through the
// crash-orphan retention path instead of blocking maintenance forever.
var errMetadataUnusable = errors.New("workspace metadata is unusable")

// AcquireScratch attaches to (or creates) the provider-free conversation
// workspace. Reuse is deliberate: the conversation's accumulated files are the
// working context of a resumed provider session.
func (m *Manager) AcquireScratch(conversationID string, runID uuid.UUID) (Acquired, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	conversationID = strings.TrimSpace(conversationID)
	if !validWorkspaceID(conversationID) || runID == uuid.Nil {
		return Acquired{}, errors.New("invalid scratch workspace identity")
	}
	if err := m.ensureWritableRoots(); err != nil {
		return Acquired{}, err
	}
	if _, busy := m.inUse[conversationID]; busy {
		return Acquired{}, errors.New("conversation workspace is already in use")
	}
	root := filepath.Join(m.root, conversationID)
	workDir := filepath.Join(root, workdirDirectoryName)
	attach, entry, err := m.attachState(root, metadataKindManagedScratch, "")
	if err != nil {
		return Acquired{}, err
	}
	if attach {
		entry.LastRunID = runID.String()
		entry.State = workspaceActive
		entry.CompletedAt, entry.OrphanedAt = nil, nil
		if err := m.writeMetadata(root, entry); err != nil {
			return Acquired{}, err
		}
		if err := os.MkdirAll(workDir, 0o700); err != nil {
			return Acquired{}, fmt.Errorf("reopen scratch conversation workdir: %w", err)
		}
		m.inUse[conversationID] = struct{}{}
		return Acquired{Root: root, WorkDir: workDir, Reused: true}, nil
	}
	if err := m.removeUnusableRoot(root); err != nil {
		return Acquired{}, err
	}
	fresh := metadata{
		Version: legacyMetadataVersion, Kind: metadataKindManagedScratch,
		ConversationID: conversationID, LastRunID: runID.String(),
		State: workspacePreparing, CreatedAt: m.now().UTC(),
	}
	// Authenticate the conversation identity before the filesystem mutation so
	// startup reconciliation can safely classify a crash at either boundary.
	if err := m.writeMetadata(root, fresh); err != nil {
		return Acquired{}, err
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return Acquired{}, fmt.Errorf("create scratch conversation workspace: %w", err)
	}
	fresh.State = workspaceActive
	if err := m.writeMetadata(root, fresh); err != nil {
		return Acquired{}, err
	}
	m.inUse[conversationID] = struct{}{}
	return Acquired{Root: root, WorkDir: workDir, Reused: false}, nil
}

// AcquireScopedScratch attaches to or creates a Task worktree inside its
// Company and Project scope. The signed sidecar binds the retained
// conversation workspace to the exact Run that populated it.
func (m *Manager) AcquireScopedScratch(
	ctx context.Context,
	companyID, scope, conversationID string,
	runID uuid.UUID,
	workspaceGeneration int64,
) (Acquired, error) {
	var acquired Acquired
	err := m.withMainMutation(ctx, func(ctx context.Context) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		var acquireErr error
		acquired, acquireErr = m.acquireScopedScratchLocked(
			ctx,
			companyID,
			scope,
			conversationID,
			runID,
			workspaceGeneration,
		)
		return acquireErr
	})
	return acquired, err
}

func (m *Manager) acquireScopedScratchLocked(
	ctx context.Context,
	companyID, scope, conversationID string,
	runID uuid.UUID,
	workspaceGeneration int64,
) (Acquired, error) {
	companyID = strings.TrimSpace(companyID)
	scope = strings.TrimSpace(scope)
	conversationID = strings.TrimSpace(conversationID)
	if !validWorkspaceID(companyID) || !validWorkspaceScope(scope) ||
		!validWorkspaceID(conversationID) || runID == uuid.Nil {
		return Acquired{}, errors.New("invalid scoped scratch workspace identity")
	}
	if err := m.ensureWritableRoots(); err != nil {
		return Acquired{}, err
	}
	if _, busy := m.inUse[conversationID]; busy {
		return Acquired{}, errors.New("conversation workspace is already in use")
	}
	container, worktreesRoot, err := m.ensureScopeContainer(companyID, scope)
	if err != nil {
		return Acquired{}, err
	}
	mainPath := filepath.Join(container, mainDirectoryName)
	if err := os.MkdirAll(mainPath, 0o700); err != nil {
		return Acquired{}, fmt.Errorf("create scoped main workspace: %w", err)
	}
	if err := requireRealDirectory(mainPath, false); err != nil {
		return Acquired{}, errors.New("scoped main workspace is not a real directory")
	}
	if err := ensureMemoryDirectory(mainPath); err != nil {
		return Acquired{}, err
	}
	if err := m.ensureMainGitRepository(ctx, mainPath); err != nil {
		return Acquired{}, err
	}

	// The generation is part of the root's name, so a Task handed to another
	// member lands in a root of its own instead of continuing the files the
	// previous assignee left behind (ADR 0014).
	generation := normalizeWorkspaceGeneration(workspaceGeneration)
	root := filepath.Join(worktreesRoot, scopedRootName(conversationID, generation))
	workDir := filepath.Join(root, workdirDirectoryName)
	branch := taskBranch(runID)
	attach, entry, err := m.attachState(root, metadataKindManagedScratch, "")
	if err != nil {
		return Acquired{}, err
	}
	if entry.State == workspaceSuperseded {
		// A newer generation already ran here. The Run is stale; neither
		// continuing nor replacing this root is safe.
		return Acquired{}, errors.New("task workspace generation was superseded by a handoff")
	}
	// Every older generation of this Task on this machine is finished for
	// good: mark it so GC keeps it instead of collecting unmerged work.
	if err := m.supersedeOlderGenerationsLocked(worktreesRoot, conversationID, generation); err != nil {
		return Acquired{}, err
	}
	if attach {
		previousBranch := entry.Branch
		entry.LastRunID = runID.String()
		entry.Version = metadataVersion
		entry.WorkspaceGeneration = generation
		entry.Branch = branch
		entry.State = workspaceActive
		entry.CompletedAt, entry.OrphanedAt = nil, nil
		if err := m.writeMetadata(root, entry); err != nil {
			return Acquired{}, err
		}
		if err := os.MkdirAll(workDir, 0o700); err != nil {
			return Acquired{}, fmt.Errorf("reopen scoped scratch conversation workdir: %w", err)
		}
		if err := m.refreshScopedWorktree(ctx, mainPath, workDir, branch, previousBranch); err != nil {
			return Acquired{}, fmt.Errorf("refresh scoped scratch worktree: %w", err)
		}
		if err := ensureMemoryDirectory(workDir); err != nil {
			return Acquired{}, err
		}
		m.inUse[conversationID] = struct{}{}
		return Acquired{
			Root: root, WorkDir: workDir, Branch: branch, Reused: true,
			GitDir: worktreeGitDir(mainPath, workDir),
		}, nil
	}
	if err := m.removeUnusableRoot(root); err != nil {
		return Acquired{}, err
	}
	fresh := metadata{
		Version: metadataVersion, Kind: metadataKindManagedScratch,
		CompanyID: companyID, Scope: scope,
		ConversationID: conversationID, LastRunID: runID.String(),
		WorkspaceGeneration: generation,
		Branch:              branch, State: workspacePreparing, CreatedAt: m.now().UTC(),
	}
	if err := m.writeMetadata(root, fresh); err != nil {
		return Acquired{}, err
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		return Acquired{}, fmt.Errorf("create scoped scratch conversation root: %w", err)
	}
	if err := m.runGit(
		ctx,
		mainPath,
		nil,
		"worktree",
		"add",
		"--force",
		"-B",
		branch,
		workDir,
		"main",
	); err != nil {
		return Acquired{}, fmt.Errorf("create scoped scratch conversation worktree: %w", err)
	}
	if err := ensureMemoryDirectory(workDir); err != nil {
		return Acquired{}, err
	}
	fresh.State = workspaceActive
	if err := m.writeMetadata(root, fresh); err != nil {
		return Acquired{}, err
	}
	m.inUse[conversationID] = struct{}{}
	return Acquired{
		Root: root, WorkDir: workDir, Branch: branch, Reused: false,
		GitDir: worktreeGitDir(mainPath, workDir),
	}, nil
}

// worktreeGitDir is the folder Git keeps in mainDir's repository for the
// worktree at workDir (.git/worktrees/<name>), spelled under mainDir. Git
// names that folder after the worktree's own name and a number, so it is
// read back from workDir's .git file. That file is the Run's to change, so
// the answer is checked both ways: the folder must lie directly in mainDir's
// .git/worktrees, as a real folder, and its gitdir file must name workDir's
// .git back. Another worktree's folder names its own worktree, so no Run
// can point this at it. It is empty when any check fails; the Run then
// writes nothing in the repository.
func worktreeGitDir(mainDir, workDir string) string {
	readPointer := func(file string) (string, bool) {
		info, err := os.Lstat(file)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
			return "", false
		}
		content, err := os.ReadFile(file)
		if err != nil {
			return "", false
		}
		target := strings.TrimSpace(strings.TrimPrefix(string(content), "gitdir:"))
		if target == "" || strings.ContainsAny(target, "\n\r") {
			return "", false
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(file), target)
		}
		resolved, err := filepath.EvalSymlinks(target)
		return resolved, err == nil
	}
	gitDir, ok := readPointer(filepath.Join(workDir, ".git"))
	if !ok {
		return ""
	}
	worktrees, err := filepath.EvalSymlinks(filepath.Join(mainDir, ".git", "worktrees"))
	if err != nil || filepath.Dir(gitDir) != worktrees {
		return ""
	}
	own := filepath.Join(mainDir, ".git", "worktrees", filepath.Base(gitDir))
	if err := requireRealDirectory(own, false); err != nil {
		return ""
	}
	back, ok := readPointer(filepath.Join(own, "gitdir"))
	if !ok {
		return ""
	}
	worktreeFile, err := filepath.EvalSymlinks(filepath.Join(workDir, ".git"))
	if err != nil || back != worktreeFile {
		return ""
	}
	return own
}

// EnsureMain returns the durable main workspace for a Company or legacy
// scope, creating and Git-initializing it on first use. Main workspaces are
// persistent Git roots that non-task conversation runs execute in directly;
// they are not Run lifecycle worktrees, so they carry no signed metadata and
// are never garbage collected.
func (m *Manager) EnsureMain(ctx context.Context, companyID, scope string) (string, error) {
	var path string
	err := m.withMainMutation(ctx, func(ctx context.Context) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		companyID = strings.TrimSpace(companyID)
		scope = strings.TrimSpace(scope)
		if !validWorkspaceID(companyID) || !validWorkspaceScope(scope) {
			return errors.New("invalid main workspace scope")
		}
		if err := m.ensureWritableRoots(); err != nil {
			return err
		}
		container, _, err := m.ensureScopeContainer(companyID, scope)
		if err != nil {
			return err
		}
		path = filepath.Join(container, mainDirectoryName)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create main workspace: %w", err)
		}
		if err := requireRealDirectory(path, false); err != nil {
			return errors.New("main workspace is not a real directory")
		}
		if err := ensureMemoryDirectory(path); err != nil {
			return err
		}
		return m.ensureMainGitRepository(ctx, path)
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

// ensureMemoryDirectory creates the workspace's `.memory` directory so agents
// and the local Memory catalog share one durable location. No `.gitkeep` is
// added: an empty memory stays invisible to Git.
func ensureMemoryDirectory(workspaceRoot string) error {
	memoryPath := filepath.Join(workspaceRoot, memoryDirectoryName)
	if err := os.MkdirAll(memoryPath, 0o700); err != nil {
		return fmt.Errorf("create workspace memory directory: %w", err)
	}
	if err := requireRealDirectory(memoryPath, false); err != nil {
		return errors.New("workspace memory directory is not a real directory")
	}
	return nil
}

func (m *Manager) ensureScopeContainer(companyID, scope string) (string, string, error) {
	companyRoot := filepath.Join(m.root, companyID)
	if err := os.MkdirAll(companyRoot, 0o700); err != nil {
		return "", "", fmt.Errorf("create workspace company container: %w", err)
	}
	if err := requireRealDirectory(companyRoot, false); err != nil {
		return "", "", errors.New("workspace company container is not a real directory")
	}
	if strings.HasPrefix(scope, "projects/") {
		projectsRoot := filepath.Join(companyRoot, "projects")
		if err := os.MkdirAll(projectsRoot, 0o700); err != nil {
			return "", "", err
		}
		if err := requireRealDirectory(projectsRoot, false); err != nil {
			return "", "", err
		}
	}
	container := filepath.Join(companyRoot, scope)
	if err := os.MkdirAll(container, 0o700); err != nil {
		return "", "", fmt.Errorf("create workspace scope container: %w", err)
	}
	if err := requireRealDirectory(container, false); err != nil {
		return "", "", errors.New("workspace scope container is not a real directory")
	}
	worktreesRoot := filepath.Join(container, "worktrees")
	if err := os.MkdirAll(worktreesRoot, 0o700); err != nil {
		return "", "", fmt.Errorf("create scoped worktrees root: %w", err)
	}
	if err := requireRealDirectory(worktreesRoot, false); err != nil {
		return "", "", errors.New("scoped worktrees root is not a real directory")
	}
	return container, worktreesRoot, nil
}

func (m *Manager) runGit(
	ctx context.Context,
	dir string,
	environment []string,
	args ...string,
) error {
	switch m.runner.(type) {
	case repocache.OSRunner, *repocache.OSRunner:
		// OSRunner appends overrides to the inherited environment and therefore
		// cannot truly unset repository-control variables. Execute the known
		// production runner here with a replacement environment instead.
		return runGit(ctx, dir, environment, nil, args...)
	}
	return m.runner.Run(ctx, dir, managedGitRunnerEnvironment(environment), args...)
}

func (m *Manager) ensureMainGitRepository(ctx context.Context, path string) error {
	if err := m.runGit(ctx, path, nil, "rev-parse", "--is-inside-work-tree"); err != nil {
		if err := m.runGit(ctx, path, nil, "init", "--initial-branch=main"); err != nil {
			return fmt.Errorf("initialize main workspace Git repository: %w", err)
		}
	}
	if err := m.runGit(ctx, path, nil, "rev-parse", "--verify", "HEAD"); err == nil {
		return nil
	}
	if err := m.runGit(ctx, path, nil, "add", "-A"); err != nil {
		return fmt.Errorf("stage initial main workspace content: %w", err)
	}
	if err := m.runGit(
		ctx,
		path,
		managedGitCommitEnvironment(),
		"commit",
		"--allow-empty",
		"-m",
		"Initialize workspace",
	); err != nil {
		return fmt.Errorf("commit initial main workspace state: %w", err)
	}
	return nil
}

func (m *Manager) refreshScopedWorktree(
	ctx context.Context,
	mainPath, workDir, branch, previousBranch string,
) error {
	base := "main"
	previousBranchExists := previousBranch != "" && m.runGit(
		ctx,
		mainPath,
		nil,
		"show-ref",
		"--verify",
		"--quiet",
		"refs/heads/"+previousBranch,
	) == nil
	if previousBranchExists {
		base = previousBranch
	} else {
		if err := m.runGit(ctx, workDir, nil, "reset", "--hard", base); err != nil {
			return fmt.Errorf("reset scoped worktree fallback: %w", err)
		}
		if err := m.runGit(ctx, workDir, nil, "clean", "-fd"); err != nil {
			return fmt.Errorf("clean scoped worktree fallback: %w", err)
		}
	}
	if err := m.runGit(ctx, workDir, nil, "checkout", "-B", branch, base); err != nil {
		return fmt.Errorf("checkout scoped Run branch: %w", err)
	}
	if previousBranch != "" && previousBranch != branch {
		// Once the reusable worktree has moved to the new Run, the old branch is
		// only local administration. Failure to remove it cannot corrupt either
		// workspace and GC will prune it later.
		_ = m.runGit(ctx, mainPath, nil, "update-ref", "-d", "refs/heads/"+previousBranch)
	}
	return nil
}

func taskBranch(runID uuid.UUID) string {
	return "zenith-team/task-" + runID.String()
}

// AcquireIsolatedScratch creates a scratch identity scoped to one cloud Run.
// Schedule occurrences use it so files from an earlier occurrence can never
// become ambient input to a later one. The derived UUID keeps the existing
// signed lifecycle and GC machinery authoritative without adding a parallel
// workspace store.
func (m *Manager) AcquireIsolatedScratch(conversationID string, runID uuid.UUID) (Acquired, error) {
	conversationID = strings.TrimSpace(conversationID)
	if !validWorkspaceID(conversationID) || runID == uuid.Nil {
		return Acquired{}, errors.New("invalid isolated scratch workspace identity")
	}
	identity := uuid.NewSHA1(
		uuid.NameSpaceOID,
		[]byte(fmt.Sprintf("zenith-team:isolated-run:%s:%s", conversationID, runID)),
	).String()
	acquired, err := m.AcquireScratch(identity, runID)
	if err != nil {
		return Acquired{}, err
	}
	if acquired.Reused {
		return Acquired{}, errors.Join(
			errors.New("isolated scratch workspace was already used"),
			m.MarkOrphaned(acquired.Root),
		)
	}
	return acquired, nil
}

// attachState decides whether the conversation environment can be re-entered:
// the root must exist as a real directory with a verified sidecar of the same
// kind (and repository cache for managed Git), past its preparing phase. A
// missing or unusable sidecar and any mismatch fall through to fresh creation.
func (m *Manager) attachState(root, kind, cacheName string) (bool, metadata, error) {
	exists, err := workspaceDirectoryExists(root)
	if err != nil {
		return false, metadata{}, fmt.Errorf("inspect conversation workspace: %w", err)
	}
	entry, err := m.readMetadata(root)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, errMetadataUnusable) {
		return false, metadata{}, nil
	}
	if err != nil {
		return false, metadata{}, err
	}
	if !exists || entry.Kind != kind || entry.State == workspacePreparing ||
		entry.Kind == metadataKindManaged && entry.CacheName != cacheName {
		return false, entry, nil
	}
	return true, entry, nil
}

// removeUnusableRoot clears a conversation directory that failed attachment.
// The deletion trigger is the authenticated cloud claim for this conversation,
// never a forgeable filesystem signal.
func (m *Manager) removeUnusableRoot(root string) error {
	exists, err := workspaceDirectoryExists(root)
	if err != nil {
		return fmt.Errorf("inspect conversation workspace: %w", err)
	}
	if !exists {
		return nil
	}
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("remove unusable conversation workspace: %w", err)
	}
	return nil
}

// MarkCompleted records the conversation's most recent Run as finished. The
// directory deliberately survives for reuse by the conversation's next Run;
// only the Completed retention clock starts here.
func (m *Manager) MarkCompleted(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := m.loadManaged(path)
	if err != nil {
		return err
	}
	delete(m.inUse, entry.ConversationID)
	if entry.State == workspaceCompleted {
		return nil
	}
	if entry.State != workspaceActive {
		return errors.New("only an active workspace can complete")
	}
	now := m.now().UTC()
	entry.State = workspaceCompleted
	entry.CompletedAt = &now
	entry.OrphanedAt = nil
	return m.writeMetadata(path, entry)
}

// MarkSuperseded ends a Run whose Task a confirmed handoff took away (ADR
// 0014): the workspace generation moved on, so this root will never be
// continued, and the work in it stays exactly where it is until a disposition
// is recorded. It releases the Conversation like MarkCompleted does.
func (m *Manager) MarkSuperseded(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := m.loadManaged(path)
	if err != nil {
		return err
	}
	delete(m.inUse, entry.ConversationID)
	return m.supersedeLocked(path, entry)
}

// SupersedeFinished retires the root of a Run that had already finished, and
// completed its root, when the daemon learned a confirmed handoff cancelled
// it: the agent finished just as the handoff landed. Unlike MarkSuperseded it
// leaves the Conversation's in-use fence alone, because the Run released it
// when it finished and a Run of the newer generation may hold it now. A root
// a Run is using again is never retired from under it.
func (m *Manager) SupersedeFinished(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := m.loadManaged(path)
	if err != nil {
		return err
	}
	if entry.State == workspacePreparing || entry.State == workspaceActive {
		return errors.New("a workspace in use cannot be superseded")
	}
	return m.supersedeLocked(path, entry)
}

// supersedeLocked retires one Task root. The record is rewritten at the
// current version so its supersededAt is signed.
func (m *Manager) supersedeLocked(path string, entry metadata) error {
	if entry.State == workspaceSuperseded {
		return nil
	}
	if entry.Kind != metadataKindManagedScratch || entry.CompanyID == "" ||
		entry.State != workspaceActive && entry.State != workspaceCompleted &&
			entry.State != workspaceOrphaned {
		return errors.New("only a prepared Task workspace can be superseded")
	}
	now := m.now().UTC()
	entry.Version = metadataVersion
	entry.State = workspaceSuperseded
	entry.CompletedAt, entry.OrphanedAt = nil, nil
	entry.SupersededAt = &now
	return m.writeMetadata(path, entry)
}

// supersedeOlderGenerationsLocked retires every root an older workspace
// generation of this Conversation left in worktreesRoot. A root still
// preparing never held a Run's work and is left to the ordinary cleanup.
func (m *Manager) supersedeOlderGenerationsLocked(worktreesRoot, conversationID string, generation int64) error {
	names, err := os.ReadDir(worktreesRoot)
	if err != nil {
		return fmt.Errorf("list Task workspace generations: %w", err)
	}
	for _, name := range names {
		rootConversationID, rootGeneration := splitScopedRootName(name.Name())
		if !name.IsDir() || rootConversationID != conversationID || rootGeneration >= generation {
			continue
		}
		root := filepath.Join(worktreesRoot, name.Name())
		entry, err := m.readMetadata(root)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, errMetadataUnusable) {
			continue
		}
		if err != nil {
			return err
		}
		if entry.Kind != metadataKindManagedScratch || entry.State == workspacePreparing {
			continue
		}
		if err := m.supersedeLocked(root, entry); err != nil {
			return fmt.Errorf("supersede workspace generation %d: %w", rootGeneration, err)
		}
	}
	return nil
}

// MarkOrphaned records an aborted prepared workspace without deleting it.
// The longer orphan retention protects provider-owned session/cache artifacts
// and leaves crash recovery deterministic.
func (m *Manager) MarkOrphaned(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := m.loadManaged(path)
	if err != nil {
		return err
	}
	delete(m.inUse, entry.ConversationID)
	if entry.State == workspaceOrphaned {
		return nil
	}
	if entry.State != workspacePreparing && entry.State != workspaceActive {
		return errors.New("only a preparing or active workspace can become orphaned")
	}
	now := m.now().UTC()
	entry.State = workspaceOrphaned
	entry.CompletedAt = nil
	entry.OrphanedAt = &now
	return m.writeMetadata(path, entry)
}

// Reconcile marks recovered local conversation workspaces absent from the
// server's exact active-conversation set as orphaned. It never deletes during
// reconciliation.
func (m *Manager) Reconcile(ctx context.Context, activeConversationIDs map[string]struct{}) error {
	return m.withMainMutation(ctx, func(context.Context) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.reconcileLocked(activeConversationIDs)
	})
}

func (m *Manager) reconcileLocked(activeConversationIDs map[string]struct{}) error {
	if err := m.ensureWritableRoots(); err != nil {
		return err
	}
	records, err := m.records()
	if err != nil {
		return err
	}
	var passErr error
	for _, path := range records {
		basename := filepath.Base(path)
		// A root's name carries its workspace generation; the Conversation it
		// belongs to is what liveness is keyed on (ADR 0014).
		recordConversationID, recordGeneration := splitScopedRootName(basename)
		if _, busy := m.inUse[recordConversationID]; busy {
			// A locally executing Run is an authoritative liveness signal even
			// when the server's recovered active set lags behind it.
			continue
		}
		pathExists, pathErr := workspaceDirectoryExists(path)
		if pathErr != nil {
			passErr = errors.Join(
				passErr,
				fmt.Errorf("inspect conversation workspace %s: %w", basename, pathErr),
			)
			continue
		}
		entry, err := m.readMetadata(path)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, errMetadataUnusable) {
			if !pathExists {
				if errors.Is(err, errMetadataUnusable) {
					// A stale unusable sidecar without a directory (for example
					// a legacy per-Run record whose crash predates this build)
					// is pure debris; removing it needs no retention.
					if metadataPath, metadataErr := m.metadataPath(path); metadataErr == nil {
						_ = os.Remove(metadataPath)
					}
				}
				continue
			}
			if !validWorkspaceID(recordConversationID) {
				continue
			}
			if _, active := activeConversationIDs[recordConversationID]; active {
				continue
			}
			// Legacy per-Run directories from previous builds land here too:
			// their old-schema sidecars fail verification, so the directory is
			// re-marked as a freshly signed crash orphan and retention-cleaned.
			now := m.now().UTC()
			relative, relativeErr := filepath.Rel(m.root, path)
			layout, recognized := parseWorkspaceRecordPath(relative)
			if relativeErr != nil || !recognized {
				continue
			}
			version, generation := legacyMetadataVersion, int64(0)
			if layout.companyID != "" {
				version, generation = metadataVersion, recordGeneration
			}
			entry = metadata{
				Version: version, Kind: metadataKindCrashOrphan,
				CompanyID: layout.companyID, Scope: layout.scope,
				ConversationID: recordConversationID, WorkspaceGeneration: generation,
				State: workspaceOrphaned, CreatedAt: now, OrphanedAt: &now,
			}
			if err := m.writeMetadata(path, entry); err != nil {
				passErr = errors.Join(
					passErr,
					fmt.Errorf("mark crash-orphan workspace %s: %w", basename, err),
				)
			}
			continue
		}
		if err != nil {
			passErr = errors.Join(
				passErr,
				fmt.Errorf("read conversation workspace %s metadata: %w", basename, err),
			)
			continue
		}
		if _, active := activeConversationIDs[entry.ConversationID]; active {
			if entry.State == workspacePreparing && pathExists {
				entry.State = workspaceActive
				if err := m.writeMetadata(path, entry); err != nil {
					passErr = errors.Join(
						passErr,
						fmt.Errorf(
							"activate recovered workspace %s: %w",
							entry.ConversationID,
							err,
						),
					)
				}
			}
			continue
		}
		if entry.State != workspacePreparing && entry.State != workspaceActive {
			continue
		}
		now := m.now().UTC()
		entry.State = workspaceOrphaned
		entry.OrphanedAt = &now
		if err := m.writeMetadata(path, entry); err != nil {
			passErr = errors.Join(passErr, fmt.Errorf("orphan workspace %s: %w", entry.ConversationID, err))
		}
	}
	return passErr
}

func (m *Manager) GC(ctx context.Context) ([]string, error) {
	var removed []string
	err := m.withMainMutation(ctx, func(ctx context.Context) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		var gcErr error
		removed, gcErr = m.gcLocked(ctx, nil)
		return gcErr
	})
	return removed, err
}

// ReconcileAndGC applies one exact cloud active-conversation set before
// considering retention. Callers should prefer this startup/periodic
// maintenance API so a conversation with an active Run is an explicit
// deletion fence.
func (m *Manager) ReconcileAndGC(ctx context.Context, activeConversationIDs map[string]struct{}) ([]string, error) {
	var removed []string
	err := m.withMainMutation(ctx, func(ctx context.Context) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		reconcileErr := m.reconcileLocked(activeConversationIDs)
		var gcErr error
		removed, gcErr = m.gcLocked(ctx, activeConversationIDs)
		return errors.Join(reconcileErr, gcErr)
	})
	return removed, err
}

func (m *Manager) gcLocked(ctx context.Context, activeConversationIDs map[string]struct{}) ([]string, error) {
	rootExists, err := m.verifyReadableRoots()
	if err != nil || !rootExists {
		return nil, err
	}
	records, err := m.records()
	if err != nil {
		return nil, err
	}
	now := m.now().UTC()
	removed := make([]string, 0)
	var passErr error
	for _, path := range records {
		recordConversationID, _ := splitScopedRootName(filepath.Base(path))
		if _, busy := m.inUse[recordConversationID]; busy {
			continue
		}
		entry, err := m.readMetadata(path)
		if errors.Is(err, os.ErrNotExist) {
			// A missing sidecar is never an age signal: provider code can remove
			// files and forge mtimes. Reconcile must first prove the conversation
			// absent from the server's active set and create a freshly signed
			// orphan marker, which then starts the full retention period.
			continue
		}
		if err != nil {
			passErr = errors.Join(
				passErr,
				fmt.Errorf("read conversation workspace %s metadata: %w", filepath.Base(path), err),
			)
			continue
		}
		if _, active := activeConversationIDs[entry.ConversationID]; active {
			continue
		}
		if _, pathErr := workspaceDirectoryExists(path); pathErr != nil {
			passErr = errors.Join(
				passErr,
				fmt.Errorf("inspect conversation workspace %s: %w", entry.ConversationID, pathErr),
			)
			continue
		}
		// A superseded root has no clock at all: it holds work a handoff took
		// away before anybody merged it, and nothing records a disposition
		// for it yet, so it is kept.
		eligible := entry.State == workspaceCompleted && entry.CompletedAt != nil &&
			!entry.CompletedAt.Add(m.retention.Completed).After(now)
		eligible = eligible || entry.State == workspaceOrphaned && entry.OrphanedAt != nil &&
			!entry.OrphanedAt.Add(m.retention.Orphaned).After(now)
		if !eligible {
			continue
		}
		if entry.Kind == metadataKindManaged {
			workDir := filepath.Join(path, workdirDirectoryName)
			workDirExists, workDirErr := workspaceDirectoryExists(workDir)
			if workDirErr != nil {
				passErr = errors.Join(
					passErr,
					fmt.Errorf(
						"inspect conversation workdir %s: %w",
						entry.ConversationID,
						workDirErr,
					),
				)
				continue
			}
			if err := m.cleanupManagedGit(ctx, workDir, workDirExists, entry); err != nil {
				passErr = errors.Join(
					passErr,
					fmt.Errorf(
						"clean expired conversation workspace %s Git state: %w",
						entry.ConversationID,
						err,
					),
				)
				continue
			}
		} else if entry.Kind == metadataKindManagedScratch && entry.Branch != "" {
			workDir := filepath.Join(path, workdirDirectoryName)
			workDirExists, workDirErr := workspaceDirectoryExists(workDir)
			if workDirErr != nil {
				passErr = errors.Join(
					passErr,
					fmt.Errorf(
						"inspect scoped conversation workdir %s: %w",
						entry.ConversationID,
						workDirErr,
					),
				)
				continue
			}
			if err := m.cleanupScopedGit(ctx, path, workDir, workDirExists, entry); err != nil {
				passErr = errors.Join(
					passErr,
					fmt.Errorf(
						"clean expired scoped workspace %s Git state: %w",
						entry.ConversationID,
						err,
					),
				)
				continue
			}
		}
		rootExists, rootErr := m.verifyReadableRoots()
		if rootErr != nil {
			passErr = errors.Join(
				passErr,
				fmt.Errorf(
					"revalidate conversation workspace %s roots: %w",
					entry.ConversationID,
					rootErr,
				),
			)
			continue
		}
		if !rootExists {
			passErr = errors.Join(
				passErr,
				fmt.Errorf(
					"revalidate conversation workspace %s roots: root disappeared",
					entry.ConversationID,
				),
			)
			continue
		}
		pathExists, pathErr := workspaceDirectoryExists(path)
		if pathErr != nil {
			passErr = errors.Join(
				passErr,
				fmt.Errorf("revalidate conversation workspace %s: %w", entry.ConversationID, pathErr),
			)
			continue
		}
		if pathExists {
			if err := os.RemoveAll(path); err != nil {
				passErr = errors.Join(
					passErr,
					fmt.Errorf(
						"delete expired conversation workspace %s: %w",
						entry.ConversationID,
						err,
					),
				)
				continue
			}
		}
		metadataPath, err := m.metadataPath(path)
		if err != nil {
			passErr = errors.Join(passErr, err)
			continue
		}
		if err := os.Remove(metadataPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			passErr = errors.Join(
				passErr,
				fmt.Errorf(
					"delete expired conversation workspace %s metadata: %w",
					entry.ConversationID,
					err,
				),
			)
			continue
		}
		removed = append(removed, path)
	}
	sort.Strings(removed)
	return removed, passErr
}

func (m *Manager) cleanupManagedGit(ctx context.Context, workDir string, workDirExists bool, entry metadata) error {
	if err := requireRealDirectory(m.cacheRoot, false); err != nil {
		return errors.New("repository cache root is not a real directory")
	}
	cachePath := filepath.Join(m.cacheRoot, entry.CacheName)
	if _, err := m.cacheName(cachePath); err != nil {
		return errors.New("workspace metadata references an invalid repository cache")
	}
	if workDirExists {
		if err := m.runGit(
			ctx,
			m.root,
			nil,
			"--git-dir",
			cachePath,
			"worktree",
			"remove",
			"--force",
			workDir,
		); err != nil {
			return fmt.Errorf("remove worktree: %w", err)
		}
	}
	if err := m.runGit(
		ctx,
		m.root,
		nil,
		"--git-dir",
		cachePath,
		"worktree",
		"prune",
		"--expire",
		"now",
	); err != nil {
		return fmt.Errorf("prune worktree administration: %w", err)
	}
	if err := m.runGit(
		ctx,
		m.root,
		nil,
		"--git-dir",
		cachePath,
		"update-ref",
		"-d",
		"refs/heads/"+entry.Branch,
	); err != nil {
		return fmt.Errorf("delete task branch: %w", err)
	}
	return nil
}

func (m *Manager) cleanupScopedGit(
	ctx context.Context,
	root, workDir string,
	workDirExists bool,
	entry metadata,
) error {
	mainPath, err := m.scopedMainPath(root)
	if err != nil {
		return err
	}
	if workDirExists {
		if err := m.runGit(ctx, mainPath, nil, "worktree", "remove", "--force", workDir); err != nil {
			return fmt.Errorf("remove scoped worktree: %w", err)
		}
	}
	if err := m.runGit(ctx, mainPath, nil, "worktree", "prune", "--expire", "now"); err != nil {
		return fmt.Errorf("prune scoped worktree administration: %w", err)
	}
	if err := m.runGit(ctx, mainPath, nil, "update-ref", "-d", "refs/heads/"+entry.Branch); err != nil {
		return fmt.Errorf("delete scoped task branch: %w", err)
	}
	return nil
}

func (m *Manager) scopedMainPath(root string) (string, error) {
	relative, err := filepath.Rel(m.root, filepath.Clean(root))
	if err != nil || filepath.IsAbs(relative) {
		return "", errors.New("scoped workspace is outside the managed root")
	}
	layout, ok := parseWorkspaceRecordPath(relative)
	if !ok || layout.scope == "" {
		return "", errors.New("scoped workspace layout is invalid")
	}
	mainPath := filepath.Join(m.root, layout.scope, mainDirectoryName)
	if layout.companyID != "" {
		mainPath = filepath.Join(m.root, layout.companyID, layout.scope, mainDirectoryName)
	}
	if err := requireRealDirectory(mainPath, false); err != nil {
		return "", errors.New("scoped main workspace is not a real directory")
	}
	return mainPath, nil
}

func (m *Manager) loadManaged(path string) (metadata, error) {
	clean := filepath.Clean(path)
	relative, err := filepath.Rel(m.root, clean)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) {
		return metadata{}, errors.New("workspace is outside the managed root")
	}
	if exists, err := workspaceDirectoryExists(clean); err != nil || !exists {
		return metadata{}, errors.New("workspace is not a managed real directory")
	}
	return m.readMetadata(clean)
}

// records discovers lifecycle-managed conversation roots across both old flat
// and new scoped layouts so GC can migrate without dropping active workspaces.
func (m *Manager) records() ([]string, error) {
	paths := make(map[string]struct{})
	if err := filepath.WalkDir(m.root, func(path string, entry os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if path == m.root {
			return nil
		}
		if entry.IsDir() && entry.Name() == metadataDirectoryName {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			relative, relErr := filepath.Rel(m.root, path)
			if relErr != nil || filepath.IsAbs(relative) {
				return nil
			}
			layout, recognized := parseWorkspaceRecordPath(relative)
			if recognized {
				if layout.companyID == "" && layout.scope == "" {
					// A top-level UUID may be either a legacy conversation or a
					// new Company namespace. A legacy workdir or top-level
					// sidecar is authoritative for this classification; `workdir`
					// itself is also a valid scope slug and must not be mistaken
					// for a Company scope.
					workdirExists, workdirErr := workspaceDirectoryExists(
						filepath.Join(path, workdirDirectoryName),
					)
					if workdirErr != nil {
						return workdirErr
					}
					_, sidecarErr := os.Lstat(
						filepath.Join(m.metaRoot, relative+metadataFileSuffix),
					)
					if sidecarErr != nil && !errors.Is(sidecarErr, os.ErrNotExist) {
						return sidecarErr
					}
					if !workdirExists && errors.Is(sidecarErr, os.ErrNotExist) {
						children, childrenErr := os.ReadDir(path)
						if childrenErr != nil {
							return childrenErr
						}
						for _, child := range children {
							if child.IsDir() && validWorkspaceScope(child.Name()) {
								return nil
							}
						}
					}
				}
				paths[path] = struct{}{}
				return filepath.SkipDir
			}
		}
		return nil
	}); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("scan worktree root: %w", err)
	}
	if err := filepath.WalkDir(m.metaRoot, func(path string, entry os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, relErr := filepath.Rel(m.metaRoot, path)
		if relErr != nil || filepath.IsAbs(relative) {
			return nil
		}
		name := strings.TrimSuffix(relative, metadataFileSuffix)
		if relative == name+metadataFileSuffix {
			_, recognized := parseWorkspaceRecordPath(name)
			if !recognized {
				return nil
			}
			paths[filepath.Join(m.root, name)] = struct{}{}
		}
		return nil
	}); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("scan worktree metadata root: %w", err)
	}
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

func workspaceDirectoryExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("workspace path is not a real directory")
	}
	return true, nil
}

func validBranch(branch string) bool {
	if branch == "" || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") ||
		strings.HasSuffix(branch, ".") || strings.HasSuffix(branch, ".lock") ||
		strings.Contains(branch, "..") || strings.Contains(branch, "@{") ||
		strings.ContainsAny(branch, " ~^:?*[\\\r\n\x00") {
		return false
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || strings.HasPrefix(component, ".") {
			return false
		}
	}
	return true
}

// metadataPath mirrors the workspace-relative path under .metadata so nested
// scoped worktrees keep independent signed lifecycle records.
func (m *Manager) metadataPath(path string) (string, error) {
	clean := filepath.Clean(path)
	relative, err := filepath.Rel(m.root, clean)
	_, recognized := parseWorkspaceRecordPath(relative)
	if err != nil || relative == "." || filepath.IsAbs(relative) || !recognized {
		return "", errors.New("workspace is outside the managed root")
	}
	return filepath.Join(m.metaRoot, relative+metadataFileSuffix), nil
}

func (m *Manager) cacheName(path string) (string, error) {
	if err := requireRealDirectory(m.cacheRoot, false); err != nil {
		return "", errors.New("repository cache root is not a real directory")
	}
	clean := filepath.Clean(path)
	relative, err := filepath.Rel(m.cacheRoot, clean)
	if err != nil || filepath.IsAbs(relative) || relative == "." ||
		strings.Contains(relative, string(filepath.Separator)) ||
		strings.HasPrefix(relative, ".") ||
		!strings.HasSuffix(relative, ".git") {
		return "", errors.New("repository cache is outside the managed root")
	}
	info, err := os.Lstat(clean)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("repository cache is not a managed real directory")
	}
	return relative, nil
}

func (m *Manager) readMetadata(path string) (metadata, error) {
	rootExists, err := m.verifyReadableRoots()
	if err != nil {
		return metadata{}, err
	}
	if !rootExists {
		return metadata{}, os.ErrNotExist
	}
	metadataPath, err := m.metadataPath(path)
	if err != nil {
		return metadata{}, err
	}
	var result metadata
	if err := readJSONFileBounded(metadataPath, &result, "workspace metadata"); err != nil {
		return metadata{}, err
	}
	if !m.verifyMetadata(result) {
		return metadata{}, fmt.Errorf("%w: workspace metadata authentication failed", errMetadataUnusable)
	}
	relative, relErr := filepath.Rel(m.root, filepath.Clean(path))
	layout, recognized := parseWorkspaceRecordPath(relative)
	validVersionedNamespace := recognized
	switch result.Version {
	case legacyMetadataVersion:
		validVersionedNamespace = validVersionedNamespace && layout.companyID == "" &&
			result.CompanyID == "" && result.Scope == ""
	case generationlessMetadataVersion, metadataVersion:
		validVersionedNamespace = validVersionedNamespace && layout.companyID != "" &&
			result.CompanyID == layout.companyID && result.Scope == layout.scope
	default:
		validVersionedNamespace = false
	}
	// A record written before generations existed is the first one, and the
	// generation in the name must be the generation in the signed body: a
	// renamed root is not the workspace it claims to be (ADR 0014). The first
	// daemon build with generations numbered the first one 1 and gave it the
	// plain name; that record is the same first generation.
	if result.Version == generationlessMetadataVersion ||
		result.Version == metadataVersion && layout.workspaceGeneration == firstWorkspaceGeneration &&
			result.WorkspaceGeneration == 1 {
		result.WorkspaceGeneration = firstWorkspaceGeneration
	}
	if result.Version != legacyMetadataVersion && result.WorkspaceGeneration != layout.workspaceGeneration {
		return metadata{}, fmt.Errorf("%w: workspace generation does not match its root", errMetadataUnusable)
	}
	if relErr != nil || !validVersionedNamespace || !validWorkspaceID(result.ConversationID) ||
		result.ConversationID != layout.conversationID || result.CreatedAt.IsZero() {
		return metadata{}, fmt.Errorf("%w: workspace metadata identity is invalid", errMetadataUnusable)
	}
	switch result.Kind {
	case metadataKindManaged:
		if result.CacheName == "" || filepath.Base(result.CacheName) != result.CacheName ||
			strings.HasPrefix(result.CacheName, ".") || !strings.HasSuffix(result.CacheName, ".git") ||
			!validBranch(result.Branch) || !validLastRunID(result.LastRunID) {
			return metadata{}, fmt.Errorf("%w: managed workspace metadata is invalid", errMetadataUnusable)
		}
	case metadataKindManagedScratch:
		if result.CacheName != "" || !validLastRunID(result.LastRunID) ||
			result.Branch != "" && !validBranch(result.Branch) {
			return metadata{}, fmt.Errorf(
				"%w: managed scratch workspace metadata is invalid",
				errMetadataUnusable,
			)
		}
	case metadataKindCrashOrphan:
		if result.CacheName != "" || result.Branch != "" || result.LastRunID != "" ||
			result.State != workspaceOrphaned || result.CompletedAt != nil || result.OrphanedAt == nil {
			return metadata{}, fmt.Errorf(
				"%w: crash-orphan workspace metadata is invalid",
				errMetadataUnusable,
			)
		}
	default:
		return metadata{}, fmt.Errorf("%w: workspace metadata kind is invalid", errMetadataUnusable)
	}
	if (result.State == workspaceSuperseded) != (result.SupersededAt != nil) {
		return metadata{}, fmt.Errorf("%w: superseded workspace metadata is invalid", errMetadataUnusable)
	}
	switch result.State {
	case workspacePreparing:
		if result.Kind != metadataKindManaged && result.Kind != metadataKindManagedScratch ||
			result.CompletedAt != nil || result.OrphanedAt != nil {
			return metadata{}, fmt.Errorf(
				"%w: preparing workspace metadata is invalid",
				errMetadataUnusable,
			)
		}
	case workspaceActive:
		if result.CompletedAt != nil || result.OrphanedAt != nil {
			return metadata{}, fmt.Errorf(
				"%w: active workspace metadata has a terminal timestamp",
				errMetadataUnusable,
			)
		}
	case workspaceCompleted:
		if result.CompletedAt == nil || result.OrphanedAt != nil {
			return metadata{}, fmt.Errorf(
				"%w: completed workspace metadata is invalid",
				errMetadataUnusable,
			)
		}
	case workspaceOrphaned:
		if result.OrphanedAt == nil || result.CompletedAt != nil {
			return metadata{}, fmt.Errorf("%w: orphaned workspace metadata is invalid", errMetadataUnusable)
		}
	case workspaceSuperseded:
		// Only a Task root has a generation to be superseded, and its record
		// is the current version, so the supersededAt it carries is signed.
		if result.Version != metadataVersion || result.Kind != metadataKindManagedScratch ||
			result.CompletedAt != nil || result.OrphanedAt != nil {
			return metadata{}, fmt.Errorf(
				"%w: superseded workspace metadata is invalid",
				errMetadataUnusable,
			)
		}
	default:
		return metadata{}, fmt.Errorf("%w: workspace metadata state is invalid", errMetadataUnusable)
	}
	return result, nil
}

func (m *Manager) writeMetadata(path string, entry metadata) error {
	if err := m.ensureWritableRoots(); err != nil {
		return err
	}
	metadataPath, err := m.metadataPath(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(metadataPath), 0o700); err != nil {
		return fmt.Errorf("create workspace metadata root: %w", err)
	}
	entry.Signature = m.signMetadata(entry)
	if entry.Signature == "" {
		return errors.New("authenticate workspace metadata")
	}
	return writeJSONFileAtomic(metadataPath, entry, "workspace metadata")
}

func validWorkspaceScope(value string) bool {
	if strings.HasPrefix(value, "projects/") {
		return validWorkspaceID(strings.TrimPrefix(value, "projects/"))
	}
	return workspaceScopePattern.MatchString(value)
}

type workspaceRecordPath struct {
	companyID      string
	scope          string
	conversationID string
	// workspaceGeneration is what the directory name says this root holds;
	// readMetadata requires the signed body to agree (ADR 0014).
	workspaceGeneration int64
}

// parseWorkspaceRecordPath recognizes only lifecycle roots owned by the
// manager. The exact shapes distinguish a top-level Company UUID from a
// legacy top-level conversation UUID.
func parseWorkspaceRecordPath(value string) (workspaceRecordPath, bool) {
	if value == "" || filepath.IsAbs(value) || value == "." {
		return workspaceRecordPath{}, false
	}
	components := strings.Split(value, string(filepath.Separator))
	// A worktree root's name is the Conversation id, plus a generation suffix
	// past the first one (ADR 0014).
	var scopedID string
	var scopedGeneration int64 = firstWorkspaceGeneration
	if last := len(components) - 1; last >= 0 {
		scopedID, scopedGeneration = splitScopedRootName(components[last])
	}
	switch {
	case len(components) == 5 && validWorkspaceID(components[0]) && components[1] == "projects" && validWorkspaceID(components[2]) && components[3] == "worktrees" && validWorkspaceID(scopedID):
		return workspaceRecordPath{
			companyID: components[0], scope: "projects/" + components[2],
			conversationID: scopedID, workspaceGeneration: scopedGeneration,
		}, true
	case len(components) == 1 && validWorkspaceID(components[0]):
		return workspaceRecordPath{
			conversationID: components[0], workspaceGeneration: firstWorkspaceGeneration,
		}, true
	case len(components) == 3 && validWorkspaceScope(components[0]) &&
		components[1] == "worktrees" && validWorkspaceID(scopedID):
		return workspaceRecordPath{
			scope: components[0], conversationID: scopedID,
			workspaceGeneration: scopedGeneration,
		}, true
	case len(components) == 4 && validWorkspaceID(components[0]) &&
		validWorkspaceScope(components[1]) && components[2] == "worktrees" &&
		validWorkspaceID(scopedID):
		return workspaceRecordPath{
			companyID: components[0], scope: components[1], conversationID: scopedID,
			workspaceGeneration: scopedGeneration,
		}, true
	default:
		return workspaceRecordPath{}, false
	}
}

// validLastRunID keeps signed decimal identities readable during the offline
// UUID conversion. Runtime APIs accept UUIDs; retained files remain discoverable
// until their database mapping can be applied.
func validLastRunID(value string) bool {
	if id, err := strconv.ParseInt(value, 10, 64); err == nil {
		return id > 0 && strconv.FormatInt(id, 10) == value
	}
	return validWorkspaceID(value)
}

func validWorkspaceID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.Variant() == uuid.RFC4122 && id.String() == value
}

func (m *Manager) signMetadata(entry metadata) string {
	body, err := metadataAuthenticationBody(entry)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, m.metadataKey)
	_, _ = mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (m *Manager) verifyMetadata(entry metadata) bool {
	provided, err := base64.RawURLEncoding.Strict().DecodeString(entry.Signature)
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	expected, err := base64.RawURLEncoding.Strict().DecodeString(m.signMetadata(entry))
	return err == nil && hmac.Equal(provided, expected)
}

func metadataAuthenticationBody(entry metadata) ([]byte, error) {
	if entry.Version == legacyMetadataVersion {
		return legacyMetadataAuthenticationBody(entry)
	}
	if entry.Version == generationlessMetadataVersion {
		return generationlessMetadataAuthenticationBody(entry)
	}
	return json.Marshal(struct {
		Version             int            `json:"version"`
		Kind                string         `json:"kind"`
		CompanyID           string         `json:"companyId"`
		Scope               string         `json:"scope"`
		ConversationID      string         `json:"conversationId"`
		LastRunID           string         `json:"lastRunId"`
		WorkspaceGeneration int64          `json:"workspaceGeneration"`
		CacheName           string         `json:"cacheName"`
		Branch              string         `json:"branch"`
		State               workspaceState `json:"state"`
		CreatedAt           time.Time      `json:"createdAt"`
		CompletedAt         *time.Time     `json:"completedAt,omitempty"`
		OrphanedAt          *time.Time     `json:"orphanedAt,omitempty"`
		// omitempty keeps every record signed before superseding existed
		// byte-for-byte what it was.
		SupersededAt *time.Time `json:"supersededAt,omitempty"`
	}{
		Version: entry.Version, Kind: entry.Kind, CompanyID: entry.CompanyID, Scope: entry.Scope,
		ConversationID: entry.ConversationID, LastRunID: entry.LastRunID,
		WorkspaceGeneration: entry.WorkspaceGeneration,
		CacheName:           entry.CacheName, Branch: entry.Branch, State: entry.State,
		CreatedAt: entry.CreatedAt, CompletedAt: entry.CompletedAt, OrphanedAt: entry.OrphanedAt,
		SupersededAt: entry.SupersededAt,
	})
}

// legacyMetadataAuthenticationBody must remain byte-for-byte compatible with
// metadata v2 so retained worktrees can still be recovered and garbage-collected.
// generationlessMetadataAuthenticationBody signs a scoped record from before
// workspace generations existed, byte for byte as that build signed it, so a
// worktree prepared by the previous release still verifies.
func generationlessMetadataAuthenticationBody(entry metadata) ([]byte, error) {
	return json.Marshal(struct {
		Version        int            `json:"version"`
		Kind           string         `json:"kind"`
		CompanyID      string         `json:"companyId"`
		Scope          string         `json:"scope"`
		ConversationID string         `json:"conversationId"`
		LastRunID      string         `json:"lastRunId"`
		CacheName      string         `json:"cacheName"`
		Branch         string         `json:"branch"`
		State          workspaceState `json:"state"`
		CreatedAt      time.Time      `json:"createdAt"`
		CompletedAt    *time.Time     `json:"completedAt,omitempty"`
		OrphanedAt     *time.Time     `json:"orphanedAt,omitempty"`
	}{
		Version: entry.Version, Kind: entry.Kind, CompanyID: entry.CompanyID, Scope: entry.Scope,
		ConversationID: entry.ConversationID, LastRunID: entry.LastRunID,
		CacheName: entry.CacheName, Branch: entry.Branch, State: entry.State,
		CreatedAt: entry.CreatedAt, CompletedAt: entry.CompletedAt, OrphanedAt: entry.OrphanedAt,
	})
}

func legacyMetadataAuthenticationBody(entry metadata) ([]byte, error) {
	return json.Marshal(struct {
		Version        int            `json:"version"`
		Kind           string         `json:"kind"`
		ConversationID string         `json:"conversationId"`
		LastRunID      string         `json:"lastRunId"`
		CacheName      string         `json:"cacheName"`
		Branch         string         `json:"branch"`
		State          workspaceState `json:"state"`
		CreatedAt      time.Time      `json:"createdAt"`
		CompletedAt    *time.Time     `json:"completedAt,omitempty"`
		OrphanedAt     *time.Time     `json:"orphanedAt,omitempty"`
	}{
		Version:        entry.Version,
		Kind:           entry.Kind,
		ConversationID: entry.ConversationID,
		LastRunID:      entry.LastRunID,
		CacheName:      entry.CacheName,
		Branch:         entry.Branch,
		State:          entry.State,
		CreatedAt:      entry.CreatedAt,
		CompletedAt:    entry.CompletedAt,
		OrphanedAt:     entry.OrphanedAt,
	})
}

// readJSONFileBounded decodes one small, strictly-shaped JSON record: at most
// 16 KiB, no unknown fields, nothing after the document. A record that fails
// to decode is unusable, not merely absent, so a caller can tell a hand-edited
// file from a missing one. label names the record in errors.
func readJSONFileBounded(path string, into any, label string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", label, err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("%w: decode %s: %w", errMetadataUnusable, label, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: %s contains trailing data", errMetadataUnusable, label)
	}
	return nil
}

// writeJSONFileAtomic publishes one lifecycle record as a 0600 file, written
// to a sibling temporary file and renamed into place so a reader never sees
// a partial document. label names the record in errors.
func writeJSONFileAtomic(path string, value any, label string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s root: %w", label, err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create %s: %w", label, err)
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure %s: %w", label, err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		_ = file.Close()
		return fmt.Errorf("encode %s: %w", label, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync %s: %w", label, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", label, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", label, err)
	}
	return nil
}
