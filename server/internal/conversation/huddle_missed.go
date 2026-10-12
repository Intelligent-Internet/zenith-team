package conversation

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// DeliverMissedInvitations materializes expired invitations into the durable
// Inbox. The deterministic dedupe key makes a retry safe after a crash.
func (s *DefaultHuddleService) DeliverMissedInvitations(ctx context.Context) (int, error) {
	candidates, err := s.store.Queries().ListUndeliveredMissedHuddleInvitations(ctx)
	if err != nil {
		return 0, fmt.Errorf("list missed Huddle invitations: %w", err)
	}
	delivered := 0
	for _, candidate := range candidates {
		var pending []events.Event
		var result inbox.CreateResult
		err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			link := fmt.Sprintf(
				"/app?conversationKind=discussion&discussionType=%s&conversationId=%s&invitationId=%s",
				candidate.SourceKind,
				candidate.SourceConversationID,
				candidate.ID,
			)
			name := []rune(strings.Join(strings.Fields(candidate.InviterName), " "))
			if len(name) > 80 {
				name = append(name[:79], '…')
			}
			var createErr error
			result, createErr = inbox.CreateInTx(ctx, q, &pending, inbox.CreateInput{
				CompanyID:       candidate.CompanyID,
				RecipientUserID: candidate.InviteeUserID,
				Type:            inbox.TypeHuddleMissed,
				Entity: inbox.Entity{
					Type: protocol.EntityConversation,
					ID:   candidate.SourceConversationID.String(),
				},
				Title:     "Missed Huddle from " + string(name),
				Body:      "Open Chat to follow up.",
				DeepLink:  &link,
				DedupeKey: "huddle-missed:" + candidate.ID.String(),
			})
			return createErr
		})
		if err != nil {
			return delivered, fmt.Errorf("deliver missed Huddle invitation %s: %w", candidate.ID, err)
		}
		for _, group := range events.GroupByTopic(pending) {
			s.bus.Publish(ctx, group.Topic, group.Events...)
		}
		if result.Created {
			delivered++
			inbox.PresentCreated(s.presenter, result)
		}
	}
	return delivered, nil
}
