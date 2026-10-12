package protocol

import "github.com/google/uuid"

// ActorKind is the closed vocabulary for who an ActorRef attributes: a user
// or an agent. The realtime envelope's actorType additionally speaks
// "system" for unattributed transitions, but a system actor never appears as
// an ActorRef — the activity feed keeps its own wider ref (nullable identity
// for system actors) for that.
type ActorKind string

const (
	ActorKindUser  ActorKind = "user"
	ActorKindAgent ActorKind = "agent"
)

// ActorRef is the one wire shape for an attributed actor, serialized as
// {id, kind, name, avatarUrl}. avatarUrl is explicit null when the actor has
// no avatar — never absent — so every consumer decodes one shape.
type ActorRef struct {
	ID          uuid.UUID `json:"id"`
	Kind        ActorKind `json:"kind"`
	DisplayName string    `json:"name"`
	AvatarURL   *string   `json:"avatarUrl"`
}
