// This file holds the topic, the event names, and the payloads that exist
// only to be published. A payload the REST surface also serves lives with the
// rest of the domain's types; the event carries that projection, it does not
// own it.
//
// Who may see an event lives in cmd/server/realtime_routing.go — a comment
// here says what fact an event records, never who receives it, because a rule
// written in two places is a rule that drifts.

package attachment

import (
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
)

// Topic is the attachment family. Every fact this domain publishes travels on
// it, and a consumer subscribes once and switches on the event name.
const Topic = "attachment"

// The wire names this domain publishes.
const (
	// EventCompleted announces a finished upload, carrying the completed
	// Response projection.
	EventCompleted = "attachment.completed"

	// EventDeleted is a removal receipt: the row is gone, so the Removal it
	// carries names the attachment rather than snapshotting it.
	EventDeleted = "attachment.deleted"
)

// Removal names the removed attachment and, when it was linked to a
// message, the message and conversation it just left.
type Removal struct {
	AttachmentID   uuid.UUID  `json:"attachmentId"`
	MessageID      *uuid.UUID `json:"messageId,omitempty"`
	ConversationID *uuid.UUID `json:"conversationId,omitempty"`
	Revision       int64      `json:"revision"`

	// CompanyID is a routing fact, read in-process off the Go value: every
	// route for this frame is scoped to the owning company.
	CompanyID uuid.UUID `json:"-"`

	// UploadedByUserID is a routing fact, read in-process: an attachment
	// linked to no message stays private to whoever uploaded it.
	UploadedByUserID uuid.UUID `json:"-"`

	// ConversationKind is a routing fact, read in-process: it is empty when
	// the attachment was linked to no message, "task" when the linked
	// conversation is company-wide, and any other kind means the frame
	// belongs to that conversation's creator alone. It is a plain string
	// because this leaf may not import the conversation domain.
	ConversationKind string `json:"-"`

	// ConversationCreatorUserID is a routing fact, read in-process: the owner
	// of a non-task conversation, and the nil UUID whenever ConversationKind
	// does not select it, so a misread fails closed at the delivery method.
	ConversationCreatorUserID uuid.UUID `json:"-"`

	// ProjectFrame carries the audience when the file left a Task chat: the
	// readers of that chat's Project (ADR 0017).
	auth.ProjectFrame
}

func (r Removal) ProjectFrameScope() (uuid.UUID, bool) {
	return uuid.Nil, r.ConversationKind == "task"
}

func (r Removal) WithProjectAudience(audience *auth.ProjectAudience) any {
	r.Audience = audience
	return r
}

// The router finds this payload by interface; the compiler checks it.
var _ auth.ProjectFramePayload = Removal{}
