package conversation

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// TopicHuddle carries realtime lifecycle facts for Huddle clients.
const TopicHuddle = "huddle"

const (
	EventHuddleInvitationCreated = "huddle.invitation_created"
	EventHuddleInvitationChanged = "huddle.invitation_changed"
	EventHuddleRosterChanged     = "huddle.roster_changed"
	EventHuddleStatusChanged     = "huddle.status_changed"
)

// HuddleStatusChangedEvent tells call windows to refetch the call and voice
// agent status (GET …/ai). Like the roster signal it names only the Huddle.
type HuddleStatusChangedEvent struct {
	HuddleID         uuid.UUID   `json:"huddleId"`
	RecipientUserIDs []uuid.UUID `json:"-"`
}

// PublishHuddleStatusChanged is a post-commit hint to everyone still in the
// call. A failed recipient read only delays clients until their fallback poll,
// so it never fails the mutation that already committed.
func PublishHuddleStatusChanged(
	ctx context.Context,
	q *generated.Queries,
	bus *events.Bus,
	companyID, huddleID uuid.UUID,
) {
	if bus == nil {
		return
	}
	rows, err := q.ListOpenHuddleConnections(
		ctx,
		generated.ListOpenHuddleConnectionsParams{CompanyID: companyID, HuddleID: huddleID},
	)
	if err != nil || len(rows) == 0 {
		return
	}
	recipients := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		if !slices.Contains(recipients, row.UserID) {
			recipients = append(recipients, row.UserID)
		}
	}
	bus.Publish(ctx, TopicHuddle, events.Event{
		EntityType: TopicHuddle,
		Name:       EventHuddleStatusChanged,
		CompanyID:  companyID,
		CreatedAt:  time.Now().UTC(),
		Actor:      events.SystemActor(),
		Payload:    HuddleStatusChangedEvent{HuddleID: huddleID, RecipientUserIDs: recipients},
	})
}

// HuddleRosterChangedEvent tells call windows to refetch the participant
// roster. It names only the Huddle; names and statuses come from the
// authorized REST read, so the frame cannot outlive anyone's access.
type HuddleRosterChangedEvent struct {
	HuddleID         uuid.UUID   `json:"huddleId"`
	RecipientUserIDs []uuid.UUID `json:"-"`
}

// huddleRosterEvent is built inside the transaction that changed a
// connection. It reaches everyone still reserved or connected plus the users
// whose connection changed, whose own windows may still show the roster.
func huddleRosterEvent(
	ctx context.Context,
	q *generated.Queries,
	companyID, huddleID uuid.UUID,
	changed ...uuid.UUID,
) (events.Event, error) {
	rows, err := q.ListOpenHuddleConnections(
		ctx,
		generated.ListOpenHuddleConnectionsParams{CompanyID: companyID, HuddleID: huddleID},
	)
	if err != nil {
		return events.Event{}, err
	}
	seen := make(map[uuid.UUID]bool, len(rows)+len(changed))
	var recipients []uuid.UUID
	add := func(userID uuid.UUID) {
		if !seen[userID] {
			seen[userID] = true
			recipients = append(recipients, userID)
		}
	}
	for _, userID := range changed {
		add(userID)
	}
	for _, row := range rows {
		add(row.UserID)
	}
	return events.Event{
		EntityType: TopicHuddle,
		Name:       EventHuddleRosterChanged,
		CompanyID:  companyID,
		CreatedAt:  time.Now().UTC(),
		Actor:      events.SystemActor(),
		Payload:    HuddleRosterChangedEvent{HuddleID: huddleID, RecipientUserIDs: recipients},
	}, nil
}

type HuddleInvitationEvent struct {
	Invitation      HuddleInvitation `json:"invitation"`
	RecipientUserID uuid.UUID        `json:"-"`
}

func huddleInvitationEvent(companyID uuid.UUID, name string, invitation HuddleInvitation) events.Event {
	// Presentation context is checked on the recipient's REST read, never frozen
	// into a realtime frame that could outlive access to the source discussion.
	invitation.InviterName = ""
	invitation.SourceConversation = nil
	return events.Event{
		EntityType: TopicHuddle,
		Name:       name,
		CompanyID:  companyID,
		CreatedAt:  time.Now().UTC(),
		Actor:      events.SystemActor(),
		Payload: HuddleInvitationEvent{
			Invitation:      invitation,
			RecipientUserID: invitation.InviteeUserID,
		},
	}
}
