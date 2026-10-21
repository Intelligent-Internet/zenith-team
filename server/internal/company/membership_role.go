package company

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// ChangeRoleInput names the member whose role changes, the role they get,
// and the revision the caller last saw.
type ChangeRoleInput struct {
	UserID           uuid.UUID
	Role             auth.CompanyRole
	ExpectedRevision int64
}

// ChangeRole moves one active member to another role. The caller needs
// members.manage; when the target is an owner or the new role is owner they
// also need members.owners.manage. A member never changes their own role
// here — leaving and transferring ownership are the two self-directed moves
// — and the last active owner is never demoted.
func (s *DefaultCompanyService) ChangeRole(
	ctx context.Context,
	principal auth.UserPrincipal,
	input ChangeRoleInput,
) (ActiveMemberResponse, error) {
	if !util.ValidUUID(input.UserID) || input.ExpectedRevision < 1 || !input.Role.Valid() {
		return ActiveMemberResponse{}, ErrInvalidInput
	}
	if input.UserID == principal.UserID {
		return ActiveMemberResponse{}, ErrInvalidInput
	}
	var result ActiveMemberResponse
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
		actorRole, err := authorizeMembershipActor(
			ctx, q, principal.CompanyID, principal.UserID, auth.PermissionMembersManage,
		)
		if err != nil {
			return err
		}
		member, err := lockActiveMemberAtRevision(
			ctx,
			q,
			principal.CompanyID,
			input.UserID,
			input.ExpectedRevision,
		)
		if err != nil {
			return err
		}
		fromRole := auth.CompanyRole(member.Role)
		if fromRole == input.Role {
			result, err = activeMemberProjection(ctx, q, principal.CompanyID, input.UserID)
			return err
		}
		if (fromRole == auth.CompanyRoleOwner || input.Role == auth.CompanyRoleOwner) &&
			!actorRole.Can(auth.PermissionMembersOwnersManage) {
			return ErrForbidden
		}
		if fromRole == auth.CompanyRoleOwner {
			if err = requireAnotherOwner(ctx, q, principal.CompanyID); err != nil {
				return err
			}
		}
		result, err = changeMemberRoleInTx(ctx, q, &pending, principal.UserID, member, input.Role)
		return err
	})
	if err != nil {
		return ActiveMemberResponse{}, err
	}
	for _, batch := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, batch.Topic, batch.Events...)
	}
	return result, nil
}

// TransferOwnership makes the target an owner and the caller an admin in one
// transaction. The caller needs members.owners.manage, which only an owner
// holds, so the Company keeps at least one owner throughout. The result lists
// the target first, then the caller.
func (s *DefaultCompanyService) TransferOwnership(
	ctx context.Context,
	principal auth.UserPrincipal,
	input MembershipMutation,
) ([]ActiveMemberResponse, error) {
	if !util.ValidUUID(input.UserID) || input.ExpectedRevision < 1 {
		return nil, ErrInvalidInput
	}
	if input.UserID == principal.UserID {
		return nil, ErrInvalidInput
	}
	var result []ActiveMemberResponse
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = nil
		result = nil
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
		if _, err := authorizeMembershipActor(
			ctx, q, principal.CompanyID, principal.UserID, auth.PermissionMembersOwnersManage,
		); err != nil {
			return err
		}
		target, err := lockActiveMemberAtRevision(
			ctx,
			q,
			principal.CompanyID,
			input.UserID,
			input.ExpectedRevision,
		)
		if err != nil {
			return err
		}
		if target.Role == string(auth.CompanyRoleOwner) {
			return ErrInvalidInput
		}
		caller, err := q.GetCompanyMemberForUpdate(ctx, generated.GetCompanyMemberForUpdateParams{
			CompanyID: principal.CompanyID,
			UserID:    principal.UserID,
		})
		if err != nil {
			return fmt.Errorf("lock transferring owner: %w", err)
		}
		promoted, err := changeMemberRoleInTx(ctx, q, &pending, principal.UserID, target, auth.CompanyRoleOwner)
		if err != nil {
			return err
		}
		demoted, err := changeMemberRoleInTx(ctx, q, &pending, principal.UserID, caller, auth.CompanyRoleAdmin)
		if err != nil {
			return err
		}
		result = []ActiveMemberResponse{promoted, demoted}
		return activity.RecordEvidenceInTx(ctx, q, &pending, activity.EvidenceInput{
			CompanyID:       principal.CompanyID,
			EntityID:        input.UserID,
			Action:          "member.ownership_transferred",
			Actor:           events.UserActor(principal.UserID.String()),
			RecipientUserID: principal.UserID,
			Payload: map[string]any{
				"fromUserId": principal.UserID,
				"toUserId":   input.UserID,
			},
		})
	})
	if err != nil {
		return nil, err
	}
	for _, batch := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, batch.Topic, batch.Events...)
	}
	return result, nil
}

