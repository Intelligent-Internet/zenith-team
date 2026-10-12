// This file holds the topic, the event names, and the payloads that exist
// only to be published. A payload the REST surface also serves lives with the
// rest of the domain's types; the event carries that projection, it does not
// own it.
//
// Who may see an event lives in cmd/server/realtime_routing.go — a comment
// here says what fact an event records, never who receives it, because a rule
// written in two places is a rule that drifts.

package auth

import "github.com/google/uuid"

// Topic carries the session facts one user's connections need: a session of
// theirs ended, so they drop it without waiting for the next request to fail.
const Topic = "auth"

// EventSessionRevoked records that one of the user's sessions stopped being
// usable.
const EventSessionRevoked = "auth.session_revoked"

// RevokedSession names the ended session and why it ended.
type RevokedSession struct {
	SessionID uuid.UUID               `json:"sessionId"`
	Reason    SessionRevocationReason `json:"reason"`

	// UserID is the routing fact: this frame belongs to one user, and the
	// realtime bridge reads it off the Go value. It stays off the wire — a
	// client being told about its own session learns nothing from being told
	// whose it is.
	UserID uuid.UUID `json:"-"`
}

// TopicUser carries the user's own profile projection.
const TopicUser = "user"

// EventUserUpdated records a change to the user's own profile.
const EventUserUpdated = "user.updated"
