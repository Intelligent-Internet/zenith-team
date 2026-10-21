package workspace

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	// LocalScratchDirectoryName holds the daemon-owned per-Run environment
	// roots of Runs that execute in place, under the workspaces root.
	LocalScratchDirectoryName = ".local-scratch"
	inPlaceReceiptFileName    = "in-place.json"
	inPlaceReceiptVersion     = 1
)

// InPlaceRunIdentity is what a Run receipt records about the work it belongs
// to, so review can reject a stale identity.
type InPlaceRunIdentity struct {
	CompanyID      string
	ConversationID string
	TaskID         string
}

// inPlaceRun is the signed receipt of one Run that executed directly in a
// bound local directory. It records the commit HEAD pointed at before the
// provider ran and the one it pointed at when the Run ended, which is the
// exact bounded deliverable diff review shows and the identity acceptance
// verifies. It lives in the Run's own environment
// root — never in the person's repository — and is authenticated with the
// same key as every other lifecycle record, so a hand-edited file is
// unusable rather than trusted.
type inPlaceRun struct {
	Version        int            `json:"version"`
	RunID          string         `json:"runId"`
	CompanyID      string         `json:"companyId"`
	ConversationID string         `json:"conversationId"`
	TaskID         string         `json:"taskId,omitempty"`
	LocalPath      string         `json:"localPath"`
	BaseCommit     string         `json:"baseCommit,omitempty"`
	HeadCommit     string         `json:"headCommit,omitempty"`
	State          workspaceState `json:"state"`
	CreatedAt      time.Time      `json:"createdAt"`
	CompletedAt    *time.Time     `json:"completedAt,omitempty"`
	Signature      string         `json:"signature"`
}

// RecordInPlaceRunStart writes the receipt for a Run about to execute in
// localPath, remembering the commit it starts from. The directory must be a
// validated local directory; an empty repository (no commit yet) records no
// base commit and reviews against the empty tree.
func (m *Manager) RecordInPlaceRunStart(
	ctx context.Context,
	runID uuid.UUID,
	identity InPlaceRunIdentity,
	localPath string,
) error {
	if m == nil || runID == uuid.Nil {
		return errors.New("invalid in-place run identity")
	}
	canonical, err := ValidateLocalDirectory(localPath)
	if err != nil {
		return err
	}
	if !validWorkspaceID(identity.CompanyID) || !validWorkspaceID(identity.ConversationID) ||
		(identity.TaskID != "" && !validWorkspaceID(identity.TaskID)) {
		return errors.New("invalid in-place run identity")
	}
	base, err := optionalHead(ctx, canonical)
	if err != nil {
		return err
	}
	return m.writeInPlaceRun(runID, inPlaceRun{
		Version:        inPlaceReceiptVersion,
		RunID:          runID.String(),
		CompanyID:      identity.CompanyID,
		ConversationID: identity.ConversationID,
		TaskID:         identity.TaskID,
		LocalPath:      canonical,
		BaseCommit:     base,
		State:          workspaceActive,
		CreatedAt:      m.now().UTC(),
	})
}

// CompleteInPlaceRun closes the receipt with the commit HEAD points at now
// that the provider has exited.
func (m *Manager) CompleteInPlaceRun(ctx context.Context, runID uuid.UUID) error {
	if m == nil || runID == uuid.Nil {
		return errors.New("invalid in-place run identity")
	}
	receipt, err := m.readInPlaceRun(runID)
	if err != nil {
		return err
	}
	if receipt.State != workspaceActive {
		return errors.New("in-place run is not active")
	}
	head, err := optionalHead(ctx, receipt.LocalPath)
	if err != nil {
		return err
	}
	completedAt := m.now().UTC()
	receipt.HeadCommit = head
	receipt.State = workspaceCompleted
	receipt.CompletedAt = &completedAt
	return m.writeInPlaceRun(runID, receipt)
}

