// Package account owns self-service account soft deletion and trusted restore.
package account

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/approval"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

const deletionReason = "account_deleted"

type ServiceOptions struct {
	Store               *db.Store
	Bus                 *events.Bus
	SessionInvalidator  auth.SessionInvalidator
	TaskMessageRuns     conversation.TaskMessageRunPreparer
	TaskTerminalEffects execution.TaskTerminalEffectWriter
}

// AccountService owns self-service deletion of the caller's own account.
type AccountService interface { //nolint:revive // AGENTS.md mandates <Domain>Service; bare Service is forbidden.
	Delete(context.Context, auth.UserPrincipal, string) (DeleteResult, error)
}

var _ AccountService = (*DefaultAccountService)(nil)

type DefaultAccountService struct {
	store               *db.Store
	bus                 *events.Bus
	sessionInvalidator  auth.SessionInvalidator
	taskMessageRuns     conversation.TaskMessageRunPreparer
	taskTerminalEffects execution.TaskTerminalEffectWriter
}

func NewAccountService(options ServiceOptions) (*DefaultAccountService, error) {
	if options.Store == nil || options.Store.Queries() == nil {
		return nil, errors.New("account database store is required")
	}
	if options.Bus == nil {
		return nil, errors.New("account event bus is required")
	}
	if isNilDependency(options.SessionInvalidator) {
		return nil, errors.New("account session invalidator is required")
	}
	if options.TaskMessageRuns == nil {
		return nil, errors.New("account Task Message Run preparer is required")
	}
	if options.TaskTerminalEffects == nil {
		return nil, errors.New("account Task terminal effect writer is required")
	}
	return &DefaultAccountService{
		store:               options.Store,
		bus:                 options.Bus,
		sessionInvalidator:  options.SessionInvalidator,
		taskMessageRuns:     options.TaskMessageRuns,
		taskTerminalEffects: options.TaskTerminalEffects,
	}, nil
}

