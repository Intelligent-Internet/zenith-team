package events

import "fmt"

// ActorType is the closed vocabulary of who commits a fact.
type ActorType string

// Actor types. A fact is committed by a signed-in user, by an agent, or by
// the platform itself.
const (
	ActorTypeUser   ActorType = "user"
	ActorTypeAgent  ActorType = "agent"
	ActorTypeSystem ActorType = "system"
)

// Actor is who committed the fact. The zero value is invalid, so staging
// makes every producer say user, agent, or system out loud.
//
// An Agent acting in a Run has three identities: the Agent (ID), the
// person it acts for, the Run's Authorizer (OnBehalfOf), and the Run itself
// (RunID). Activity stores all three, so a row says "Agent A for Mia" and
// links the Run. A user or system actor carries neither extra field.
type Actor struct {
	Type ActorType
	ID   string
	// OnBehalfOf is the person an Agent actor acted for; empty for a user or
	// system actor and for an Agent read back from a row that names no Run.
	OnBehalfOf string
	// RunID is the Run an Agent actor acted in. It is set only together with
	// OnBehalfOf: a Run always works for one person.
	RunID string
}

// UserActor attributes the fact to a signed-in user.
func UserActor(id string) Actor { return Actor{Type: ActorTypeUser, ID: id} }

// AgentActor attributes the fact to an Agent alone. It is for an actor read
// back from a stored row that names no Run, such as an Inbox item's or a
// Task report's recorded Agent. A write a Run proves uses RunActor.
func AgentActor(id string) Actor { return Actor{Type: ActorTypeAgent, ID: id} }

// RunActor attributes the fact to an Agent acting in a Run for that Run's
// Authorizer, the person it works for.
func RunActor(agentID, runID, authorizerID string) Actor {
	return Actor{Type: ActorTypeAgent, ID: agentID, OnBehalfOf: authorizerID, RunID: runID}
}

// SystemActor attributes the fact to the platform itself — a sweeper, a
// scheduler, a reconciliation pass. It carries no id.
func SystemActor() Actor { return Actor{Type: ActorTypeSystem} }

// Valid reports whether the actor names a known type, with an id exactly when
// that type has one. A user or system actor names no person it acted for and
// no Run; an Agent actor names a Run only with the person it acted for.
func (a Actor) Valid() bool {
	switch a.Type {
	case ActorTypeUser:
		return a.ID != "" && a.OnBehalfOf == "" && a.RunID == ""
	case ActorTypeAgent:
		return a.ID != "" && (a.RunID == "" || a.OnBehalfOf != "")
	case ActorTypeSystem:
		return a.ID == "" && a.OnBehalfOf == "" && a.RunID == ""
	default:
		return false
	}
}

func (a Actor) validationError(name string) error {
	if a.Valid() {
		return nil
	}
	return fmt.Errorf("events: %s event with invalid actor %+v", name, a)
}
