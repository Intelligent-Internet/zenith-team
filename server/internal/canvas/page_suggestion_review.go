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
			PageChangeDecision{ProjectID: project, PageID: pageID, ChangeID: id, Input: in},
		)
		return err
	})
	if err != nil {
		return PageEditResult{}, err
	}
	s.bus.Publish(ctx, Topic, pending...)
	return result, nil
}

// PageChangeDecision identifies the exact document and proposal being reviewed.
type PageChangeDecision struct {
	ProjectID uuid.UUID             `json:"projectId"`
	PageID    uuid.UUID             `json:"pageId"`
	ChangeID  uuid.UUID             `json:"changeId"`
	Input     PageChangeReviewInput `json:"input"`
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
		if _, err := authorize(ctx, q, p, project, true); err != nil {
			return err
		}
		if _, err := q.EnsureProjectCanvas(
			ctx,
			generated.EnsureProjectCanvasParams{CompanyID: p.CompanyID, ProjectID: project},
		); err != nil {
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
		if row.PageID != pageID {
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
		review, err := pageSuggestionState(row, current, metadata.Theme, peers)
		if err != nil {
			return err
		}
		if review.fingerprint != in.Fingerprint {
			return ErrConflict
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
		selected := make([]wiki.BlockOperation, 0, len(indexes))
		for _, i := range indexes {
			if review.operations[i].Type != "theme" {
				selected = append(selected, review.operations[i])
			}
		}
		var beforeRevision, resultRevision uuid.NullUUID
		resultTheme := metadata.Theme
		result = PageEditResult{ID: id, Status: row.Status, Sequence: draft.HeadSeq}
		if in.Action == "apply" || in.Action == "undo" {
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
				state, edited, err = wiki.ApplyBlockOperations(draft.State, selected)
			}
			if err != nil {
				return err
			}
			if err = validateBlockReferences(ctx, q, p, project, edited); err != nil {
				return err
			}
			structural := in.Action == "undo"
			for _, op := range selected {
				if op.Type != "insert" {
					structural = true
				}
			}
			if err = savePageEdit(
				ctx,
				q,
				pageDraftEdit{
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
			event, e := changeEvent(ctx, q, p, project, result.Sequence, pageID)
			if e != nil {
				return e
			}
			if event != nil {
				pending = append(pending, *event)
			}
		}
		if in.Action == "apply" || in.Action == "undo" {
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
		decision := map[string]string{"apply": "applied", "reject": "rejected", "resolve": "already_reflected", "undo": "undone"}[in.Action]
		if in.Action == "request_changes" {
			result.Status = "needs_update"
		} else {
			for _, index := range indexes {
				review.decisions[index] = decision
			}
			result.Status = decision
			if in.Action == "resolve" {
				result.Status = "resolved"
			}
			if len(review.operations) > 0 {
				unresolved, applied := 0, 0
				for i := range review.operations {
					if review.decisions[i] == "" {
						unresolved++
					}
					if review.decisions[i] == "applied" {
						applied++
					}
				}
				switch {
				case unresolved > 0:
					result.Status = "proposed"
					if applied > 0 {
						result.Status = "partially_applied"
					}
				case applied == len(review.operations):
					result.Status = "applied"
				case applied > 0:
					result.Status = "resolved"
				}
			}
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
				version, e := q.GetProjectPageVersion(
					ctx,
					generated.GetProjectPageVersionParams{
						CompanyID: p.CompanyID,
						ProjectID: project,
						ID:        pageID,
					},
				)
				if e != nil {
					return e
				}
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
				revision, e := wiki.CheckpointCanvasPageInTx(
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
				number, e := q.AllocateCanvasVersion(ctx, version.ArtifactID)
				if e != nil {
					return e
				}
				if _, e = q.CreateCanvasVersion(
					ctx,
					generated.CreateCanvasVersionParams{
						CompanyID:       p.CompanyID,
						ProjectID:       project,
						ArtifactID:      version.ArtifactID,
						Number:          number,
						SourceType:      "wiki",
						WikiPageID:      nullable(&pageID),
						WikiRevisionID:  nullable(&revision),
						BaseVersionID:   nullable(&version.ID),
						CreatedByUserID: p.UserID,
					},
				); e != nil {
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
						project: project,
						board:   board.ID,
						page:    pageID,
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
