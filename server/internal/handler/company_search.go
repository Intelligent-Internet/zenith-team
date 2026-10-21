package handler

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
)

type CompanySearchService interface {
	Search(context.Context, auth.UserPrincipal, company.SearchInput) (company.SearchPage, error)
}

type companySearchRequest struct {
	ConversationID  *string    `json:"conversationId"  binding:"omitempty,uuid"`
	AuthorID        *string    `json:"authorId"        binding:"omitempty,uuid"`
	After           *time.Time `json:"after"`
	Before          *time.Time `json:"before"`
	HasFile         bool       `json:"hasFile"`
	Query           string     `json:"query"           binding:"required,max=256"`
	Kinds           []string   `json:"kinds"`
	IncludeArchived bool       `json:"includeArchived"`
	// Limit is a pointer so the validator can tell an explicit 0, which is
	// below the minimum and refused, from an absent or null key, which takes
	// the default.
	Limit *int `json:"limit" binding:"omitempty,min=1,max=50"`
}

func (h *Handler) registerCompanySearchRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.CompanySearchService == nil {
		return
	}
	routes := group.Group(
		"/companies/:companyId/search",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	routes.POST("", h.searchCompany)
}

func (h *Handler) searchCompany(c *gin.Context) {
	request, err := bindRequest[companySearchRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	input, ok := companySearchInput(request)
	if !ok {
		writeCompanySearchError(c, company.ErrInvalidInput)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeCompanySearchError(c, company.ErrUnauthenticated)
		return
	}
	page, err := h.CompanySearchService.Search(c.Request.Context(), principal, input)
	if err != nil {
		writeCompanySearchError(c, err)
		return
	}
	if page.Items == nil {
		page.Items = []company.SearchResult{}
	}
	c.JSON(http.StatusOK, page)
}

func companySearchInput(request companySearchRequest) (company.SearchInput, bool) {
	request.Query = strings.Join(strings.Fields(request.Query), " ")
	if utf8.RuneCountInString(request.Query) < 2 || len(request.Query) > 256 ||
		!hasCompanySearchToken(request.Query) {
		return company.SearchInput{}, false
	}
	kinds := make([]company.SearchType, 0, len(request.Kinds))
	if request.Kinds == nil {
		kinds = append(
			kinds,
			company.SearchTypeTask,
			company.SearchTypeConversation,
			company.SearchTypeMessage,
		)
	} else {
		if len(request.Kinds) == 0 {
			return company.SearchInput{}, false
		}
		seen := make(map[company.SearchType]struct{}, len(request.Kinds))
		for _, raw := range request.Kinds {
			kind := company.SearchType(raw)
			if kind != company.SearchTypeTask && kind != company.SearchTypeConversation &&
				kind != company.SearchTypeMessage {
				return company.SearchInput{}, false
			}
			if _, exists := seen[kind]; exists {
				return company.SearchInput{}, false
			}
			seen[kind] = struct{}{}
			kinds = append(kinds, kind)
		}
	}
	limit := 20
	if request.Limit != nil {
		limit = *request.Limit
	}
	if request.After != nil && request.Before != nil && !request.After.Before(*request.Before) {
		return company.SearchInput{}, false
	}
	var conversationID, authorID *uuid.UUID
	if request.ConversationID != nil {
		id, err := uuid.Parse(*request.ConversationID)
		if err != nil {
			return company.SearchInput{}, false
		}
		conversationID = &id
	}
	if request.AuthorID != nil {
		id, err := uuid.Parse(*request.AuthorID)
		if err != nil {
			return company.SearchInput{}, false
		}
		authorID = &id
	}
	return company.SearchInput{
		ConversationID:  conversationID,
		AuthorID:        authorID,
		After:           request.After,
		Before:          request.Before,
		HasFile:         request.HasFile,
		Query:           request.Query,
		Types:           kinds,
		IncludeArchived: request.IncludeArchived,
		Limit:           limit,
	}, true
}

func hasCompanySearchToken(query string) bool {
	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

var companySearchErrorMappings = []errorMapping{
	{
		company.ErrUnauthenticated,
		http.StatusUnauthorized,
		"authentication is required",
		"authentication_required",
	},
	{
		company.ErrInvalidInput,
		http.StatusUnprocessableEntity,
		"request validation failed",
		"validation_failed",
	},
}

func writeCompanySearchError(c *gin.Context, err error) {
	writeMappedError(c, err, companySearchErrorMappings, internalErrorFallback)
}
