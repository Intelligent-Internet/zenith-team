package conversation

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/attachment"
)

const (
	maxDiscussionBodyBytes       = 64 << 10
	maxDiscussionItemBytes       = 128 << 10
	maxDiscussionPageBytes       = 512 << 10
	maxDiscussionInteger   int64 = 9007199254740991
)

var (
	ErrDiscussionNotFound         = errors.New("discussion not found")
	ErrDiscussionForbidden        = errors.New("discussion operation forbidden")
	ErrDiscussionInvalid          = errors.New("invalid discussion input")
	ErrDiscussionRevisionConflict = errors.New("discussion revision conflict")
	ErrDiscussionRequestConflict  = errors.New("discussion request conflict")
	ErrDiscussionCursorExpired    = errors.New("discussion cursor expired")
	ErrDiscussionInvalidCursor    = errors.New("invalid discussion cursor")
	ErrDiscussionReadOnly         = errors.New("discussion is read-only")
	ErrDiscussionLimit            = errors.New("discussion limit reached")
	ErrDiscussionUnavailable      = errors.New("discussion unavailable")
	// ErrDiscussionAgentRequired refuses a message that tags an Agent the
	// conversation does not have, so a client can offer to add it.
	ErrDiscussionAgentRequired = errors.New("discussion Agent is not in the conversation")
)

type DiscussionType string

const (
	DiscussionTypeHumanDirect DiscussionType = "human_direct"
	DiscussionTypeChannel     DiscussionType = "channel"
)

type DiscussionBody struct {
	Version    int               `json:"version"`
	Markdown   string            `json:"markdown"`
	References []json.RawMessage `json:"references"`
}

type DiscussionAuthor struct {
	Type        string     `json:"type"`
	ID          *uuid.UUID `json:"id"`
	DisplayName string     `json:"displayName"`
	AvatarURL   *string    `json:"avatarUrl"`
}

type DiscussionSystem struct {
	AgentName       string      `json:"agentName,omitempty"`
	SubjectUserIDs  []uuid.UUID `json:"subjectUserIds,omitempty"`
	SubjectAgentIDs []uuid.UUID `json:"subjectAgentIds,omitempty"`
	Action          string      `json:"action"`
	ActorUserID     *uuid.UUID  `json:"actorUserId"`
	SubjectUserID   *uuid.UUID  `json:"subjectUserId,omitempty"`
	SubjectAgentID  *uuid.UUID  `json:"subjectAgentId,omitempty"`
}

type DiscussionMessage struct {
	Huddle         *HuddleCard           `json:"huddle,omitempty"`
	ID             uuid.UUID             `json:"id"`
	ConversationID uuid.UUID             `json:"conversationId"`
	Seq            int64                 `json:"seq"`
	Revision       int64                 `json:"revision"`
	Type           string                `json:"type"`
	Author         DiscussionAuthor      `json:"author"`
	Body           *DiscussionBody       `json:"body"`
	CreatedAt      time.Time             `json:"createdAt"`
	EditedAt       *time.Time            `json:"editedAt"`
	DeletedAt      *time.Time            `json:"deletedAt"`
	ThreadRootID   *uuid.UUID            `json:"threadRootId"`
	Attachments    []attachment.Response `json:"attachments"`
	// AttachmentsWithheld is true when a ready file on the message was left
	// out for this reader: an active Project file of a Project they do not
	// read (ADR 0017).
	AttachmentsWithheld bool                        `json:"attachmentsWithheld"`
	System              *DiscussionSystem           `json:"system,omitempty"`
	Pinned              bool                        `json:"pinned"`
	ThreadSummary       *DiscussionThreadSummary    `json:"threadSummary,omitempty"`
	ReactionSummary     []DiscussionReactionSummary `json:"reactionSummary"`
	Artifacts           []DiscussionArtifact        `json:"artifacts"`
}

type DiscussionCapabilities struct {
	ReadHistory   bool `json:"readHistory"`
	Send          bool `json:"send"`
	ManageMembers bool `json:"manageMembers"`
	Archive       bool `json:"archive"`
	Restore       bool `json:"restore"`
	Attachments   bool `json:"attachments"`
	References    bool `json:"references"`
	Threads       bool `json:"threads"`
	Edit          bool `json:"edit"`
	Delete        bool `json:"delete"`
	Reactions     bool `json:"reactions"`
	Pins          bool `json:"pins"`
	ReadState     bool `json:"readState"`
	Search        bool `json:"search"`
	StartHuddle   bool `json:"startHuddle"`
}

type DiscussionUserState struct {
	ReadThroughSeq    int64      `json:"readThroughSeq"`
	ManualUnreadSeq   *int64     `json:"manualUnreadSeq"`
	Revision          int64      `json:"revision"`
	PinnedAt          *time.Time `json:"pinnedAt"`
	HiddenAt          *time.Time `json:"hiddenAt"`
	NotificationLevel string     `json:"notificationLevel"`
	UnreadCount       *int64     `json:"unreadCount"`
}

