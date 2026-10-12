package agent

// Agent runtime bindings (ADR 0014). An Agent is a Company-owned template;
// a member runs it by binding it to one of their own runtimes of the
// template's provider. A binding is that member's private setup: the Runs it
// admits are the ones the member authorizes, it never lends the machine to
// anyone else, and only the member sees it — an Agent read carries the
// viewer's binding or nothing. Bind and unbind are the member's own writes,
// independent of who may configure the template (ADR 0013).

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

var (
	// ErrBindingProviderMismatch: the runtime runs another provider than the
	// Agent template. Structural — the composite foreign keys would refuse
	// the row — but named first so the refusal is a readable 422.
	ErrBindingProviderMismatch = errors.New("runtime provider does not match the Agent's")
	// ErrBindingAgentArchived: an archived Agent cannot be set up; restore it
	// first.
	ErrBindingAgentArchived = errors.New("archived Agent cannot be bound")
)

// BindingService owns a member's bindings of Agent templates to their own
// runtimes: setting one up, moving it, removing it and listing them.
type BindingService interface {
	Bind(context.Context, auth.UserPrincipal, uuid.UUID, BindInput) (Response, error)
	Unbind(context.Context, auth.UserPrincipal, uuid.UUID) error
	ListForViewer(context.Context, auth.UserPrincipal, BindingListOptions) ([]Binding, error)
}

var _ BindingService = (*DefaultBindingService)(nil)

type DefaultBindingService struct {
	store *db.Store
	bus   *events.Bus
}

type BindingServiceOptions struct {
	Store *db.Store
	Bus   *events.Bus
}

func NewBindingService(options BindingServiceOptions) (*DefaultBindingService, error) {
	if options.Store == nil {
		return nil, errors.New("Agent binding database store is required")
	}
	if options.Bus == nil {
		return nil, errors.New("Agent binding event bus is required")
	}
	return &DefaultBindingService{store: options.Store, bus: options.Bus}, nil
}

// BindInput names the caller's runtime the Agent should run on.
type BindInput struct {
	RuntimeID uuid.UUID
}

// BindingListOptions narrows the caller's bindings to one of their runtimes.
type BindingListOptions struct {
	RuntimeID *uuid.UUID
}

// Binding is one of the caller's bindings as GET /agent-bindings lists it.
type Binding struct {
	ID        uuid.UUID  `json:"id"`
	AgentID   uuid.UUID  `json:"agentId"`
	Runtime   RuntimeRef `json:"runtime"`
	CreatedAt time.Time  `json:"createdAt"`
}

