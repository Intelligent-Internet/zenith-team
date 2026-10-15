package conversation

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	"boardly.dev/boardly/server/pkg/protocol"
)

const (
	maxMessageAttachments = 20
	runQueueLimit         = 20
	defaultListLimit      = 50
	maxListLimit          = 100
)

type CreateDirectInput struct {
	RequestKey *uuid.UUID
	AgentID    uuid.UUID
	Title      *string
}

type RenameDirectInput struct {
	Title            string
	ExpectedRevision int64
}

type DirectPage struct {
	Conversations []ListItem
}

type CreateDirectResult struct {
	Conversation Response
	Created      bool
}

type ActorKind = protocol.ActorKind

const (
	ActorUser  ActorKind = "user"
	ActorAgent ActorKind = "agent"
)

type ActorRef = protocol.ActorRef

// AgentRef is the agent on the other side of a conversation, denormalized to
// the fields a row or a panel header renders — name, title and the viewer's
// two verdicts — so neither has to resolve an id against the agent roster.
// Both verdicts are the VIEWER's (ADR 0014): RuntimeAccess.Execute is true
// when the viewer has set the agent up on one of their own runtimes, and
// RuntimeAvailable when that runtime is online, usable and signed in. A
// composer whose agent is not set up offers the setup step before a send is
// refused. Nil on a conversation whose agent has been deleted.
type AgentRef struct {
	ID               uuid.UUID              `json:"id"`
	Name             string                 `json:"name"`
	Title            string                 `json:"title"`
	RuntimeAvailable bool                   `json:"runtimeAvailable"`
	RuntimeAccess    protocol.RuntimeAccess `json:"runtimeAccess"`
}

// ListItem is the exact per-user Conversation projection returned by chat
// lists and detail reads. The conversation's own fields (including taskId)
// live on Conversation; Summary is the shared activity summary the events
// also carry, and UserState carries the caller's pin/archive state.
type ListItem struct {
	Conversation Response    `json:"conversation"`
	Agent        *AgentRef   `json:"agent"`
	Summary      ListSummary `json:"summary"`
	UserState    UserState   `json:"userState"`
}

// MessageView is the HTTP history projection. Ready Attachment DTOs remain a
// distinct cacheable resource and never become part of canonical MessageResponse.
type MessageView struct {
	Message     MessageResponse       `json:"message"`
	Attachments []attachment.Response `json:"attachments"`
	// AttachmentsWithheld is true when a ready file on the message was left
	// out for this reader: an active Project file of a Project they do not
	// read (ADR 0017). The reader sees that a file is there, never which.
	AttachmentsWithheld bool `json:"attachmentsWithheld"`

	// carriesProjectFile is true when a file on the message is an active
	// Project file, whether or not this reader may open it. The
	// message.created frame then carries no attachments (ADR 0017).
	carriesProjectFile bool
}

type TargetKind string

const (
	TargetConversation TargetKind = "conversation"
	TargetTask         TargetKind = "task"
)

type MessageTarget struct {
	Kind TargetKind
	ID   uuid.UUID
}

type SkillReference struct {
	Start     int       `json:"start"`
	End       int       `json:"end"`
	SkillID   uuid.UUID `json:"skillId"`
	VersionID uuid.UUID `json:"versionId"`
}

type SendInput struct {
	ConnectorReferences []ConnectorReference
	SkillReferences     []SkillReference
	RequestKey          uuid.UUID
	Content             string
	AttachmentIDs       []uuid.UUID
	ArtifactIDs         []uuid.UUID
	// SuppressUserMessage starts the Agent without writing the prompt into the
	// transcript. It exists for prompts the person never typed — onboarding
	// hands the CEO Agent its opening instruction, and showing the founder a
	// message they did not write is worse than showing nothing. The words
	// still reach the Agent: they are frozen into the Run's context, which is
	// where an Agent reads its prompt from in any case.
	//
	// It hides nothing that matters for accountability: the Run, its context
	// and everything the Agent does are recorded exactly as always.
	SuppressUserMessage bool
}