type DiscussionPeer struct {
	UserID      uuid.UUID `json:"userId"`
	DisplayName string    `json:"displayName"`
	AvatarURL   *string   `json:"avatarUrl"`
	Active      bool      `json:"active"`
}

type DiscussionSummary struct {
	IsGeneral             bool            `json:"isGeneral"`
	ID                    uuid.UUID       `json:"id"`
	CompanyID             uuid.UUID       `json:"companyId"`
	Type                  DiscussionType  `json:"type"`
	Title                 string          `json:"title"`
	Topic                 *string         `json:"topic"`
	ArchivedAt            *time.Time      `json:"archivedAt"`
	CreatedAt             time.Time       `json:"createdAt"`
	UpdatedAt             time.Time       `json:"updatedAt"`
	ConversationRevision  int64           `json:"conversationRevision"`
	ConfigurationRevision *int64          `json:"configurationRevision"`
	HeadSeq               int64           `json:"headSeq"`
	MemberCount           int64           `json:"memberCount"`
	AgentCount            int64           `json:"agentCount"`
	Peer                  *DiscussionPeer `json:"peer,omitempty"`
	// BoardID names the Board whose chat this is; nil for any other chat.
	BoardID      *uuid.UUID             `json:"boardId"`
	Capabilities DiscussionCapabilities `json:"capabilities"`
	UserState    *DiscussionUserState   `json:"userState"`
}

type DiscussionState struct {
	ConversationID            uuid.UUID              `json:"conversationId"`
	ConversationRevision      int64                  `json:"conversationRevision"`
	ConfigurationRevision     *int64                 `json:"configurationRevision"`
	HeadSeq                   int64                  `json:"headSeq"`
	GrantGeneration           *int64                 `json:"grantGeneration"`
	GrantRevision             *int64                 `json:"grantRevision"`
	CompanyMembershipRevision int64                  `json:"companyMembershipRevision"`
	Capabilities              DiscussionCapabilities `json:"capabilities"`
	UserState                 *DiscussionUserState   `json:"userState"`
}

type DiscussionMember struct {
	UserID      uuid.UUID `json:"userId"`
	DisplayName string    `json:"displayName"`
	AvatarURL   *string   `json:"avatarUrl"`
	Active      bool      `json:"active"`
	Generation  int64     `json:"generation"`
	Revision    int64     `json:"revision"`
}

type DiscussionAgent struct {
	AgentID    uuid.UUID `json:"agentId"`
	Name       string    `json:"name"`
	Generation int64     `json:"generation"`
	Revision   int64     `json:"revision"`
}

type CreateHumanDirectInput struct {
	PeerUserIDs []uuid.UUID
	AgentIDs    []uuid.UUID
}

type InviteHumanDirectInput struct {
	MemberUserIDs []uuid.UUID
	AgentIDs      []uuid.UUID
}

type HumanDirectResult struct {
	Conversation DiscussionSummary `json:"conversation"`
	Created      bool              `json:"created"`
}
type HumanDirectDetail struct {
	Conversation DiscussionSummary  `json:"conversation"`
	Members      []DiscussionMember `json:"members"`
	Agents       []DiscussionAgent  `json:"agents"`
}
type ChannelDetail struct {
	Conversation DiscussionSummary  `json:"conversation"`
	Members      []DiscussionMember `json:"members"`
	Agents       []DiscussionAgent  `json:"agents"`
}

// ChannelManagement deliberately carries no message state or personal projection.
type ChannelManagement struct {
	IsGeneral             bool               `json:"isGeneral"`
	ID                    uuid.UUID          `json:"id"`
	CompanyID             uuid.UUID          `json:"companyId"`
	Name                  string             `json:"name"`
	Topic                 string             `json:"topic"`
	ArchivedAt            *time.Time         `json:"archivedAt"`
	ConfigurationRevision int64              `json:"configurationRevision"`
	Members               []DiscussionMember `json:"members"`
	Agents                []DiscussionAgent  `json:"agents"`
}
type ChannelReceipt struct {
	Conversation DiscussionSummary `json:"conversation"`
	RequestKey   uuid.UUID         `json:"requestKey"`
	Replayed     bool              `json:"replayed"`
}
type DiscussionList struct {
	Items      []DiscussionSummary `json:"items"`
	NextCursor *string             `json:"nextCursor"`
}
type ChannelManagementList struct {
	Items      []ChannelManagement `json:"items"`
	NextCursor *string             `json:"nextCursor"`
}
type DiscussionReceipt struct {
	Message              DiscussionMessage `json:"message"`
	RequestKey           uuid.UUID         `json:"requestKey"`
	Replayed             bool              `json:"replayed"`
	ConversationRevision int64             `json:"conversationRevision"`
	HeadSeq              int64             `json:"headSeq"`
}
type DiscussionPage struct {
	StreamHeadSeq             int64               `json:"streamHeadSeq"`
	Items                     []DiscussionMessage `json:"items"`
	OlderCursor               *string             `json:"olderCursor"`
	NewerCursor               string              `json:"newerCursor"`
	RefreshCursor             string              `json:"refreshCursor"`
	HeadSeq                   int64               `json:"headSeq"`
	ConversationRevision      int64               `json:"conversationRevision"`
	GrantGeneration           *int64              `json:"grantGeneration"`
	CompanyMembershipRevision int64               `json:"companyMembershipRevision"`
}

