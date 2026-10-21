package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type DocumentHistoryRef struct{ ProjectID, VersionID, OutputID, ItemID uuid.UUID }

func (r DocumentHistoryRef) target() documentTarget {
	return documentTarget(r)
}

type DocumentHistoryInput struct {
	ActorID        *uuid.UUID
	After, Before  *time.Time
	BeforeSequence int64
}
type DocumentHistoryEntry struct {
	Sequence         int64                     `json:"sequence"`
	SavedAt          time.Time                 `json:"savedAt"`
	ActorType        string                    `json:"actorType"`
	ActorID          *uuid.UUID                `json:"actorId,omitempty"`
	ActorName        string                    `json:"actorName"`
	ChangeType       string                    `json:"changeType"`
	Label            string                    `json:"label"`
	SuggestionReview *DocumentSuggestionReview `json:"suggestionReview,omitempty"`
}

// DocumentSuggestionReview distinguishes the proposing Agent from the member
// whose decision saved this document state.
type DocumentSuggestionReview struct {
	ID               uuid.UUID  `json:"id"`
	ChangeID         uuid.UUID  `json:"changeId"`
	ProjectID        uuid.UUID  `json:"projectId"`
	PageID           uuid.UUID  `json:"pageId"`
	AgentID          *uuid.UUID `json:"agentId,omitempty"`
	AgentName        string     `json:"agentName"`
	Action           string     `json:"action"`
	BeforeSequence   int64      `json:"beforeSequence"`
	BeforeRevisionID *uuid.UUID `json:"beforeRevisionId,omitempty"`
	ResultRevisionID *uuid.UUID `json:"resultRevisionId,omitempty"`
	BeforeTheme      string     `json:"beforeTheme"`
	ResultTheme      string     `json:"resultTheme"`
}
type DocumentContributor struct {
	ActorType string    `json:"actorType"`
	ActorID   uuid.UUID `json:"actorId"`
	ActorName string    `json:"actorName"`
}
type DocumentHistory struct {
	Items              []DocumentHistoryEntry `json:"items"`
	Contributors       []DocumentContributor  `json:"contributors"`
	NextBeforeSequence int64                  `json:"nextBeforeSequence"`
	CurrentSequence    int64                  `json:"currentSequence"`
	CanEdit            bool                   `json:"canEdit"`
}
type DocumentHistoryState struct {
	PreviousSequence  int64                      `json:"previousSequence"`
	Sequence          int64                      `json:"sequence"`
	Markdown          string                     `json:"markdown"`
	Content           json.RawMessage            `json:"content"`
	SuggestionCapture *DocumentSuggestionCapture `json:"suggestionCapture,omitempty"`
}

type DocumentSuggestionCapture struct {
	BeforeContent  json.RawMessage `json:"beforeContent"`
	ResultContent  json.RawMessage `json:"resultContent"`
	BeforeMarkdown string          `json:"beforeMarkdown"`
	ResultMarkdown string          `json:"resultMarkdown"`
}
type (
	DocumentHistoryRestore struct{ Sequence, ExpectedSequence int64 }
	DocumentHistoryName    struct {
		Sequence int64
		Label    string
	}
)

