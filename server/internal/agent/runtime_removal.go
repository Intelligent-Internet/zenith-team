package agent

// Runtime removal: the registrant takes one of their machines out of the
// company (ADR 0014). Run history outlives a machine (runs.runtime_id points
// at the row), so removal is a soft removal: the row is stamped removed_at
// rather than deleted, every runtime read hides it, and a re-registration
// from the same machine brings it back. The registrant's bindings on the
// machine either move to another of their runtimes of the same provider —
// their queued work follows — or are dropped, in which case that queued work
// is cancelled. Nobody else's setup lives on the machine, so nobody else is
// affected.

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// ErrRuntimeRemoveBlockedOnline refuses to remove a runtime whose daemon is
// still heartbeating: stop the daemon on that machine first, or it would
// re-register the row moments later.
var ErrRuntimeRemoveBlockedOnline = errors.New("runtime removal blocked: the runtime is online")

// RuntimeRemovalInput is the removal request after the handler bound it. A
// nil TargetRuntimeID drops the caller's bindings on the machine and cancels
// their queued work there; a target — another runtime the caller
// registered, of the same provider — takes the bindings and the queued work.
type RuntimeRemovalInput struct {
	TargetRuntimeID *uuid.UUID
}

// RuntimeRemoval is the receipt: what was removed, how many bindings moved,
// how many queued and deferred runs followed them, and how many were
// cancelled because no target was named.
type RuntimeRemoval struct {
	RemovedRuntimeID   uuid.UUID `json:"removedRuntimeId"`
	MovedBindingCount  int64     `json:"movedBindingCount"`
	RetargetedRunCount int64     `json:"retargetedRunCount"`
	CancelledRunCount  int64     `json:"cancelledRunCount"`
}

