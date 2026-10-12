package task

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

type SubmitHuddleProposalInput struct {
	ExpectedRevision     int64
	Confirm              bool
	MilestoneType        MilestoneType
	ExpectedTaskRevision int64
}

func validateProposalSubmission(in SubmitHuddleProposalInput, target uuid.NullUUID) error {
	if !in.Confirm || in.ExpectedRevision < 1 {
		return ErrInvalidInput
	}
	if target.Valid {
		if in.ExpectedTaskRevision < 1 || in.MilestoneType != "" {
			return ErrInvalidInput
		}
	} else if !ValidMilestoneType(in.MilestoneType) || in.ExpectedTaskRevision != 0 {
		return ErrInvalidInput
	}
	return nil
}

// SubmitHuddleProposal saves a Task without starting execution. The human's
// immutable confirmation receipt fences retries; speech never authorizes it.
func (s *DefaultTaskService) SubmitHuddleProposal(ctx context.Context, p auth.UserPrincipal, huddleID, proposalID uuid.UUID, in SubmitHuddleProposalInput) (conversation.HuddleProposal, error) {
	var result conversation.HuddleProposal
	var effects taskWriteEffects
	hash := sha256.Sum256([]byte(string(in.MilestoneType) + "\x00" + strconv.FormatInt(in.ExpectedTaskRevision, 10)))
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorizeTaskMutation(ctx, q, p); err != nil {
			return err
		}
		row, projection, err := conversation.LockHuddleProposalInTx(ctx, q, p, huddleID, proposalID)
		if err != nil {
			return err
		}
		if row.State == "submitted" {
			if !in.Confirm || row.SubmittedByUserID.UUID != p.UserID || row.SubmittedRevision.Int64 != in.ExpectedRevision || !bytes.Equal(row.SubmissionHash, hash[:]) {
				return conversation.ErrHuddleProposalConflict
			}
			result = projection
			return nil
		}
		if err = validateProposalSubmission(in, row.TaskID); err != nil {
			return err
		}
		if !projection.CanEdit || row.Revision != in.ExpectedRevision || !row.ProjectID.Valid || (row.TaskKey != "" && !row.TaskID.Valid) {
			return conversation.ErrHuddleProposalConflict
		}
		if row.TaskID.Valid {
			current, _, access, lockErr := lockTaskMutationTarget(ctx, q, p, row.TaskID.UUID, in.ExpectedTaskRevision, false, "proposal confirmation")
			if lockErr != nil {
				return lockErr
			}
			// Confirming work on a Task is a Task write in its Project
			// (ADR 0017); a create below checks the same permission.
			if err := requireProjectPermission(access, auth.ProjectPermissionTasksWrite); err != nil {
				return err
			}
			if current.Assignee.ID != p.UserID {
				return ErrAssigneeRequired
			}
			if current.ExecutedBy.ID != row.CreatedByAgentID {
				return ErrConflict
			}
			effects.result.Task = current
		} else {
			input, normalizeErr := normalizeCreateInput(CreateInput{ProjectID: row.ProjectID.UUID, MilestoneType: in.MilestoneType, Title: row.Title, Description: row.Brief, ExecutedBy: &ActorRef{Kind: ActorAgent, ID: row.CreatedByAgentID}}, p.UserID)
			if normalizeErr != nil {
				return normalizeErr
			}
			effects, err = s.createTaskWithExecutionInTx(ctx, q, p, input, false)
		}
		if err != nil {
			return err
		}
		result, err = conversation.RecordHuddleProposalSubmissionInTx(ctx, q, generated.SubmitHuddleProposalParams{
			CompanyID: p.CompanyID, HuddleID: huddleID, ID: proposalID, SubmittedByUserID: uuid.NullUUID{UUID: p.UserID, Valid: true}, SubmissionHash: hash[:],
			SubmittedTaskID: uuid.NullUUID{UUID: effects.result.Task.ID, Valid: true}, SubmittedTaskKey: pgtype.Text{String: effects.result.Task.Key, Valid: true},
		})
		return err
	})
	if err != nil {
		return conversation.HuddleProposal{}, fmt.Errorf("submit Huddle proposal: %w", err)
	}
	s.publishTaskWrite(ctx, effects)
	conversation.PublishHuddleStatusChanged(ctx, s.store.Queries(), s.bus, p.CompanyID, huddleID)
	return result, nil
}