// lockActiveMemberAtRevision locks the target row and applies the two checks
// every membership mutation makes: the row is active, and it is the revision
// the caller last saw.
func lockActiveMemberAtRevision(
	ctx context.Context,
	q *generated.Queries,
	companyID, userID uuid.UUID,
	expectedRevision int64,
) (generated.CompanyMember, error) {
	member, err := q.GetCompanyMemberForUpdate(ctx, generated.GetCompanyMemberForUpdateParams{
		CompanyID: companyID,
		UserID:    userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.CompanyMember{}, ErrNotFound
	}
	if err != nil {
		return generated.CompanyMember{}, fmt.Errorf("lock Company member: %w", err)
	}
	if member.Version != expectedRevision {
		return generated.CompanyMember{}, ErrMemberRevisionConflict
	}
	if member.Status != string(MembershipStatusActive) {
		return generated.CompanyMember{}, ErrNotFound
	}
	return member, nil
}

// requireAnotherOwner refuses to demote or remove an owner when they are the
// only one left.
func requireAnotherOwner(ctx context.Context, q *generated.Queries, companyID uuid.UUID) error {
	count, err := q.CountActiveCompanyOwners(ctx, companyID)
	if err != nil {
		return fmt.Errorf("count active Company owners: %w", err)
	}
	if count <= 1 {
		return ErrLastOwner
	}
	return nil
}

// changeMemberRoleInTx writes the new role through the compare-and-swap
// query, stages the member.updated and auth.company_access_changed pair, and
// records the Activity row. The caller has already locked the member and
// checked the policy.
func changeMemberRoleInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	actorUserID uuid.UUID,
	member generated.CompanyMember,
	role auth.CompanyRole,
) (ActiveMemberResponse, error) {
	updated, err := q.ChangeCompanyMemberRole(ctx, generated.ChangeCompanyMemberRoleParams{
		Role:            string(role),
		CompanyID:       member.CompanyID,
		UserID:          member.UserID,
		ExpectedVersion: member.Version,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ActiveMemberResponse{}, ErrMemberRevisionConflict
	}
	if err != nil {
		return ActiveMemberResponse{}, fmt.Errorf("change Company member role: %w", err)
	}
	active, err := activeMemberProjection(ctx, q, member.CompanyID, member.UserID)
	if err != nil {
		return ActiveMemberResponse{}, err
	}
	if err = stageMembershipEvents(
		pending,
		actorUserID,
		memberResponse(updated),
		active,
		MembershipRoleChanged,
	); err != nil {
		return ActiveMemberResponse{}, err
	}
	return active, activity.RecordEvidenceInTx(ctx, q, pending, activity.EvidenceInput{
		CompanyID:       member.CompanyID,
		EntityID:        member.UserID,
		Action:          "member.role_changed",
		Actor:           events.UserActor(actorUserID.String()),
		RecipientUserID: actorUserID,
		Payload: map[string]any{
			"userId":   member.UserID,
			"fromRole": member.Role,
			"toRole":   string(role),
		},
	})
}

// activeMemberProjection reads the roster row for one active member, the
// shape member.added and member.updated carry.
func activeMemberProjection(
	ctx context.Context,
	q *generated.Queries,
	companyID, userID uuid.UUID,
) (ActiveMemberResponse, error) {
	row, err := q.GetActiveCompanyMemberProjection(ctx, generated.GetActiveCompanyMemberProjectionParams{
		CompanyID: companyID,
		UserID:    userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ActiveMemberResponse{}, ErrNotFound
	}
	if err != nil {
		return ActiveMemberResponse{}, fmt.Errorf("load Company member projection: %w", err)
	}
	return activeMemberResponse(
		memberResponse(row.CompanyMember),
		row.MemberEmail,
		row.MemberDisplayName,
		util.TextToPtr(row.MemberAvatarUrl),
		row.AccountDisabled,
	), nil
}