type MessageListOptions struct {
	AfterSeq   int64
	ThroughSeq *int64
	Limit      int
	// FromNewest anchors the page at the far end of the range instead: the
	// newest Limit messages at or below the bound, rather than the first Limit
	// after AfterSeq, which is then ignored. A panel opening a conversation
	// wants a count of messages, and the seq range that would hold that many
	// is not knowable before reading them. See newestMessageViews.
	FromNewest bool
}

// MessagePage is a bounded message window with its navigation scalars.
// Whether older or newer rows exist is derived by the client from the
// window's own seq range against EarliestAvailableSeq and ThroughSeq, so the
// page carries no hasOlder/hasNewer flags.
type MessagePage struct {
	CompanyID            uuid.UUID     `json:"-"`
	ConversationID       uuid.UUID     `json:"-"`
	Target               MessageTarget `json:"-"`
	Items                []MessageView
	EarliestAvailableSeq int64
	ThroughSeq           int64
}

// MessageWindow selects which slice of a conversation's messages a combined
// detail read returns. With neither cursor set the window is the newest Limit
// messages, counted rather than ranged: seq is gap-free over the whole stream,
// but the chat surface is two of its six types, so the last Limit seq numbers
// are not the last Limit messages. See newestMessageViews.
type MessageWindow struct {
	Limit      int
	AfterSeq   *int64
	ThroughSeq *int64
}

// DirectDetail is one direct conversation with a bounded window of its messages
// and the panel's co-resident state — agent, per-user read state, run summary
// and run queue — assembled for the combined read so the panel paints from one
// response.
type DirectDetail struct {
	Conversation         Response
	Agent                *AgentRef
	Messages             []MessageView
	EarliestAvailableSeq int64
	ThroughSeq           int64
	ResetRequired        bool
	UserState            UserState
	Runs                 RunsSummary
	RunQueue             RunQueueProjection
}

// MessageContextPage is a bounded, authorization-checked history window used
// to deep-link to one immutable Message without loading the full Conversation.
// Like every message window it carries only the navigation scalars; missing
// older/newer rows are derived from the items against the bounds.
type MessageContextPage struct {
	Items                []MessageView `json:"items"`
	TargetSeq            int64         `json:"targetSeq"`
	EarliestAvailableSeq int64         `json:"earliestAvailableSeq"`
	ThroughSeq           int64         `json:"throughSeq"`
}

type SendDisposition string

const (
	SendDispositionMessage SendDisposition = "message"
	SendDispositionQueued  SendDisposition = "queued"
	SendDispositionRemoved SendDisposition = "removed"
)

// CurrentRun is the conversation's oldest live Run. RuntimeID names the
// machine it is placed on to that machine's registrant alone, and is nil for
// every other viewer (ADR 0014).
type CurrentRun struct {
	RuntimeID  *uuid.UUID `json:"runtimeId"`
	WaitReason string     `json:"waitReason,omitempty"`
	ID         uuid.UUID  `json:"id"`
	Status     RunStatus  `json:"status"`
	QueuedAt   time.Time  `json:"queuedAt"`
}

// RunQueueItem is one prompt waiting for a Run. InitiatedBy is the member
// who wrote it: in a Task chat that can be a colleague of the assignee
// (ADR 0014), whose words the assignee's next Run carries.
type RunQueueItem struct {
	ID          uuid.UUID             `json:"id"`
	MessageID   uuid.UUID             `json:"messageId"`
	Content     string                `json:"content"`
	Attachments []attachment.Response `json:"attachments"`
	InitiatedBy ActorRef              `json:"initiatedBy"`
	QueuedAt    time.Time             `json:"queuedAt"`
	Revision    int64                 `json:"revision"`
}

