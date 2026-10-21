package voice

import (
	"time"

	"github.com/google/uuid"
)

// ContextRequest is a parsed, read-only request. Scope comes from the Huddle lease.
type ContextRequest struct {
	Tool         string
	RequestID    string
	Query        string
	Status       string
	ProjectID    uuid.UUID
	TaskID       uuid.UUID
	ProposalID   uuid.UUID
	AfterID      uuid.UUID
	BeforeNumber int64
	// AuthorizerUserID is the person who switched Company context on, the
	// voice assistant's Authorizer. The Huddle service sets it from the
	// assistant's state, never the model. The reads use that person's access
	// by the delegate rule; everyone in the call hears the answer. The
	// zero value reads nothing.
	AuthorizerUserID uuid.UUID
}

type ContextItem struct {
	ID             uuid.UUID     `json:"id"`
	Type           string        `json:"type"`
	Key            string        `json:"key,omitempty"`
	Title          string        `json:"title"`
	Description    string        `json:"description,omitempty"`
	Status         string        `json:"status,omitempty"`
	ProjectID      *uuid.UUID    `json:"projectId,omitempty"`
	Assignee       string        `json:"assignee,omitempty"`
	DueDate        string        `json:"dueDate,omitempty"`
	BlockedByCount int64         `json:"blockedByCount,omitempty"`
	UpdatedAt      time.Time     `json:"updatedAt"`
	Truncated      bool          `json:"truncated"`
	Work           *WorkProgress `json:"work,omitempty"`
}

// WorkProgress is receipt-bound shared Task work, never a private Run transcript.
type WorkProgress struct {
	State                string     `json:"state"`
	TaskID               *uuid.UUID `json:"taskId,omitempty"`
	TaskStatus           string     `json:"taskStatus,omitempty"`
	RunID                *uuid.UUID `json:"runId,omitempty"`
	RunStatus            string     `json:"runStatus,omitempty"`
	NewerTaskExecution   bool       `json:"newerTaskExecution"`
	HasResult            bool       `json:"hasResult"`
	ResultMessageID      *uuid.UUID `json:"resultMessageId,omitempty"`
	ResultMessageSeq     int64      `json:"resultMessageSeq,omitempty"`
	ResultConversationID *uuid.UUID `json:"resultConversationId,omitempty"`
	ResultText           string     `json:"resultText,omitempty"`
	ResultTruncated      bool       `json:"resultTruncated"`
	CheckedAt            time.Time  `json:"checkedAt"`
}

type ContextResult struct {
	Items            []ContextItem `json:"items"`
	ReadAt           time.Time     `json:"readAt"`
	HasMore          bool          `json:"hasMore"`
	NextBeforeNumber int64         `json:"nextBeforeNumber,omitempty"`
	NextAfterID      *uuid.UUID    `json:"nextAfterId,omitempty"`
}

// ToolActivity is a bounded display projection, not a saved tool transcript.
type ToolActivity struct {
	RequestID string        `json:"requestId"`
	Tool      string        `json:"tool"`
	State     string        `json:"state"`
	At        time.Time     `json:"at"`
	Sources   []ContextItem `json:"sources"`
}
