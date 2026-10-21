// Package events is the server's post-commit event spine.
//
// An entity type is a domain: "task", "run", "company". Every fact that domain
// publishes travels on its one topic, and a consumer subscribes once and
// switches on the event name. Producers stage events inside the transaction
// that commits the fact and publish the batch after commit; subscribers
// registered in the composition root react without the producer knowing they
// exist.
//
// An event carries what happened and who did it. It carries no audience:
// deciding who may see a fact belongs to the consumer that delivers it, so
// the browser bridge subscribes like any other consumer.
package events

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// topicName is a domain: one lowercase word.
var topicName = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

// eventName is the wire vocabulary: the entity type, a dot, one lowercase
// verb — run.completed, auth.session_revoked.
var eventName = regexp.MustCompile(`^[a-z][a-z0-9]*\.[a-z][a-z0-9_]*$`)

// ValidTopic reports whether value is shaped like a topic. The Activity feed
// filter has its own, wider shape (activity.ValidEntityTypeFilter), because
// Activity also stores facts that never travel on the bus.
func ValidTopic(value string) bool { return topicName.MatchString(value) }

// ValidEventName reports whether value is shaped like an event name. It
// checks the shape, not membership: the names are declared across the domain
// packages, and an enumeration here would be stale the day a domain adds one.
func ValidEventName(value string) bool { return eventName.MatchString(value) }

// Event is the one envelope a subscriber sees. A producer builds it whole,
// inside the transaction that commits the fact, and nothing edits it
// afterwards: it records something that has already happened.
//
// Payload is the domain's own projection for this event name. A consumer that
// wants it typed calls PayloadOf, which reports a mismatch as an error the
// bus logs — a shape nobody expected is never dropped in silence.
type Event struct {
	// ActivityRecorded prevents copying a transactionally captured event as legacy evidence.
	ActivityRecorded bool `json:"-"`
	// EntityType is the domain the fact belongs to — "task", "run",
	// "company". It is the same string the domain declares as its Topic and
	// a consumer names in Subscribe, seen from the event's side rather than
	// the bus's, and it is what the activity feed stores in entity_type.
	EntityType string

	// Name is the fact as it appears on the wire: entity type, a dot, a verb.
	Name string

	// CompanyID is the tenant the fact belongs to. It is on the envelope
	// rather than read out of each payload because every consumer wants it
	// for the same reasons — which company to deliver to, which company to
	// log against — and most payloads carry it anyway, so leaving it there
	// alone made every consumer dig for the same field in a different place.
	//
	// It is uuid.Nil for account-scoped facts such as user.updated,
	// auth.session_revoked, company.membership_changed and personal Inbox
	// notifications. Each follows a user across every company
	// they belong to, so no single tenant owns it.
	CompanyID uuid.UUID

	// CreatedAt is when the fact was established. Producers stamp it as they
	// stage, inside the transaction, because that is the moment the fact
	// became true — publishing happens after commit, and a transaction's
	// facts may leave in more than one batch.
	CreatedAt time.Time

	// RunID, TaskID and ConversationID name the rows this fact is about,
	// when it is about them. They are optional and usually absent: an agent
	// rename is about none of the three, while a run frame is about all
	// three at once.
	//
	// They are on the envelope for the same reason CompanyID is — a consumer
	// that wants to know which run a fact belongs to should not have to know
	// the shape of that domain's payload to find out — and every producer
	// that has one in hand sets it.
	//
	// Absent is uuid.Nil, as it is for CompanyID. All three identities
	// are database-generated UUIDs.
	RunID          uuid.UUID
	TaskID         uuid.UUID
	ConversationID uuid.UUID

	Actor   Actor
	Payload any
}

// PayloadOf re-types an event's payload for the consumer that expects it.
//
// It returns an error rather than a bare ok, because a handler that returns
// it gets the mismatch logged against the site that subscribed. Silently
// skipping the event instead is the failure this design exists to remove.
func PayloadOf[P any](event Event) (P, error) {
	payload, ok := event.Payload.(P)
	if !ok {
		var zero P
		return zero, fmt.Errorf("events: %s carried %T, want %T", event.Name, event.Payload, zero)
	}
	return payload, nil
}

// validate reports why an event cannot be delivered on the named topic, or
// nil when it can.
//
// The checks live here, at the one seam every event crosses, rather than in a
// constructor each of the thirty-odd producers would have to route through.
// A producer writes a plain struct literal; the bus is what makes sure the
// result is something a consumer can act on.
func (e Event) validate(topic string) error {
	switch {
	case !ValidTopic(topic):
		return fmt.Errorf("events: %q is not a valid topic", topic)
	case e.Name == "":
		return fmt.Errorf("events: event with no name on topic %q", topic)
	case e.EntityType != topic:
		return fmt.Errorf("events: %s is a %q fact, published on %q", e.Name, e.EntityType, topic)
	case !ValidEventName(e.Name):
		return fmt.Errorf("events: %q is not a valid event name", e.Name)
	case !strings.HasPrefix(e.Name, topic+"."):
		return fmt.Errorf("events: %s does not belong to topic %q", e.Name, topic)
	case e.Payload == nil:
		return fmt.Errorf("events: %s has no payload", e.Name)
	case e.CreatedAt.IsZero():
		// The producer stamps this in the literal, so a zero value means a
		// literal that forgot it. Left alone it reaches the browser as a
		// year-1 timestamp, which nothing downstream would report.
		return fmt.Errorf("events: %s was not stamped with a time", e.Name)
	default:
		return e.Actor.validationError(e.Name)
	}
}
