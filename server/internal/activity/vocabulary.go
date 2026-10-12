package activity

import "regexp"

// A stored row's entity_type is a domain name and its action is
// "<domain>.<verb>". Most rows use the event bus vocabulary, where a domain
// is one lowercase word. Activity also records facts that are never bus
// events, and their domain may be two words joined by an underscore: the
// project_grant rows (ADR 0017). The feed filter accepts both shapes, so a
// typo is still bad input and a new domain needs no list kept here.
var (
	entityTypeShape = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	actionShape     = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)
)

// ValidEntityTypeFilter reports whether value is shaped like a stored
// entity_type.
func ValidEntityTypeFilter(value string) bool { return entityTypeShape.MatchString(value) }

// ValidActionFilter reports whether value is shaped like a stored action.
func ValidActionFilter(value string) bool { return actionShape.MatchString(value) }
