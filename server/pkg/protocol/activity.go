package protocol

// Activity vocabulary: the closed sets every activity_events row draws from.
// The DB stores plain text, so this file is the single authority — writers
// pick from these constants and readers can rely on the values, exactly like
// the flat event registry in events.go.

// EntityType names the kind of entity an activity entry is about. entity_id
// carries that entity's id (text, so non-uuid keys stay representable).
type EntityType string

const (
	EntityCalendar     EntityType = "calendar"
	EntityCompany      EntityType = "company"
	EntityUser         EntityType = "user"
	EntityAgent        EntityType = "agent"
	EntityTask         EntityType = "task"
	EntityRun          EntityType = "run"
	EntityConversation EntityType = "conversation"
	EntityMessage      EntityType = "message"
	EntityInbox        EntityType = "inbox"
	EntityResource     EntityType = "resource"
	EntityRuntime      EntityType = "runtime"
	EntityApproval     EntityType = "approval"
	EntityQuestion     EntityType = "question"
	EntityDeliverable  EntityType = "deliverable"
	// EntityProject names a Project itself: the subject of the Inbox notice
	// that someone can now see it (project_access_granted, D31).
	EntityProject EntityType = "project"
)

// ValidEntityType reports whether value is one of the registered entity
// types. The baseline database CHECK mirrors this registry and changes with it.
func ValidEntityType(value string) bool {
	switch EntityType(value) {
	case EntityCompany, EntityUser, EntityAgent, EntityTask, EntityRun,
		EntityConversation, EntityMessage, EntityInbox, EntityResource, EntityRuntime,
		EntityApproval, EntityQuestion, EntityDeliverable, EntityCalendar, EntityProject:
		return true
	default:
		return false
	}
}

// Activity actor vocabulary. It is the feed's own three-value set, matched
// by the activity_events actor_type CHECK; an unknown actor defaults to
// "system".
const (
	ActivityActorUser   = "user"
	ActivityActorAgent  = "agent"
	ActivityActorSystem = "system"
)
