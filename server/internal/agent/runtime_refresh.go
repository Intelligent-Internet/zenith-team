package agent

import (
	"context"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/pkg/protocol"
)

// RequestRefresh asks the daemon behind runtimeID to probe its provider
// setup again — CLI path, version, sign-in — and register what it finds.
// The answer is not awaited: it arrives as the ordinary register upsert,
// which already publishes daemon.roster_changed for every roster to refetch.
// It makes a machine do work, so the gate is the machine's, not a Company
// role's: only its registrant may ask (ADR 0014), and another member's
// machine never reaches this point — the read answers ErrRuntimeNotFound. Refused for an offline runtime because no daemon would hear
// the hint. The hint rides the runtime-scoped pending-work channel with its
// own kind, so one click reaches the API node that holds the daemon's socket
// without a second relay.
func (s *DefaultRuntimeService) RequestRefresh(
	ctx context.Context,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
) error {
	runtime, err := s.runtimeForCompany(ctx, principal, runtimeID)
	if err != nil {
		return err
	}
	if runtime.Status != "online" {
		return ErrRuntimeOffline
	}
	if s.bus != nil {
		s.bus.Publish(context.Background(), TopicRuntime, events.Event{
			EntityType: TopicRuntime,
			Name:       EventRuntimePendingWork,
			CompanyID:  runtime.CompanyID,
			CreatedAt:  time.Now().UTC(),
			Actor:      events.UserActor(principal.UserID.String()),
			Payload: PendingWork{
				RuntimeID: runtime.ID.String(),
				Kind:      protocol.PendingWorkKindRuntimeRefresh,
			},
		})
	}
	return nil
}