func (s *DefaultPageService) DocumentHistory(
	ctx context.Context,
	p auth.UserPrincipal,
	ref DocumentHistoryRef,
	input DocumentHistoryInput,
) (DocumentHistory, error) {
	result := DocumentHistory{Items: []DocumentHistoryEntry{}, Contributors: []DocumentContributor{}}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		page, edit, err := ref.target().resolve(ctx, q, p, false)
		if err != nil {
			return err
		}
		// Published Wiki access does not grant access to its draft history.
		if !page.ProjectID.Valid && !page.SourceOutputID.Valid {
			return ErrForbidden
		}
		result.CanEdit = edit
		head, err := q.GetWikiBlockDraftHead(
			ctx,
			generated.GetWikiBlockDraftHeadParams{CompanyID: p.CompanyID, PageID: page.ID},
		)
		if err != nil {
			return lookup(err)
		}
		result.CurrentSequence = head.HeadSeq
		params := generated.ListDocumentChangesParams{
			CompanyID:      p.CompanyID,
			PageID:         page.ID,
			ActorID:        nullable(input.ActorID),
			BeforeSequence: input.BeforeSequence,
		}
		if input.After != nil {
			params.AfterTime = pgtype.Timestamptz{Time: *input.After, Valid: true}
		}
		if input.Before != nil {
			params.BeforeTime = pgtype.Timestamptz{Time: *input.Before, Valid: true}
		}
		rows, err := q.ListDocumentChanges(ctx, params)
		if err != nil {
			return err
		}
		if len(rows) > 100 {
			rows = rows[:100]
			result.NextBeforeSequence = rows[99].Sequence
		}
		sequences := make([]int64, len(rows))
		for index, row := range rows {
			sequences[index] = row.Sequence
		}
		reviews, err := q.DocumentSuggestionReviews(ctx, generated.DocumentSuggestionReviewsParams{
			CompanyID: p.CompanyID, PageID: page.ID, Sequences: sequences,
		})
		if err != nil {
			return err
		}
		applications := make(map[int64]*DocumentSuggestionReview, len(reviews))
		for _, review := range reviews {
			applications[review.AfterSequence] = &DocumentSuggestionReview{
				ID:        review.ID,
				ChangeID:  review.ChangeID,
				ProjectID: review.ProjectID,
				PageID:    review.PageID,
				AgentID: outputSource(
					review.ActorAgentID,
				),
				AgentName:      review.AgentName,
				Action:         review.Action,
				BeforeSequence: review.BeforeSequence,
				BeforeRevisionID: outputSource(
					review.BeforeRevisionID,
				),
				ResultRevisionID: outputSource(review.ResultRevisionID),
				BeforeTheme:      review.BeforeTheme.String,
				ResultTheme:      review.ResultTheme.String,
			}
		}
		for _, row := range rows {
			result.Items = append(
				result.Items,
				DocumentHistoryEntry{
					Sequence:         row.Sequence,
					SavedAt:          row.SavedAt.Time,
					ActorType:        row.ActorType,
					ActorID:          outputSource(row.ActorID),
					ActorName:        row.ActorName,
					ChangeType:       row.ChangeType,
					Label:            row.Label,
					SuggestionReview: applications[row.Sequence],
				},
			)
		}
		contributors, err := q.DocumentHistoryContributors(
			ctx,
			generated.DocumentHistoryContributorsParams{CompanyID: p.CompanyID, PageID: page.ID},
		)
		if err != nil {
			return err
		}
		for _, row := range contributors {
			result.Contributors = append(
				result.Contributors,
				DocumentContributor{
					ActorType: row.ActorType,
					ActorID:   row.ActorID.UUID,
					ActorName: row.ActorName,
				},
			)
		}
		return nil
	})
	return result, err
}

func (s *DefaultPageService) DocumentHistoryState(
	ctx context.Context,
	p auth.UserPrincipal,
	ref DocumentHistoryRef,
	sequence int64,
) (DocumentHistoryState, error) {
	var result DocumentHistoryState
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		page, _, err := ref.target().resolve(ctx, q, p, false)
		if err != nil {
			return err
		}
		// Published Wiki access does not grant access to its draft history.
		if !page.ProjectID.Valid && !page.SourceOutputID.Valid {
			return ErrForbidden
		}
		state, err := wiki.ReadDocumentHistoryInTx(
			ctx,
			q,
			wiki.HistoryPoint{CompanyID: p.CompanyID, PageID: page.ID, Sequence: sequence},
		)
		if err != nil {
			return lookup(err)
		}
		if page.ProjectID.Valid {
			var content wiki.BlockNode
			if err = json.Unmarshal(state.Content, &content); err != nil {
				return err
			}
			if err = redactUnavailableReferences(ctx, q, p, page.ProjectID.UUID, &content); err != nil {
				return err
			}
			state.Content, err = json.Marshal(content)
			if err != nil {
				return err
			}
			state.Markdown = wiki.BlocksMarkdown(content)
		}
		previous, err := q.PreviousDocumentChange(
			ctx,
			generated.PreviousDocumentChangeParams{
				CompanyID: p.CompanyID,
				PageID:    page.ID,
				Sequence:  sequence,
			},
		)
		if err != nil {
			return err
		}
		result = DocumentHistoryState{
			Sequence:         sequence,
			PreviousSequence: previous,
			Markdown:         state.Markdown,
			Content:          state.Content,
		}
		result.SuggestionCapture, err = documentSuggestionCapture(ctx, q, p, wiki.HistoryPoint{
			CompanyID: p.CompanyID, PageID: page.ID, Sequence: sequence,
		})
		return err
	})
	return result, err
}

