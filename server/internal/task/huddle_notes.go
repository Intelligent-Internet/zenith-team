package task

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type SubmitHuddleNotesInput struct {
	HuddleID         uuid.UUID     `json:"huddleId"`
	ActionIndex      int32         `json:"actionIndex"`
	ExpectedRevision int64         `json:"expectedRevision"`
	ProjectID        uuid.UUID     `json:"projectId"`
	AgentID          uuid.UUID     `json:"agentId"`
	Title            string        `json:"title"`
	MilestoneType    MilestoneType `json:"milestoneType"`
	Confirm          bool          `json:"confirm"`
}
type HuddleNotesSubmission struct {
	TaskID uuid.UUID `json:"taskId"`
}

func (s *DefaultTaskService) SubmitHuddleNotes(
	ctx context.Context,
	p auth.UserPrincipal,
	in SubmitHuddleNotesInput,
) (HuddleNotesSubmission, error) {
	var result HuddleNotesSubmission
	if !in.Confirm || in.ExpectedRevision < 1 || in.ProjectID == uuid.Nil || !ValidMilestoneType(in.MilestoneType) {
		return result, ErrInvalidInput
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		return result, err
	}
	hash := sha256.Sum256(encoded)
	var effects taskWriteEffects
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorizeTaskMutation(ctx, q, p); err != nil {
			return err
		}
		n, action, err := conversation.LockHuddleNotesActionInTx(
			ctx,
			q,
			p,
			conversation.HuddleNotesActionInput{
				HuddleID:         in.HuddleID,
				ActionIndex:      in.ActionIndex,
				ExpectedRevision: in.ExpectedRevision,
			},
		)
		if err != nil {
			return err
		}
		prior, err := q.GetHuddleNotesAction(
			ctx,
			generated.GetHuddleNotesActionParams{
				CompanyID:   p.CompanyID,
				NotesID:     n.ID,
				ActionIndex: in.ActionIndex,
			},
		)
		if err == nil {
			if prior.SubmittedByUserID != p.UserID || !bytes.Equal(prior.SubmissionHash, hash[:]) ||
				!prior.TaskID.Valid {
				return conversation.ErrHuddleNotesConflict
			}
			if _, err = auth.RequireTaskPermission(
				ctx,
				q,
				p,
				prior.TaskID.UUID,
				auth.ProjectPermissionTasksWrite,
			); err != nil {
				return err
			}
			result.TaskID = prior.TaskID.UUID
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if n.RecapRevision != in.ExpectedRevision {
			return conversation.ErrHuddleNotesConflict
		}
		input := CreateInput{
			ProjectID:     in.ProjectID,
			MilestoneType: in.MilestoneType,
			Title:         in.Title,
			Description:   action.Text,
		}
		if in.AgentID != uuid.Nil {
			input.ExecutedBy = &ActorRef{Kind: ActorAgent, ID: in.AgentID}
		}
		input, err = normalizeCreateInput(input, p.UserID)
		if err != nil {
			return err
		}
		effects, err = s.createTaskWithExecutionInTx(ctx, q, p, input, false)
		if err != nil {
			return err
		}
		result.TaskID = effects.result.Task.ID
		_, err = q.InsertHuddleNotesAction(
			ctx,
			generated.InsertHuddleNotesActionParams{
				CompanyID:         p.CompanyID,
				NotesID:           n.ID,
				ActionIndex:       in.ActionIndex,
				RecapRevision:     in.ExpectedRevision,
				TaskID:            uuid.NullUUID{UUID: result.TaskID, Valid: true},
				SubmittedByUserID: p.UserID,
				SubmissionHash:    hash[:],
			},
		)
		return err
	})
	if err != nil {
		return HuddleNotesSubmission{}, err
	}
	s.publishTaskWrite(ctx, effects)
	conversation.PublishHuddleStatusChanged(ctx, s.store.Queries(), s.bus, p.CompanyID, in.HuddleID)
	return result, nil
}
