package company

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/task"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func (s *DefaultCompanyService) ListMembers(
	ctx context.Context,
	principal auth.UserPrincipal,
) ([]ActiveMemberResponse, error) {
	current, err := s.Authorize(ctx, principal, principal.CompanyID)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.Queries().ListActiveCompanyMembers(ctx,
		generated.ListActiveCompanyMembersParams{
			CompanyID:   current.CompanyID,
			ActorUserID: current.UserID,
		})
	if err != nil {
		return nil, fmt.Errorf("list active Company members: %w", err)
	}
	members := make([]ActiveMemberResponse, 0, len(rows))
	for _, row := range rows {
		members = append(members, activeMemberResponse(
			memberResponse(row.CompanyMember),
			row.MemberEmail,
			row.MemberDisplayName,
			util.TextToPtr(row.MemberAvatarUrl),
			row.AccountDisabled,
		))
	}
	return members, nil
}

// stageMembershipEvents stages the Company-scoped member.* event and the
// personal auth.* counterpart for the affected user. One fact produces both,
// and they leave in that order: the company frame before its personal twin.
func stageMembershipEvents(
	pending *[]events.Event,
	actorUserID uuid.UUID,
	member MemberResponse,
	active ActiveMemberResponse,
	outcome MembershipOutcome,
) error {
	return stageMembershipEventsAs(
		pending,
		events.UserActor(actorUserID.String()),
		member,
		active,
		outcome,
	)
}

// stageMembershipEventsAs is the actor-neutral core used when the platform,
// rather than an ordinary membership actor, removes access during account
// deletion.
func stageMembershipEventsAs(
	pending *[]events.Event,
	actor events.Actor,
	member MemberResponse,
	active ActiveMemberResponse,
	outcome MembershipOutcome,
) error {
	if outcome == MembershipUnchanged {
		return nil
	}
	stamp := time.Now().UTC()
	fact := func(entityType, name string, payload any) events.Event {
		return events.Event{
			EntityType: entityType,
			Name:       name,
			CompanyID:  member.CompanyID,
			CreatedAt:  stamp,
			Actor:      actor,
			Payload:    payload,
		}
	}
	accessChanged := func(reason AccessReason) events.Event {
		return fact(TopicAuth, EventAuthCompanyAccessChanged, AccessChangedReceipt{
			CompanyID:   member.CompanyID,
			Role:        member.Role,
			Permissions: auth.PermissionsOf(member.Role),
			Reason:      reason,
			UserID:      member.UserID,
		})
	}
	accessRevoked := func(reason AccessReason) events.Event {
		return fact(TopicAuth, EventAuthCompanyAccessRevoked, AccessRevokedReceipt{
			CompanyID: member.CompanyID,
			Reason:    reason,
			UserID:    member.UserID,
		})
	}
	memberRemoved := func() events.Event {
		return fact(TopicMember, EventMemberRemoved, MemberRemovalReceipt{
			UserID:    member.UserID,
			Revision:  member.Revision,
			CompanyID: member.CompanyID,
		})
	}
	memberActive := func(name string) func() events.Event {
		return func() events.Event { return fact(TopicMember, name, active) }
	}

	var companyEvent, accessEvent func() events.Event
	switch outcome {
	case MembershipGranted:
		companyEvent = memberActive(EventMemberAdded)
		accessEvent = func() events.Event { return accessChanged(AccessReasonMembershipGranted) }
	case MembershipReactivated:
		companyEvent = memberActive(EventMemberAdded)
		accessEvent = func() events.Event { return accessChanged(AccessReasonMembershipReactivated) }
	case MembershipRoleChanged:
		companyEvent = memberActive(EventMemberUpdated)
		accessEvent = func() events.Event { return accessChanged(AccessReasonRoleChanged) }
	case MembershipRemoved:
		companyEvent = memberRemoved
		accessEvent = func() events.Event { return accessRevoked(AccessReasonMembershipRemoved) }
	case MembershipLeft:
		companyEvent = memberRemoved
		accessEvent = func() events.Event { return accessRevoked(AccessReasonMembershipLeft) }
	case MembershipCompanyDeleted:
		companyEvent = memberRemoved
		accessEvent = func() events.Event { return accessRevoked(AccessReasonCompanyDeleted) }
	default:
		return fmt.Errorf("unsupported membership outcome %q", outcome)
	}

	*pending = append(*pending, companyEvent(), accessEvent())
	return nil
}

