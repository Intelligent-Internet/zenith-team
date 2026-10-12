package mailbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func (s *DefaultDraftService) UpdateDraft(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	draftID uuid.UUID,
	input UpdateDraftInput,
) (Draft, error) {
	if err := validateDraftMutationIdentity(
		principal,
		conversationID,
		draftID,
		input.ExpectedRevision,
	); err != nil {
		return Draft{}, err
	}
	canonical, _, err := canonicalizeDraftInputs([]DraftInput{{
		Subject: input.Subject, Text: input.Text, Deliveries: input.Deliveries,
	}})
	if err != nil {
		return Draft{}, err
	}
	now := s.now().UTC()
	var result Draft
	var change events.Event
	err = s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite},
		func(queries *generated.Queries) error {
			if lockErr := lockMailboxMutationMembership(
				ctx,
				queries,
				principal,
				ErrAgentMailForbidden,
			); lockErr != nil {
				return lockErr
			}
			lockedRow, lockErr := lockDraftForMember(ctx, queries, principal, conversationID, draftID)
			if lockErr != nil {
				return lockErr
			}
			locked := lockedRow.MailboxDraft
			if locked.Version != input.ExpectedRevision ||
				(locked.Status != string(DraftStatusDraft) && locked.Status != string(DraftStatusFailed)) {
				return ErrAgentMailDraftConflict
			}
			if _, deleteErr := queries.DeleteMailboxDraftDeliveries(
				ctx, generated.DeleteMailboxDraftDeliveriesParams{
					CompanyID: principal.CompanyID, DraftID: draftID,
				},
			); deleteErr != nil {
				return fmt.Errorf("replace Agent Mail deliveries: %w", deleteErr)
			}
			updated, updateErr := queries.UpdateMailboxDraftContent(
				ctx, generated.UpdateMailboxDraftContentParams{
					Subject: canonical[0].Subject, BodyText: canonical[0].Text,
					CompanyID: principal.CompanyID, DraftID: draftID,
					ExpectedVersion: input.ExpectedRevision,
				},
			)
			if updateErr != nil {
				if errors.Is(updateErr, pgx.ErrNoRows) {
					return ErrAgentMailDraftConflict
				}
				return fmt.Errorf("update Agent Mail draft: %w", updateErr)
			}
			deliveries := make([]generated.MailboxDraftDelivery, 0, len(canonical[0].Deliveries))
			for index, delivery := range canonical[0].Deliveries {
				row, insertErr := queries.InsertMailboxDraftDelivery(
					ctx, generated.InsertMailboxDraftDeliveryParams{
						CompanyID: principal.CompanyID,
						DraftID:   draftID,
						Ordinal: int32(
							index,
						),
						ToAddresses: delivery.To,
						CcAddresses: delivery.CC,
					},
				)
				if insertErr != nil {
					return fmt.Errorf("insert replacement Agent Mail delivery: %w", insertErr)
				}
				deliveries = append(deliveries, row)
			}
			groupSize, sizeErr := queries.GetMailboxDraftGroupSize(
				ctx, generated.GetMailboxDraftGroupSizeParams{
					CompanyID: principal.CompanyID, GroupID: locked.GroupID,
				},
			)
			if sizeErr != nil {
				return fmt.Errorf("measure Agent Mail draft group: %w", sizeErr)
			}
			if groupSize.DeliveryCount > maxAgentMailDeliveries ||
				groupSize.RecipientCount > maxAgentMailRecipients {
				return ErrAgentMailSendLimitExceeded
			}
			result = draftFromRows(updated, deliveries)
			mappedChange, mapErr := newDraftChangedEvent(
				ctx, queries,
				principal.CompanyID, conversationID, events.UserActor(principal.UserID.String()),
				result, mailboxDraftChangeUpdated, lockedRow.ConversationKind,
				util.NullUUIDPtr(lockedRow.CreatedByUserID), now,
			)
			change = mappedChange
			return mapErr
		},
	)
	if err != nil {
		return Draft{}, err
	}
	publishMailboxEvents(ctx, s.bus, []events.Event{change})
	return result, nil
}

