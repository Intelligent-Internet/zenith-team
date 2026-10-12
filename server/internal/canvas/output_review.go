package canvas

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// OutputReview presents a non-Task Output, its editable documents and saved
// revisions through the shared artifact review contract.
type OutputReview struct {
	Artifact   Artifact             `json:"artifact"`
	Items      []Thread             `json:"items"`
	CanComment bool                 `json:"canComment"`
	CanEdit    bool                 `json:"canEdit"`
	Documents  []OutputDocumentInfo `json:"documents"`
	Versions   []Version            `json:"versions"`
}

func authorizeOutputReview(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	id uuid.UUID,
) (generated.GetOutputReviewSourceRow, error) {
	level, err := auth.OutputAccessInTx(ctx, q, p, id)
	if err != nil {
		return generated.GetOutputReviewSourceRow{}, err
	}
	if level == 0 {
		return generated.GetOutputReviewSourceRow{}, ErrNotFound
	}
	row, err := q.GetOutputReviewSource(ctx, generated.GetOutputReviewSourceParams{
		CompanyID:   p.CompanyID,
		OutputID:    id,
		ActorUserID: p.UserID,
	})
	return row, lookup(err)
}

func (s *DefaultCanvasService) OutputReview(
	ctx context.Context, p auth.UserPrincipal, id uuid.UUID,
) (OutputReview, error) {
	var result OutputReview
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		source, err := authorizeOutputReview(ctx, q, p, id)
		if err != nil {
			return err
		}
		rows, err := q.ListOutputReviewThreads(ctx, generated.ListOutputReviewThreadsParams{
			CompanyID: p.CompanyID,
			OutputID:  nullable(&id),
		})
		if err != nil {
			return err
		}
		threads, err := decodeRows[Thread](rows)
		if err != nil {
			return err
		}
		documentAccess, err := auth.OutputDocumentAccessInTx(ctx, q, p, id)
		if err != nil {
			return err
		}
		if !documentAccess.Read {
			threads = slices.DeleteFunc(
				threads,
				func(t Thread) bool { return t.PageID != nil || t.VersionID != id },
			)
		}
		var open int64
		for _, thread := range threads {
			if thread.Status == "open" {
				open++
			}
		}
		version := Version{
			ID: id, ArtifactID: id, Number: 1, SourceType: "output",
			OutputID: &id, SourceOutputID: &id, RunID: util.NullUUIDPtr(source.RunID),
			BranchTitle: "Main", Excerpt: source.Summary,
			CreatedAt: source.CreatedAt.Time, OpenComments: open,
		}
		documents, err := q.ListOutputDocuments(
			ctx,
			generated.ListOutputDocumentsParams{CompanyID: p.CompanyID, OutputID: id},
		)
		if err != nil {
			return err
		}
		docs := make([]OutputDocumentInfo, 0, len(documents))
		for _, doc := range documents {
			if !documentAccess.Read {
				break
			}
			docs = append(
				docs,
				OutputDocumentInfo{
					OutputItemID: doc.OutputItemID,
					PageID:       doc.PageID,
					Title:        doc.Title,
				},
			)
		}
		revisions, err := q.ListOutputDocumentVersions(
			ctx,
			generated.ListOutputDocumentVersionsParams{CompanyID: p.CompanyID, OutputID: id},
		)
		if err != nil {
			return err
		}
		versions := make([]Version, 0, len(revisions)+1)
		for _, r := range revisions {
			if !documentAccess.Read {
				break
			}
			versions = append(
				versions,
				Version{
					ID:             r.ID,
					OutputItemID:   &r.OutputItemID,
					ArtifactID:     id,
					Number:         r.Number + 1,
					SourceType:     "wiki",
					WikiPageID:     &r.PageID,
					WikiRevisionID: &r.ID,
					SourceOutputID: &id,
					BranchTitle:    r.Title,
					Excerpt:        r.Excerpt,
					CreatedAt:      r.CreatedAt.Time,
				},
			)
		}
		versions = append(versions, version)
		result = OutputReview{
			Artifact: Artifact{
				ID:              id,
				Title:           source.Title,
				Revision:        1,
				LatestVersionID: versions[0].ID,
				LatestVersion:   versions[0],
				CreatorName:     source.CreatorName,
				ReviewState:     "needs_review",
				UpdatedAt:       source.CreatedAt.Time,
				OpenComments:    open,
			},
			Items:      threads,
			CanComment: true,
			CanEdit:    documentAccess.Edit,
			Documents:  docs,
			Versions:   versions,
		}
		return nil
	})
	return result, err
}

