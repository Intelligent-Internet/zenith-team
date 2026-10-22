package canvas

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func (s *DefaultPageService) PageReview(ctx context.Context, p auth.UserPrincipal, page uuid.UUID) ([]Thread, error) {
	var result []Thread
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		owner, _, err := (documentTarget{PageID: page}).resolve(ctx, q, p, false)
		if err != nil {
			return err
		}
		if owner.ArtifactType != "page" {
			return ErrInvalid
		}
		rows, err := q.ListPageReviewThreads(
			ctx,
			generated.ListPageReviewThreadsParams{CompanyID: p.CompanyID, PageID: nullable(&page)},
		)
		if err != nil {
			return err
		}
		result, err = decodeRows[Thread](rows)
		return err
	})
	return result, mapWrite(err)
}

func (s *DefaultPageService) MutatePageReview(
	ctx context.Context,
	p auth.UserPrincipal,
	page uuid.UUID,
	input Command,
) (Result, error) {
	agents, writing := input.AgentIDs, input.WritingTarget
	input.AgentIDs, input.WritingTarget = nil, nil
	c, err := normalizeOutputReviewCommand(input)
	if err != nil {
		return Result{}, err
	}
	if c.PageID != nil && *c.PageID != page {
		return Result{}, ErrInvalid
	}
	if c.VersionID != nil || c.OutputID != nil {
		return Result{}, ErrInvalid
	}
	c.PageID = &page
	c.AgentIDs, c.WritingTarget = agents, writing
	if err := validateCommentAgents(c); err != nil {
		return Result{}, err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return Result{}, err
	}
	fingerprint := sha256.Sum256(raw)
	var result Result
	var notification *events.Event
	var pending []events.Event
	for range 3 {
		result, notification, pending = Result{}, nil, nil
		err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			if err := auth.LockArtifactLifecycle(ctx, q, p, page); err != nil {
				return err
			}
			// Completion holds the conversation before it locks the Page and thread.
			conversations, err := lockCommentAgentConversations(
				ctx,
				q,
				commentAgentRequest{Principal: p, Command: c},
			)
			if err != nil {
				return err
			}
			if err := lockPageCommentAccess(ctx, q, p, page); err != nil {
				return err
			}
			if err := conversations.check(ctx, q); err != nil {
				return err
			}
			owner, _, err := (documentTarget{PageID: page}).resolve(ctx, q, p, false)
			if err != nil {
				return err
			}
			if owner.ArtifactType != "page" {
				return ErrInvalid
			}
			prior, err := q.GetPageReviewOperation(
				ctx,
				generated.GetPageReviewOperationParams{
					PageID:      page,
					ActorUserID: p.UserID,
					RequestKey:  c.RequestKey,
				},
			)
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
			id, err := mutatePageComment(
				ctx,
				q,
				pageCommentMutation{Principal: p, PageID: page, Command: c},
			)
			if err != nil {
				return err
			}
			result = Result{ID: id}
			if err := invokePageCommentAgents(
				ctx,
				q,
				&pending,
				commentAgentRequest{
					Principal: p,
					ThreadID:  id,
					Command:   c,
					Invoke:    s.invokeDocumentAgent,
				},
			); err != nil {
				return err
			}
			if err = q.CreatePageReviewOperation(
				ctx,
				generated.CreatePageReviewOperationParams{
					CompanyID:   p.CompanyID,
					PageID:      page,
					ActorUserID: p.UserID,
					RequestKey:  c.RequestKey,
					Fingerprint: fingerprint[:],
					ResultID:    id,
				},
			); err != nil {
				return err
			}
			notification, err = (documentTarget{PageID: page}).changed(ctx, q, p, 0, page)
			return err
		})
		if !errors.Is(err, errDocumentAgentConversationChanged) {
			break
		}
	}
	if errors.Is(err, errDocumentAgentConversationChanged) {
		err = ErrConflict
	}
	if err == nil && notification != nil {
		s.bus.Publish(ctx, Topic, *notification)
	}
	if err == nil {
		for _, group := range events.GroupByTopic(pending) {
			s.bus.Publish(ctx, group.Topic, group.Events...)
		}
	}
	return result, mapWrite(err)
}

type pageCommentMutation struct {
	Principal auth.UserPrincipal
	PageID    uuid.UUID
	Command   Command
}

func mutatePageComment(ctx context.Context, q *generated.Queries, m pageCommentMutation) (uuid.UUID, error) {
	p, id, c := m.Principal, m.PageID, m.Command
	var threadID uuid.UUID
	if c.Type == "comment" {
		var anchor struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(c.Anchor, &anchor) != nil ||
			!slices.Contains([]string{"version", "textQuote", "block"}, anchor.Type) {
			return uuid.Nil, ErrInvalid
		}
		count, err := q.CountPageReviewThreads(
			ctx,
			generated.CountPageReviewThreadsParams{CompanyID: p.CompanyID, PageID: nullable(&id)},
		)
		if err != nil {
			return uuid.Nil, err
		}
		if count >= 100 {
			return uuid.Nil, ErrLimit
		}
		threadID, err = q.CreatePageReviewThread(
			ctx,
			generated.CreatePageReviewThreadParams{
				CompanyID:       p.CompanyID,
				PageID:          id,
				Anchor:          c.Anchor,
				CreatedByUserID: p.UserID,
			},
		)
		if err != nil {
			return uuid.Nil, err
		}
	} else {
		thread, err := q.GetPageReviewThread(
			ctx,
			generated.GetPageReviewThreadParams{
				CompanyID: p.CompanyID,
				PageID:    nullable(&id),
				ID:        *c.ThreadID,
			},
		)
		if err != nil {
			return uuid.Nil, lookup(err)
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
		count, err := q.ResolvePageReviewThread(ctx, generated.ResolvePageReviewThreadParams{
			CompanyID:        p.CompanyID,
			PageID:           id,
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
		count, err := q.UpdatePageReviewComment(ctx, generated.UpdatePageReviewCommentParams{
			CompanyID:        p.CompanyID,
			PageID:           id,
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

type pageReviewScope struct {
	ProjectID, PageID uuid.UUID
	Write             bool
}

func authorizePageReview(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	scope pageReviewScope,
) error {
	if scope.ProjectID != uuid.Nil {
		if _, err := authorize(ctx, q, p, scope.ProjectID, scope.Write); err != nil {
			return err
		}
	}
	_, err := authorizeArtifactContent(ctx, q, p, scope.PageID, scope.Write)
	return err
}

// Page entry points derive ownership from the Page; a Project URL must match it.
func pageReviewProject(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	scope pageReviewScope,
) (uuid.UUID, error) {
	page, _, err := (documentTarget{PageID: scope.PageID}).resolve(ctx, q, p, false)
	if err != nil {
		return uuid.Nil, err
	}
	if page.ArtifactType != "page" || (scope.ProjectID != uuid.Nil && scope.ProjectID != page.ProjectID.UUID) {
		return uuid.Nil, ErrNotFound
	}
	return page.ProjectID.UUID, nil
}