type DiscussionArtifactFile struct {
	FileName    string `json:"fileName"`
	ContentType string `json:"contentType"`
}

type DiscussionArtifact struct {
	Files       []DiscussionArtifactFile `json:"files"`
	Type        string                   `json:"type"`
	FileCount   int64                    `json:"fileCount"`
	FileName    string                   `json:"fileName,omitempty"`
	ContentType string                   `json:"contentType,omitempty"`
	ID          uuid.UUID                `json:"id"`
	Title       string                   `json:"title"`
}

type DiscussionSendInput struct {
	ArtifactIDs   []uuid.UUID
	RequestKey    uuid.UUID
	Body          DiscussionBody
	AttachmentIDs []uuid.UUID
	ThreadRootID  *uuid.UUID
}
type DiscussionPageOptions struct {
	Limit  int
	Cursor string
}
type DiscussionListOptions struct {
	Limit         int
	Cursor        string
	IncludeHidden bool
	Archived      bool
	Query         string
}
type DiscussionStateInput struct {
	NotificationLevel *string
	ExpectedRevision  int64
	Pinned            *bool
	Hidden            *bool
}
type CreateChannelInput struct {
	RequestKey    uuid.UUID
	Name          string
	Topic         string
	MemberUserIDs []uuid.UUID
	AgentIDs      []uuid.UUID
}
type ChannelUpdateInput struct {
	ExpectedRevision int64
	Name             *string
	Topic            *string
}
type ChannelMemberInput struct {
	ExpectedRevision int64
	UserID           uuid.UUID
	Active           bool
}
type ChannelAgentInput struct {
	ExpectedRevision int64
	AgentID          uuid.UUID
	Active           bool
}
type ChannelArchiveInput struct {
	ExpectedRevision int64
	Archived         bool
}

// DiscussionMessageTarget always carries both scopes; an item ID is not authority.
type DiscussionMessageTarget struct {
	ConversationID uuid.UUID
	MessageID      uuid.UUID
}
type DiscussionThreadSummary struct {
	UnreadCount     int64              `json:"unreadCount"`
	ManualUnreadSeq *int64             `json:"manualUnreadSeq"`
	ReplyCount      int64              `json:"replyCount"`
	LastReplyAt     *time.Time         `json:"lastReplyAt"`
	RecentAuthors   []DiscussionAuthor `json:"recentAuthors"`
}
type DiscussionReactionSummary struct {
	Emoji   string `json:"emoji"`
	Count   int64  `json:"count"`
	Reacted bool   `json:"reacted"`
}
type DiscussionReaction struct {
	DiscussionReactionSummary
	Users []DiscussionAuthor `json:"users"`
}
type DiscussionReactions struct {
	Items                []DiscussionReaction `json:"items"`
	NextCursor           *string              `json:"nextCursor"`
	ConversationRevision int64                `json:"conversationRevision"`
}
type DiscussionThreadState struct {
	ReadThroughSeq  int64     `json:"readThroughSeq"`
	ManualUnreadSeq *int64    `json:"manualUnreadSeq"`
	RootID          uuid.UUID `json:"rootId"`
	Revision        int64     `json:"revision"`
	GrantGeneration *int64    `json:"grantGeneration"`
	Follow          string    `json:"follow"`
	UnreadCount     *int64    `json:"unreadCount"`
}
type DiscussionThreadPage struct {
	DiscussionPage
	Root  DiscussionMessage     `json:"root"`
	State DiscussionThreadState `json:"state"`
}
type DiscussionContext struct {
	DiscussionPage
	Root     *DiscussionMessage `json:"root"`
	TargetID uuid.UUID          `json:"targetId"`
}
type DiscussionMutation struct {
	Message              DiscussionMessage `json:"message"`
	ConversationRevision int64             `json:"conversationRevision"`
	HeadSeq              int64             `json:"headSeq"`
}
type DiscussionPin struct {
	Message        DiscussionMessage  `json:"message"`
	Root           *DiscussionMessage `json:"root"`
	PinnedAt       time.Time          `json:"pinnedAt"`
	PinnedByUserID uuid.UUID          `json:"pinnedByUserId"`
}
type DiscussionPins struct {
	Items                []DiscussionPin `json:"items"`
	NextCursor           *string         `json:"nextCursor"`
	TotalCount           int64           `json:"totalCount"`
	ConversationRevision int64           `json:"conversationRevision"`
}
type DiscussionEditInput struct {
	ExpectedRevision int64
	Body             DiscussionBody
}
type DiscussionReactionInput struct {
	Emoji  string
	Active bool
}
type DiscussionThreadStateInput struct {
	ReadThroughSeq   *int64
	ManualUnreadSeq  *int64
	GrantGeneration  int64
	ExpectedRevision int64
	Follow           string
}
