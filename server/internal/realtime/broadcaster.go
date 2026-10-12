package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/oklog/ulid/v2"

	"boardly.dev/boardly/server/internal/events"
)

// Relay scope types are an internal Redis transport vocabulary. Browser
// clients only join company and user identity rooms; they never choose a
// scope themselves.
const (
	ScopeDisconnectCompanyUser = "disconnect_company_user"
	ScopeCompany               = "company"
	ScopeUser                  = "user"
	ScopeCompanyUser           = "company_user"
	ScopeCompanyOrUser         = "company_or_user"

	// ScopeDaemonRuntime is reserved for daemon control-plane frames. The
	// client hub never consumes it.
	ScopeDaemonRuntime = "daemon_runtime"
)

// BroadcastEvent is the browser event, and the JSON that goes on the wire is
// this struct. There used to be a second, identical one for marshaling; the
// only thing that differed was EventID, which the broadcaster mints at
// publish time, so it is empty on the value a consumer hands over and filled
// on the copy that is written.
//
// ActorID and CompanyID are pointers because the wire distinguishes null from
// "": a system event has no actor, and a fact that follows a person across
// every company they belong to has no tenant, and both must serialize as
// JSON null.
//
// This is a contract, not a preference. The client reads them through a
// helper that treats a blank string as a malformed field and drops the whole
// frame — see realtime-client.service.ts — and every run.* frame is
// system-authored, so emitting "" instead of null would silently lose them
// all. `omitempty` fails the same way: the envelope schema requires both keys
// and forbids extras.
type BroadcastEvent struct {
	EventID   string    `json:"eventId"`
	Type      string    `json:"type"`
	CompanyID *string   `json:"companyId"`
	CreatedAt time.Time `json:"createdAt"`
	ActorID   *string   `json:"actorId"`
	ActorType string    `json:"actorType"`
	Payload   any       `json:"payload"`
}

// Broadcaster is the only browser-event fanout surface. Its delivery methods
// are the routing vocabulary: a consumer that has decided who may see a fact
// says so by choosing one, and everything behind them — room keying, event
// ids, loopback de-duplication, cross-node relay — is this package's
// business. There is no audience value to build, mis-populate, or forget.
//
// Every method rejects an identifier that would address nobody, so a handler
// that read a zero-value routing field fails loudly instead of delivering
// into an empty room.
type Broadcaster interface {
	// DisconnectCompanyUser delivers the final access receipt and closes the
	// selected connections. This is a lifecycle control, not another audience.
	DisconnectCompanyUser(companyID, userID string, finalEvent BroadcastEvent) error
	// BroadcastToCompany reaches every authenticated connection in one
	// company.
	BroadcastToCompany(companyID string, event BroadcastEvent) error
	// BroadcastToUser reaches one user's connections across companies.
	BroadcastToUser(userID string, event BroadcastEvent) error
	// BroadcastToCompanyUser reaches the intersection: one user, in one
	// company.
	BroadcastToCompanyUser(companyID, userID string, event BroadcastEvent) error
	// BroadcastToCompanyOrUser reaches a company plus a user who may not
	// have a connection in it yet, delivering once to anyone in both.
	BroadcastToCompanyOrUser(companyID, userID string, event BroadcastEvent) error
	// BroadcastToCompanyUsers reaches each listed user, in one company, in
	// one bounded batch of at most 50 distinct users. It returns one result
	// per user in order, so a relay write that failed for one user leaves
	// the others delivered; an error refuses the whole batch.
	BroadcastToCompanyUsers(
		ctx context.Context,
		companyID string,
		userIDs []string,
		event BroadcastEvent,
	) ([]RelayBatchResult, error)
}

// NewBroadcastEvent adapts a bus event into the browser event.
//
// The payload crosses the wire as JSON, so a routing fact a consumer read off
// the Go struct stays behind whenever it is tagged json:"-".
func NewBroadcastEvent(event events.Event) BroadcastEvent {
	var actorID *string
	if event.Actor.ID != "" {
		id := event.Actor.ID
		actorID = &id
	}
	var companyID *string
	if event.CompanyID != uuid.Nil {
		id := event.CompanyID.String()
		companyID = &id
	}
	return BroadcastEvent{
		Type:      event.Name,
		CompanyID: companyID,
		CreatedAt: event.CreatedAt,
		ActorID:   actorID,
		ActorType: string(event.Actor.Type),
		Payload:   event.Payload,
	}
}

// audienceType is this package's private routing vocabulary. It exists to
// key rooms and relay scopes; nothing outside realtime names it.
type audienceType string

const (
	audienceCompany       audienceType = "company"
	audienceUser          audienceType = "user"
	audienceCompanyUser   audienceType = "company_user"
	audienceCompanyOrUser audienceType = "company_or_user"
)

// audience is the resolved destination of one frame.
type audience struct {
	typ       audienceType
	companyID string
	userID    string
}

// nilUUIDText is a zero uuid rendered as text. It is non-empty, so a
// zero-value identifier passes an emptiness check while addressing nobody.
const nilUUIDText = "00000000-0000-0000-0000-000000000000"

func addressable(id string) bool { return id != "" && id != nilUUIDText }

// DaemonRuntimeDeliverer consumes daemon-runtime scoped relay frames.
type DaemonRuntimeDeliverer interface {
	DeliverDaemonRuntime(scopeID string, frame []byte, eventID string)
}