// Bind sets the caller's binding of the Agent to one of their runtimes, or
// moves it there. Everything is judged and written in ONE transaction: the
// caller's membership is locked; the Agent must exist in the Company and
// not be archived (ErrNotFound, ErrBindingAgentArchived); the runtime must
// be one the caller registered and not removed — another member's machine
// is ErrRuntimeNotFound, exactly like an unknown id — of the Agent's
// provider (ErrBindingProviderMismatch); it may be offline, since a binding
// is setup, not a start. A binding moving to another runtime takes the
// caller's queued and deferred Runs on the Agent with it; Runs a daemon
// already claimed finish where they are. Naming the runtime already bound
// changes nothing. After commit the caller alone receives
// agent.binding_changed, and the moved Runs wake the target's daemon.
func (s *DefaultBindingService) Bind(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	input BindInput,
) (Response, error) {
	if !util.ValidUUID(agentID) {
		return Response{}, ErrNotFound
	}
	if !util.ValidUUID(input.RuntimeID) {
		return Response{}, ErrRuntimeNotFound
	}
	var (
		result     Response
		pending    []events.Event
		retargeted []generated.Run
	)
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		pending = pending[:0]
		retargeted = nil
		if _, err := lockMemberRole(ctx, tx, principal.CompanyID, principal.UserID, "binding"); err != nil {
			return err
		}
		agent, err := tx.GetAgentForCompany(ctx, generated.GetAgentForCompanyParams{
			AgentID: agentID, CompanyID: principal.CompanyID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// GetAgentForCompany hides archived rows; tell the two apart so the
			// client can offer a restore rather than a missing Agent.
			if _, archivedErr := projectionForMember(ctx, tx, principal, agentID); archivedErr == nil {
				return ErrBindingAgentArchived
			}
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load Agent for binding: %w", err)
		}
		runtime, err := tx.GetRuntimeForUpdate(ctx, generated.GetRuntimeForUpdateParams{
			CompanyID: principal.CompanyID, ID: input.RuntimeID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRuntimeNotFound
		}
		if err != nil {
			return fmt.Errorf("lock runtime for binding: %w", err)
		}
		if !registeredBy(runtime.OwnerUserID, principal.UserID) {
			return ErrRuntimeNotFound
		}
		if runtime.Provider != agent.Provider {
			return ErrBindingProviderMismatch
		}
		current, err := tx.GetAgentRuntimeBindingForUserForUpdate(ctx, generated.GetAgentRuntimeBindingForUserForUpdateParams{
			CompanyID: principal.CompanyID, AgentID: agentID, UserID: principal.UserID,
		})
		hadBinding := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("lock current binding: %w", err)
		}
		if hadBinding && current.RuntimeID == runtime.ID {
			result, err = projectionForMember(ctx, tx, principal, agentID)
			return err
		}
		binding, err := tx.UpsertAgentRuntimeBinding(ctx, generated.UpsertAgentRuntimeBindingParams{
			UserID: principal.UserID, RuntimeID: runtime.ID,
			AgentID: agentID, CompanyID: principal.CompanyID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// The write restates every invariant checked above; a miss means
			// one of them stopped holding under the locks — the Agent was
			// archived or the membership revoked in between.
			return ErrBindingAgentArchived
		}
		if err != nil {
			return fmt.Errorf("write Agent binding: %w", err)
		}
		if hadBinding {
			retargeted, err = execution.RetargetPendingRunsForBindingsInTx(
				ctx, tx, &pending, events.UserActor(principal.UserID.String()),
				execution.BindingWork{
					CompanyID:       principal.CompanyID,
					SourceRuntimeID: current.RuntimeID,
					UserID:          principal.UserID,
					AgentIDs:        []uuid.UUID{agentID},
				},
				runtime.ID,
			)
			if err != nil {
				return err
			}
		}
		pending = append(pending, bindingChangeEvent(
			principal.CompanyID, principal.UserID, agentID, bindingRefFromRow(binding, runtimeRefFromRow(runtime)),
		))
		result, err = projectionForMember(ctx, tx, principal, agentID)
		return err
	})
	if err != nil {
		return Response{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	for _, run := range retargeted {
		execution.PublishRuntimeWork(ctx, s.bus, run)
	}
	return result, nil
}

// cancelBindingWorkInTx cancels a member's queued work on some Agents of one
// of their runtimes, then has the Conversation domain close the channel and
// group-DM tags those Runs served, so the next tag of each Agent there can
// start. It returns the cancelled Runs and the Runs that promotion enqueued;
// the caller wakes the latter's runtimes after commit. The caller deletes the
// bindings first: a next tag from the same member then finds no binding and
// is closed with agent_unbound instead of queuing on the old runtime.
func cancelBindingWorkInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	work execution.BindingWork,
	reason string,
) ([]generated.Run, []generated.Run, error) {
	cancelled, err := execution.CancelPendingRunsForBindingsInTx(ctx, q, pending, work, reason)
	if err != nil {
		return nil, nil, err
	}
	promotedIDs, err := conversation.CloseCancelledChannelTagsInTx(ctx, q, pending, cancelled)
	if err != nil {
		return nil, nil, fmt.Errorf("close cancelled channel tags: %w", err)
	}
	promoted := make([]generated.Run, 0, len(promotedIDs))
	for _, id := range promotedIDs {
		run, err := q.GetRun(ctx, id)
		if err != nil {
			return nil, nil, fmt.Errorf("load promoted channel tag Run: %w", err)
		}
		promoted = append(promoted, run)
	}
	return cancelled, promoted, nil
}

// Unbind removes the caller's binding of the Agent. Their queued and
// deferred Runs on it are cancelled (reason agent_unbound); Runs a daemon
// already claimed finish on the caller's own machine. Idempotent: no binding
// is a no-op. After commit the caller alone receives agent.binding_changed
// with no binding.
func (s *DefaultBindingService) Unbind(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
) error {
	if !util.ValidUUID(agentID) {
		return ErrNotFound
	}
	var (
		pending  []events.Event
		promoted []generated.Run
	)
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		pending = pending[:0]
		promoted = nil
		if _, err := lockMemberRole(ctx, tx, principal.CompanyID, principal.UserID, "binding"); err != nil {
			return err
		}
		if _, err := projectionForMember(ctx, tx, principal, agentID); err != nil {
			return err
		}
		current, err := tx.GetAgentRuntimeBindingForUserForUpdate(ctx, generated.GetAgentRuntimeBindingForUserForUpdateParams{
			CompanyID: principal.CompanyID, AgentID: agentID, UserID: principal.UserID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock current binding: %w", err)
		}
		// The binding goes before the cancel pass: promoting the caller's
		// next tag of the Agent must find no binding and close it
		// (agent_unbound), not queue a Run the claim can never admit.
		if _, err := tx.DeleteAgentRuntimeBinding(ctx, generated.DeleteAgentRuntimeBindingParams{
			CompanyID: principal.CompanyID, AgentID: agentID, UserID: principal.UserID,
		}); err != nil {
			return fmt.Errorf("delete Agent binding: %w", err)
		}
		_, promoted, err = cancelBindingWorkInTx(ctx, tx, &pending, execution.BindingWork{
			CompanyID:       principal.CompanyID,
			SourceRuntimeID: current.RuntimeID,
			UserID:          principal.UserID,
			AgentIDs:        []uuid.UUID{agentID},
		}, execution.RunReasonAgentUnbound)
		if err != nil {
			return err
		}
		pending = append(pending, bindingChangeEvent(principal.CompanyID, principal.UserID, agentID, nil))
		return nil
	})
	if err != nil {
		return err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	for _, run := range promoted {
		execution.PublishRuntimeWork(ctx, s.bus, run)
	}
	return nil
}

// ListForViewer lists the caller's own bindings in the Company, oldest
// first, optionally narrowed to one of their runtimes. Always the caller's:
// nobody else's setup is listed, whatever the caller's role.
func (s *DefaultBindingService) ListForViewer(
	ctx context.Context,
	principal auth.UserPrincipal,
	options BindingListOptions,
) ([]Binding, error) {
	if err := authorizeMemberRead(ctx, s.store.Queries(), principal); err != nil {
		return nil, err
	}
	rows, err := s.store.Queries().ListAgentRuntimeBindingsForUser(ctx, generated.ListAgentRuntimeBindingsForUserParams{
		CompanyID: principal.CompanyID,
		UserID:    principal.UserID,
		RuntimeID: util.NullUUIDFromPtr(options.RuntimeID),
	})
	if err != nil {
		return nil, fmt.Errorf("list Agent bindings: %w", err)
	}
	bindings := make([]Binding, 0, len(rows))
	for _, row := range rows {
		bindings = append(bindings, Binding{
			ID:      row.AgentRuntimeBinding.ID,
			AgentID: row.AgentRuntimeBinding.AgentID,
			Runtime: RuntimeRef{
				ID:             row.AgentRuntimeBinding.RuntimeID,
				Name:           row.RuntimeName,
				CustomName:     util.TextToPtr(row.RuntimeCustomName),
				Status:         row.RuntimeStatus,
				Provider:       row.AgentRuntimeBinding.Provider,
				SignIn:         runtimeSignInRef(row.RuntimeSignInState, row.RuntimeSignInDetail, row.RuntimeSignInCheckedAt),
				UnusableReason: runtimeUnusableReasonRef(row.RuntimeUnusableReasonCode, row.RuntimeUnusableReasonMessage),
			},
			CreatedAt: row.AgentRuntimeBinding.CreatedAt.Time.UTC(),
		})
	}
	return bindings, nil
}

// runtimeRefFromRow projects a stored runtimes row into the reference a
// binding carries.
func runtimeRefFromRow(row generated.Runtime) RuntimeRef {
	return RuntimeRef{
		ID:             row.ID,
		Name:           row.Name,
		CustomName:     util.TextToPtr(row.CustomName),
		Status:         row.Status,
		Provider:       row.Provider,
		SignIn:         runtimeSignInRef(row.ProviderSignInState, row.ProviderSignInDetail, row.ProviderSignInCheckedAt),
		UnusableReason: runtimeUnusableReasonRef(row.UnusableReasonCode, row.UnusableReasonMessage),
	}
}

// bindingRefFromRow pairs a stored binding row with the runtime reference it
// names.
func bindingRefFromRow(row generated.AgentRuntimeBinding, runtime RuntimeRef) *BindingRef {
	return &BindingRef{ID: row.ID, AgentID: row.AgentID, Runtime: runtime, CreatedAt: row.CreatedAt.Time.UTC()}
}
