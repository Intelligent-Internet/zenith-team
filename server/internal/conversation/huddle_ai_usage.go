package conversation

import (
	"context"
	"errors"
	"math"
	"time"

	"boardly.dev/boardly/server/internal/usage"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// HuddleAIUsage carries cumulative counters per provider session, never deltas.
// Final means the model socket closed and its final usage event was received.
type HuddleAIUsage struct {
	ModelSessionID    string  `json:"modelSessionId"`
	ModelSeconds      float64 `json:"modelSeconds"`
	InputTokens       int64   `json:"inputTokens"`
	CachedInputTokens int64   `json:"cachedInputTokens"`
	OutputTokens      int64   `json:"outputTokens"`
	Final             bool    `json:"final"`
	Closed            bool    `json:"closed"`
	StopReason        string  `json:"stopReason,omitempty"`
}

func (s *DefaultHuddleAIService) Finish(ctx context.Context, id uuid.UUID, token string, in HuddleAIUsage) error {
	switch in.StopReason {
	case "":
	case "provider_error", "provider_closed", "control_unavailable", "authority_revoked", "lease_expired", "reasoning_budget", "microphone_error", "worker_shutdown", "internal_error", "agent_left":
		if !in.Closed {
			return ErrHuddleInvalid
		}
	default:
		return ErrHuddleInvalid
	}
	if in.ModelSessionID == "" || len(in.ModelSessionID) > 200 || math.IsNaN(in.ModelSeconds) || math.IsInf(in.ModelSeconds, 0) || in.ModelSeconds < 0 || in.ModelSeconds > 7200 || in.InputTokens < 0 || in.InputTokens > 100000000 || in.CachedInputTokens < 0 || in.CachedInputTokens > in.InputTokens || in.OutputTokens < 0 || in.OutputTokens > 100000000 || in.Final && !in.Closed {
		return ErrHuddleInvalid
	}
	err := s.withAttempt(ctx, id, func(q *generated.Queries, h generated.Huddle, state generated.HuddleAiState, a generated.HuddleAiSession) error {
		// A revoked capability can submit accounting only; it cannot renew or read content.
		if err := checkHuddleAICapability(a, token); err != nil {
			return err
		}
		if in.StopReason != "" {
			if err := q.RecordHuddleAIStopReason(ctx, generated.RecordHuddleAIStopReasonParams{ID: id, StopReason: in.StopReason}); err != nil {
				return err
			}
		}
		receipt, err := q.GetHuddleAIUsageReceipt(ctx, generated.GetHuddleAIUsageReceiptParams{AttemptID: id, ModelSessionID: in.ModelSessionID})
		if err == nil && receipt.Final {
			if receipt.ModelSeconds != in.ModelSeconds || receipt.InputTokens != in.InputTokens || receipt.CachedInputTokens != in.CachedInputTokens || receipt.OutputTokens != in.OutputTokens || !in.Final {
				return usage.ErrHuddleBudgetConflict
			}
			return nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		_, err = q.UpsertHuddleAIUsage(ctx, generated.UpsertHuddleAIUsageParams{CompanyID: h.CompanyID, HuddleID: h.ID, AttemptID: id, ModelSessionID: in.ModelSessionID, ModelSeconds: in.ModelSeconds, InputTokens: in.InputTokens, CachedInputTokens: in.CachedInputTokens, OutputTokens: in.OutputTokens, Final: in.Final})
		if errors.Is(err, pgx.ErrNoRows) {
			return usage.ErrHuddleBudgetConflict
		}
		if err != nil {
			return err
		}
		rows, err := q.ListHuddleAIUsage(ctx, generated.ListHuddleAIUsageParams{CompanyID: h.CompanyID, HuddleID: h.ID})
		if err != nil {
			return err
		}
		var total, attemptCost int64
		var seconds float64
		final := true
		for _, r := range rows {
			cost := int64(math.Ceil(r.ModelSeconds*float64(s.options.TicksPerSecond))) + (r.InputTokens-r.CachedInputTokens)*s.options.InputTokenTicks + r.CachedInputTokens*s.options.CachedInputTokenTicks + r.OutputTokens*s.options.OutputTokenTicks
			total += cost
			if r.AttemptID == id {
				attemptCost += cost
				seconds += r.ModelSeconds
				final = final && r.Final
			}
		}
		if err = q.RecordHuddleAIAttemptUsage(ctx, generated.RecordHuddleAIAttemptUsageParams{ID: id, UsageFinal: final, ModelSeconds: seconds, SpentUsdTicks: attemptCost}); err != nil {
			return err
		}
		if in.Closed {
			if err = q.FinishHuddleAISession(ctx, id); err != nil {
				return err
			}
			if state.Generation == a.Generation && !state.Disabled {
				if err = s.setState(ctx, q, state, "unavailable", state.Generation, state.Failures+1, time.Now().Add(10*time.Second), nil); err != nil {
					return err
				}
			}
		}
		uncertain, err := q.CountUncertainHuddleAISessions(ctx, generated.CountUncertainHuddleAISessionsParams{CompanyID: h.CompanyID, HuddleID: h.ID})
		if err != nil {
			return err
		}
		settled := uncertain == 0 && (h.State == "ended" || h.State == "ending")
		return usage.RecordHuddleSpendInTx(ctx, q, usage.RecordHuddleSpendInput{CompanyID: h.CompanyID, HuddleID: h.ID, SpentUSDTicks: total, Final: settled})
	})
	if err == nil && in.Closed {
		s.signalAttempt(ctx, id)
	}
	return err
}
