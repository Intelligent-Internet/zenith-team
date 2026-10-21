package conversation

import (
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func discussionChangedEvent(row generated.Conversation) events.Event {
	return events.Event{
		EntityType:     Topic,
		Name:           EventDiscussionChanged,
		CompanyID:      row.CompanyID,
		ConversationID: row.ID,
		CreatedAt:      row.UpdatedAt.Time.UTC(),
		Actor:          events.SystemActor(),
		Payload:        DiscussionChanged{ConversationID: row.ID, HeadSeq: row.HeadSeq, Revision: row.Version},
	}
}

func discussionStateEvent(row generated.ConversationUserState) events.Event {
	return events.Event{
		EntityType:     Topic,
		Name:           EventDiscussionStateChanged,
		CompanyID:      row.CompanyID,
		ConversationID: row.ConversationID,
		CreatedAt:      row.UpdatedAt.Time.UTC(),
		Actor:          events.SystemActor(),
		Payload: DiscussionStateChanged{
			ConversationID: row.ConversationID,
			Revision:       row.Version,
			UserID:         row.UserID,
		},
	}
}

func discussionRevokeEvent(row generated.ConversationMember) events.Event {
	return events.Event{
		EntityType:     Topic,
		Name:           EventDiscussionAccessRevoked,
		CompanyID:      row.CompanyID,
		ConversationID: row.ConversationID,
		CreatedAt:      time.Now().UTC(),
		Actor:          events.SystemActor(),
		Payload: DiscussionAccessRevoked{
			ConversationID:  row.ConversationID,
			GrantGeneration: row.Generation,
			UserID:          row.UserID,
		},
	}
}