// authorizeMembershipActor locks the acting member row and rechecks the
// permission beside the lock it guards, so a concurrent role change or
// revocation serializes behind this transaction instead of racing it. An
// empty permission admits any active member.
func authorizeMembershipActor(
	ctx context.Context,
	q *generated.Queries,
	companyID, actorUserID uuid.UUID,
	required auth.Permission,
) (auth.CompanyRole, error) {
	actor, err := q.GetCompanyMemberForUpdate(ctx, generated.GetCompanyMemberForUpdateParams{
		CompanyID: companyID,
		UserID:    actorUserID,
	})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && actor.Status != string(MembershipStatusActive)) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("lock acting Company member: %w", err)
	}
	role := auth.CompanyRole(actor.Role)
	if required != "" && !role.Can(required) {
		return "", ErrForbidden
	}
	return role, nil
}

var ErrMemberRevisionConflict = errors.New("Company membership revision changed")

type MembershipMutation struct {
	UserID           uuid.UUID
	ExpectedRevision int64
}

func (s *DefaultCompanyService) RemoveMember(
	ctx context.Context,
	principal auth.UserPrincipal,
	input MembershipMutation,
) error {
	return s.revokeMember(ctx, principal, input, false)
}

func (s *DefaultCompanyService) Leave(ctx context.Context, principal auth.UserPrincipal, expectedRevision int64) error {
	return s.revokeMember(
		ctx,
		principal,
		MembershipMutation{UserID: principal.UserID, ExpectedRevision: expectedRevision},
		true,
	)
}