func (s *DefaultDraftService) DeleteDraft(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	draftID uuid.UUID,
	expectedRevision int64,
) error {
	if err := validateDraftMutationIdentity(principal, conversationID, draftID, expectedRevision); err != nil {
		return err
	}
	now := s.now().UTC()
	var change events.Event
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite},
		func(queries *generated.Queries) error {
			if lockErr := lockMailboxMutationMembership(
				ctx,
				queries,
				principal,
				ErrAgentMailForbidden,
			); lockErr != nil {
				return lockErr
			}
			lockedRow, lockErr := lockDraftForMember(ctx, queries, principal, conversationID, draftID)
			if lockErr != nil {
				return lockErr
			}
			locked := lockedRow.MailboxDraft
			if locked.Version != expectedRevision ||
				(locked.Status != string(DraftStatusDraft) && locked.Status != string(DraftStatusFailed)) {
				return ErrAgentMailDraftConflict
			}
			deliveries, deliveryErr := loadDraftDeliveries(ctx, queries, principal.CompanyID, draftID)
			if deliveryErr != nil {
				return deliveryErr
			}
			recipientCount := countDeliveryRows(deliveries)
			if _, deleteErr := queries.DeleteMailboxDraftDeliveries(
				ctx, generated.DeleteMailboxDraftDeliveriesParams{
					CompanyID: principal.CompanyID, DraftID: draftID,
				},
			); deleteErr != nil {
				return fmt.Errorf("delete Agent Mail deliveries: %w", deleteErr)
			}
			deleted, deleteErr := queries.DeleteMailboxDraft(ctx, generated.DeleteMailboxDraftParams{
				CompanyID: principal.CompanyID, DraftID: draftID, ExpectedVersion: expectedRevision,
			})
			if deleteErr != nil {
				return fmt.Errorf("delete Agent Mail draft: %w", deleteErr)
			}
			if deleted != 1 {
				return ErrAgentMailDraftConflict
			}
			var eventErr error
			change, eventErr = newDraftDeletedEvent(
				ctx, queries,
				principal.CompanyID, conversationID, events.UserActor(principal.UserID.String()),
				locked.GroupID, draftID, recipientCount, lockedRow.ConversationKind,
				util.NullUUIDPtr(lockedRow.CreatedByUserID), now,
			)
			return eventErr
		},
	)
	if err != nil {
		return err
	}
	publishMailboxEvents(ctx, s.bus, []events.Event{change})
	return nil
}

func (s *DefaultDraftService) QueueDraft(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	draftID uuid.UUID,
	expectedRevision int64,
) (Draft, error) {
	if err := validateDraftMutationIdentity(principal, conversationID, draftID, expectedRevision); err != nil {
		return Draft{}, err
	}
	now := s.now().UTC()
	var result Draft
	var change events.Event
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite},
		func(queries *generated.Queries) error {
			if lockErr := lockMailboxMutationMembership(
				ctx,
				queries,
				principal,
				ErrAgentMailForbidden,
			); lockErr != nil {
				return lockErr
			}
			lockedRow, lockErr := lockDraftForMember(ctx, queries, principal, conversationID, draftID)
			if lockErr != nil {
				return lockErr
			}
			if err := lockAgentMailQuotaAuthority(ctx, queries, principal.CompanyID); err != nil {
				return err
			}
			locked := lockedRow.MailboxDraft
			if locked.Version != expectedRevision ||
				(locked.Status != string(DraftStatusDraft) && locked.Status != string(DraftStatusFailed)) {
				return ErrAgentMailDraftConflict
			}
			deliveries, deliveryErr := loadDraftDeliveries(ctx, queries, principal.CompanyID, draftID)
			if deliveryErr != nil {
				return deliveryErr
			}
			if err := s.enforceDailyRecipientLimit(
				ctx,
				queries,
				principal.CompanyID,
				countDeliveryRows(deliveries),
				now,
			); err != nil {
				return err
			}
			queued, queueErr := queueDraftRow(ctx, queries, principal, locked, now)
			if queueErr != nil {
				return queueErr
			}
			result = draftFromRows(queued, deliveries)
			mappedChange, mapErr := newDraftChangedEvent(
				ctx, queries,
				principal.CompanyID, conversationID, events.UserActor(principal.UserID.String()),
				result, mailboxDraftChangeQueued, lockedRow.ConversationKind,
				util.NullUUIDPtr(lockedRow.CreatedByUserID), now,
			)
			change = mappedChange
			return mapErr
		},
	)
	if err != nil {
		return Draft{}, err
	}
	publishMailboxEvents(ctx, s.bus, []events.Event{change})
	return result, nil
}