// resolveInPlaceRun returns the receipt of runID, or fs.ErrNotExist when
// there is none, it cannot be trusted, or the directory it names is no
// longer a real local directory. There is no clock on it: a managed
// worktree lapses because it is collected, but an in-place Run's commits
// stay in the person's repository, and a review may come days later.
// LocalPath is the canonical path RecordInPlaceRunStart validated,
// re-checked here so every caller gets a directory it can use as-is.
func (m *Manager) resolveInPlaceRun(runID uuid.UUID) (inPlaceRun, error) {
	receipt, err := m.readInPlaceRun(runID)
	if err != nil {
		return inPlaceRun{}, fs.ErrNotExist
	}
	if _, err := ValidateLocalDirectory(receipt.LocalPath); err != nil {
		return inPlaceRun{}, fs.ErrNotExist
	}
	return receipt, nil
}

// reviewInPlaceRun is ReviewTask for a Run that executed in place: the diff
// between the commit the Run started from and the commit it ended on, with
// the same dirty-tree rule acceptance applies (review shows what
// acceptance will decide). Read-only — no command here touches the index or
// the tree of the person's repository.
func (m *Manager) reviewInPlaceRun(ctx context.Context, runID uuid.UUID) (TaskReview, error) {
	receipt, err := m.resolveInPlaceRun(runID)
	if err != nil {
		return TaskReview{}, ErrTaskReviewUnavailable
	}
	canonical := receipt.LocalPath
	if inside, err := gitOutput(ctx, canonical, "rev-parse", "--is-inside-work-tree"); err != nil ||
		strings.TrimSpace(inside) != "true" {
		return TaskReview{}, ErrTaskReviewUnavailable
	}
	head := receipt.HeadCommit
	if receipt.State == workspaceActive {
		if head, err = optionalHead(ctx, canonical); err != nil {
			return TaskReview{}, errors.Join(ErrTaskReviewUnavailable, err)
		}
	}
	review := TaskReview{
		RunID:   runID,
		BaseRef: shortCommit(receipt.BaseCommit),
		HeadRef: shortCommit(head),
	}
	// The Run's commit must still be in the tree: rewritten history, or a
	// directory another Run reused from an unrelated state, cannot prove the
	// retained identity (docs/api/workspaces.md: workspace_unavailable).
	if head != "" {
		if err := runGit(ctx, canonical, nil, nil, "merge-base", "--is-ancestor", head, "HEAD"); err != nil {
			return TaskReview{}, ErrTaskReviewUnavailable
		}
	}
	if dirty, err := gitDirty(ctx, canonical); err != nil {
		return TaskReview{}, errors.Join(ErrTaskReviewUnavailable, err)
	} else if dirty {
		review.Status = TaskReviewMainDirty
		review.Message = "The bound directory has uncommitted changes. Commit or clear them before review."
		return review, nil
	}
	if head == "" || head == receipt.BaseCommit {
		review.Status = TaskReviewReady
		review.Message = "No file changes were found."
		return review, nil
	}
	diff, err := inPlaceRunDiff(ctx, canonical, receipt.BaseCommit, head)
	if errors.Is(err, errTaskMergeDiffTooLarge) {
		return TaskReview{}, ErrTaskReviewTooLarge
	}
	if err != nil {
		return TaskReview{}, errors.Join(ErrTaskReviewUnavailable, err)
	}
	if !utf8.Valid(diff) {
		return TaskReview{}, ErrTaskReviewUnsupported
	}
	review.Diff = string(diff)
	review.Status = TaskReviewReady
	if len(bytes.TrimSpace(diff)) == 0 {
		review.Message = "No file changes were found."
		return review, nil
	}
	review.Message = "Task changes are committed in place and ready to accept."
	return review, nil
}

// inPlaceRunDiff is the Run's own deliverable: base..head, or everything
// since the empty tree when the Run made the repository's first commit.
func inPlaceRunDiff(ctx context.Context, dir, base, head string) ([]byte, error) {
	from := base
	if from == "" {
		emptyTree, err := gitOutput(ctx, dir, "hash-object", "-t", "tree", os.DevNull)
		if err != nil || !validCommitID(strings.TrimSpace(emptyTree)) {
			return nil, errors.New("empty tree is unavailable")
		}
		from = strings.TrimSpace(emptyTree)
	}
	return gitOutputBytes(
		ctx, dir, nil, maximumTaskMergeDiffBytes,
		"diff", "--full-index", "--find-renames", from, head, "--", ".",
	)
}