func (s *DefaultCompanyService) revokeMember(
	ctx context.Context,
	principal auth.UserPrincipal,
	input MembershipMutation,
	leave bool,
) error {
	if !util.ValidUUID(input.UserID) || input.ExpectedRevision < 1 {
		return ErrInvalidInput
	}
	if !leave && input.UserID == principal.UserID {
		return ErrInvalidInput
	}
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = nil
		if _, err := q.LockCompanyForMembershipMutation(
			ctx,
			principal.CompanyID,
		); errors.Is(
			err,
			pgx.ErrNoRows,
		) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		required := auth.PermissionMembersManage
		if leave {
			required = ""
		}
		actorRole, err := authorizeMembershipActor(ctx, q, principal.CompanyID, principal.UserID, required)
		if err != nil {
			return err
		}
		member, err := q.GetCompanyMemberForUpdate(
			ctx,
			generated.GetCompanyMemberForUpdateParams{CompanyID: principal.CompanyID, UserID: input.UserID},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if member.Version != input.ExpectedRevision {
			return ErrMemberRevisionConflict
		}
		if member.Status != "active" {
			return ErrNotFound
		}
		if member.Role == string(auth.CompanyRoleOwner) {
			if !leave && !actorRole.Can(auth.PermissionMembersOwnersManage) {
				return ErrForbidden
			}
			if err = requireAnotherOwner(ctx, q, principal.CompanyID); err != nil {
				return err
			}
		}
		member, err = q.RevokeCompanyMember(
			ctx,
			generated.RevokeCompanyMemberParams{
				CompanyID:       principal.CompanyID,
				UserID:          input.UserID,
				ActorUserID:     principal.UserID,
				ExpectedVersion: input.ExpectedRevision,
			},
		)
		if err != nil {
			return err
		}
		// Project grants go with the membership (ADR 0017): a rejoin restores
		// none of them. Deleting them advances each Project's access revision,
		// which updates the Project rows, so it comes before any Conversation
		// or Run lock (company -> member -> project -> conversation -> run).
		if err = revokeProjectGrantsInTx(ctx, q, &pending, projectGrantAuthor{
			companyID: principal.CompanyID, actor: events.UserActor(principal.UserID.String()), recipient: principal.UserID,
		}, input.UserID); err != nil {
			return err
		}
		// Board grants go the same way; the Company lock held here orders the
		// Board locks this takes before any Conversation lock below.
		if err = canvas.RevokeBoardGrantsInTx(ctx, q, &pending, principal.CompanyID, input.UserID, principal.UserID); err != nil {
			return err
		}
		if err = conversation.RevokeDiscussionMembersInTx(
			ctx,
			q,
			&pending,
			conversation.DiscussionUserRevocation{
				CompanyID:   principal.CompanyID,
				UserID:      input.UserID,
				ActorUserID: principal.UserID,
			},
		); err != nil {
			return err
		}
		outcome := MembershipRemoved
		if leave {
			outcome = MembershipLeft
		}
		if err = stageMembershipEvents(
			&pending,
			principal.UserID,
			memberResponse(member),
			ActiveMemberResponse{},
			outcome,
		); err != nil {
			return err
		}
		pending = append(pending, events.Event{
			EntityType: Topic,
			Name:       EventCompanyMembershipChanged,
			CompanyID:  principal.CompanyID,
			CreatedAt:  time.Now().UTC(),
			Actor:      events.UserActor(principal.UserID.String()),
			Payload:    Membership{UserID: input.UserID.String()},
		})
		// Their queued prompts and saved Task Messages go too: a Message whose
		// author has left can never be written, so it must not wait for the
		// assignee's next Run. This locks Conversations, so it comes before
		// the Run cancellation below.
		if _, err = conversation.RemoveRunQueueItemsForAuthorizationRevocationInTx(
			ctx,
			q,
			&pending,
			principal.CompanyID,
			input.UserID,
		); err != nil {
			return err
		}
		// The departed member's Agent bindings go with them (ADR 0014); their
		// Tasks stay assigned and read as not ready until a tasks.assign holder
		// reassigns them.
		if err = agent.RevokeRuntimeOwnerInTx(
			ctx,
			q,
			&pending,
			principal.CompanyID,
			input.UserID,
		); err != nil {
			return err
		}
		// This also ends the member's Runs in every Project they lost: under
		// ADR 0014 a Run executes only on its authorizer's own runtime, so
		// every live Run the member authorized is on a runtime they own and
		// is cancelled here. The Project sweep
		// (execution.CancelProjectAccessRevokedRunsInTx) is therefore not run
		// as well; it would find nothing left to cancel.
		runs, err := execution.CancelRuntimeOwnerRunsInTx(
			ctx,
			q,
			&pending,
			execution.RuntimeOwnerRevocation{CompanyID: principal.CompanyID, UserID: input.UserID},
		)
		if err != nil {
			return err
		}
		for _, run := range runs {
			if err = task.ApplyRunTerminalInTx(ctx, q, &pending, run); err != nil {
				return err
			}
		}
		return activity.RecordEvidenceInTx(
			ctx,
			q,
			&pending,
			activity.EvidenceInput{
				CompanyID:       principal.CompanyID,
				EntityID:        member.UserID,
				Action:          "member." + string(outcome),
				Actor:           events.UserActor(principal.UserID.String()),
				RecipientUserID: principal.UserID,
				Payload: map[string]any{
					"userId": input.UserID,
					"status": "revoked",
					"reason": outcome,
				},
			},
		)
	})
	if err != nil {
		return err
	}
	for _, batch := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, batch.Topic, batch.Events...)
	}
	return nil
}
