package company

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// SearchService owns cross-entity search within one Company.
type SearchService interface {
	Search(context.Context, auth.UserPrincipal, SearchInput) (SearchPage, error)
}

var _ SearchService = (*DefaultSearchService)(nil)

type DefaultSearchService struct {
	store *db.Store
}

func NewSearchService(store *db.Store) (*DefaultSearchService, error) {
	if store == nil {
		return nil, errors.New("Company search store is required")
	}
	return &DefaultSearchService{store: store}, nil
}

func (s *DefaultSearchService) Search(
	ctx context.Context,
	principal auth.UserPrincipal,
	input SearchInput,
) (SearchPage, error) {
	tsQuery, selected := searchParameters(input)
	rows, err := s.store.Queries().SearchCompany(ctx, generated.SearchCompanyParams{
		PageSize:             int32(input.Limit),
		Query:                strings.ToLower(input.Query),
		TsQuery:              tsQuery,
		ActorUserID:          principal.UserID,
		CompanyID:            principal.CompanyID,
		IncludeTasks:         selected[SearchTypeTask],
		IncludeConversations: selected[SearchTypeConversation],
		IncludeMessages:      selected[SearchTypeMessage],
		IncludeArchived:      input.IncludeArchived,
		ConversationFilter: util.NullUUIDFromPtr(
			input.ConversationID,
		),
		AuthorFilter: util.NullUUIDFromPtr(input.AuthorID),
		AfterTime: searchTimestamp(
			input.After,
		),
		BeforeTime: searchTimestamp(input.Before),
		HasFile:    input.HasFile,
	})
	if err != nil {
		return SearchPage{}, fmt.Errorf("search Company: %w", err)
	}
	page := SearchPage{Items: make([]SearchResult, 0, len(rows))}
	for _, row := range rows {
		page.Items = append(page.Items, resultFromRow(row, input.Query))
	}
	return page, nil
}

func searchParameters(input SearchInput) (string, map[SearchType]bool) {
	selected := map[SearchType]bool{}
	for _, searchType := range input.Types {
		selected[searchType] = true
	}
	tokens := searchTokens(input.Query)
	parts := make([]string, 0, len(tokens))
	for _, token := range tokens {
		parts = append(parts, "'"+token+"':*")
	}
	return strings.Join(parts, " & "), selected
}

func searchTokens(value string) []string {
	const maxTokens = 12
	var tokens []string
	seen := make(map[string]struct{})
	for _, field := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		if _, exists := seen[field]; exists {
			continue
		}
		seen[field] = struct{}{}
		tokens = append(tokens, field)
		if len(tokens) == maxTokens {
			break
		}
	}
	return tokens
}

func resultFromRow(row generated.SearchCompanyRow, query string) SearchResult {
	agent := NamedRef{ID: row.AgentID.UUID, Name: row.AgentName.String}
	routeKind := SearchRouteType(row.RouteKind)
	switch SearchType(row.ResultKind) {
	case SearchTypeTask:
		return SearchTaskResult{
			Type:           SearchTaskResultTypeTask,
			ID:             row.EntityID,
			Key:            row.Key,
			Number:         row.TaskNumber,
			Title:          row.Title,
			Status:         row.Status,
			Priority:       row.Priority,
			Agent:          agent,
			ConversationID: row.ConversationID,
			RouteKind:      routeKind,
			UpdatedAt:      row.UpdatedAt.Time.UTC(),
		}
	case SearchTypeConversation:
		return SearchConversationResult{
			Type:      SearchConversationResultTypeConversation,
			ID:        row.EntityID,
			Title:     row.Title,
			Agent:     agent,
			RouteKind: routeKind,
			UpdatedAt: row.UpdatedAt.Time.UTC(),
		}
	}
	var taskID *uuid.UUID
	var key *string
	var number *int64
	if row.HasTaskID {
		taskID = &row.TaskID
		key = &row.Key
		number = &row.TaskNumber
	}
	var messageAgent *NamedRef
	if row.AgentID.Valid {
		messageAgent = &agent
	}
	return SearchMessageResult{
		ThreadRootID:     searchUUID(row.ThreadRootID),
		Type:             SearchMessageResultTypeMessage,
		ID:               row.EntityID,
		Seq:              row.MessageSeq.Int64,
		ConversationID:   row.ConversationID,
		ConversationKind: row.ConversationKind,
		TaskID:           taskID,
		Agent:            messageAgent,
		Key:              key,
		Number:           number,
		Title:            row.Title,
		Snippet:          messageSnippet(row.SnippetSource.String, query),
		Author: ActorRef{
			ID:          row.AuthorID.UUID,
			Kind:        ActorType(row.AuthorKind.String),
			DisplayName: row.AuthorDisplayName.String,
			AvatarURL:   util.TextToPtr(row.AuthorAvatarUrl),
		},
		CreatedAt: row.CreatedAt.Time.UTC(),
		RouteKind: routeKind,
	}
}

func messageSnippet(content, query string) string {
	content = strings.Join(strings.Fields(content), " ")
	runes := []rune(content)
	if len(runes) <= maxSnippetRunes {
		return content
	}
	start := 0
	if tokens := searchTokens(query); len(tokens) > 0 {
		lowerRunes := []rune(strings.ToLower(content))
		tokenRunes := []rune(tokens[0])
		for index := 0; index+len(tokenRunes) <= len(lowerRunes); index++ {
			if string(lowerRunes[index:index+len(tokenRunes)]) == string(tokenRunes) {
				start = index - 60
				if start < 0 {
					start = 0
				}
				break
			}
		}
	}
	end := start + maxSnippetRunes
	if end > len(runes) {
		end = len(runes)
		start = end - maxSnippetRunes
	}
	return string(runes[start:end])
}

func searchTimestamp(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}

func searchUUID(value uuid.NullUUID) *uuid.UUID {
	if !value.Valid {
		return nil
	}
	return &value.UUID
}