func normalizeOutputReviewCommand(c Command) (Command, error) {
	if len(c.AgentIDs) > 0 || c.WritingTarget != nil {
		return c, ErrInvalid
	}
	if c.RequestKey == uuid.Nil {
		return c, ErrInvalid
	}
	c.Body = strings.TrimSpace(c.Body)
	switch c.Type {
	case "comment", "reply", "edit_comment":
		if c.Body == "" {
			return c, ErrInvalid
		}
		if utf8.RuneCountInString(c.Body) > 10000 {
			return c, ErrLimit
		}
	case "resolve":
		if c.Action != "resolve" && c.Action != "reopen" {
			return c, ErrInvalid
		}
	case "delete_comment":
	default:
		return c, ErrInvalid
	}
	if c.Type == "comment" {
		if len(c.Anchor) == 0 {
			c.Anchor = json.RawMessage(`{"type":"version"}`)
		}
		if _, err := parseReviewAnchor(c.Anchor); err != nil {
			return c, err
		}
	} else if c.ThreadID == nil || *c.ThreadID == uuid.Nil {
		return c, ErrInvalid
	}
	if c.Type == "resolve" || c.Type == "edit_comment" || c.Type == "delete_comment" {
		if c.ExpectedRevision < 1 {
			return c, ErrInvalid
		}
	}
	if c.Type == "edit_comment" || c.Type == "delete_comment" {
		if c.ID == nil || *c.ID == uuid.Nil {
			return c, ErrInvalid
		}
	}
	return c, nil
}

func (s *DefaultCanvasService) MutateOutputReview(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	input Command,
) (Result, error) {
	c, err := normalizeOutputReviewCommand(input)
	if err != nil {
		return Result{}, err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return Result{}, err
	}
	fingerprint := sha256.Sum256(raw)
	var result Result
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorizeOutputReview(ctx, q, p, id); err != nil {
			return err
		}
		prior, err := q.GetOutputReviewOperation(ctx, generated.GetOutputReviewOperationParams{
			OutputID:    id,
			ActorUserID: p.UserID,
			RequestKey:  c.RequestKey,
		})
		if err == nil {
			if !bytes.Equal(prior.Fingerprint, fingerprint[:]) {
				return ErrConflict
			}
			result = Result{ID: prior.ResultID}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		threadID, err := mutateOutputComment(
			ctx,
			q,
			outputCommentMutation{Principal: p, OutputID: id, Command: c},
		)
		if err != nil {
			return err
		}
		result = Result{ID: threadID}
		return q.CreateOutputReviewOperation(ctx, generated.CreateOutputReviewOperationParams{
			CompanyID:   p.CompanyID,
			OutputID:    id,
			ActorUserID: p.UserID,
			RequestKey:  c.RequestKey,
			Fingerprint: fingerprint[:],
			ResultID:    threadID,
		})
	})
	return result, err
}

type outputCommentMutation struct {
	Principal auth.UserPrincipal
	OutputID  uuid.UUID
	Command   Command
}