// Remove soft-removes one of the principal's runtimes. Everything is judged
// and written in ONE transaction under the runtime and binding locks: a
// non-member, a foreign or unknown runtime and another member's machine are
// all ErrRuntimeNotFound (ADR 0014: nobody else's machine is visible to the
// caller, whatever their role); the runtime must be offline
// (ErrRuntimeRemoveBlockedOnline); a target must be another live runtime the
// caller registered (ErrRuntimeNotFound) of the same provider
// (ErrBindingProviderMismatch). Then the caller's bindings on the runtime
// move to the target with their queued and deferred runs, or are deleted
// with those runs cancelled, and the row is stamped removed_at.
//
// After commit: one agent.binding_changed per moved or dropped binding, the
// agent.status_changed for the target when anything moved, the roster
// `remove` naming the machine and the row, and one runtime.work_available
// per moved run so the target's daemon claims it now rather than at its next
// poll.
func (s *DefaultRuntimeService) Remove(
	ctx context.Context,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
	input RuntimeRemovalInput,
) (RuntimeRemoval, error) {
	if input.TargetRuntimeID != nil && *input.TargetRuntimeID == runtimeID {
		// "Another runtime of yours": the row being removed is not one.
		return RuntimeRemoval{}, ErrRuntimeNotFound
	}

	var (
		receipt    RuntimeRemoval
		daemonID   string
		pending    []events.Event
		retargeted []generated.Run
		promoted   []generated.Run
	)
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		pending = pending[:0]
		retargeted = nil
		promoted = nil
		if _, err := lockMemberRole(ctx, tx, principal.CompanyID, principal.UserID, "runtime removal"); errors.Is(err, ErrNotFound) {
			return ErrRuntimeNotFound
		} else if err != nil {
			return err
		}

		ids := []uuid.UUID{runtimeID}
		if input.TargetRuntimeID != nil {
			ids = append(ids, *input.TargetRuntimeID)
		}
		locked, err := tx.LockRuntimesForRemoval(ctx, generated.LockRuntimesForRemovalParams{
			CompanyID: principal.CompanyID,
			Ids:       ids,
		})
		if err != nil {
			return fmt.Errorf("lock runtimes for removal: %w", err)
		}
		source, target, found := removalRuntimes(locked, runtimeID, input.TargetRuntimeID)
		if !found {
			return ErrRuntimeNotFound
		}
		// Another member's machine is invisible to every role, so it answers
		// like an unknown id — for the source and for the target alike.
		if !registeredBy(source.OwnerUserID, principal.UserID) {
			return ErrRuntimeNotFound
		}
		if input.TargetRuntimeID != nil && !registeredBy(target.OwnerUserID, principal.UserID) {
			return ErrRuntimeNotFound
		}
		if source.Status != "offline" {
			return ErrRuntimeRemoveBlockedOnline
		}
		if input.TargetRuntimeID != nil && target.Provider != source.Provider {
			return ErrBindingProviderMismatch
		}
		daemonID = source.DaemonID

		bindings, err := tx.ListAgentRuntimeBindingsForRuntimeForUpdate(ctx, generated.ListAgentRuntimeBindingsForRuntimeForUpdateParams{
			CompanyID: principal.CompanyID,
			RuntimeID: runtimeID,
		})
		if err != nil {
			return fmt.Errorf("lock bindings for runtime removal: %w", err)
		}
		agentIDs := make([]uuid.UUID, 0, len(bindings))
		for _, binding := range bindings {
			agentIDs = append(agentIDs, binding.AgentID)
		}
		work := execution.BindingWork{
			CompanyID:       principal.CompanyID,
			SourceRuntimeID: runtimeID,
			UserID:          principal.UserID,
			AgentIDs:        agentIDs,
		}
		if input.TargetRuntimeID != nil {
			moved, err := tx.RetargetAgentRuntimeBindings(ctx, generated.RetargetAgentRuntimeBindingsParams{
				TargetRuntimeID: target.ID,
				CompanyID:       principal.CompanyID,
				SourceRuntimeID: runtimeID,
				UserID:          principal.UserID,
			})
			if err != nil {
				return fmt.Errorf("move bindings for runtime removal: %w", err)
			}
			targetRef := runtimeRefFromRow(target)
			for _, binding := range moved {
				pending = append(pending, bindingChangeEvent(
					principal.CompanyID, principal.UserID, binding.AgentID, bindingRefFromRow(binding, targetRef),
				))
			}
			retargeted, err = execution.RetargetPendingRunsForBindingsInTx(
				ctx, tx, &pending, events.UserActor(principal.UserID.String()), work, target.ID,
			)
			if err != nil {
				return err
			}
			receipt.MovedBindingCount = int64(len(moved))
		} else {
			// The bindings go before the cancel pass, as in
			// releasePreviousRegistrantInTx: promoting the caller's next tag
			// of an Agent must find no binding and close it (agent_unbound),
			// not queue a Run on this machine that no claim can admit.
			if _, err := tx.DeleteAgentRuntimeBindingsForRuntime(ctx, generated.DeleteAgentRuntimeBindingsForRuntimeParams{
				CompanyID: principal.CompanyID,
				RuntimeID: runtimeID,
			}); err != nil {
				return fmt.Errorf("drop bindings for runtime removal: %w", err)
			}
			var cancelled []generated.Run
			cancelled, promoted, err = cancelBindingWorkInTx(
				ctx, tx, &pending, work, execution.RunReasonRuntimeRemoved,
			)
			if err != nil {
				return err
			}
			for _, binding := range bindings {
				pending = append(pending, bindingChangeEvent(principal.CompanyID, principal.UserID, binding.AgentID, nil))
			}
			receipt.CancelledRunCount = int64(len(cancelled))
		}

		if _, err := tx.MarkRuntimeRemoved(ctx, generated.MarkRuntimeRemovedParams{
			ID:        runtimeID,
			CompanyID: principal.CompanyID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrRuntimeRemoveBlockedOnline
			}
			return fmt.Errorf("mark runtime removed: %w", err)
		}
		receipt.RemovedRuntimeID = runtimeID
		receipt.RetargetedRunCount = int64(len(retargeted))
		return nil
	})
	if err != nil {
		return RuntimeRemoval{}, err
	}

	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	if receipt.MovedBindingCount != 0 {
		// The moved bindings' availability is now the target's; the batched
		// frame is what the roster and cockpit caches patch from.
		s.publishAgentStatuses(ctx, principal.CompanyID, *input.TargetRuntimeID)
	}
	s.publishRoster(ctx, protocol.DaemonRosterActionRemove, principal.CompanyID, principal.UserID, daemonID,
		[]string{runtimeID.String()})
	for _, run := range retargeted {
		execution.PublishRuntimeWork(ctx, s.bus, run)
	}
	for _, run := range promoted {
		execution.PublishRuntimeWork(ctx, s.bus, run)
	}
	return receipt, nil
}

// removalRuntimes picks the runtime being removed and, when the request
// names one, the target out of the locked rows. found is false when either
// is missing — not in the company, never registered, or already removed.
func removalRuntimes(
	locked []generated.Runtime,
	runtimeID uuid.UUID,
	targetID *uuid.UUID,
) (source, target generated.Runtime, found bool) {
	foundSource, foundTarget := false, targetID == nil
	for _, row := range locked {
		switch {
		case row.ID == runtimeID:
			source, foundSource = row, true
		case targetID != nil && row.ID == *targetID:
			target, foundTarget = row, true
		}
	}
	return source, target, foundSource && foundTarget
}
