package company

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// Delete permanently removes a Company and every database row it owns. The
// membership receipts are captured before the cascade removes their rows, so
// every affected account and daemon can discard its former access after the
// transaction commits.
func (s *DefaultCompanyService) Delete(
	ctx context.Context,
	principal auth.UserPrincipal,
	expectedRevision int64,
) error {
	if expectedRevision < 1 || expectedRevision > protocol.MaxJSONSafeInteger {
		return ErrInvalidInput
	}

	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = pending[:0]
		companyRow, err := q.LockCompanyForMembershipMutation(ctx, principal.CompanyID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Company for deletion: %w", err)
		}
		if _, err := authorizeMembershipActor(
			ctx,
			q,
			principal.CompanyID,
			principal.UserID,
			auth.PermissionCompanyDelete,
		); err != nil {
			return err
		}
		if companyRow.Version != expectedRevision {
			return ErrDeleteConflict
		}

		members, err := q.ListActiveCompanyMembershipsForDeletion(ctx, principal.CompanyID)
		if err != nil {
			return fmt.Errorf("list Company memberships for deletion: %w", err)
		}
		for _, row := range members {
			eventStart := len(pending)
			member := memberResponse(row)
			member.Status = MembershipStatusRevoked
			member.Revision++
			if err := stageMembershipEvents(
				&pending,
				principal.UserID,
				member,
				ActiveMemberResponse{},
				MembershipCompanyDeleted,
			); err != nil {
				return fmt.Errorf("stage Company deletion membership events: %w", err)
			}
			// The Company's retained Activity is erased in this transaction.
			// Keep these post-commit delivery receipts out of the catch-all
			// Activity sink so they cannot recreate history for the deleted Company.
			for index := eventStart; index < len(pending); index++ {
				pending[index].ActivityRecorded = true
				if pending[index].EntityType == TopicAuth {
					pending[index].CompanyID = uuid.Nil
				}
			}
			pending = append(pending, events.Event{
				ActivityRecorded: true,
				EntityType:       Topic,
				Name:             EventCompanyMembershipChanged,
				CreatedAt:        time.Now().UTC(),
				Actor:            events.UserActor(principal.UserID.String()),
				Payload:          Membership{UserID: member.UserID.String()},
			})
		}
		if err := q.DeleteCompanyDependentRecords(ctx, principal.CompanyID); err != nil {
			return fmt.Errorf("delete Company dependent records: %w", err)
		}

		if _, err := q.DeleteCompany(ctx, generated.DeleteCompanyParams{
			CompanyID:       principal.CompanyID,
			ExpectedVersion: expectedRevision,
		}); errors.Is(err, pgx.ErrNoRows) {
			return ErrDeleteConflict
		} else if err != nil {
			return fmt.Errorf("delete Company: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return nil
}