type RunQueueProjection struct {
	CurrentRun *CurrentRun    `json:"currentRun"`
	Items      []RunQueueItem `json:"items"`
	Limit      int            `json:"limit"`
}

type RunQueueOrderItem struct {
	ID               uuid.UUID `json:"id"`
	ExpectedRevision int64     `json:"expectedRevision"`
}

type RunQueueOrderInput struct {
	Items []RunQueueOrderItem
}

type DeleteRunQueueItemInput struct {
	ExpectedRevision int64
}

// SendResult reports what one send became. RunID names the Run that took the
// words, and is nil for the queued and removed dispositions: runs.id is a
// database-generated UUID the promotion transaction takes by inserting the Run, so a prompt
// still waiting behind a live Run has no Run identity to report yet. A caller
// following a deferred prompt follows QueueItem.ID.
type SendResult struct {
	CompanyID        uuid.UUID     `json:"-"`
	ConversationKind Kind          `json:"-"`
	Target           MessageTarget `json:"-"`
	Disposition      SendDisposition
	// Suppressed reports that no transcript message was written, so View is
	// empty and a caller must not present one.
	Suppressed bool
	View       MessageView
	QueueItem  *RunQueueItem
	RunID      *uuid.UUID
	Created    bool `json:"-"`
}

type UpdateUserStateInput struct {
	ExpectedRevision int64
	Pinned           *bool
	Archived         *bool
}

func normalizeCreateDirectInput(input CreateDirectInput) (CreateDirectInput, error) {
	if !util.ValidUUID(input.AgentID) || input.RequestKey != nil && !util.ValidUUID(*input.RequestKey) {
		return CreateDirectInput{}, ErrInvalidInput
	}
	if input.Title != nil {
		title := protocol.TrimContractWhitespace(*input.Title)
		if !validTitle(title) {
			return CreateDirectInput{}, ErrInvalidInput
		}
		input.Title = &title
	}
	return input, nil
}

func normalizeRenameDirectInput(input RenameDirectInput) (RenameDirectInput, error) {
	input.Title = protocol.TrimContractWhitespace(input.Title)
	if !validTitle(input.Title) || input.ExpectedRevision < 1 ||
		input.ExpectedRevision > protocol.MaxJSONSafeInteger {
		return RenameDirectInput{}, ErrInvalidInput
	}
	return input, nil
}

func normalizeSendInput(input SendInput) (SendInput, error) {
	if err := validateMessageConnectorReferences(
		input.Content,
		input.ConnectorReferences,
		input.SkillReferences,
	); err != nil {
		return SendInput{}, err
	}
	if !util.ValidUUID(input.RequestKey) || len(input.AttachmentIDs) > maxMessageAttachments ||
		len(
			input.ArtifactIDs,
		) > 10 || (input.SuppressUserMessage && (len(input.ArtifactIDs) > 0 || len(input.SkillReferences) > 0 || len(input.ConnectorReferences) > 0)) {
		return SendInput{}, ErrInvalidInput
	}
	originalContent := input.Content
	input.Content = protocol.TrimContractWhitespace(input.Content)
	if len(input.SkillReferences) > 0 {
		input.SkillReferences = slices.Clone(input.SkillReferences)
		leading := strings.Index(originalContent, input.Content)
		shift := utf16Len(originalContent[:leading])
		for i := range input.SkillReferences {
			input.SkillReferences[i].Start -= shift
			input.SkillReferences[i].End -= shift
		}
	}
	if len(input.ConnectorReferences) > 0 {
		input.ConnectorReferences = slices.Clone(input.ConnectorReferences)
		leading := strings.Index(originalContent, input.Content)
		shift := utf16Len(originalContent[:leading])
		for i := range input.ConnectorReferences {
			input.ConnectorReferences[i].Start -= shift
			input.ConnectorReferences[i].End -= shift
		}
	}
	if input.Content == "" && len(input.ArtifactIDs) > 0 {
		input.Content = "Attached artifacts."
	}
	if !validMessageContent(input.Content) {
		return SendInput{}, ErrInvalidInput
	}
	seen := make(map[uuid.UUID]struct{}, len(input.AttachmentIDs))
	for _, attachmentID := range input.AttachmentIDs {
		if !util.ValidUUID(attachmentID) {
			return SendInput{}, ErrInvalidInput
		}
		if _, exists := seen[attachmentID]; exists {
			return SendInput{}, ErrInvalidInput
		}
		seen[attachmentID] = struct{}{}
	}
	artifactIDs := make(map[uuid.UUID]bool, len(input.ArtifactIDs))
	for _, id := range input.ArtifactIDs {
		if !util.ValidUUID(id) || artifactIDs[id] {
			return SendInput{}, ErrInvalidInput
		}
		artifactIDs[id] = true
	}
	if input.AttachmentIDs == nil {
		input.AttachmentIDs = []uuid.UUID{}
	}
	return input, nil
}