func (s *DefaultAccountService) Delete(
	ctx context.Context,
	principal auth.UserPrincipal,
	confirmation string,
) (DeleteResult, error) {
	if s == nil || s.store == nil {
		return DeleteResult{}, errors.New("account service is not initialized")
	}
	if confirmation != Confirmation {
		return DeleteResult{}, ErrInvalidConfirmation
	}

	var result DeleteResult
	var revocations []auth.SessionRevocation
	var pending []events.Event
	var cancelledRuns []generated.Run
	var promotedRuns []generated.Run
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		pending = pending[:0]
		cancelledRuns = cancelledRuns[:0]
		promotedRuns = promotedRuns[:0]
		authority, err := lockDeletionAuthority(ctx, tx, principal.SessionID, principal.UserID)
		if err != nil {
			return err
		}
		// Lock affected Companies in id order before any Company-owned row.
		companyIDs, err := tx.LockAccountCompaniesForDeletion(ctx, principal.UserID)
		if err != nil {
			return fmt.Errorf("lock account Companies: %w", err)
		}
		// Every company mutation takes its Company lock before the acting
		// membership row. The rows above are therefore the fence: a mutation that
		// entered with a now-stale principal waits here, then sees this revocation
		// when its own transaction rechecks membership.
		if err := company.RevokeAccountMembershipsInTx(
			ctx,
			tx,
			&pending,
			principal.UserID,
			companyIDs,
		); err != nil {
			return fmt.Errorf("revoke account Company memberships: %w", err)
		}
		// Cancel every active Run the disabled account triggered, including
		// retry descendants — which hold no 'user_message' item of their own
		// and inherit the claim through parent_run_id — in the same
		// commit that revokes its sessions: the cancellation fences pending
		// claims (a cancelled run is unclaimable) and the token revocation
		// closes the brt_ credentials of in-flight runs. Daemon authority dies
		// with the revoked sessions below — the daemon's bearer token stops
		// authenticating the moment this transaction commits.
		for _, companyID := range companyIDs {
			// The account's Agent bindings go with it (ADR 0014): bindings are
			// personal, so no other member's Agent availability changes, and
			// the frames this stages would reach only the deleted account's own
			// sockets, which die with its sessions below. A company where the
			// account bound nothing stages nothing.
			if err := agent.RevokeRuntimeOwnerInTx(
				ctx,
				tx,
				&pending,
				companyID,
				principal.UserID,
			); err != nil {
				return fmt.Errorf("revoke runtime owner bindings: %w", err)
			}
			// The lifecycle worker takes its Company's share lock before it
			// claims a receipt, then locks the Conversation and materializes a
			// continuation Run. So the two queue at the Company lock above, and
			// after it both take receipts before Conversations. If the worker
			// wins, this waits there for its delivery commit and the candidate
			// read below sees the new Run under a fresh READ COMMITTED snapshot.
			if err := approval.AbandonAccountContinuationsInTx(
				ctx,
				tx,
				&pending,
				companyID,
				principal.UserID,
				deletionReason,
			); err != nil {
				return fmt.Errorf("abandon account human-input continuations: %w", err)
			}
			authoredRunConversationIDs, err := tx.ListActiveRunConversationIDsForCompanyUser(
				ctx,
				generated.ListActiveRunConversationIDsForCompanyUserParams{
					CompanyID: companyID,
					AuthorID:  uuid.NullUUID{UUID: principal.UserID, Valid: true},
				},
			)
			if err != nil {
				return fmt.Errorf("list account Run Conversations: %w", err)
			}
			lockedConversationIDs, err := conversation.LockAuthorizationRevocationRunQueuesInTx(
				ctx,
				tx,
				companyID,
				principal.UserID,
				authoredRunConversationIDs,
			)
			if err != nil {
				return fmt.Errorf("lock account Run queue Conversations: %w", err)
			}
			historyRunIDs, err := tx.ListActiveRunIDsInConversationsForUpdate(ctx, generated.ListActiveRunIDsInConversationsForUpdateParams{
				CompanyID: companyID, ConversationIds: lockedConversationIDs,
			})
			if err != nil {
				return err
			}
			beforeHistory, err := execution.ObserveRunsInTx(ctx, tx, historyRunIDs)
			if err != nil {
				return err
			}
			cancelled, err := tx.CancelRunsForCompanyUser(ctx, generated.CancelRunsForCompanyUserParams{
				CompanyID: companyID,
				AuthorID:  uuid.NullUUID{UUID: principal.UserID, Valid: true},
				Reason:    deletionReason,
			})
			if err != nil {
				return fmt.Errorf("cancel account Runs: %w", err)
			}
			cancelledForCompany := make([]generated.Run, 0, len(cancelled))
			for _, row := range cancelled {
				run := generated.Run(row)
				if err := activity.RecordInTx(ctx, tx, &pending, events.UserActor(principal.UserID.String()), execution.EventRunCancelled, beforeHistory[run.ID], execution.Topic, run.ID); err != nil {
					return err
				}
				cancelledForCompany = append(cancelledForCompany, run)
				if err := tx.DeleteRunTokensByRun(ctx, run.ID); err != nil {
					return fmt.Errorf("revoke account Run tokens: %w", err)
				}
				view, err := execution.LoadRunSnapshotInTx(ctx, tx, run)
				if err != nil {
					return fmt.Errorf("load cancelled account Run projection: %w", err)
				}
				// The projection carries the facts that decide who may
				// see the run, so there is no audience to resolve here.
				taskID := uuid.Nil
				if view.TaskID != nil {
					taskID = *view.TaskID
				}
				pending = append(pending, events.Event{
					EntityType:     execution.Topic,
					Name:           execution.EventRunCancelled,
					CompanyID:      view.CompanyID,
					CreatedAt:      time.Now().UTC(),
					RunID:          view.ID,
					TaskID:         taskID,
					ConversationID: view.ConversationID,
					Actor:          events.SystemActor(),
					Payload:        view,
				})
			}
			cancelledRuns = append(cancelledRuns, cancelledForCompany...)
			promoted, err := conversation.FinalizeAuthorizationRevocationRunQueuesInTx(
				ctx,
				tx,
				&pending,
				s.taskMessageRuns,
				companyID,
				principal.UserID,
				lockedConversationIDs,
			)
			if err != nil {
				return fmt.Errorf("finalize account Conversation Run queues: %w", err)
			}
			promotedRuns = append(promotedRuns, promoted...)
			// Promotion runs first: a materialized successor advances the Task
			// execution generation, so the cancelled predecessor's Task effect
			// becomes an atomic no-op. With no successor, the current cancelled
			// Run moves its Task to cancelled through the same Task-owned seam as
			// the ordinary Run lifecycle.
			for _, run := range cancelledForCompany {
				if err := s.taskTerminalEffects.ApplyRunTerminalInTx(
					ctx,
					tx,
					&pending,
					run,
				); err != nil {
					return fmt.Errorf("apply account-cancelled Run Task effect: %w", err)
				}
			}
		}

		deletedAt, err := markDeleted(ctx, tx, principal.UserID, authority.Version)
		if err != nil {
			return err
		}
		result.DeletedAt = deletedAt

		rows, err := revokeAllLiveSessions(ctx, tx, principal.UserID)
		if err != nil {
			return err
		}
		for _, revocation := range rows {
			pending = append(pending,
				auth.NewUserDisabledSessionRevokedEvent(revocation, principal.UserID))
		}
		revocations = rows
		if len(revocations) == 0 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return DeleteResult{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	for _, run := range promotedRuns {
		execution.PublishRuntimeWork(ctx, s.bus, run)
	}
	for _, run := range cancelledRuns {
		execution.PublishRuntimeWork(ctx, s.bus, run)
	}
	s.sessionInvalidator.InvalidateSessions(
		context.WithoutCancel(ctx), append([]auth.SessionRevocation(nil), revocations...),
	)
	// The revoked sessions stop authenticating immediately; the wakeup makes
	// the user's daemons notice now rather than on their periodic sync.
	s.bus.Publish(ctx, company.Topic, events.Event{
		EntityType: company.Topic,
		Name:       company.EventCompanyMembershipChanged,
		CreatedAt:  time.Now().UTC(),
		Actor:      events.SystemActor(),
		Payload:    company.Membership{UserID: principal.UserID.String()},
	})
	return result, nil
}

// lockDeletionAuthority pins the caller's own session and user row for the rest of
// the deletion transaction, refusing anything but a live, self-owned account.
func lockDeletionAuthority(
	ctx context.Context,
	q *generated.Queries,
	sessionID, userID uuid.UUID,
) (DeletionAuthority, error) {
	row, err := q.LockAccountDeletionAuthority(ctx, generated.LockAccountDeletionAuthorityParams{
		AuthSessionID: sessionID, UserID: userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return DeletionAuthority{}, ErrUnauthenticated
	}
	if err != nil {
		return DeletionAuthority{}, fmt.Errorf("lock account deletion authority: %w", err)
	}
	return DeletionAuthority{UserID: userID, Version: row.Version}, nil
}

// markDeleted is the optimistic-concurrency step: the row must move to exactly
// expectedVersion+1 with deleted_at and disabled_at stamped from the same clock.
func markDeleted(
	ctx context.Context,
	q *generated.Queries,
	userID uuid.UUID,
	expectedVersion int64,
) (time.Time, error) {
	row, err := q.MarkAccountDeleted(ctx, generated.MarkAccountDeletedParams{
		UserID: userID, ExpectedVersion: expectedVersion,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrConflict
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("mark account deleted: %w", err)
	}
	return row.DeletedAt.Time.UTC(), nil
}

func revokeAllLiveSessions(
	ctx context.Context,
	q *generated.Queries,
	userID uuid.UUID,
) ([]auth.SessionRevocation, error) {
	rows, err := q.RevokeAllLiveAuthSessionsForAccountDeletion(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("revoke account auth sessions: %w", err)
	}
	revocations := make([]auth.SessionRevocation, 0, len(rows))
	for _, row := range rows {
		revocations = append(revocations, auth.SessionRevocation{
			SessionID: row.ID, UserID: row.UserID,
			Reason: auth.SessionRevocationUserDisabled, EffectiveAt: row.RevokedAt.Time.UTC(),
		})
	}
	return revocations, nil
}

func isNilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
