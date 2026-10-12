package workspace

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const maximumTaskMergeDiffBytes = 4 << 20

const taskMergeCleanupTimeout = 15 * time.Second

// TaskMergeInput names either the exact retained scoped worktree for a Task
// Run or the local directory in which that Run executed in place.
type TaskMergeInput struct {
	CompanyID      string
	Scope          string
	ConversationID string
	RunID          uuid.UUID
	// WorkspaceGeneration is the one the Run froze; it names the root the
	// Run worked in (ADR 0014).
	WorkspaceGeneration int64
	LocalPath           string
	// LeaseFence must synchronously prove authority over the exact claim
	// attempt. MergeTask may call it several times while holding both local and
	// cross-process mutation guards.
	LeaseFence func(context.Context) error
}

// TaskMergeResult is the local Git outcome. An empty ResultCode means the
// merge succeeded and MergedCommit names the accepted commit.
type TaskMergeResult struct {
	ResultCode   string
	MergedCommit string
}

type taskMergeWorkspace struct {
	mainDir string
	workDir string
	branch  string
}

// MergeTask is the single serialized mutation seam for managed main
// workspaces. The same process and OS guards cover local-directory verification
// so concurrent daemons cannot interleave acceptance decisions.
func (m *Manager) MergeTask(ctx context.Context, input TaskMergeInput) (TaskMergeResult, error) {
	if m == nil || !validWorkspaceID(input.CompanyID) || !validWorkspaceScope(input.Scope) ||
		!validWorkspaceID(input.ConversationID) || input.RunID == uuid.Nil {
		return blockedTaskMerge("invalid_run", errors.New("invalid task merge identity"))
	}
	if input.LeaseFence == nil {
		return TaskMergeResult{}, errors.New("task merge lease fence is required")
	}
	if err := ctx.Err(); err != nil {
		return TaskMergeResult{}, context.Cause(ctx)
	}

	var result TaskMergeResult
	err := m.withMainMutation(ctx, func(ctx context.Context) error {
		if err := fenceTaskMergeLease(ctx, input.LeaseFence); err != nil {
			return err
		}
		var mergeErr error
		if input.LocalPath != "" {
			result, mergeErr = verifyLocalTaskMerge(ctx, input.LocalPath, input.LeaseFence)
		} else {
			result, mergeErr = m.mergeScopedTask(ctx, input)
		}
		return mergeErr
	})
	return result, err
}

