// The topics this domain publishes, its event names, and the payloads that
// exist only to be published.
//
// A payload the REST surface also serves stays with the domain's other types:
// the event carries the domain's projection, it does not own it. Who may see
// an event lives in cmd/server/realtime_routing.go — a comment here says what
// fact an event records, never who receives it, because a rule written in two
// places is a rule that drifts.

package conversation

import (
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/attachment"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
)

// Topic carries the conversation lifecycle.
//
// created and updated share the {conversation, summary} Snapshot, so a client
// runs one upsert handler for either. read carries UserState: one member's own
// pin/archive/read watermark.
const Topic = "conversation"

// The conversation.* wire names.
const (
	EventConversationCreated = "conversation.created"
	EventConversationUpdated = "conversation.updated"
	EventConversationRead    = "conversation.read"
)

// Snapshot is the one payload conversation.created and
// conversation.updated both carry: the owning domain's public projection with
// the same activity summary a list row renders, so either frame writes
// straight into existing cache entries. The conversation carries its own
// taskId; there is no wrapper-level copy. Per-user state is intentionally
// absent because a Task conversation update can be delivered to several
// members.
//
// Its routing facts are the conversation's own: Conversation.CompanyID,
// Conversation.Kind and Conversation.CreatedByUserID.
type Snapshot struct {
	Conversation Response    `json:"conversation"`
	Summary      ListSummary `json:"summary"`
	// ProjectFrame carries a Task chat frame's audience, the readers of
	// its Project (ADR 0017).
	auth.ProjectFrame
}

// TopicMessage carries durable message rows. A message is a different fact
// with a different shape, so it travels under its own name rather than as a
// conversation frame.
const TopicMessage = "message"

// The message.* wire names.
const (
	// EventMessageCreated is a durable message row with its attachments
	// nested exactly as the conversation read returns them.
	EventMessageCreated = "message.created"
	// EventMessageUpdated says only that one message's attachments may now
	// read differently: a file on it joined or left a Project, and a Project
	// file shows only to that Project's readers (ADR 0017). A client
	// refetches that message's attachments through its own filtered read.
	EventMessageUpdated = "message.updated"
)

// PostedMessage is the committed message flattened with its ready
// attachments, exactly as the conversation history read returns the pair.
//
// AttachmentsWithheld is true when a file on the message is an active
// Project file. Such a file follows its Projects' readers, not this frame's
// audience, so the frame then carries no attachments at all and the client
// refetches the message through its own read, where the same field says
// whether a file was left out for that reader. One name on both surfaces.
type PostedMessage struct {
	MessageResponse
	Attachments         []attachment.Response `json:"attachments"`
	AttachmentsWithheld bool                  `json:"attachmentsWithheld"`

	// CompanyID, ConversationKind and ConversationCreatedByUserID are
	// routing facts, read in-process off the Go value and never marshaled.
	// The message projection names only its conversation, and who may see a
	// conversation's words is decided by that conversation's kind and
	// creator — a Task chat reaches its Project's readers (ADR 0017), a
	// direct chat only the member who opened it.
	CompanyID                   uuid.UUID  `json:"-"`
	ConversationKind            Kind       `json:"-"`
	ConversationCreatedByUserID *uuid.UUID `json:"-"`
	// ProjectFrame carries a Task chat frame's audience (ADR 0017).
	auth.ProjectFrame
}

// MessageUpdated is the opaque message.updated payload: which message to
// refetch, nothing about what changed. The kind and creator are the
// conversation's routing facts and never leave the process.
type MessageUpdated struct {
	ConversationID uuid.UUID `json:"conversationId"`
	MessageID      uuid.UUID `json:"messageId"`

	ConversationKind            Kind       `json:"-"`
	ConversationCreatedByUserID *uuid.UUID `json:"-"`
	auth.ProjectFrame
}

// A Task chat's frames reach the readers of its Project (ADR 0017); every
// other chat keeps the route its kind and creator give it. The Project is
// read from the chat the envelope names.

func (s Snapshot) ProjectFrameScope() (uuid.UUID, bool) {
	return uuid.Nil, s.Conversation.Kind == KindTask
}

func (s Snapshot) WithProjectAudience(audience *auth.ProjectAudience) any {
	s.Audience = audience
	return s
}

func (m PostedMessage) ProjectFrameScope() (uuid.UUID, bool) {
	return uuid.Nil, m.ConversationKind == KindTask
}

func (m PostedMessage) WithProjectAudience(audience *auth.ProjectAudience) any {
	m.Audience = audience
	return m
}

func (m MessageUpdated) ProjectFrameScope() (uuid.UUID, bool) {
	return uuid.Nil, m.ConversationKind == KindTask
}

func (m MessageUpdated) WithProjectAudience(audience *auth.ProjectAudience) any {
	m.Audience = audience
	return m
}

// The router finds these payloads by interface, so a changed method would
// silently drop their frames; the compiler checks them instead.
var (
	_ auth.ProjectFramePayload = Snapshot{}
	_ auth.ProjectFramePayload = PostedMessage{}
	_ auth.ProjectFramePayload = MessageUpdated{}
)

const (
	EventDiscussionChanged       = "conversation.discussion_changed"
	EventDiscussionHuddleChanged = "conversation.discussion_huddle_changed"
	EventDiscussionStateChanged  = "conversation.discussion_state_changed"
	EventDiscussionAccessRevoked = "conversation.discussion_access_revoked"
	EventChannelAgentsChanged    = "conversation.channel_agents_changed"
	EventDiscussionResync        = "conversation.discussion_resync"
)

// ChannelAgentsChanged invalidates the channel's authorized Agent views.
// ActivityOnly lets transcript hints avoid rereading unchanged status lists.
type ChannelAgentsChanged struct {
	ConversationID uuid.UUID `json:"conversationId"`
	ActivityOnly   bool      `json:"activityOnly,omitempty"`
}

type DiscussionChanged struct {
	ConversationID uuid.UUID `json:"conversationId"`
	HeadSeq        int64     `json:"headSeq"`
	Revision       int64     `json:"revision"`
}

type DiscussionStateChanged struct {
	ConversationID uuid.UUID `json:"conversationId"`
	Revision       int64     `json:"revision"`
	UserID         uuid.UUID `json:"-"`
}

type DiscussionAccessRevoked struct {
	ConversationID  uuid.UUID `json:"conversationId"`
	GrantGeneration int64     `json:"grantGeneration"`
	UserID          uuid.UUID `json:"-"`
}
