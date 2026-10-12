package mailbox

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func loadDraftGroups(
	ctx context.Context,
	queries *generated.Queries,
	groupRows []generated.MailboxDraftGroup,
) ([]DraftGroup, error) {
	if len(groupRows) == 0 {
		return []DraftGroup{}, nil
	}
	groupIDs := make([]uuid.UUID, len(groupRows))
	for index, row := range groupRows {
		groupIDs[index] = row.ID
	}
	draftRows, err := queries.ListMailboxDraftsForGroups(
		ctx, generated.ListMailboxDraftsForGroupsParams{
			CompanyID: groupRows[0].CompanyID, GroupIds: groupIDs,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list Agent Mail drafts: %w", err)
	}
	draftIDs := make([]uuid.UUID, len(draftRows))
	for index, row := range draftRows {
		draftIDs[index] = row.ID
	}
	var deliveryRows []generated.MailboxDraftDelivery
	if len(draftIDs) != 0 {
		deliveryRows, err = queries.ListMailboxDraftDeliveriesForDrafts(
			ctx, generated.ListMailboxDraftDeliveriesForDraftsParams{
				CompanyID: groupRows[0].CompanyID, DraftIds: draftIDs,
			},
		)
		if err != nil {
			return nil, fmt.Errorf("list Agent Mail deliveries: %w", err)
		}
	}
	draftsByGroup := make(map[uuid.UUID][]generated.MailboxDraft, len(groupRows))
	groupByDraft := make(map[uuid.UUID]uuid.UUID, len(draftRows))
	for _, row := range draftRows {
		draftsByGroup[row.GroupID] = append(draftsByGroup[row.GroupID], row)
		groupByDraft[row.ID] = row.GroupID
	}
	deliveriesByGroup := make(map[uuid.UUID][]generated.MailboxDraftDelivery, len(groupRows))
	for _, row := range deliveryRows {
		groupID, exists := groupByDraft[row.DraftID]
		if exists {
			deliveriesByGroup[groupID] = append(deliveriesByGroup[groupID], row)
		}
	}
	result := make([]DraftGroup, len(groupRows))
	for index, groupRow := range groupRows {
		result[index] = composeDraftGroup(
			groupRow, draftsByGroup[groupRow.ID], deliveriesByGroup[groupRow.ID],
		)
	}
	return result, nil
}

func loadDraftGroup(
	ctx context.Context,
	queries *generated.Queries,
	groupRow generated.MailboxDraftGroup,
) (DraftGroup, error) {
	groups, err := loadDraftGroups(ctx, queries, []generated.MailboxDraftGroup{groupRow})
	if err != nil {
		return DraftGroup{}, err
	}
	return groups[0], nil
}

func composeDraftGroup(
	groupRow generated.MailboxDraftGroup,
	draftRows []generated.MailboxDraft,
	deliveryRows []generated.MailboxDraftDelivery,
) DraftGroup {
	group := DraftGroup{
		ID: groupRow.ID, ConversationID: groupRow.ConversationID,
		SourceRunID: groupRow.SourceRunID, AgentID: groupRow.AgentID,
		Drafts: make([]Draft, 0, len(draftRows)), CreatedAt: groupRow.CreatedAt.Time.UTC(),
		UpdatedAt: groupRow.UpdatedAt.Time.UTC(),
	}
	deliveriesByDraft := make(map[uuid.UUID][]generated.MailboxDraftDelivery, len(draftRows))
	for _, deliveryRow := range deliveryRows {
		deliveriesByDraft[deliveryRow.DraftID] = append(
			deliveriesByDraft[deliveryRow.DraftID], deliveryRow,
		)
	}
	for _, draftRow := range draftRows {
		draft := draftFromRows(draftRow, deliveriesByDraft[draftRow.ID])
		group.RecipientCount += draft.RecipientCount
		group.Drafts = append(group.Drafts, draft)
	}
	return group
}

func draftFromRows(
	row generated.MailboxDraft,
	deliveryRows []generated.MailboxDraftDelivery,
) Draft {
	status := DraftStatus(row.Status)
	draft := Draft{
		ID: row.ID, GroupID: row.GroupID, Subject: row.Subject, Text: row.BodyText,
		FromName: row.FromName, FromAddress: row.FromAddress, Status: status,
		SendGeneration: row.SendGeneration, Deliveries: make([]DraftDelivery, 0, len(deliveryRows)),
		SentAt: util.TimestampToTime(row.SentAt), FailedAt: util.TimestampToTime(row.FailedAt),
		CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(), Revision: row.Version,
	}
	if row.FailureReason.Valid {
		reason := row.FailureReason.String
		draft.FailureReason = &reason
	}
	slices.SortFunc(deliveryRows, func(left, right generated.MailboxDraftDelivery) int {
		if left.Ordinal < right.Ordinal {
			return -1
		}
		if left.Ordinal > right.Ordinal {
			return 1
		}
		return bytes.Compare(left.ID[:], right.ID[:])
	})
	for _, deliveryRow := range deliveryRows {
		delivery := DraftDelivery{
			ID: deliveryRow.ID, To: append([]string(nil), deliveryRow.ToAddresses...),
			CC:         append([]string{}, deliveryRow.CcAddresses...),
			AcceptedAt: util.TimestampToTime(deliveryRow.AcceptedAt),
		}
		draft.RecipientCount += len(delivery.To) + len(delivery.CC)
		draft.Deliveries = append(draft.Deliveries, delivery)
	}
	return draft
}

func loadDraftDeliveries(
	ctx context.Context,
	queries *generated.Queries,
	companyID uuid.UUID,
	draftID uuid.UUID,
) ([]generated.MailboxDraftDelivery, error) {
	rows, err := queries.ListMailboxDraftDeliveriesForDrafts(
		ctx, generated.ListMailboxDraftDeliveriesForDraftsParams{
			CompanyID: companyID, DraftIds: []uuid.UUID{draftID},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list Agent Mail deliveries: %w", err)
	}
	return rows, nil
}