// RelayPublisher is the low-level shared transport used by both browser
// events and daemon-runtime control frames. companyID is populated only for
// audience types that combine company and user identity.
type RelayPublisher interface {
	PublishWithID(scopeType, scopeID, companyID string, frame []byte, eventID string) error
}

// EventBroadcaster mints one event id, builds one canonical frame, delivers
// it locally, then publishes that exact frame and id to Redis when requested.
// A nil relay is the supported single-node configuration; local frames still
// carry eventId.
type EventBroadcaster struct {
	local *Hub
	relay RelayPublisher
}

// NewEventBroadcaster returns a browser broadcaster for a required local hub
// and an optional cross-node relay.
func NewEventBroadcaster(local *Hub, relay RelayPublisher) *EventBroadcaster {
	return &EventBroadcaster{local: local, relay: relay}
}

// BroadcastToCompany reaches every authenticated connection in one company.
func (b *EventBroadcaster) BroadcastToCompany(companyID string, event BroadcastEvent) error {
	if !addressable(companyID) {
		return fmt.Errorf("realtime: %s addressed to company %q, which reaches nobody", event.Type, companyID)
	}
	return b.publish(audience{typ: audienceCompany, companyID: companyID}, event)
}

// BroadcastToUser reaches one user's connections across companies.
func (b *EventBroadcaster) BroadcastToUser(userID string, event BroadcastEvent) error {
	if !addressable(userID) {
		return fmt.Errorf("realtime: %s addressed to user %q, which reaches nobody", event.Type, userID)
	}
	return b.publish(audience{typ: audienceUser, userID: userID}, event)
}

// BroadcastToCompanyUser reaches one user, in one company.
func (b *EventBroadcaster) BroadcastToCompanyUser(companyID, userID string, event BroadcastEvent) error {
	if !addressable(companyID) || !addressable(userID) {
		return fmt.Errorf("realtime: %s addressed to company %q and user %q, which reaches nobody",
			event.Type, companyID, userID)
	}
	return b.publish(audience{typ: audienceCompanyUser, companyID: companyID, userID: userID}, event)
}

// BroadcastToCompanyOrUser reaches a company plus a user who may not have joined it
// yet, delivering once to anyone in both.
func (b *EventBroadcaster) BroadcastToCompanyOrUser(companyID, userID string, event BroadcastEvent) error {
	if !addressable(companyID) || !addressable(userID) {
		return fmt.Errorf("realtime: %s addressed to company %q or user %q, which reaches nobody",
			event.Type, companyID, userID)
	}
	return b.publish(audience{typ: audienceCompanyOrUser, companyID: companyID, userID: userID}, event)
}

// NewEventID mints the opaque dedupe id a frame carries. publish stamps one
// on every frame that arrives without it; a caller that sends one fact down
// two paths (a revocation is both a user frame and a goodbye) stamps the id
// itself so the per-connection dedupe collapses the second copy.
func NewEventID() string { return ulid.Make().String() }

// publish mints the frame id, delivers locally, and relays that exact frame
// and id to the other nodes.
func (b *EventBroadcaster) publish(to audience, event BroadcastEvent) error {
	if b == nil || b.local == nil {
		return errors.New("realtime hub is required")
	}
	if event.Type == "" || event.CreatedAt.IsZero() {
		return errors.New("realtime event metadata is incomplete")
	}
	eventID := event.EventID
	if eventID == "" {
		eventID = NewEventID()
	}
	// event is this call's own copy, so stamping the minted id onto it is
	// filling in the one field the transport owns, not editing anyone's frame.
	event.EventID = eventID
	event.CreatedAt = event.CreatedAt.UTC()
	frame, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal realtime event: %w", err)
	}

	b.local.Deliver(to, frame, eventID)
	M.RecordEvent(event.Type)
	if b.relay == nil {
		return nil
	}

	scopeType, scopeID, companyID, err := relayRoute(to)
	if err != nil {
		return err
	}
	return b.relay.PublishWithID(scopeType, scopeID, companyID, frame, eventID)
}

func relayRoute(to audience) (scopeType, scopeID, companyID string, err error) {
	switch to.typ {
	case audienceCompany:
		return ScopeCompany, to.companyID, "", nil
	case audienceUser:
		return ScopeUser, to.userID, "", nil
	case audienceCompanyUser:
		return ScopeCompanyUser, to.userID, to.companyID, nil
	case audienceCompanyOrUser:
		return ScopeCompanyOrUser, to.userID, to.companyID, nil
	default:
		return "", "", "", errors.New("realtime audience type is unsupported")
	}
}

var _ Broadcaster = (*EventBroadcaster)(nil)

// DisconnectCompanyUser carries revocation through the same durable relay as
// data frames, so other server nodes also stop the selected connections.
func (b *EventBroadcaster) DisconnectCompanyUser(companyID, userID string, finalEvent BroadcastEvent) error {
	if !addressable(companyID) || !addressable(userID) || b == nil || b.local == nil {
		return errors.New("realtime disconnect identity and hub are required")
	}
	if finalEvent.EventID == "" {
		finalEvent.EventID = NewEventID()
	}
	finalEvent.CreatedAt = finalEvent.CreatedAt.UTC()
	frame, err := json.Marshal(finalEvent)
	if err != nil {
		return fmt.Errorf("marshal realtime disconnect receipt: %w", err)
	}
	b.local.disconnectCompanyUser(companyID, userID, frame, finalEvent.EventID)
	if b.relay == nil {
		return nil
	}
	return b.relay.PublishWithID(ScopeDisconnectCompanyUser, userID, companyID, frame, finalEvent.EventID)
}
