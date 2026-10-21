// This file holds the topic, the event names, and the payloads that exist
// only to be published. A payload the REST surface also serves lives with the
// domain's other types — the event carries the domain's projection, it does
// not own it.
//
// Who may see an event lives in cmd/server/realtime_routing.go — a comment
// here says what fact an event records, never who receives it, because a rule
// written in two places is a rule that drifts.

package agent

import (
	"context"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// Topic carries the Agent lifecycle family: the committed Agent projection on
// creation and on every mutation, plus the availability transition of every
// Agent bound to a runtime whose liveness just flipped.
const Topic = "agent"

const (
	EventAgentCreated = "agent.created"
	// EventAgentUpdated carries every committed mutation — configuration,
	// archive state, and appearance all land on this one frame.
	EventAgentUpdated = "agent.updated"
	// EventAgentStatusChanged batches the availability transition of every
	// binding on one runtime: the transition is the event, not the agent.
	EventAgentStatusChanged = "agent.status_changed"
	// EventAgentBindingChanged records that one member's binding of an Agent
	// was set, moved or removed (ADR 0014).
	EventAgentBindingChanged = "agent.binding_changed"
)

// AvailabilityChange is the agent.status event body: one batched availability
// transition for one member. Every Agent the member has bound to the runtime
// whose liveness just flipped arrives in one frame. The full Agent projection
// remains authoritative for every other field.
type AvailabilityChange struct {
	Agents []Availability `json:"agents"`

	// CompanyID and UserID are the routing facts: this frame belongs to one
	// member of one Company — availability is per viewer (ADR 0014) — and the
	// realtime bridge reads them off the Go value. They stay off the wire.
	CompanyID uuid.UUID `json:"-"`
	UserID    uuid.UUID `json:"-"`
}

// Availability is one Agent's entry in an AvailabilityChange. RuntimeAvailable is
// the same composite the Agent projection serves the binder (not archived,
// bound, runtime online and usable, provider signed in), never the raw
// runtime liveness flip.
type Availability struct {
	AgentID          uuid.UUID `json:"agentId"`
	RuntimeAvailable bool      `json:"runtimeAvailable"`
}

// BindingChange is the agent.binding_changed event body: the binder's new
// binding of the Agent, or nil once removed. Delivered to the binder alone —
// a binding is a member's private setup — so the routing facts stay off the
// wire like AvailabilityChange's.
type BindingChange struct {
	AgentID uuid.UUID   `json:"agentId"`
	Binding *BindingRef `json:"binding"`

	CompanyID uuid.UUID `json:"-"`
	UserID    uuid.UUID `json:"-"`
}

// TopicDaemon carries the roster-changed signal. Its subject is a join across
// daemons and runtimes with no single row to carry, so clients re-read rather
// than cache-write it.
const TopicDaemon = "daemon"

const EventDaemonRosterChanged = "daemon.roster_changed"

// DaemonRoster is the roster-changed frame. It wraps the shared wire payload
// so the frame belongs to this domain, and pkg/protocol stays a description
// of bytes rather than of who may read them.
type DaemonRoster struct {
	protocol.DaemonRegisterPayload
	// UserID routes the frame to the registrant (ADR 0014): a machine's
	// roster is its registrant's. Nil on the sweeps and the collector, which
	// name no machine and go to the whole Company as a refetch signal.
	UserID uuid.UUID `json:"-"`
}

// TopicConnector carries changes to shared Connector execution policy.
const TopicConnector = "connector"

const EventConnectorPolicyChanged = "connector.policy_changed"

func connectorPolicyChangedEvent(companyID uuid.UUID) events.Event {
	return events.Event{
		EntityType: TopicConnector, Name: EventConnectorPolicyChanged,
		CompanyID: companyID, CreatedAt: time.Now().UTC(), Actor: events.SystemActor(),
		Payload: struct{}{},
	}
}

// TopicRuntime carries the nudge asking the daemon hosting a runtime to send
// one immediate heartbeat, and committed capability discovery changes.
// Only the capabilities-changed fact is forwarded to a browser.
const TopicRuntime = "runtime"

const (
	EventRuntimePendingWork         = "runtime.pending_work"
	EventRuntimeCapabilitiesChanged = "runtime.capabilities_changed"
)

// CapabilitiesChanged names the replacement catalog, never provider credentials.
type CapabilitiesChanged struct {
	RuntimeID uuid.UUID `json:"runtimeId"`
	Catalog   string    `json:"catalog"`
	UserID    uuid.UUID `json:"-"`
}

func publishCapabilitiesChanged(
	ctx context.Context,
	bus *events.Bus,
	companyID, userID, runtimeID uuid.UUID,
	catalog string,
) {
	bus.Publish(ctx, TopicRuntime, events.Event{
		EntityType: TopicRuntime, Name: EventRuntimeCapabilitiesChanged,
		CompanyID: companyID, CreatedAt: time.Now().UTC(), Actor: events.SystemActor(),
		Payload: CapabilitiesChanged{RuntimeID: runtimeID, Catalog: catalog, UserID: userID},
	})
}

// PendingWork names the runtime to nudge and why.
type PendingWork struct {
	RuntimeID string
	Kind      string
}