func normalizeRunQueueOrderInput(input RunQueueOrderInput) (RunQueueOrderInput, error) {
	if len(input.Items) < 1 || len(input.Items) > runQueueLimit {
		return RunQueueOrderInput{}, ErrInvalidInput
	}
	// The revision bound is the request binding's — `revision` on an
	// optional[int64] now reads through the wrapper. What is left here is the
	// pair no tag can state: `binding:"uuid"` is a hex shape that accepts the
	// nil UUID, and nothing in a tag or a column can say that two items of one
	// request must not name the same queue item.
	seen := make(map[uuid.UUID]struct{}, len(input.Items))
	for _, item := range input.Items {
		if !util.ValidUUID(item.ID) {
			return RunQueueOrderInput{}, ErrInvalidInput
		}
		if _, exists := seen[item.ID]; exists {
			return RunQueueOrderInput{}, ErrInvalidInput
		}
		seen[item.ID] = struct{}{}
	}
	return input, nil
}

func normalizeMessageListOptions(options MessageListOptions) (MessageListOptions, error) {
	if options.Limit == 0 {
		options.Limit = defaultListLimit
	}
	if options.Limit < 1 || options.Limit > maxListLimit || options.AfterSeq < 0 ||
		options.AfterSeq > protocol.MaxJSONSafeInteger ||
		options.ThroughSeq != nil &&
			(*options.ThroughSeq < options.AfterSeq || *options.ThroughSeq > protocol.MaxJSONSafeInteger) {
		return MessageListOptions{}, ErrInvalidInput
	}
	return options, nil
}

// normalizeUpdateUserStateInput keeps its own revision bound rather than the
// shared binding:"revision" rule its siblings use: user state accepts a
// revision of 0, the value that claims the row synthesized on first read,
// where every other surface starts at 1. The rule is genuinely different, so
// this guard is the boundary for this route and not a duplicate of one.
func normalizeUpdateUserStateInput(input UpdateUserStateInput) (UpdateUserStateInput, error) {
	if input.ExpectedRevision < 0 || input.ExpectedRevision > protocol.MaxJSONSafeInteger ||
		input.Pinned == nil && input.Archived == nil {
		return UpdateUserStateInput{}, ErrInvalidInput
	}
	return input, nil
}

func validMessageTarget(target MessageTarget) bool {
	return (target.Kind == TargetConversation || target.Kind == TargetTask) && util.ValidUUID(target.ID)
}

func validTitle(value string) bool {
	return value != "" && utf8.ValidString(value) && protocol.TrimContractWhitespace(value) == value &&
		utf8.RuneCountInString(value) <= 120
}

func validMessageContent(value string) bool {
	return value != "" && utf8.ValidString(value) && protocol.TrimContractWhitespace(value) == value &&
		utf8.RuneCountInString(value) <= 64*1024 && len(value) <= 64*1024
}

