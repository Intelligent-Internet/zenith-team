package conversation

import (
	"context"
	"time"

	"github.com/google/uuid"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type HuddleAILeaveResult struct {
	Left bool `json:"left"`
}

// Leave withdraws only this attempt's invitation. The durable stop reason makes
// retries harmless even if a new Agent has since been invited to the call.
func (s *DefaultHuddleAIService) Leave(
	ctx context.Context,
	attemptID uuid.UUID,
	token string,
) (HuddleAILeaveResult, error) {
	err := s.withAttempt(
		ctx,
		attemptID,
		func(q *generated.Queries, h generated.Huddle, state generated.HuddleAiState, a generated.HuddleAiSession) error {
			if err := checkHuddleAICapability(a, token); err != nil {
				return err
			}
			if a.StopReason == "agent_left" {
				return nil
			}
			now := time.Now()
			if state.Disabled || state.Generation != a.Generation || a.State != "claimed" ||
				!a.LeaseExpiresAt.Time.After(
					now,
				) || !a.DeadlineAt.Time.After(now) || h.State != "open" {
				return ErrHuddleUnavailable
			}
			if err := sealHuddleTranscript(ctx, q, state); err != nil {
				return err
			}
			if _, err := q.ToggleHuddleAI(ctx, generated.ToggleHuddleAIParams{
				CompanyID: h.CompanyID, HuddleID: h.ID, Disabled: true, AgentProfile: []byte(`{}`),
			}); err != nil {
				return err
			}
			if err := q.RecordHuddleAIStopReason(
				ctx,
				generated.RecordHuddleAIStopReasonParams{ID: a.ID, StopReason: "agent_left"},
			); err != nil {
				return err
			}
			return q.StopHuddleAISession(ctx, a.ID)
		},
	)
	if err == nil {
		s.signalAttempt(ctx, attemptID)
	}
	return HuddleAILeaveResult{Left: err == nil}, err
}