func mutateOutputComment(ctx context.Context, q *generated.Queries, m outputCommentMutation) (uuid.UUID, error) {
	p, id, c := m.Principal, m.OutputID, m.Command
	var threadID uuid.UUID
	if c.Type == "comment" {
		if c.PageID != nil || (c.VersionID != nil && *c.VersionID != id) {
			if _, err := authorizeOutputDocument(ctx, q, p, id, false); err != nil {
				return uuid.Nil, err
			}
		}
		var revision *uuid.UUID
		if c.PageID == nil && c.VersionID != nil && *c.VersionID != id {
			if _, err := q.GetOutputDocumentVersion(
				ctx,
				generated.GetOutputDocumentVersionParams{
					CompanyID: p.CompanyID,
					OutputID:  id,
					ID:        *c.VersionID,
				},
			); err != nil {
				return uuid.Nil, lookup(err)
			}
			revision = c.VersionID
		}
		if c.PageID != nil {
			exists, err := q.OutputDocumentPageExists(
				ctx,
				generated.OutputDocumentPageExistsParams{
					CompanyID: p.CompanyID,
					OutputID:  id,
					PageID:    *c.PageID,
				},
			)
			if err != nil {
				return uuid.Nil, err
			}
			if !exists {
				return uuid.Nil, ErrNotFound
			}
			if _, err = q.LockCanvasWikiPage(
				ctx,
				generated.LockCanvasWikiPageParams{CompanyID: p.CompanyID, ID: *c.PageID},
			); err != nil {
				return uuid.Nil, lookup(err)
			}
			var anchor struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(c.Anchor, &anchor) != nil ||
				!slices.Contains([]string{"version", "textQuote", "block"}, anchor.Type) {
				return uuid.Nil, ErrInvalid
			}
		}
		anchor, err := parseReviewAnchor(c.Anchor)
		if err != nil {
			return uuid.Nil, err
		}
		if anchor != nil {
			if anchor.Target.OutputItemID == nil {
				return uuid.Nil, ErrInvalid
			}
			contentType, err := q.OutputReviewAnchorSource(ctx, generated.OutputReviewAnchorSourceParams{
				CompanyID:      p.CompanyID,
				OutputID:       id,
				OutputItemID:   *anchor.Target.OutputItemID,
				AssetVersionID: anchor.Target.AssetVersionID,
			})
			if err != nil {
				return uuid.Nil, lookup(err)
			}
			if anchorSurface(contentType) != anchor.Surface {
				return uuid.Nil, ErrInvalid
			}
		}
		count, err := q.CountOutputReviewThreads(ctx, generated.CountOutputReviewThreadsParams{
			CompanyID: p.CompanyID,
			OutputID:  nullable(&id),
		})
		if err != nil {
			return uuid.Nil, err
		}
		if count >= 100 {
			return uuid.Nil, ErrLimit
		}
		threadID, err = q.CreateOutputReviewThread(ctx, generated.CreateOutputReviewThreadParams{
			CompanyID:          p.CompanyID,
			OutputID:           nullable(&id),
			Anchor:             c.Anchor,
			CreatedByUserID:    p.UserID,
			PageID:             nullable(c.PageID),
			DocumentRevisionID: nullable(revision),
		})
		if err != nil {
			return uuid.Nil, err
		}
	} else {
		thread, err := q.GetOutputReviewThread(ctx, generated.GetOutputReviewThreadParams{
			CompanyID: p.CompanyID,
			OutputID:  nullable(&id),
			ID:        *c.ThreadID,
		})
		if err != nil {
			return uuid.Nil, lookup(err)
		}
		if thread.PageID.Valid || thread.DocumentRevisionID.Valid {
			if _, err := authorizeOutputDocument(ctx, q, p, id, false); err != nil {
				return uuid.Nil, err
			}
		}
		threadID = thread.ID
	}
	switch c.Type {
	case "comment", "reply":
		count, err := q.CountOutputReviewComments(ctx, threadID)
		if err != nil {
			return uuid.Nil, err
		}
		if count >= 100 {
			return uuid.Nil, ErrLimit
		}
		commentID, err := q.CreateOutputReviewComment(ctx, generated.CreateOutputReviewCommentParams{
			CompanyID:    p.CompanyID,
			ThreadID:     threadID,
			AuthorUserID: p.UserID,
			Body:         c.Body,
		})
		if err != nil {
			return uuid.Nil, err
		}
		err = q.RecordReviewCommentRevision(
			ctx,
			generated.RecordReviewCommentRevisionParams{CommentID: commentID, ActorUserID: p.UserID},
		)
		return threadID, err
	case "resolve":
		status := "resolved"
		if c.Action == "reopen" {
			status = "open"
		}
		count, err := q.ResolveOutputReviewThread(ctx, generated.ResolveOutputReviewThreadParams{
			CompanyID:        p.CompanyID,
			OutputID:         id,
			ID:               threadID,
			ExpectedRevision: c.ExpectedRevision, ActorUserID: p.UserID,
			Status: status,
		})
		if err != nil {
			return uuid.Nil, err
		}
		if count == 0 {
			return uuid.Nil, ErrConflict
		}
		action := "resolved"
		if status == "open" {
			action = "reopened"
		}
		if err = q.RecordReviewThreadEvent(
			ctx,
			generated.RecordReviewThreadEventParams{
				ThreadID:    threadID,
				Action:      action,
				ActorUserID: p.UserID,
			},
		); err != nil {
			return uuid.Nil, err
		}
	case "edit_comment", "delete_comment":
		body := c.Body
		if c.Type == "delete_comment" {
			body = ""
		}
		count, err := q.UpdateOutputReviewComment(ctx, generated.UpdateOutputReviewCommentParams{
			CompanyID:        p.CompanyID,
			OutputID:         id,
			ThreadID:         threadID,
			ID:               *c.ID,
			ActorUserID:      p.UserID,
			ExpectedRevision: c.ExpectedRevision,
			Body:             body,
			Remove:           c.Type == "delete_comment",
		})
		if err != nil {
			return uuid.Nil, err
		}
		if count == 0 {
			return uuid.Nil, ErrConflict
		}
		if err = q.RecordReviewCommentRevision(
			ctx,
			generated.RecordReviewCommentRevisionParams{CommentID: *c.ID, ActorUserID: p.UserID},
		); err != nil {
			return uuid.Nil, err
		}

	}
	return threadID, nil
}

type OutputDocumentInfo struct {
	OutputItemID uuid.UUID `json:"outputItemId"`
	PageID       uuid.UUID `json:"pageId"`
	Title        string    `json:"title"`
}