func newMessageCreatedPayload(
	companyID uuid.UUID,
	kind Kind,
	createdByUserID *uuid.UUID,
	view MessageView,
) PostedMessage {
	attachments := view.Attachments
	if attachments == nil || view.carriesProjectFile {
		// A Project file follows its Projects' readers, never this frame's
		// audience: send none and let each client refetch its own view.
		attachments = []attachment.Response{}
	}
	return PostedMessage{
		MessageResponse:             view.Message,
		Attachments:                 attachments,
		AttachmentsWithheld:         view.carriesProjectFile,
		CompanyID:                   companyID,
		ConversationKind:            kind,
		ConversationCreatedByUserID: createdByUserID,
	}
}

func newConversationEvent(
	actorID uuid.UUID,
	taskID *uuid.UUID,
	value Response,
	name string,
	summary *ListSummary,
) events.Event {
	return NewMemberEvent(actorID, taskID, value, name, summary)
}

// NewMemberEvent constructs a Conversation lifecycle event for a mutation
// owned by another domain transaction, such as Task creation. The caller
// stages it and publishes only after that transaction commits. taskID is a
// defensive backfill for a caller that resolved the task separately; a
// projection built from the conversation row already carries it. A nil
// summary means "no activity yet": the payload then carries the zero-activity
// summary a freshly created conversation truthfully has, because created and
// updated share one payload shape — which is also why the event name is a
// parameter rather than a second constructor.
func NewMemberEvent(
	actorID uuid.UUID,
	taskID *uuid.UUID,
	value Response,
	name string,
	summary *ListSummary,
) events.Event {
	return newConversationEventForActor(
		events.UserActor(actorID.String()), taskID, value, name, summary,
	)
}

func newConversationEventForActor(
	actor events.Actor,
	taskID *uuid.UUID,
	value Response,
	name string,
	summary *ListSummary,
) events.Event {
	if value.TaskID == nil {
		value.TaskID = taskID
	}
	if summary == nil {
		summary = &ListSummary{
			UpdatedAt: value.UpdatedAt,
			Runs:      RunsSummary{},
		}
	}
	return events.Event{
		EntityType:     Topic,
		Name:           name,
		CompanyID:      value.CompanyID,
		CreatedAt:      time.Now().UTC(),
		TaskID:         util.UUIDToValue(value.TaskID),
		ConversationID: value.ID,
		Actor:          actor,
		Payload:        Snapshot{Conversation: value, Summary: *summary},
	}
}

func newMessageCreatedEvent(
	actorID uuid.UUID,
	value Response,
	message MessageView,
) events.Event {
	return NewMemberMessageCreatedEvent(
		actorID, value.CompanyID, value.Kind, value.CreatedByUserID, message,
	)
}

// NewMemberMessageCreatedEvent constructs the canonical item projection event
// for a user-authored item inserted by another domain transaction. The
// company, kind and creator are the conversation's own routing facts: they
// stay off the wire and decide, in the consumer that delivers, whether the
// frame reaches the whole company or only the member who opened the chat.
//
// message.created belongs to the user path alone now, and the item is always
// a member's: everything an Agent produces enters the one stream through
// execution.AppendTranscriptItem, which redacts every text and error part
// before the row is written and broadcasts the committed item as run.message.
// The Agent-authored constructor and the redaction guard it carried are gone
// with the second write they existed for.
func NewMemberMessageCreatedEvent(
	actorID, companyID uuid.UUID,
	kind Kind,
	createdByUserID *uuid.UUID,
	message MessageView,
) events.Event {
	return NewMemberMessageCreatedEventBy(
		events.UserActor(actorID.String()), companyID, kind, createdByUserID, message,
	)
}

