package canvas

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func (s *DefaultPageService) ReviewPageChange(
	ctx context.Context,
	p auth.UserPrincipal,
	project, pageID, id uuid.UUID,
	in PageChangeReviewInput,
) (PageEditResult, error) {
	var result PageEditResult
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var err error
		result, pending, err = ReviewPageChangeInTx(
			ctx,
			q,
			p,
			PageChangeDecision{
				Authoring:   s.authoring,
				InvokeAgent: s.invokeDocumentAgent,
				ProjectID:   project,
				PageID:      pageID,
				ChangeID:    id,
				Input:       in,
			},
		)
		return err
	})
	if err != nil {
		return PageEditResult{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

// PageChangeDecision identifies the exact document and proposal being reviewed.
type PageChangeDecision struct {
	InvokeAgent DocumentAgentInvoker   `json:"-"`
	Authoring   wiki.UIAuthoringPolicy `json:"-"`
	ProjectID   uuid.UUID              `json:"projectId"`
	PageID      uuid.UUID              `json:"pageId"`
	ChangeID    uuid.UUID              `json:"changeId"`
	Input       PageChangeReviewInput  `json:"input"`
}

// ReviewPageChangeInTx joins a caller-owned transaction. The caller publishes
// returned events only after commit, so multi-document approval is atomic.
func ReviewPageChangeInTx(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	request PageChangeDecision,
) (PageEditResult, []events.Event, error) {
	project, pageID, id, in := request.ProjectID, request.PageID, request.ChangeID, request.Input
	if in.RequestKey == uuid.Nil || len(in.Fingerprint) != 64 || len(in.Indexes) > 10000 ||
		len(in.Feedback) > 4000 {
		return PageEditResult{}, nil, ErrInvalid
	}
	switch in.Action {
	case "apply", "reject", "undo", "resolve":
	case "request_changes":
		if strings.TrimSpace(in.Feedback) == "" {
			return PageEditResult{}, nil, ErrInvalid
		}
	default:
		return PageEditResult{}, nil, ErrInvalid
	}
	raw, _ := json.Marshal(struct {
		ChangeID uuid.UUID             `json:"changeId"`
		PageID   uuid.UUID             `json:"pageId"`
		Input    PageChangeReviewInput `json:"input"`
	}{id, pageID, in})
	hash := sha256.Sum256(raw)
	requestHash := hex.EncodeToString(hash[:])
	var result PageEditResult
	var pending []events.Event
	err := func() error {
		var err error
		project, err = pageReviewProject(ctx, q, p, pageReviewScope{ProjectID: project, PageID: pageID})
		if err != nil {
			return err
		}
		request.ProjectID = project
		if project != uuid.Nil {
			if _, err := authorize(ctx, q, p, project, true); err != nil {
				return err
			}
			if _, err := q.EnsureProjectCanvas(
				ctx,
				generated.EnsureProjectCanvasParams{CompanyID: p.CompanyID, ProjectID: project},
			); err != nil {
				return err
			}
		}
		if _, err := authorizeArtifactContent(ctx, q, p, pageID, true); err != nil {
			return err
		}
		if _, err := editableProjectPage(ctx, q, p, project, pageID); err != nil {
			return err
		}
		receipt, err := q.GetPageSuggestionReview(
			ctx,
			generated.GetPageSuggestionReviewParams{
				CompanyID:   p.CompanyID,
				ActorUserID: p.UserID,
				RequestKey:  in.RequestKey,
			},
		)
		if err == nil {
			if receipt.Fingerprint != requestHash {
				return ErrConflict
			}
			return json.Unmarshal(receipt.Result, &result)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		row, err := q.GetProjectPageChange(
			ctx,
			generated.GetProjectPageChangeParams{CompanyID: p.CompanyID, ProjectID: project, ID: id},
		)
		if err != nil {
			return lookup(err)
		}
		if row.PageID != pageID || row.ActorUserID != p.UserID {
			return ErrNotFound
		}
		draft, err := wiki.BlockDraftInTx(ctx, q, p.CompanyID, pageID)
		if err != nil {
			return err
		}
		var current wiki.BlockNode
		if err = json.Unmarshal(draft.Content, &current); err != nil {
			return err
		}
		peers, err := q.ListPageSuggestionChanges(
			ctx,
			generated.ListPageSuggestionChangesParams{CompanyID: p.CompanyID, PageID: pageID},
		)
		if err != nil {
			return err
		}
		metadata, err := q.GetProjectPageMetadata(
			ctx,
			generated.GetProjectPageMetadataParams{
				CompanyID: p.CompanyID,
				ProjectID: nullable(&project),
				ID:        pageID,
			},
		)
		if err != nil {
			return err
		}

		review, err := pageSuggestionState(
			row,
			pageSuggestionSnapshot{
				root:              current,
				state:             draft.State,
				theme:             metadata.Theme,
				structureSequence: draft.StructureSeq,
			},
			peers,
		)
		if err != nil {
			return err
		}
		if in.Action == "undo" {
			if row.Status != "applied" && row.Status != "partially_applied" && row.Status != "resolved" {
				return ErrConflict
			}
		} else if row.Status != "proposed" && row.Status != "partially_applied" && row.Status != "needs_update" {
			return ErrConflict
		}
		var indexes []int
		if in.Action == "undo" {
			for i := range review.operations {
				if review.decisions[i] == "applied" ||
					row.Status == "applied" && len(review.decisions) == 0 {
					indexes = append(indexes, i)
				}
			}
			if len(indexes) == 0 {
				return ErrConflict
			}
		} else {
			indexes, err = suggestionSelection(review, in)
			if err != nil {
				return err
			}
		}
		// Text acceptance merges the immutable proposal into the locked latest
		// document. Other actions and structural selections retain exact review.
		if review.fingerprint != in.Fingerprint && !acceptsLatestPageText(review, in.Action, indexes) {
			return ErrConflict
		}
		selected := make([]wiki.BlockOperation, 0, len(indexes))
		for _, i := range indexes {
			if in.Action == "apply" && review.suggestions[i].Status == "already_reflected" {
				continue
			}
			if review.operations[i].Type != "theme" {
				op := review.operations[i]
				if in.Action == "apply" && op.Type == "insert" {
					op = insertionBesideAcceptedSibling(review, current, i, indexes)
				}
				selected = append(selected, op)
			}
		}
		changesDocument := in.Action == "undo" ||
			in.Action == "apply" &&
				(len(selected) > 0 || row.AfterTheme.Valid && review.suggestions[0].Status == "pending")
		var beforeRevision, resultRevision uuid.NullUUID
		resultTheme := metadata.Theme
		result = PageEditResult{ID: id, Status: row.Status, Sequence: draft.HeadSeq}
		if changesDocument {
			beforeRevision, err = captureReviewRevision(
				ctx,
				q,
				p,
				pageReviewCapture{ProjectID: project, PageID: pageID},
			)
			if err != nil {
				return err
			}

			if row.AfterTheme.Valid {
				expected, target := row.BeforeTheme.String, row.AfterTheme
				if in.Action == "undo" {
					expected, target = row.AfterTheme.String, row.BeforeTheme
				}
				resultTheme = target.String
				if metadata.Theme != expected {
					return ErrConflict
				}
				if e := q.UpdateProjectPageMetadata(
					ctx,
					generated.UpdateProjectPageMetadataParams{
						CompanyID: p.CompanyID,
						ProjectID: project,
						PageID:    pageID,
						Theme:     target,
					},
				); e != nil {
					return e
				}
			}
			var state []byte
			var edited wiki.BlockNode
			if in.Action == "undo" {
				var before wiki.BlockNode
				if err = json.Unmarshal(row.BeforeContent, &before); err != nil {
					return err
				}
				state, edited, err = undoPageOperations(draft.State, before, selected)
			} else {
				state, edited, err = applyPageSuggestions(draft.State, review, indexes)
			}
			if err != nil {
				return err
			}
			if err = validateDocumentReferences(ctx, q, p, nullable(&project), edited); err != nil {
				return err
			}
			structural := in.Action == "undo"
			for _, op := range selected {
				if op.Type != "insert" {
					// Only retained text objects can accept an older editor's edits.
					old := wiki.FindBlock(current, op.BlockID)
					if op.Type != "update" || old == nil || op.Content == nil ||
						!wiki.IsTextBlockUpdate(*old, *op.Content) {
						structural = true
					}
				}
			}
			if err = savePageEdit(
				ctx,
				q,
				pageDraftEdit{
					authoring:   request.Authoring,
					savedSchema: int(draft.SchemaVersion),
					principal:   p,
					page:        pageID,
					sequence:    draft.HeadSeq,
					beforeState: draft.State,
					state:       state,
					root:        edited,
					structural:  structural,
				},
			); err != nil {
				return err
			}
			if err = forgetHumanChanges(ctx, q, p, pageID, edited); err != nil {
				return err
			}
			result.Sequence++
			event, e := (documentTarget{PageID: pageID}).changed(ctx, q, p, result.Sequence, pageID)
			if e != nil {
				return e
			}
			if event != nil {
				pending = append(pending, *event)
			}
		}
		if changesDocument {
			resultRevision, err = captureReviewRevision(
				ctx,
				q,
				p,
				pageReviewCapture{ProjectID: project, PageID: pageID},
			)
			if err != nil {
				return err
			}
		}
		decision := map[string]string{"apply": "applied", "reject": "rejected", "resolve": "resolved", "undo": "undone"}[in.Action]
		if in.Action == "request_changes" {
			if err = requestPageRevision(
				ctx,
				q,
				&pending,
				pageRevisionRequest{principal: p, decision: request, change: row, indexes: indexes},
			); err != nil {
				return err
			}
			result.Status = "needs_update"
		} else {
			for _, index := range indexes {
				review.decisions[index] = decision
				if in.Action == "apply" && review.suggestions[index].Status == "already_reflected" {
					review.decisions[index] = "resolved"
				}
			}
			result.Status = suggestionDecisionStatus(len(review.operations), review.decisions, decision)
		}
		if result.Status == "applied" {
			count, e := q.AcceptArtifactCreation(
				ctx,
				generated.AcceptArtifactCreationParams{
					CompanyID:        p.CompanyID,
					CreationChangeID: nullable(&id),
				},
			)
			if e != nil {
				return e
			}
			if count > 0 {
				accepted, e := wiki.BlockDraftInTx(ctx, q, p.CompanyID, pageID)
				if e != nil {
					return e
				}
				var root wiki.BlockNode
				if e = json.Unmarshal(accepted.Content, &root); e != nil {
					return e
				}
				frozen, e := frozenPageContent(ctx, q, p, project, root)
				if e != nil {
					return e
				}
				content, e := json.Marshal(frozen)
				if e != nil {
					return e
				}
				snapshotID, e := wiki.CheckpointCanvasPageInTx(
					ctx,
					q,
					wiki.CanvasCheckpointInput{
						Principal: p,
						PageID:    pageID,
						Body:      wiki.BlocksMarkdown(frozen),
						Sequence:  accepted.HeadSeq,
						Content:   content,
					},
				)
				if e != nil {
					return e
				}
				board, e := q.EnsureProjectCanvas(
					ctx,
					generated.EnsureProjectCanvasParams{CompanyID: p.CompanyID, ProjectID: project},
				)
				if e != nil {
					return e
				}
				event, e := registerNativeOutput(
					ctx,
					q,
					nativeOutputRegistration{
						input: OutputRegistrationInput{
							CompanyID:   p.CompanyID,
							ActorUserID: p.UserID,
						},
						project:  project,
						board:    board.ID,
						snapshot: snapshotID,
					},
				)
				if e != nil {
					return e
				}
				if event != nil {
					pending = append(pending, *event)
				}
			}
		}
		decisions, _ := json.Marshal(review.decisions)
		if err = q.UpdatePageSuggestionDecisions(
			ctx,
			generated.UpdatePageSuggestionDecisionsParams{
				CompanyID: p.CompanyID,
				ID:        id,
				Decisions: decisions,
				Status:    result.Status,
			},
		); err != nil {
			return err
		}
		selection, _ := json.Marshal(indexes)
		encoded, _ := json.Marshal(result)
		if _, err = q.RecordPageSuggestionReview(
			ctx,
			generated.RecordPageSuggestionReviewParams{
				CompanyID:        p.CompanyID,
				ChangeID:         id,
				ActorUserID:      p.UserID,
				RequestKey:       in.RequestKey,
				Fingerprint:      requestHash,
				Action:           in.Action,
				Selection:        selection,
				Feedback:         in.Feedback,
				BeforeSequence:   draft.HeadSeq,
				AfterSequence:    result.Sequence,
				Result:           encoded,
				BeforeRevisionID: beforeRevision,
				ResultRevisionID: resultRevision,
				BeforeTheme:      util.StrToText(metadata.Theme),
				ResultTheme:      util.StrToText(resultTheme),
			},
		); err != nil {
			return err
		}
		if project == uuid.Nil {
			event, err := (documentTarget{PageID: pageID}).changed(ctx, q, p, result.Sequence, pageID)
			if event != nil {
				pending = append(pending, *event)
			}
			return err
		}
		sequence, err := q.AdvanceCanvasHead(
			ctx,
			generated.AdvanceCanvasHeadParams{CompanyID: p.CompanyID, ProjectID: project},
		)
		if err != nil {
			return err
		}
		event, err := changeEvent(ctx, q, p, project, sequence, uuid.Nil)
		if event != nil {
			pending = append(pending, *event)
		}
		return err
	}()
	if err != nil {
		return PageEditResult{}, nil, mapWrite(err)
	}
	return result, pending, nil
}

type pageReviewCapture struct{ ProjectID, PageID uuid.UUID }

func captureReviewRevision(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	in pageReviewCapture,
) (uuid.NullUUID, error) {
	draft, err := wiki.BlockDraftInTx(ctx, q, p.CompanyID, in.PageID)
	if err != nil {
		return uuid.NullUUID{}, err
	}
	var root wiki.BlockNode
	if err = json.Unmarshal(draft.Content, &root); err != nil {
		return uuid.NullUUID{}, err
	}
	frozen, err := frozenPageContent(ctx, q, p, in.ProjectID, root)
	if err != nil {
		return uuid.NullUUID{}, err
	}
	content, err := json.Marshal(frozen)
	if err != nil {
		return uuid.NullUUID{}, err
	}
	revision, err := wiki.CheckpointCanvasPageInTx(
		ctx,
		q,
		wiki.CanvasCheckpointInput{
			Principal: p,
			PageID:    in.PageID,
			Body:      wiki.BlocksMarkdown(frozen),
			Sequence:  draft.HeadSeq,
			Content:   content,
		},
	)
	if err != nil {
		return uuid.NullUUID{}, err
	}
	return nullable(&revision), nil
}

// Selection validation runs first, retaining decision, grouping and applicability
// checks even when a text preview has changed since it was fetched.
func acceptsLatestPageText(review suggestionReviewState, action string, indexes []int) bool {
	if action != "apply" || len(indexes) == 0 {
		return false
	}
	for _, index := range indexes {
		if review.operations[index].Type != "update" || index >= len(review.updates) ||
			len(review.updates[index]) == 0 {
			return false
		}
	}
	return true
}