func (m *Manager) mergeScopedTask(ctx context.Context, input TaskMergeInput) (TaskMergeResult, error) {
	workspace, release, err := m.retainTaskMergeWorkspace(input)
	if err != nil {
		return blockedTaskMerge("workspace_unavailable", err)
	}
	defer release()

	if commit, found, err := existingTaskMergeCommit(ctx, workspace.mainDir, input.RunID); err != nil {
		return blockedTaskMerge("workspace_unavailable", err)
	} else if found {
		if err := fenceTaskMergeLease(ctx, input.LeaseFence); err != nil {
			return TaskMergeResult{}, err
		}
		return TaskMergeResult{MergedCommit: commit}, nil
	}
	if branch, err := gitOutput(
		ctx,
		workspace.mainDir,
		"branch",
		"--show-current",
	); err != nil ||
		strings.TrimSpace(branch) != "main" {
		return blockedTaskMerge("workspace_unavailable", errors.New("scoped main branch is unavailable"))
	}
	if branch, err := gitOutput(
		ctx,
		workspace.workDir,
		"branch",
		"--show-current",
	); err != nil ||
		strings.TrimSpace(branch) != workspace.branch {
		return blockedTaskMerge("workspace_unavailable", errors.New("task worktree branch is unavailable"))
	}
	if dirty, err := gitDirty(ctx, workspace.mainDir); err != nil {
		return blockedTaskMerge("workspace_unavailable", err)
	} else if dirty {
		return blockedTaskMerge("main_dirty", errors.New("scoped main workspace is dirty"))
	}
	original, err := taskMergeHead(ctx, workspace.mainDir)
	if err != nil {
		return original, err
	}

	diff, err := taskMergeDiff(ctx, workspace.workDir)
	if err != nil {
		if errors.Is(err, errTaskMergeDiffTooLarge) {
			return blockedTaskMerge("diff_too_large", err)
		}
		return blockedTaskMerge("workspace_unavailable", err)
	}
	if len(bytes.TrimSpace(diff)) == 0 {
		if err := fenceTaskMergeLease(ctx, input.LeaseFence); err != nil {
			return TaskMergeResult{}, err
		}
		return fencedTaskMergeHead(ctx, workspace.mainDir, input.LeaseFence)
	}
	if err := fenceTaskMergeLease(ctx, input.LeaseFence); err != nil {
		return TaskMergeResult{}, err
	}
	applies, err := taskMergePatchApplies(ctx, workspace.mainDir, diff)
	if err != nil {
		return blockedTaskMerge("local_failure", err)
	}
	if !applies {
		return blockedTaskMerge("conflict", errors.New("task changes conflict with main"))
	}
	if err := fenceTaskMergeLease(ctx, input.LeaseFence); err != nil {
		return TaskMergeResult{}, err
	}
	if err := runGit(ctx, workspace.mainDir, nil, diff, "apply", "--3way", "--index", "-"); err != nil {
		return blockedTaskMerge(
			"merge_failed",
			errors.Join(err, rollbackTaskMerge(ctx, workspace.mainDir, original.MergedCommit)),
		)
	}
	if err := fenceTaskMergeLease(ctx, input.LeaseFence); err != nil {
		return TaskMergeResult{}, errors.Join(
			err,
			rollbackTaskMerge(ctx, workspace.mainDir, original.MergedCommit),
		)
	}
	if err := runGit(
		ctx,
		workspace.mainDir,
		managedGitCommitEnvironment(),
		nil,
		"commit",
		"-m",
		taskMergeCommitSubject(input.RunID),
	); err != nil {
		inspectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskMergeCleanupTimeout)
		commit, found, inspectErr := existingTaskMergeCommit(inspectCtx, workspace.mainDir, input.RunID)
		cancel()
		if found {
			if fenceErr := fenceTaskMergeLease(ctx, input.LeaseFence); fenceErr != nil {
				return TaskMergeResult{}, errors.Join(
					fenceErr,
					rollbackTaskMerge(ctx, workspace.mainDir, original.MergedCommit),
				)
			}
			return TaskMergeResult{MergedCommit: commit}, nil
		}
		if inspectErr != nil {
			err = errors.Join(err, inspectErr)
		}
		return blockedTaskMerge(
			"commit_failed",
			errors.Join(err, rollbackTaskMerge(ctx, workspace.mainDir, original.MergedCommit)),
		)
	}
	if err := fenceTaskMergeLease(ctx, input.LeaseFence); err != nil {
		return TaskMergeResult{}, errors.Join(
			err,
			rollbackTaskMerge(ctx, workspace.mainDir, original.MergedCommit),
		)
	}
	result, err := fencedTaskMergeHead(ctx, workspace.mainDir, input.LeaseFence)
	if err != nil {
		return TaskMergeResult{}, errors.Join(
			err,
			rollbackTaskMerge(ctx, workspace.mainDir, original.MergedCommit),
		)
	}
	return result, nil
}

func (m *Manager) retainTaskMergeWorkspace(
	input TaskMergeInput,
) (taskMergeWorkspace, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, busy := m.inUse[input.ConversationID]; busy {
		return taskMergeWorkspace{}, nil, errors.New("task workspace is in use")
	}
	root := filepath.Join(
		m.root, input.CompanyID, input.Scope, "worktrees",
		scopedRootName(input.ConversationID, normalizeWorkspaceGeneration(input.WorkspaceGeneration)),
	)
	entry, err := m.readMetadata(root)
	if err != nil {
		return taskMergeWorkspace{}, nil, err
	}
	wantRunID := input.RunID.String()
	isExactIdentity := entry.Version == metadataVersion && entry.Kind == metadataKindManagedScratch &&
		entry.CompanyID == input.CompanyID && entry.Scope == input.Scope &&
		entry.ConversationID == input.ConversationID && entry.LastRunID == wantRunID &&
		entry.Branch == taskBranch(input.RunID)
	if !isExactIdentity || entry.State != workspaceCompleted || entry.CompletedAt == nil ||
		!m.now().UTC().Before(entry.CompletedAt.UTC().Add(m.retention.Completed)) {
		return taskMergeWorkspace{}, nil, errors.New("retained task workspace identity is unavailable")
	}
	workDir := filepath.Join(root, workdirDirectoryName)
	mainDir := filepath.Join(m.root, input.CompanyID, input.Scope, mainDirectoryName)
	for _, path := range []string{root, workDir, mainDir} {
		if err := requireRealDirectory(path, false); err != nil {
			return taskMergeWorkspace{}, nil, errors.New("retained task workspace is unavailable")
		}
	}
	m.inUse[input.ConversationID] = struct{}{}
	release := func() {
		m.mu.Lock()
		delete(m.inUse, input.ConversationID)
		m.mu.Unlock()
	}
	return taskMergeWorkspace{mainDir: mainDir, workDir: workDir, branch: entry.Branch}, release, nil
}