// NewMemberMessageCreatedEventBy is NewMemberMessageCreatedEvent with the
// actor named in full. A member's item a Run wrote for them, such as the
// prompt of a Task a Run created and started, names the Run's Agent acting
// for that member (D31).
func NewMemberMessageCreatedEventBy(
	actor events.Actor,
	companyID uuid.UUID,
	kind Kind,
	createdByUserID *uuid.UUID,
	message MessageView,
) events.Event {
	payload := newMessageCreatedPayload(companyID, kind, createdByUserID, message)
	return events.Event{
		EntityType:     TopicMessage,
		Name:           EventMessageCreated,
		CompanyID:      companyID,
		CreatedAt:      time.Now().UTC(),
		RunID:          payload.RunID,
		ConversationID: payload.ConversationID,
		Actor:          actor,
		Payload:        payload,
	}
}

// NewMessageUpdatedEvent tells whoever may see a message to refetch its
// attachments, for a change another domain made in its own transaction (a
// file on the message joined or left a Project). The kind and creator are
// the conversation's routing facts.
func NewMessageUpdatedEvent(
	actorID, companyID, conversationID, messageID uuid.UUID,
	kind Kind,
	createdByUserID *uuid.UUID,
) events.Event {
	return events.Event{
		EntityType:     TopicMessage,
		Name:           EventMessageUpdated,
		CompanyID:      companyID,
		CreatedAt:      time.Now().UTC(),
		ConversationID: conversationID,
		Actor:          events.UserActor(actorID.String()),
		Payload: MessageUpdated{
			ConversationID: conversationID, MessageID: messageID,
			ConversationKind: kind, ConversationCreatedByUserID: createdByUserID,
		},
	}
}

// newConversationUserStateEvent builds the conversation.read watermark event.
// The state already names the one member it belongs to; companyID completes
// the route.
func newConversationUserStateEvent(
	actorID, companyID uuid.UUID,
	state UserState,
) events.Event {
	state.CompanyID = companyID
	return events.Event{
		EntityType:     Topic,
		Name:           EventConversationRead,
		CompanyID:      companyID,
		CreatedAt:      time.Now().UTC(),
		ConversationID: state.ConversationID,
		Actor:          events.UserActor(actorID.String()),
		Payload:        state,
	}
}

// Kind separates the conversations the product has: one owned by a Task and
// read by the readers of its Project (ADR 0017), and a direct chat belonging
// to the member who opened it. With the Task chat's Project audience, it is
// the routing rule for every conversation-derived event.
type Kind string

// Conversation kinds.
const (
	KindTask     Kind = "task"
	KindDirect   Kind = "direct"
	KindSchedule Kind = "schedule"
)

