package workspace

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"unicode/utf8"

	"github.com/google/uuid"
)

// TaskReviewStatus is the read-only merge readiness of one retained Task Run.
type TaskReviewStatus string

const (
	TaskReviewReady     TaskReviewStatus = "ready"
	TaskReviewConflict  TaskReviewStatus = "conflict"
	TaskReviewMainDirty TaskReviewStatus = "main_dirty"
	TaskReviewMerged    TaskReviewStatus = "merged"
)

// TaskReview is a bounded read-only view of the exact diff the shared Task
// merge path will apply. ReviewTask never writes to the managed main branch.
type TaskReview struct {
	RunID   uuid.UUID
	Status  TaskReviewStatus
	BaseRef string
	HeadRef string
	Diff    string
	Message string
}

var (
	// ErrTaskReviewUnavailable means the exact authenticated retained workspace cannot be read.
	ErrTaskReviewUnavailable = errors.New("task review workspace is unavailable")
	// ErrTaskReviewTooLarge means the bounded Task diff cannot be returned to Desktop.
	ErrTaskReviewTooLarge = errors.New("task review diff is too large")
	// ErrTaskReviewUnsupported means the Task diff is not valid displayable UTF-8.
	ErrTaskReviewUnsupported = errors.New("task review diff is not displayable")
)

// ResolveRunWorkDir returns the authenticated work directory for the latest
// retained occurrence of runID: a managed workspace (a superseded Task root
// included), or the bound local directory a Run executed in place, from its
// signed receipt. A reused conversation workspace deliberately stops
// resolving an older Run once its signed LastRunID advances.
func (m *Manager) ResolveRunWorkDir(ctx context.Context, runID uuid.UUID) (string, error) {
	if m == nil || runID == uuid.Nil {
		return "", fs.ErrNotExist
	}
	var workDir string
	err := m.withMainMutation(ctx, func(context.Context) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		var resolveErr error
		workDir, _, resolveErr = m.resolveRunWorkspaceLocked(runID, false)
		if !errors.Is(resolveErr, fs.ErrNotExist) {
			return resolveErr
		}
		receipt, err := m.resolveInPlaceRun(runID)
		if err != nil {
			return resolveErr
		}
		workDir = receipt.LocalPath
		return nil
	})
	return workDir, err
}

// ResolveMain returns an existing real managed main directory without
// creating Company or scope state as a side effect of a desktop read.
func (m *Manager) ResolveMain(ctx context.Context, companyID, scope string) (string, error) {
	if m == nil || !validWorkspaceID(companyID) || !validWorkspaceScope(scope) {
		return "", fs.ErrNotExist
	}
	var mainDir string
	err := m.withMainMutation(ctx, func(context.Context) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		mainDir = filepath.Join(m.root, companyID, scope, mainDirectoryName)
		if err := requireRealDirectory(mainDir, false); err != nil {
			return fs.ErrNotExist
		}
		return nil
	})
	return mainDir, err
}

// ReviewTask reports the same diff and conflict decision used by MergeTask,
// while the shared mutation guard prevents a concurrent main write from
// changing the decision mid-review.
func (m *Manager) ReviewTask(ctx context.Context, runID uuid.UUID) (TaskReview, error) {
	if m == nil || runID == uuid.Nil {
		return TaskReview{}, ErrTaskReviewUnavailable
	}
	var review TaskReview
	err := m.withMainMutation(ctx, func(ctx context.Context) error {
		m.mu.Lock()
		workDir, entry, err := m.resolveRunWorkspaceLocked(runID, true)
		m.mu.Unlock()
		if errors.Is(err, fs.ErrNotExist) {
			// Not a managed worktree: a Run that executed in place reviews
			// from its signed receipt instead.
			review, err = m.reviewInPlaceRun(ctx, runID)
			return err
		}
		if err != nil {
			return ErrTaskReviewUnavailable
		}
		mainDir := filepath.Join(m.root, entry.CompanyID, entry.Scope, mainDirectoryName)
		if err := requireRealDirectory(mainDir, false); err != nil {
			return ErrTaskReviewUnavailable
		}
		review = TaskReview{
			RunID: runID, BaseRef: "main", HeadRef: entry.Branch,
		}
		if _, found, err := existingTaskMergeCommit(ctx, mainDir, runID); err != nil {
			return errors.Join(ErrTaskReviewUnavailable, err)
		} else if found {
			review.Status = TaskReviewMerged
			review.Message = "Task changes were merged into main."
			return nil
		}
		if dirty, err := gitDirty(ctx, mainDir); err != nil {
			return errors.Join(ErrTaskReviewUnavailable, err)
		} else if dirty {
			review.Status = TaskReviewMainDirty
			review.Message = "Main has uncommitted changes. Commit or clear them before review."
			return nil
		}
		diff, err := taskMergeDiff(ctx, workDir)
		if errors.Is(err, errTaskMergeDiffTooLarge) {
			return ErrTaskReviewTooLarge
		}
		if err != nil {
			return errors.Join(ErrTaskReviewUnavailable, err)
		}
		if !utf8.Valid(diff) {
			return ErrTaskReviewUnsupported
		}
		review.Diff = string(diff)
		if len(bytes.TrimSpace(diff)) == 0 {
			review.Status = TaskReviewReady
			review.Message = "No file changes were found."
			return nil
		}
		applies, err := taskMergePatchApplies(ctx, mainDir, diff)
		if err != nil {
			return errors.Join(ErrTaskReviewUnavailable, err)
		}
		if !applies {
			review.Status = TaskReviewConflict
			review.Diff = ""
			review.Message = "Task changes conflict with main."
			return nil
		}
		review.Status = TaskReviewReady
		review.Message = "Task changes are ready to merge."
		return nil
	})
	return review, err
}

func (m *Manager) resolveRunWorkspaceLocked(
	runID uuid.UUID,
	requireScoped bool,
) (string, metadata, error) {
	rootExists, err := m.verifyReadableRoots()
	if err != nil || !rootExists {
		return "", metadata{}, fs.ErrNotExist
	}
	records, err := m.records()
	if err != nil {
		return "", metadata{}, err
	}
	wantRunID := runID.String()
	for _, root := range records {
		entry, err := m.readMetadata(root)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errMetadataUnusable) {
			continue
		}
		if err != nil {
			return "", metadata{}, err
		}
		// A superseded root keeps its files until a disposition is recorded,
		// so they stay readable; review and merge are never offered for a
		// generation a handoff ended.
		retained := m.runWorkspaceRetained(entry) || !requireScoped && entry.State == workspaceSuperseded
		if entry.LastRunID != wantRunID || !retained {
			continue
		}
		if requireScoped && (entry.Version != metadataVersion || entry.CompanyID == "" ||
			entry.Scope == "" || entry.Branch != taskBranch(runID)) {
			continue
		}
		workDir := filepath.Join(root, workdirDirectoryName)
		if err := requireRealDirectory(workDir, false); err != nil {
			return "", metadata{}, fs.ErrNotExist
		}
		return workDir, entry, nil
	}
	return "", metadata{}, fs.ErrNotExist
}

func (m *Manager) runWorkspaceRetained(entry metadata) bool {
	now := m.now().UTC()
	switch entry.State {
	case workspaceActive:
		return true
	case workspaceCompleted:
		return entry.CompletedAt != nil && now.Before(entry.CompletedAt.UTC().Add(m.retention.Completed))
	case workspaceOrphaned:
		return entry.OrphanedAt != nil && now.Before(entry.OrphanedAt.UTC().Add(m.retention.Orphaned))
	default:
		return false
	}
}
