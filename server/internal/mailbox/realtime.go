package mailbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func publishMailboxEvents(ctx context.Context, bus *events.Bus, pending []events.Event) {
	for _, group := range events.GroupByTopic(pending) {
		bus.Publish(ctx, group.Topic, group.Events...)
	}
}

func newChangedEvent(
	ctx context.Context,
	queries *generated.Queries,
	companyID uuid.UUID,
	actorKind string,
	actorID *uuid.UUID,
	reason ChangeReason,
	messageID *uuid.UUID,
	occurredAt time.Time,
) (events.Event, error) {
	if !util.ValidUUID(companyID) || occurredAt.IsZero() {
		return events.Event{}, errors.New("Mailbox change event identity is invalid")
	}
	payload, err := NewChangedPayload(reason, messageID)
	if err != nil {
		return events.Event{}, fmt.Errorf("build Mailbox changed payload: %w", err)
	}
	payload.RecipientUserIDs, err = mailboxAudience(ctx, queries, companyID)
	if err != nil {
		return events.Event{}, err
	}
	actor, err := mailboxEventActor(actorKind, actorID)
	if err != nil {
		return events.Event{}, err
	}
	return events.Event{
		EntityType: Topic,
		Name:       EventChanged,
		CompanyID:  companyID,
		CreatedAt:  occurredAt.UTC(),
		Actor:      actor,
		Payload:    payload,
	}, nil
}

func mailboxEventActor(kind string, id *uuid.UUID) (events.Actor, error) {
	switch kind {
	case "user":
		if id != nil && util.ValidUUID(*id) {
			return events.UserActor(id.String()), nil
		}
	case "agent":
		if id != nil && util.ValidUUID(*id) {
			return events.AgentActor(id.String()), nil
		}
	case "system":
		if id == nil {
			return events.SystemActor(), nil
		}
	}
	return events.Actor{}, errors.New("Mailbox change event actor is invalid")
}

// mailboxAudience keeps even refetch hints inside the Mailbox permission boundary.
// Call it in the transaction that produces the change.
func mailboxAudience(ctx context.Context, queries *generated.Queries, companyID uuid.UUID) ([]uuid.UUID, error) {
	recipients, err := queries.ListActiveCompanyMemberIDsByRoles(
		ctx,
		generated.ListActiveCompanyMemberIDsByRolesParams{
			CompanyID: companyID, Roles: auth.RolesWith(auth.PermissionMailboxManage),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list Mailbox audience: %w", err)
	}
	return recipients, nil
}