// Response marshals as the strict public discriminated union. Direct-only fields
// are omitted—not emitted as null—from Task conversations. TaskID rides the
// conversation itself (explicit null on a direct chat) so no surface has to
// correlate two objects to learn what a conversation is about.
//
// It carries its own routing facts on the wire: CompanyID, Kind and
// CreatedByUserID are what a consumer reads to decide who sees the frame.
type Response struct {
	ID              uuid.UUID  `json:"id"`
	CompanyID       uuid.UUID  `json:"companyId"`
	Kind            Kind       `json:"kind"`
	TaskID          *uuid.UUID `json:"taskId"`
	AgentID         *uuid.UUID `json:"agentId,omitempty"`
	CreatedByUserID *uuid.UUID `json:"createdByUserId,omitempty"`
	Title           *string    `json:"title"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	Revision        int64      `json:"revision"`
}

// RunStatus is the lifecycle of the Run a conversation summary reports.
type RunStatus string

// Run statuses.
const (
	RunStatusQueued                RunStatus = "queued"
	RunStatusDeferred              RunStatus = "deferred"
	RunStatusDispatched            RunStatus = "dispatched"
	RunStatusWaitingLocalDirectory RunStatus = "waiting_local_directory"
	RunStatusRunning               RunStatus = "running"
	RunStatusCompleted             RunStatus = "completed"
	RunStatusFailed                RunStatus = "failed"
	RunStatusCancelled             RunStatus = "cancelled"
)

// ItemType is the conversation_items type column, which is the whole
// visibility rule: the chat surface is exactly the two values below, and the
// four run-detail types ('reasoning', 'tool_call', 'tool_result', 'error')
// are filtered out in SQL, so they never reach a projection carrying this.
type ItemType string

// Chat-visible item types.
const (
	ItemTypeUserMessage ItemType = "user_message"
	ItemTypeAgentText   ItemType = "agent_text"
)

// LastMessageSummary is the preview line inside a conversation summary.
// Content is omitted for an item that carries no displayable text, and
// contentTruncated is present only when the preview was cut server-side.
type LastMessageSummary struct {
	Seq              int64              `json:"seq"`
	Content          string             `json:"content,omitempty"`
	ContentTruncated bool               `json:"contentTruncated,omitempty"`
	CreatedAt        time.Time          `json:"createdAt"`
	Type             ItemType           `json:"type"`
	AuthorKind       protocol.ActorKind `json:"authorKind"`
}

// LatestRunSummary is the most recent Run of a conversation, as a list row
// and an event frame both render it.
type LatestRunSummary struct {
	RunID         uuid.UUID `json:"runId"`
	AgentID       uuid.UUID `json:"agentId"`
	Status        RunStatus `json:"status"`
	CreatedAt     time.Time `json:"createdAt"`
	FailureReason *string   `json:"failureReason"`
	Attempt       int32     `json:"attempt"`
	MaxAttempts   int32     `json:"maxAttempts"`
}

// RunsSummary counts a conversation's queued and active Runs and names its
// latest one.
type RunsSummary struct {
	QueuedCount int64             `json:"queuedCount"`
	ActiveCount int64             `json:"activeCount"`
	Latest      *LatestRunSummary `json:"latest"`
}

// ListSummary is the one conversation activity summary wire type: the
// `summary` member of a list row and the `summary` slot of the
// conversation.created / conversation.updated event payloads are these
// identical bytes, so an event frame writes into a row without reshaping.
// LastMessage is explicit null on a conversation with no messages.
type ListSummary struct {
	UpdatedAt   time.Time           `json:"updatedAt"`
	LastMessage *LastMessageSummary `json:"lastMessage"`
	Runs        RunsSummary         `json:"runs"`
}

// UserState is personal chat filing: creator pin/archive for direct chats,
// and viewer-private pin/archive for task chats. Unread remains owned by Inbox.
type UserState struct {
	ConversationID uuid.UUID  `json:"conversationId"`
	UserID         uuid.UUID  `json:"userId"`
	PinnedAt       *time.Time `json:"pinnedAt"`
	ArchivedAt     *time.Time `json:"archivedAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	Revision       int64      `json:"revision"`

	// CompanyID is a routing fact, read in-process off the Go value and
	// never marshaled: a reader told about its own watermark learns nothing
	// from being told which company it is in, and UserID already names the
	// one member this frame belongs to.
	CompanyID uuid.UUID `json:"-"`
}

// MessageResponse is the canonical socket-safe item of the chat surface.
// Everything about it is immutable: an item is written once and never
// updated, so there is no pending-to-terminal envelope any more.
//
// RunID is not optional and not "the Run that produced this". Every item
// belongs to exactly one Run — the user's words to the Run that consumed
// them, the agent's answer to the Run that wrote it — which is what the
// NOT NULL label on the row says.
type MessageResponse struct {
	ConnectorReferences []ConnectorReference `json:"connectorReferences,omitempty"`
	SkillReferences     []SkillReference     `json:"skillReferences,omitempty"`
	ID                  uuid.UUID            `json:"id"`
	ConversationID      uuid.UUID            `json:"conversationId"`
	Seq                 int64                `json:"seq"`
	Type                ItemType             `json:"type"`
	Author              protocol.ActorRef    `json:"author"`
	RunID               uuid.UUID            `json:"runId"`
	Content             string               `json:"content"`
	CreatedAt           time.Time            `json:"createdAt"`
}