func verifyLocalTaskMerge(
	ctx context.Context,
	path string,
	leaseFence func(context.Context) error,
) (TaskMergeResult, error) {
	canonical, err := ValidateLocalDirectory(path)
	if err != nil {
		return blockedTaskMerge("workspace_unavailable", err)
	}
	inside, err := gitOutput(ctx, canonical, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(inside) != "true" {
		return blockedTaskMerge("workspace_unavailable", errors.New("local directory is not a git worktree"))
	}
	if dirty, err := gitDirty(ctx, canonical); err != nil {
		return blockedTaskMerge("workspace_unavailable", err)
	} else if dirty {
		return blockedTaskMerge("main_dirty", errors.New("local directory is dirty"))
	}
	if err := fenceTaskMergeLease(ctx, leaseFence); err != nil {
		return TaskMergeResult{}, err
	}
	return fencedTaskMergeHead(ctx, canonical, leaseFence)
}

func fenceTaskMergeLease(ctx context.Context, leaseFence func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	if leaseFence == nil {
		return nil
	}
	if err := leaseFence(ctx); err != nil {
		return fmt.Errorf("fence task merge lease: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	return nil
}

func taskMergeHead(ctx context.Context, dir string) (TaskMergeResult, error) {
	head, err := gitOutput(ctx, dir, "rev-parse", "HEAD")
	commit := strings.TrimSpace(head)
	if err != nil || !validCommitID(commit) {
		return blockedTaskMerge("commit_unavailable", errors.New("task merge commit is unavailable"))
	}
	return TaskMergeResult{MergedCommit: commit}, nil
}

func fencedTaskMergeHead(
	ctx context.Context,
	dir string,
	leaseFence func(context.Context) error,
) (TaskMergeResult, error) {
	result, err := taskMergeHead(ctx, dir)
	if err != nil {
		return result, err
	}
	if err := fenceTaskMergeLease(ctx, leaseFence); err != nil {
		return TaskMergeResult{}, err
	}
	return result, nil
}

func existingTaskMergeCommit(ctx context.Context, mainDir string, runID uuid.UUID) (string, bool, error) {
	// Offline identity conversion retains an existing merge with a UUID-named
	// Git receipt. It is valid only while that commit is still part of main.
	receipt, err := gitOutput(ctx, mainDir, "for-each-ref", "--format=%(objectname)", taskMergeReceiptRef(runID))
	if err != nil {
		return "", false, err
	}
	if receipt = strings.TrimSpace(receipt); receipt != "" {
		if !validCommitID(receipt) {
			return "", false, errors.New("converted task merge receipt is invalid")
		}
		if err := runGit(ctx, mainDir, nil, nil, "merge-base", "--is-ancestor", receipt, "main"); err != nil {
			return "", false, fmt.Errorf("converted task merge is no longer in main: %w", err)
		}
		return receipt, true, nil
	}
	output, err := gitOutput(
		ctx,
		mainDir,
		"log",
		"main",
		"--max-count=1",
		"--format=%H",
		"--grep=^"+taskMergeCommitSubject(runID)+"$",
	)
	if err != nil {
		return "", false, err
	}
	commit := strings.TrimSpace(output)
	if commit == "" {
		return "", false, nil
	}
	if !validCommitID(commit) {
		return "", false, errors.New("existing task merge commit is invalid")
	}
	return commit, true, nil
}

func taskMergeReceiptRef(runID uuid.UUID) string {
	return "refs/boardly/run-merges/" + runID.String()
}

func taskMergeCommitSubject(runID uuid.UUID) string {
	return "Merge Zenith Team task run " + runID.String()
}

func taskMergeDiff(ctx context.Context, workDir string) (diff []byte, returnErr error) {
	base, err := gitOutput(ctx, workDir, "merge-base", "main", "HEAD")
	if err != nil || !validCommitID(strings.TrimSpace(base)) {
		return nil, errors.New("task worktree merge base is unavailable")
	}
	temporary, err := os.MkdirTemp("", "boardly-task-merge-index-*")
	if err != nil {
		return nil, fmt.Errorf("create task merge index: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, os.RemoveAll(temporary))
	}()
	environment := []string{"GIT_INDEX_FILE=" + filepath.Join(temporary, "index")}
	if err := runGit(ctx, workDir, environment, nil, "read-tree", "HEAD"); err != nil {
		return nil, err
	}
	if err := runGit(ctx, workDir, environment, nil, "add", "-A", "--", "."); err != nil {
		return nil, err
	}
	// Full blob IDs let apply --3way restore binary contents from the shared
	// object database without embedding large assets in the review response.
	return gitOutputBytes(
		ctx,
		workDir,
		environment,
		maximumTaskMergeDiffBytes,
		"diff",
		"--cached",
		"--full-index",
		"--find-renames",
		strings.TrimSpace(base),
		"--",
		".",
	)
}

func taskMergePatchApplies(ctx context.Context, mainDir string, diff []byte) (bool, error) {
	temporary, err := os.MkdirTemp("", "boardly-task-merge-check-*")
	if err != nil {
		return false, fmt.Errorf("create task merge check workspace: %w", err)
	}
	if err := runGit(ctx, mainDir, nil, nil, "worktree", "add", "--detach", temporary, "main"); err != nil {
		return false, errors.Join(err, cleanupTaskMergeCheck(ctx, mainDir, temporary))
	}
	applyErr := runGit(ctx, temporary, nil, diff, "apply", "--3way", "--index", "-")
	cleanupErr := cleanupTaskMergeCheck(ctx, mainDir, temporary)
	if cleanupErr != nil {
		return false, cleanupErr
	}
	if err := ctx.Err(); err != nil {
		return false, context.Cause(ctx)
	}
	return applyErr == nil, nil
}

func cleanupTaskMergeCheck(ctx context.Context, mainDir, temporary string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskMergeCleanupTimeout)
	removeWorktreeErr := runGit(cleanupCtx, mainDir, nil, nil, "worktree", "remove", "--force", temporary)
	pruneErr := runGit(cleanupCtx, mainDir, nil, nil, "worktree", "prune", "--expire", "now")
	cancel()
	removeErr := os.RemoveAll(temporary)
	return errors.Join(removeWorktreeErr, pruneErr, removeErr)
}

func rollbackTaskMerge(ctx context.Context, mainDir, originalHead string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskMergeCleanupTimeout)
	defer cancel()
	resetErr := runGit(ctx, mainDir, nil, nil, "reset", "--hard", originalHead)
	cleanErr := runGit(ctx, mainDir, nil, nil, "clean", "-fd")
	return errors.Join(resetErr, cleanErr)
}

func gitDirty(ctx context.Context, dir string) (bool, error) {
	status, err := gitOutputBytes(ctx, dir, nil, maximumTaskMergeDiffBytes, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(status)) > 0, nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	output, err := gitOutputBytes(ctx, dir, nil, 64<<10, args...)
	return string(output), err
}

func gitOutputBytes(
	ctx context.Context,
	dir string,
	environment []string,
	limit int64,
	args ...string,
) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	command.Env = gitEnvironment(environment)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("prepare git output: %w", err)
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start git operation: %w", err)
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, limit+1))
	_, drainErr := io.Copy(io.Discard, stdout)
	waitErr := command.Wait()
	if readErr != nil || drainErr != nil || waitErr != nil {
		return nil, errors.Join(readErr, drainErr, waitErr)
	}
	if int64(len(output)) > limit {
		return nil, errTaskMergeDiffTooLarge
	}
	return output, nil
}

