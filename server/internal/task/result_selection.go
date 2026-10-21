package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// ResultPolicy is fixed before execution; an Agent cannot relax application rules.
type ResultPolicy struct {
	ProposalOnly   bool `json:"proposalOnly"`
	ReviewRequired bool `json:"reviewRequired"`
}

type ResultSelectionInput struct {
	RequestKey    uuid.UUID                   `json:"requestKey"              binding:"required"`
	OutputIDs     []uuid.UUID                 `json:"outputIds"               binding:"required,min=1,max=32"`
	AcceptChanges []canvas.PageChangeDecision `json:"acceptChanges,omitempty" binding:"max=32"`
}

type ResultSelection struct {
	ResultPolicy
	HasUnavailable bool        `json:"hasUnavailable"`
	PolicyEditable bool        `json:"policyEditable"`
	TaskRevision   int64       `json:"taskRevision"`
	SelectionID    *uuid.UUID  `json:"selectionId"`
	OutputIDs      []uuid.UUID `json:"outputIds"`
	ApprovedAt     *time.Time  `json:"approvedAt"`
}

func (s *DefaultTaskService) ResultSelection(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
) (ResultSelection, error) {
	var result ResultSelection
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := taskProjectionForActiveMember(ctx, q, p, id); err != nil {
			return err
		}
		state, err := q.GetTaskResultState(
			ctx,
			generated.GetTaskResultStateParams{CompanyID: p.CompanyID, ID: id},
		)
		if err != nil {
			return err
		}
		frozen, err := q.TaskResultPolicyFrozen(
			ctx,
			generated.TaskResultPolicyFrozenParams{
				CompanyID: p.CompanyID,
				TaskID:    util.NullUUIDFromPtr(&id),
			},
		)
		if err != nil {
			return err
		}
		result = ResultSelection{
			PolicyEditable: !frozen,
			ResultPolicy:   ResultPolicy{state.ResultProposalOnly, state.ResultReviewRequired},
			TaskRevision:   state.Version,
			OutputIDs:      state.OutputIds,
			ApprovedAt:     util.TimestampToTime(state.ApprovedAt),
		}
		result.OutputIDs = []uuid.UUID{}
		for _, outputID := range state.OutputIds {
			level, err := q.GetOutputAccessForManage(
				ctx,
				generated.GetOutputAccessForManageParams{
					CompanyID:   p.CompanyID,
					ActorUserID: p.UserID,
					OutputID:    outputID,
				},
			)
			if err != nil {
				return err
			}
			if level == 0 {
				result.HasUnavailable = true
				continue
			}
			result.OutputIDs = append(result.OutputIDs, outputID)
		}
		if state.ResultSetID != uuid.Nil {
			result.SelectionID = &state.ResultSetID
		}
		return nil
	})
	return result, err
}

type taskResultMutation struct {
	RunID   uuid.UUID
	Current Response
	Target  *Response
	Input   UpdateInput
	Pending *[]events.Event
}

