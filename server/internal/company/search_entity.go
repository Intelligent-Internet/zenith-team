package company

import (
	"time"

	"github.com/google/uuid"
)

const (
	maxSnippetRunes = 240
)

type SearchType string

const (
	SearchTypeTask         SearchType = "task"
	SearchTypeConversation SearchType = "conversation"
	SearchTypeMessage      SearchType = "message"
)

type SearchRouteType string

const (
	SearchRouteTask               SearchRouteType = "task"
	SearchRouteDirectConversation SearchRouteType = "direct_conversation"
	SearchRouteTaskConversation   SearchRouteType = "task_conversation"
)

type SearchInput struct {
	ConversationID  *uuid.UUID
	AuthorID        *uuid.UUID
	After           *time.Time
	Before          *time.Time
	HasFile         bool
	Query           string
	Types           []SearchType
	IncludeArchived bool
	Limit           int
}

type NamedRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type SearchResult interface {
	searchResult()
}

type SearchTaskResultType string

const SearchTaskResultTypeTask SearchTaskResultType = "task"

type SearchConversationResultType string

const SearchConversationResultTypeConversation SearchConversationResultType = "conversation"

type SearchMessageResultType string

const SearchMessageResultTypeMessage SearchMessageResultType = "message"

// SearchTaskResult carries both task identities: the stable key, and the
// number every surface displays as {taskPrefix}-{number}.
type SearchTaskResult struct {
	Type           SearchTaskResultType `json:"kind"`
	ID             uuid.UUID            `json:"id"`
	Key            string               `json:"key"`
	Number         int64                `json:"number"`
	Title          string               `json:"title"`
	Status         string               `json:"status"`
	Priority       string               `json:"priority"`
	Agent          NamedRef             `json:"agent"`
	ConversationID uuid.UUID            `json:"conversationId"`
	RouteKind      SearchRouteType      `json:"routeKind"`
	UpdatedAt      time.Time            `json:"updatedAt"`
}

func (SearchTaskResult) searchResult() {}

type SearchConversationResult struct {
	Type      SearchConversationResultType `json:"kind"`
	ID        uuid.UUID                    `json:"id"`
	Title     string                       `json:"title"`
	Agent     NamedRef                     `json:"agent"`
	RouteKind SearchRouteType              `json:"routeKind"`
	UpdatedAt time.Time                    `json:"updatedAt"`
}

func (SearchConversationResult) searchResult() {}

type SearchMessageResult struct {
	ThreadRootID     *uuid.UUID              `json:"threadRootId"`
	Type             SearchMessageResultType `json:"kind"`
	ID               uuid.UUID               `json:"id"`
	Seq              int64                   `json:"seq"`
	ConversationID   uuid.UUID               `json:"conversationId"`
	ConversationKind string                  `json:"conversationKind"`
	TaskID           *uuid.UUID              `json:"taskId"`
	Agent            *NamedRef               `json:"agent"`
	Key              *string                 `json:"key"`
	Number           *int64                  `json:"number"`
	Title            string                  `json:"title"`
	Snippet          string                  `json:"snippet"`
	Author           ActorRef                `json:"author"`
	CreatedAt        time.Time               `json:"createdAt"`
	RouteKind        SearchRouteType         `json:"routeKind"`
}

func (SearchMessageResult) searchResult() {}

type SearchPage struct {
	Items []SearchResult `json:"items"`
}