func runGit(
	ctx context.Context,
	dir string,
	environment []string,
	input []byte,
	args ...string,
) error {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	command.Env = gitEnvironment(environment)
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return fmt.Errorf("git operation failed: %w", err)
	}
	return nil
}

func gitEnvironment(overrides []string) []string {
	environment := make([]string, 0, len(os.Environ())+16+len(overrides))
	for _, value := range os.Environ() {
		key, _, found := strings.Cut(value, "=")
		if found && strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			continue
		}
		environment = append(environment, value)
	}
	return append(environment, managedGitEnvironment(overrides)...)
}

func managedGitRunnerEnvironment(overrides []string) []string {
	environment := make([]string, 0, len(os.Environ())+16+len(overrides))
	for _, value := range os.Environ() {
		key, _, found := strings.Cut(value, "=")
		if found && strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			environment = append(environment, key+"=")
		}
	}
	return append(environment, managedGitEnvironment(overrides)...)
}

func managedGitEnvironment(overrides []string) []string {
	environment := []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=4",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=" + os.DevNull,
		"GIT_CONFIG_KEY_1=commit.gpgSign",
		"GIT_CONFIG_VALUE_1=false",
		"GIT_CONFIG_KEY_2=tag.gpgSign",
		"GIT_CONFIG_VALUE_2=false",
		"GIT_CONFIG_KEY_3=core.fsmonitor",
		"GIT_CONFIG_VALUE_3=false",
	}
	return append(environment, overrides...)
}

func managedGitCommitEnvironment() []string {
	return []string{
		"GIT_AUTHOR_NAME=Zenith Team Daemon",
		"GIT_AUTHOR_EMAIL=daemon@boardly.local",
		"GIT_COMMITTER_NAME=Zenith Team Daemon",
		"GIT_COMMITTER_EMAIL=daemon@boardly.local",
	}
}

func validCommitID(value string) bool {
	if value != strings.ToLower(value) || len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func blockedTaskMerge(code string, err error) (TaskMergeResult, error) {
	return TaskMergeResult{ResultCode: code}, err
}

var errTaskMergeDiffTooLarge = errors.New("task merge diff is too large")
