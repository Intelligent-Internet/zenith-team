package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent/voice"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

var ErrHuddleToolLimit = errors.New("huddle tool rate limit reached")

// huddleContextGrantActive reports whether the person who switched Company
// context on still may: an active member who is still in the call. Every
// context read, heartbeat and reconcile asks it, so leaving the call ends
// the reads and the session.
func huddleContextGrantActive(ctx context.Context, q *generated.Queries, state generated.HuddleAiState) (bool, error) {
	return q.HuddleContextGrantActive(ctx, generated.HuddleContextGrantActiveParams{
		CompanyID: state.CompanyID, HuddleID: state.HuddleID,
		AgentID: state.AgentID.UUID, GrantedBy: state.ContextGrantedByUserID.UUID,
	})
}

// huddleChatParticipantActive reports whether one person is in the call:
// connected to the Huddle and an active member of its chat.
func huddleChatParticipantActive(
	ctx context.Context,
	q *generated.Queries,
	h generated.Huddle,
	userID uuid.UUID,
) (bool, error) {
	return q.HuddleChatParticipantActive(ctx, generated.HuddleChatParticipantActiveParams{
		CompanyID: h.CompanyID, HuddleID: h.ID, UserID: userID,
	})
}

// ReadContext never impersonates a speaker. Its grant covers only Company-shared
// projections. The injected reader participates in the same transaction as the
// lease checks, and cannot perform external work while those locks are held.
func (s *DefaultHuddleAIService) ReadContext(
	ctx context.Context,
	id uuid.UUID,
	token string,
	in voice.ContextRequest,
) (voice.ContextResult, error) {
	activity := voice.ToolActivity{
		RequestID: in.RequestID,
		Tool:      in.Tool,
		State:     "reading",
		At:        time.Now().UTC(),
		Sources:   []voice.ContextItem{},
	}
	authorize := func(q *generated.Queries, h generated.Huddle, state generated.HuddleAiState, a generated.HuddleAiSession) error {
		if err := checkHuddleAICapability(a, token); err != nil {
			return err
		}
		if err := s.authorized(ctx, q, h, state, a); err != nil {
			return err
		}
		if !state.AllowCompanyContext || s.options.ReadContext == nil {
			return ErrHuddleForbidden
		}
		return nil
	}
	err := s.withAttempt(
		ctx,
		id,
		func(q *generated.Queries, h generated.Huddle, state generated.HuddleAiState, a generated.HuddleAiSession) error {
			if err := authorize(q, h, state, a); err != nil {
				return err
			}
			count, err := q.ClaimHuddleContextRead(ctx, id)
			if err != nil {
				return err
			}
			if count == 0 {
				return ErrHuddleToolLimit
			}
			return setHuddleToolActivity(ctx, q, state, activity)
		},
	)
	if err != nil {
		return voice.ContextResult{}, err
	}
	s.signalAttempt(ctx, id)
	var result voice.ContextResult
	err = s.withAttempt(
		ctx,
		id,
		func(q *generated.Queries, h generated.Huddle, state generated.HuddleAiState, a generated.HuddleAiSession) error {
			if err := authorize(q, h, state, a); err != nil {
				return err
			}
			var err error
			// The assistant is a delegate: it reads with the access of the
			// person who switched Company context on, who must be in the call.
			// Everyone in the call hears the answer.
			in.AuthorizerUserID = state.ContextGrantedByUserID.UUID
			switch in.Tool {
			case "get_work_status", "get_work_result":
				params := huddleWorkParams(h, in.AuthorizerUserID)
				params.ProposalID, params.IncludeResult = in.ProposalID, in.Tool == "get_work_result"
				result, err = readHuddleWork(ctx, q, params)
			default:
				result, err = s.options.ReadContext(ctx, q, h.CompanyID, in)
			}
			if err != nil {
				return err
			}
			// Recheck revocation after the read, before handing facts to the model.
			if err = authorize(q, h, state, a); err != nil {
				return err
			}
			result.ReadAt = time.Now().UTC()
			activity.State, activity.At = "completed", result.ReadAt
			for _, item := range result.Items[:min(5, len(result.Items))] {
				activity.Sources = append(
					activity.Sources,
					voice.ContextItem{
						ID:        item.ID,
						Type:      item.Type,
						Title:     item.Title,
						Key:       item.Key,
						UpdatedAt: item.UpdatedAt,
					},
				)
			}
			var current voice.ToolActivity
			_ = json.Unmarshal(state.ToolActivity, &current)
			if current.RequestID != in.RequestID {
				return nil
			}
			return setHuddleToolActivity(ctx, q, state, activity)
		},
	)
	// An unfinished display expires in the UI; never detach a cleanup write from
	// a canceled request or return facts from a failed/revoked transaction.
	if err != nil {
		return voice.ContextResult{}, err
	}
	s.signalAttempt(ctx, id)
	return result, nil
}

func setHuddleToolActivity(
	ctx context.Context,
	q *generated.Queries,
	state generated.HuddleAiState,
	activity voice.ToolActivity,
) error {
	raw, err := json.Marshal(activity)
	if err != nil {
		return err
	}
	return q.SetHuddleToolActivity(
		ctx,
		generated.SetHuddleToolActivityParams{
			CompanyID: state.CompanyID,
			HuddleID:  state.HuddleID,
			Activity:  raw,
		},
	)
}