func documentSuggestionCapture(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	point wiki.HistoryPoint,
) (*DocumentSuggestionCapture, error) {
	row, err := q.DocumentSuggestionCapture(ctx, generated.DocumentSuggestionCaptureParams{
		CompanyID: point.CompanyID, PageID: point.PageID, AfterSequence: point.Sequence,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var before, result wiki.BlockNode
	if err = json.Unmarshal(row.BeforeContent, &before); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(row.ResultContent, &result); err != nil {
		return nil, err
	}
	for _, content := range []*wiki.BlockNode{&before, &result} {
		if err = redactUnavailableReferences(ctx, q, p, row.ProjectID, content); err != nil {
			return nil, err
		}
	}
	beforeContent, err := json.Marshal(before)
	if err != nil {
		return nil, err
	}
	resultContent, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &DocumentSuggestionCapture{
		BeforeContent: beforeContent, ResultContent: resultContent,
		BeforeMarkdown: wiki.BlocksMarkdown(before), ResultMarkdown: wiki.BlocksMarkdown(result),
	}, nil
}

func (s *DefaultPageService) NameDocumentHistory(
	ctx context.Context,
	p auth.UserPrincipal,
	ref DocumentHistoryRef,
	input DocumentHistoryName,
) error {
	input.Label = strings.TrimSpace(input.Label)
	if len([]rune(input.Label)) > 200 || input.Sequence < 1 {
		return ErrInvalid
	}
	return s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		page, _, err := ref.target().resolve(ctx, q, p, true)
		if err != nil {
			return err
		}
		n, err := q.NameDocumentChange(
			ctx,
			generated.NameDocumentChangeParams{
				CompanyID: p.CompanyID,
				PageID:    page.ID,
				Sequence:  input.Sequence,
				Label:     input.Label,
			},
		)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *DefaultPageService) RestoreDocumentHistory(
	ctx context.Context,
	p auth.UserPrincipal,
	ref DocumentHistoryRef,
	input DocumentHistoryRestore,
) (DocumentHistoryState, error) {
	if input.Sequence < 1 || input.ExpectedSequence < 1 {
		return DocumentHistoryState{}, ErrInvalid
	}
	var result DocumentHistoryState
	var pending *events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		target := ref.target()
		page, _, err := target.resolve(ctx, q, p, true)
		if err != nil {
			return err
		}
		if _, err = q.LockCanvasWikiPage(
			ctx,
			generated.LockCanvasWikiPageParams{CompanyID: p.CompanyID, ID: page.ID},
		); err != nil {
			return lookup(err)
		}
		draft, err := q.LockWikiDraft(
			ctx,
			generated.LockWikiDraftParams{CompanyID: p.CompanyID, PageID: page.ID},
		)
		if err != nil {
			return lookup(err)
		}
		if input.ExpectedSequence != draft.HeadSeq {
			return ErrConflict
		}
		historical, err := wiki.ReadDocumentHistoryInTx(
			ctx,
			q,
			wiki.HistoryPoint{CompanyID: p.CompanyID, PageID: page.ID, Sequence: input.Sequence},
		)
		if err != nil {
			return lookup(err)
		}
		var root wiki.BlockNode
		if err = json.Unmarshal(historical.Content, &root); err != nil {
			return err
		}
		if err = validateDocumentReferences(ctx, q, p, page.ProjectID, root); err != nil {
			return err
		}
		state, root, markdown, err := wiki.ReplaceDocumentContent(draft.State, root)
		if err != nil {
			return err
		}
		content, err := json.Marshal(root)
		if err != nil {
			return err
		}
		n, err := q.UpdateWikiBlockDraft(
			ctx,
			generated.UpdateWikiBlockDraftParams{
				CompanyID:  p.CompanyID,
				PageID:     page.ID,
				State:      state,
				Content:    content,
				Markdown:   markdown,
				HeadSeq:    draft.HeadSeq,
				Structural: true,
			},
		)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrConflict
		}
		if err = wiki.RecordDocumentChangeInTx(
			ctx,
			q,
			wiki.DocumentChangeInput{
				CompanyID:  p.CompanyID,
				PageID:     page.ID,
				ActorType:  "user",
				ActorID:    p.UserID,
				ChangeType: "restore",
				Sequence:   draft.HeadSeq + 1,
				Before:     draft.State,
				State:      state,
			},
		); err != nil {
			return err
		}
		if err = reconcilePageComments(
			ctx,
			q,
			pageCommentEdit{companyID: p.CompanyID, pageID: page.ID, before: draft.State, after: state},
		); err != nil {
			return err
		}
		if err = forgetHumanChanges(ctx, q, p, page.ID, root); err != nil {
			return err
		}
		pending, err = target.changed(ctx, q, p, draft.HeadSeq+1, page.ID)
		result = DocumentHistoryState{Sequence: draft.HeadSeq + 1, Markdown: markdown, Content: content}
		return err
	})
	if err != nil {
		return result, mapWrite(err)
	}
	if pending != nil {
		s.bus.Publish(ctx, Topic, *pending)
	}
	return result, nil
}