func (s *DefaultDraftService) QueueGroup(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	groupID uuid.UUID,
	versions []DraftRevision,
) (DraftGroup, error) {
	if !validPrincipal(principal) || !util.ValidUUID(conversationID) ||
		!util.ValidUUID(groupID) {
		return DraftGroup{}, ErrUnauthenticated
	}
	if !principal.Can(auth.PermissionMailboxManage) {
		return DraftGroup{}, ErrAgentMailForbidden
	}
	expected, err := canonicalDraftRevisions(versions)
	if err != nil {
		return DraftGroup{}, err
	}
	now := s.now().UTC()
	var result DraftGroup
	var changes []events.Event
	err = s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite},
		func(queries *generated.Queries) error {
			if lockErr := lockMailboxMutationMembership(
				ctx,
				queries,
				principal,
				ErrAgentMailForbidden,
			); lockErr != nil {
				return lockErr
			}
			if lockErr := lockAgentMailConversationForMutation(
				ctx,
				queries,
				principal,
				conversationID,
			); lockErr != nil {
				return lockErr
			}
			rows, listErr := queries.ListMailboxGroupDraftsForMemberUpdate(
				ctx, generated.ListMailboxGroupDraftsForMemberUpdateParams{
					ActorRoles:     auth.RolesWith(auth.PermissionMailboxManage),
					ActorUserID:    principal.UserID,
					CompanyID:      principal.CompanyID,
					GroupID:        groupID,
					ConversationID: conversationID,
				},
			)
			if listErr != nil {
				return fmt.Errorf("lock Agent Mail group drafts: %w", listErr)
			}
			if len(rows) == 0 {
				return ErrAgentMailDraftNotFound
			}
			if err := lockAgentMailQuotaAuthority(ctx, queries, principal.CompanyID); err != nil {
				return err
			}
			candidates := make([]generated.MailboxDraft, 0, len(rows))
			for _, row := range rows {
				if row.MailboxDraft.Status == string(DraftStatusDraft) {
					candidates = append(candidates, row.MailboxDraft)
				}
			}
			if len(candidates) != len(expected) {
				return ErrAgentMailDraftConflict
			}
			draftIDs := make([]uuid.UUID, len(candidates))
			for index, candidate := range candidates {
				version, ok := expected[candidate.ID]
				if !ok || version != candidate.Version {
					return ErrAgentMailDraftConflict
				}
				draftIDs[index] = candidate.ID
			}
			deliveries, deliveryErr := queries.ListMailboxDraftDeliveriesForDrafts(
				ctx, generated.ListMailboxDraftDeliveriesForDraftsParams{
					CompanyID: principal.CompanyID, DraftIds: draftIDs,
				},
			)
			if deliveryErr != nil {
				return fmt.Errorf("list Agent Mail group deliveries: %w", deliveryErr)
			}
			if err := s.enforceDailyRecipientLimit(
				ctx, queries, principal.CompanyID, countDeliveryRows(deliveries), now,
			); err != nil {
				return err
			}
			queuedRows := make([]generated.MailboxDraft, 0, len(candidates))
			for _, candidate := range candidates {
				queued, queueErr := queueDraftRow(ctx, queries, principal, candidate, now)
				if queueErr != nil {
					return queueErr
				}
				queuedRows = append(queuedRows, queued)
			}
			groupRow, groupErr := queries.GetMailboxDraftGroup(
				ctx,
				generated.GetMailboxDraftGroupParams{CompanyID: principal.CompanyID, GroupID: groupID},
			)
			if groupErr != nil {
				return fmt.Errorf("get queued Agent Mail group: %w", groupErr)
			}
			mapped, mapErr := loadDraftGroup(ctx, queries, groupRow)
			if mapErr != nil {
				return mapErr
			}
			result = mapped
			queuedIDs := make(map[uuid.UUID]struct{}, len(queuedRows))
			for _, row := range queuedRows {
				queuedIDs[row.ID] = struct{}{}
			}
			for _, draft := range result.Drafts {
				if _, queued := queuedIDs[draft.ID]; !queued {
					continue
				}
				change, eventErr := newDraftChangedEvent(
					ctx, queries,
					principal.CompanyID, conversationID, events.UserActor(principal.UserID.String()),
					draft, mailboxDraftChangeQueued, rows[0].ConversationKind,
					util.NullUUIDPtr(rows[0].CreatedByUserID), now,
				)
				if eventErr != nil {
					return eventErr
				}
				changes = append(changes, change)
			}
			return nil
		},
	)
	if err != nil {
		return DraftGroup{}, err
	}
	publishMailboxEvents(ctx, s.bus, changes)
	return result, nil
}

func validateDraftMutationIdentity(
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	draftID uuid.UUID,
	expectedRevision int64,
) error {
	if !validPrincipal(principal) || !util.ValidUUID(conversationID) ||
		!util.ValidUUID(draftID) {
		return ErrUnauthenticated
	}
	if !principal.Can(auth.PermissionMailboxManage) {
		return ErrAgentMailForbidden
	}
	if expectedRevision < 1 || expectedRevision > 9007199254740991 {
		return ErrAgentMailInvalidInput
	}
	return nil
}