func changeTaskResultsInTx(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	m taskResultMutation,
) error {
	if policy := m.Input.ResultPolicy; policy != nil {
		frozen, err := q.TaskResultPolicyFrozen(
			ctx,
			generated.TaskResultPolicyFrozenParams{
				CompanyID: p.CompanyID,
				TaskID:    util.NullUUIDFromPtr(&m.Current.ID),
			},
		)
		if err != nil {
			return err
		}
		if frozen {
			return ErrConflict
		}
		if err = q.SetTaskResultPolicy(
			ctx,
			generated.SetTaskResultPolicyParams{
				CompanyID:            p.CompanyID,
				ID:                   m.Current.ID,
				ResultProposalOnly:   policy.ProposalOnly,
				ResultReviewRequired: policy.ReviewRequired,
			},
		); err != nil {
			return err
		}
	}
	in := m.Input.ResultSelection
	if in == nil {
		return nil
	}
	if in.RequestKey == uuid.Nil || len(in.OutputIDs) == 0 || len(in.OutputIDs) > 32 || len(in.AcceptChanges) > 32 {
		return ErrInvalidInput
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range in.OutputIDs {
		if id == uuid.Nil || seen[id] {
			return ErrInvalidInput
		}
		seen[id] = true
		var eligible bool
		var err error
		if m.RunID != uuid.Nil {
			eligible, err = q.TaskResultOutputFromRunningRun(
				ctx,
				generated.TaskResultOutputFromRunningRunParams{
					CompanyID:   p.CompanyID,
					TaskID:      m.Current.ID,
					OutputID:    id,
					ActorUserID: p.UserID,
					RunID:       m.RunID,
				},
			)
		} else {
			eligible, err = q.TaskResultOutputEligible(
				ctx,
				generated.TaskResultOutputEligibleParams{
					CompanyID:   p.CompanyID,
					TaskID:      m.Current.ID,
					OutputID:    id,
					ActorUserID: p.UserID,
				},
			)
		}
		if err != nil {
			return err
		}
		if !eligible {
			return ErrConflict
		}
	}
	active, err := q.TaskHasNonterminalRunForCurrentExecutionGeneration(
		ctx,
		generated.TaskHasNonterminalRunForCurrentExecutionGenerationParams{
			CompanyID: p.CompanyID,
			TaskID:    m.Current.ID,
		},
	)
	if err != nil {
		return err
	}
	if active && m.RunID == uuid.Nil {
		return ErrConflict
	}
	// The output selection and its application decisions share the Task revision.
	hash := taskResultRequestHash(m.Input)
	if m.RunID != uuid.Nil {
		hash = taskResultRunRequestHash(m.Input, m.RunID)
	}
	selected, err := q.CreateTaskResultSet(
		ctx,
		generated.CreateTaskResultSetParams{
			CompanyID:           p.CompanyID,
			TaskID:              m.Current.ID,
			TaskRevision:        m.Current.Revision + 1,
			ExecutionGeneration: m.Current.ExecutionGeneration,
			ActorUserID:         p.UserID,
			RequestKey:          in.RequestKey,
			RequestHash:         hash,
			SourceRunID:         uuid.NullUUID{UUID: m.RunID, Valid: m.RunID != uuid.Nil},
		},
	)
	if err != nil {
		return err
	}
	for index, id := range in.OutputIDs {
		if err = q.CreateTaskResultItem(
			ctx,
			generated.CreateTaskResultItemParams{
				CompanyID:   p.CompanyID,
				ResultSetID: selected.ID,
				Ordinal:     int32(index),
				OutputID:    id,
			},
		); err != nil {
			return err
		}
	}
	suggestions, err := q.GetTaskResultOutputSuggestions(
		ctx,
		generated.GetTaskResultOutputSuggestionsParams{CompanyID: p.CompanyID, OutputIds: in.OutputIDs},
	)
	if err != nil {
		return err
	}
	decisions := slices.Clone(in.AcceptChanges)
	slices.SortFunc(decisions, func(a, b canvas.PageChangeDecision) int {
		return strings.Compare(
			a.ProjectID.String()+a.PageID.String()+a.ChangeID.String(),
			b.ProjectID.String()+b.PageID.String()+b.ChangeID.String(),
		)
	})

	for _, decision := range decisions {
		if decision.Input.Action != "apply" {
			return ErrInvalidInput
		}
		allowed := false
		for _, suggestion := range suggestions {
			if suggestion.ID == decision.ChangeID && suggestion.PageID == decision.PageID &&
				suggestion.ProjectID == decision.ProjectID {
				allowed = true
				break
			}
		}
		if !allowed {
			return ErrInvalidInput
		}
		_, pending, err := canvas.ReviewPageChangeInTx(ctx, q, p, decision)
		if errors.Is(err, canvas.ErrConflict) {
			return ErrConflict
		}
		if errors.Is(err, canvas.ErrInvalid) {
			return ErrInvalidInput
		}
		if errors.Is(err, canvas.ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		*m.Pending = append(*m.Pending, pending...)
	}
	state, err := q.GetTaskResultState(
		ctx,
		generated.GetTaskResultStateParams{CompanyID: p.CompanyID, ID: m.Current.ID},
	)
	if err != nil {
		return err
	}
	if m.Input.Status == nil {
		m.Target.Status = StatusInReview
		if !state.ResultReviewRequired {
			unapplied, err := q.TaskHasUnappliedResultSuggestions(
				ctx,
				generated.TaskHasUnappliedResultSuggestionsParams{
					CompanyID: p.CompanyID,
					ID:        m.Current.ID,
				},
			)
			if err != nil {
				return err
			}
			if !unapplied {
				m.Target.Status = StatusDone
			}
		}
	}
	if m.Target.Status == StatusDone {
		if err := requireResultApplication(ctx, q, *m.Target, p.UserID); err != nil {
			return err
		}
	}
	return nil
}

func approveTaskResultsInTx(ctx context.Context, q *generated.Queries, target Response, reviewer uuid.UUID) error {
	if err := requireResultApplication(ctx, q, target, reviewer); err != nil {
		return err
	}
	state, err := q.GetTaskResultState(ctx, generated.GetTaskResultStateParams{
		CompanyID: target.CompanyID, ID: target.ID,
	})
	if err != nil {
		return err
	}
	if state.ResultSetID == uuid.Nil || state.ApprovedAt.Valid {
		return nil
	}
	return q.ApproveTaskResultSet(ctx, generated.ApproveTaskResultSetParams{
		CompanyID: target.CompanyID, ID: state.ResultSetID,
		ApprovedByUserID: uuid.NullUUID{UUID: reviewer, Valid: true},
	})
}

func requireResultApplication(ctx context.Context, q *generated.Queries, target Response, reviewer uuid.UUID) error {
	state, err := q.GetTaskResultState(
		ctx,
		generated.GetTaskResultStateParams{CompanyID: target.CompanyID, ID: target.ID},
	)
	if err != nil {
		return err
	}
	ids := slices.Clone(state.OutputIds)
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	for _, id := range ids {
		if reviewer == uuid.Nil {
			return ErrReviewPermissionRequired
		}
		level, err := auth.OutputAccessInTx(
			ctx,
			q,
			auth.UserPrincipal{CompanyID: target.CompanyID, UserID: reviewer},
			id,
		)
		if err != nil {
			return err
		}
		if level == 0 {
			return ErrNotFound
		}
	}

	missing, err := q.TaskNeedsResultSelection(
		ctx,
		generated.TaskNeedsResultSelectionParams{CompanyID: target.CompanyID, ID: target.ID},
	)
	if err != nil {
		return err
	}
	if missing {
		return ErrResultSelectionRequired
	}
	unapplied, err := q.TaskHasUnappliedResultSuggestions(
		ctx,
		generated.TaskHasUnappliedResultSuggestionsParams{CompanyID: target.CompanyID, ID: target.ID},
	)
	if err != nil {
		return err
	}
	if unapplied {
		return ErrResultApplicationRequired
	}
	return nil
}

func taskResultRequestHash(in UpdateInput) string {
	payload, _ := json.Marshal(struct {
		Selection *ResultSelectionInput `json:"selection"`
		Status    *Status               `json:"status"`
		Revision  int64                 `json:"revision"`
		Policy    *ResultPolicy         `json:"policy"`
	}{in.ResultSelection, in.Status, in.ExpectedRevision, in.ResultPolicy})
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}

func replayTaskResult(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	id uuid.UUID,
	in UpdateInput,
) (*Response, error) {
	if in.ResultSelection == nil {
		return nil, nil
	}
	if _, err := authorizeTaskMutation(ctx, q, p); err != nil {
		return nil, err
	}
	receipt, err := q.GetTaskResultReceipt(
		ctx,
		generated.GetTaskResultReceiptParams{
			CompanyID:   p.CompanyID,
			ActorUserID: p.UserID,
			RequestKey:  in.ResultSelection.RequestKey,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if receipt.TaskID != id || receipt.RequestHash != taskResultRequestHash(in) {
		return nil, ErrConflict
	}
	current, err := taskProjectionForActiveMember(ctx, q, p, id)
	if err != nil {
		return nil, err
	}
	for _, output := range in.ResultSelection.OutputIDs {
		level, err := auth.OutputAccessInTx(ctx, q, p, output)
		if err != nil {
			return nil, err
		}
		if level == 0 {
			return nil, ErrNotFound
		}
	}
	return &current, nil
}
