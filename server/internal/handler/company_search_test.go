package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
)

func TestCompanySearchRouteBindsPrivateSearchBody(t *testing.T) {
	t.Parallel()

	userID, companyID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	messageID, conversationID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	taskID, taskConversationID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	principal := phase4Principal(userID, companyID)
	service := &companySearchServiceStub{search: func(
		_ context.Context,
		got auth.UserPrincipal,
		input company.SearchInput,
	) (company.SearchPage, error) {
		if got != principal || input.Query != "release plan" || input.Limit != 20 || input.IncludeArchived ||
			len(input.Types) != 3 {
			t.Fatalf("Search input = %+v / %+v", got, input)
		}
		return company.SearchPage{Items: []company.SearchResult{
			company.SearchTaskResult{
				Type:           company.SearchTaskResultTypeTask,
				ID:             taskID,
				Key:            "REL-42",
				Title:          "Release plan task",
				Status:         "in_progress",
				Priority:       "high",
				Agent:          company.NamedRef{ID: agentID, Name: "CEO Agent"},
				ConversationID: taskConversationID,
				RouteKind:      company.SearchRouteTask,
				UpdatedAt:      time.Date(2026, 8, 4, 1, 2, 2, 0, time.UTC),
			},
			company.SearchConversationResult{
				Type:      company.SearchConversationResultTypeConversation,
				ID:        conversationID,
				Title:     "CEO conversation",
				Agent:     company.NamedRef{ID: agentID, Name: "CEO Agent"},
				RouteKind: company.SearchRouteDirectConversation,
				UpdatedAt: time.Date(2026, 8, 4, 1, 2, 2, 0, time.UTC),
			},
			company.SearchMessageResult{
				Type:             company.SearchMessageResultTypeMessage,
				ID:               messageID,
				ConversationID:   conversationID,
				ConversationKind: "direct",
				Seq:              12,
				Agent:            &company.NamedRef{ID: agentID, Name: "CEO Agent"},
				Title:            "CEO conversation",
				Snippet:          "release plan approved",
				Author: company.ActorRef{
					ID:          agentID,
					Kind:        company.ActorTypeAgent,
					DisplayName: "CEO Agent",
				},
				CreatedAt: time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC),
				RouteKind: company.SearchRouteDirectConversation,
			},
		}}, nil
	}}
	handler := companySearchHTTPHandler(userID, principal, service)
	response := serveBearerJSON(
		handler,
		http.MethodPost,
		"/api/v1/companies/"+companyID.String()+"/search",
		[]byte(`{
		"query":"  release plan  ",
		"kinds":["task","conversation","message"],
		"includeArchived":false,
		"limit":20
	}`),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body.Items) != 3 {
		t.Fatalf("response = %s, %v", response.Body.String(), err)
	}
	results := make(map[string]map[string]any, len(body.Items))
	for _, result := range body.Items {
		kind, _ := result["kind"].(string)
		results[kind] = result
		if _, exists := result["route"]; exists {
			t.Fatalf("legacy route field in result = %#v", result)
		}
	}
	task := results["task"]
	taskAgent, _ := task["agent"].(map[string]any)
	if task["id"] != taskID.String() || task["conversationId"] != taskConversationID.String() ||
		task["routeKind"] != "task" ||
		taskAgent["name"] != "CEO Agent" {
		t.Fatalf("task result = %#v", task)
	}
	conversation := results["conversation"]
	conversationAgent, _ := conversation["agent"].(map[string]any)
	if conversation["id"] != conversationID.String() ||
		conversation["routeKind"] != "direct_conversation" ||
		conversationAgent["id"] != agentID.String() {
		t.Fatalf("conversation result = %#v", conversation)
	}
	message := results["message"]
	if message["id"] != messageID.String() || message["routeKind"] != "direct_conversation" {
		t.Fatalf("message result = %#v", message)
	}
	if _, exists := message["messageId"]; exists {
		t.Fatalf("legacy messageId field in result = %#v", message)
	}
}

func TestCompanySearchRouteRejectsInvalidBodiesBeforeService(t *testing.T) {
	t.Parallel()

	userID, companyID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	principal := phase4Principal(userID, companyID)
	calls := 0
	service := &companySearchServiceStub{
		search: func(context.Context, auth.UserPrincipal, company.SearchInput) (company.SearchPage, error) {
			calls++
			return company.SearchPage{}, nil
		},
	}
	handler := companySearchHTTPHandler(userID, principal, service)
	path := "/api/v1/companies/" + companyID.String() + "/search"
	for _, body := range []string{
		`{}`,
		`{"query":"a"}`,
		`{"query":"--"}`,
		`{"query":"valid","kinds":[]}`,
		`{"query":"valid","kinds":["unknown"]}`,
		`{"query":"valid","kinds":["task","task"]}`,
		`{"query":"valid","limit":0}`,
		`{"query":"valid","limit":51}`,
		`{"query":"` + strings.Repeat("x", 257) + `"}`,
	} {
		response := serveBearerJSON(handler, http.MethodPost, path, []byte(body))
		assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
	}
	response := serveBearerJSON(handler, http.MethodPost, path, []byte(`{"query":"valid","unknown":true}`))
	assertErrorResponse(t, response, http.StatusBadRequest, "invalid_request")
	if calls != 0 {
		t.Fatalf("Search calls = %d, want 0", calls)
	}
}

// A limit that is absent or null takes the documented default of 20; 0 is
// not "absent", it is an integer below the minimum and is refused above.
func TestCompanySearchRouteDefaultsAnAbsentOrNullLimit(t *testing.T) {
	t.Parallel()

	userID, companyID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	principal := phase4Principal(userID, companyID)
	service := &companySearchServiceStub{
		search: func(_ context.Context, _ auth.UserPrincipal, input company.SearchInput) (company.SearchPage, error) {
			if input.Limit != 20 {
				t.Errorf("Search limit = %d, want the default 20", input.Limit)
			}
			return company.SearchPage{}, nil
		},
	}
	handler := companySearchHTTPHandler(userID, principal, service)
	path := "/api/v1/companies/" + companyID.String() + "/search"
	for _, body := range []string{
		`{"query":"valid"}`,
		`{"query":"valid","limit":null}`,
	} {
		response := serveBearerJSON(handler, http.MethodPost, path, []byte(body))
		if response.Code != http.StatusOK {
			t.Fatalf("%s: status = %d; body = %s", body, response.Code, response.Body.String())
		}
	}
}

type companySearchServiceStub struct {
	search func(context.Context, auth.UserPrincipal, company.SearchInput) (company.SearchPage, error)
}

func (stub *companySearchServiceStub) Search(
	ctx context.Context,
	principal auth.UserPrincipal,
	input company.SearchInput,
) (company.SearchPage, error) {
	return stub.search(ctx, principal, input)
}

func companySearchHTTPHandler(
	userID uuid.UUID,
	principal auth.UserPrincipal,
	service CompanySearchService,
) http.Handler {
	return New(Handler{
		AuthService: authenticatedAuthStub(userID),
		CompanyService: &companyServiceStub{authorize: func(
			context.Context,
			auth.UserPrincipal,
			uuid.UUID,
		) (auth.UserPrincipal, error) {
			return principal, nil
		}},
		CompanySearchService: service,
	})
}