func lockDraftForMember(
	ctx context.Context,
	queries *generated.Queries,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	draftID uuid.UUID,
) (generated.LockMailboxDraftForMemberRow, error) {
	if err := lockAgentMailConversationForMutation(
		ctx,
		queries,
		principal,
		conversationID,
	); err != nil {
		return generated.LockMailboxDraftForMemberRow{}, err
	}
	row, err := queries.LockMailboxDraftForMember(ctx, generated.LockMailboxDraftForMemberParams{
		ActorRoles:  auth.RolesWith(auth.PermissionMailboxManage),
		ActorUserID: principal.UserID,
		CompanyID:   principal.CompanyID, DraftID: draftID, ConversationID: conversationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.LockMailboxDraftForMemberRow{}, ErrAgentMailDraftNotFound
	}
	if err != nil {
		return generated.LockMailboxDraftForMemberRow{}, fmt.Errorf("lock Agent Mail draft: %w", err)
	}
	return row, nil
}

func lockAgentMailConversationForMutation(
	ctx context.Context,
	queries *generated.Queries,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
) error {
	locked, err := queries.LockAgentMailConversationForMutation(
		ctx,
		generated.LockAgentMailConversationForMutationParams{
			ActorUserID: principal.UserID,
			CompanyID:   principal.CompanyID, ConversationID: conversationID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAgentMailDraftNotFound
	}
	if err != nil {
		return fmt.Errorf("lock Agent Mail Conversation: %w", err)
	}
	// Editing, deleting or sending a Task chat's drafts is posting there:
	// a reader without project.tasks.write sees them but may not change or
	// send them (ADR 0017). The Company lock the caller holds keeps a grant
	// change from landing between this check and the write.
	if locked.ConversationKind == "task" && locked.ProjectID.Valid {
		return auth.RequireProjectPermissionHeld(
			ctx, queries, principal.CompanyID, locked.ProjectID.UUID, principal.UserID,
			auth.ProjectPermissionTasksWrite,
		)
	}
	return nil
}

func lockAgentMailQuotaAuthority(
	ctx context.Context,
	queries *generated.Queries,
	companyID uuid.UUID,
) error {
	_, err := queries.LockCompanyMailboxForAgentMailSend(
		ctx,
		companyID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAgentMailCompanyEmailRequired
	}
	if err != nil {
		return fmt.Errorf("lock Agent Mail quota authority: %w", err)
	}
	return nil
}

func (s *DefaultDraftService) enforceDailyRecipientLimit(
	ctx context.Context,
	queries *generated.Queries,
	companyID uuid.UUID,
	additional int,
	now time.Time,
) error {
	if additional < 1 || additional > maxAgentMailRecipients {
		return ErrAgentMailInvalidRecipients
	}
	used, err := queries.CountAgentMailRecipientUnitsSince(
		ctx, generated.CountAgentMailRecipientUnitsSinceParams{
			CompanyID: companyID, SinceAt: timestamp(now.Add(-dailyRecipientWindow)),
		},
	)
	if err != nil {
		return fmt.Errorf("count Agent Mail recipient quota: %w", err)
	}
	if used < 0 || used > int64(s.dailyRecipientLimit) ||
		used+int64(additional) > int64(s.dailyRecipientLimit) {
		return ErrAgentMailSendLimitExceeded
	}
	return nil
}

func queueDraftRow(
	ctx context.Context,
	queries *generated.Queries,
	principal auth.UserPrincipal,
	row generated.MailboxDraft,
	now time.Time,
) (generated.MailboxDraft, error) {
	if _, err := queries.ResetMailboxDraftDeliveryAcceptance(
		ctx, generated.ResetMailboxDraftDeliveryAcceptanceParams{
			CompanyID: principal.CompanyID, DraftID: row.ID,
		},
	); err != nil {
		return generated.MailboxDraft{}, fmt.Errorf("reset Agent Mail delivery acceptance: %w", err)
	}
	queued, err := queries.QueueMailboxDraft(ctx, generated.QueueMailboxDraftParams{
		NowAt: timestamp(now), SentByUserID: principal.UserID, CompanyID: principal.CompanyID,
		DraftID: row.ID, ExpectedVersion: row.Version,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.MailboxDraft{}, ErrAgentMailDraftConflict
	}
	if err != nil {
		return generated.MailboxDraft{}, fmt.Errorf("queue Agent Mail draft: %w", err)
	}
	return queued, nil
}

func countDeliveryRows(rows []generated.MailboxDraftDelivery) int {
	total := 0
	for _, row := range rows {
		total += len(row.ToAddresses) + len(row.CcAddresses)
	}
	return total
}

func canonicalDraftRevisions(versions []DraftRevision) (map[uuid.UUID]int64, error) {
	if len(versions) == 0 || len(versions) > maxAgentMailDeliveries {
		return nil, ErrAgentMailInvalidInput
	}
	result := make(map[uuid.UUID]int64, len(versions))
	for _, version := range versions {
		if !util.ValidUUID(version.ID) || version.Revision < 1 ||
			version.Revision > 9007199254740991 {
			return nil, ErrAgentMailInvalidInput
		}
		if _, duplicate := result[version.ID]; duplicate {
			return nil, ErrAgentMailInvalidInput
		}
		result[version.ID] = version.Revision
	}
	return result, nil
}