// optionalHead is the commit HEAD points at, or "" for a repository that has
// no commit yet.
func optionalHead(ctx context.Context, dir string) (string, error) {
	if err := runGit(ctx, dir, nil, nil, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		return "", nil
	}
	head, err := gitOutput(ctx, dir, "rev-parse", "HEAD")
	commit := strings.TrimSpace(head)
	if err != nil || !validCommitID(commit) {
		return "", errors.New("local directory HEAD is unavailable")
	}
	return commit, nil
}

func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

func (m *Manager) inPlaceReceiptPath(runID uuid.UUID) string {
	return filepath.Join(m.root, LocalScratchDirectoryName, runID.String(), inPlaceReceiptFileName)
}

func (m *Manager) writeInPlaceRun(runID uuid.UUID, receipt inPlaceRun) error {
	receipt.Signature = m.signInPlaceRun(receipt)
	if receipt.Signature == "" {
		return errors.New("authenticate in-place run receipt")
	}
	return writeJSONFileAtomic(m.inPlaceReceiptPath(runID), receipt, "in-place run receipt")
}

func (m *Manager) readInPlaceRun(runID uuid.UUID) (inPlaceRun, error) {
	var receipt inPlaceRun
	if err := readJSONFileBounded(m.inPlaceReceiptPath(runID), &receipt, "in-place run receipt"); err != nil {
		return inPlaceRun{}, err
	}
	if !m.verifyInPlaceRun(receipt) {
		return inPlaceRun{}, fmt.Errorf("%w: in-place run receipt authentication failed", errMetadataUnusable)
	}
	if receipt.Version != inPlaceReceiptVersion || receipt.RunID != runID.String() ||
		!validWorkspaceID(receipt.CompanyID) || !validWorkspaceID(receipt.ConversationID) ||
		(receipt.TaskID != "" && !validWorkspaceID(receipt.TaskID)) ||
		!filepath.IsAbs(receipt.LocalPath) || filepath.Clean(receipt.LocalPath) != receipt.LocalPath ||
		(receipt.BaseCommit != "" && !validCommitID(receipt.BaseCommit)) ||
		(receipt.HeadCommit != "" && !validCommitID(receipt.HeadCommit)) ||
		receipt.CreatedAt.IsZero() {
		return inPlaceRun{}, fmt.Errorf("%w: in-place run receipt is invalid", errMetadataUnusable)
	}
	switch receipt.State {
	case workspaceActive:
		if receipt.HeadCommit != "" || receipt.CompletedAt != nil {
			return inPlaceRun{}, fmt.Errorf(
				"%w: active in-place run receipt is invalid",
				errMetadataUnusable,
			)
		}
	case workspaceCompleted:
		if receipt.CompletedAt == nil {
			return inPlaceRun{}, fmt.Errorf(
				"%w: completed in-place run receipt is invalid",
				errMetadataUnusable,
			)
		}
	default:
		return inPlaceRun{}, fmt.Errorf("%w: in-place run receipt state is invalid", errMetadataUnusable)
	}
	return receipt, nil
}

func (m *Manager) signInPlaceRun(receipt inPlaceRun) string {
	receipt.Signature = ""
	body, err := json.Marshal(receipt)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, m.metadataKey)
	_, _ = mac.Write([]byte("in-place-run\n"))
	_, _ = mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (m *Manager) verifyInPlaceRun(receipt inPlaceRun) bool {
	provided, err := base64.RawURLEncoding.Strict().DecodeString(receipt.Signature)
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	expected, err := base64.RawURLEncoding.Strict().DecodeString(m.signInPlaceRun(receipt))
	return err == nil && hmac.Equal(provided, expected)
}
