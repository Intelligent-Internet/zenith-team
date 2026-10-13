package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/execution"
)

type companyArtifactsStub struct {
	deliverableDownloadStub
	listed   []execution.CompanyArtifactListOptions
	resolved [][]uuid.UUID
}

func (s *companyArtifactsStub) ListArtifacts(
	_ context.Context,
	_ auth.UserPrincipal,
	options execution.CompanyArtifactListOptions,
) (execution.ArtifactPage, error) {
	s.listed = append(s.listed, options)
	return execution.ArtifactPage{Items: []execution.ArtifactRunResult{}}, nil
}

func (s *companyArtifactsStub) ResolveArtifacts(
	_ context.Context,
	_ auth.UserPrincipal,
	ids []uuid.UUID,
) ([]execution.ArtifactRunResult, error) {
	s.resolved = append(s.resolved, ids)
	return []execution.ArtifactRunResult{}, nil
}

func companyArtifactsHandler(t *testing.T) (http.Handler, *companyArtifactsStub, string) {
	t.Helper()
	principal := auth.UserPrincipal{UserID: uuid.New(), CompanyID: uuid.New(), Role: auth.CompanyRoleMember}
	resolve := func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
		return principal, nil
	}
	stub := &companyArtifactsStub{}
	handler := New(Handler{
		AuthService:        authenticatedAuthStub(principal.UserID),
		CompanyService:     &companyServiceStub{authorize: resolve, requirePermission: resolve},
		DeliverableService: stub,
		RunProofResolver:   runProofResolverStub{},
	})
	return handler, stub, "/api/v1/companies/" + principal.CompanyID.String() + "/artifacts"
}

func serveArtifactRequest(handler http.Handler, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Authorization", "Bearer access-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestListArtifactsPassesSearchAndPaging(t *testing.T) {
	t.Parallel()
	handler, stub, base := companyArtifactsHandler(t)
	response := serveArtifactRequest(handler, base+"?search=churn+report&limit=10")
	if response.Code != http.StatusOK {
		t.Fatalf("list = %d %s", response.Code, response.Body)
	}
	if len(stub.listed) != 1 || stub.listed[0].Search != "churn report" || stub.listed[0].Limit != 10 {
		t.Fatalf("list options = %+v", stub.listed)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil ||
		string(body["items"]) != "[]" || string(body["nextCursor"]) != "null" {
		t.Fatalf("list body = %s", response.Body)
	}
}

func TestListArtifactsRejectsUnknownOrRepeatedParameters(t *testing.T) {
	t.Parallel()
	handler, stub, base := companyArtifactsHandler(t)
	for _, query := range []string{"?owner=me", "?search=a&search=b", "?limit=51", "?cursor=nope"} {
		response := serveArtifactRequest(handler, base+query)
		if response.Code != http.StatusUnprocessableEntity && response.Code != http.StatusBadRequest {
			t.Fatalf("list%s = %d %s", query, response.Code, response.Body)
		}
	}
	if len(stub.listed) != 0 {
		t.Fatalf("service reached with invalid input: %+v", stub.listed)
	}
}

func TestResolveArtifactsDeduplicatesIDs(t *testing.T) {
	t.Parallel()
	handler, stub, base := companyArtifactsHandler(t)
	first, second := uuid.New(), uuid.New()
	response := serveArtifactRequest(handler, base+"/resolve?id="+first.String()+"&id="+second.String()+"&id="+first.String())
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"items":[]}` {
		t.Fatalf("resolve = %d %s", response.Code, response.Body)
	}
	if len(stub.resolved) != 1 || len(stub.resolved[0]) != 2 ||
		stub.resolved[0][0] != first || stub.resolved[0][1] != second {
		t.Fatalf("resolved ids = %v", stub.resolved)
	}
}

func TestResolveArtifactsRejectsInvalidRequests(t *testing.T) {
	t.Parallel()
	handler, stub, base := companyArtifactsHandler(t)
	tooMany := make([]string, 51)
	for i := range tooMany {
		tooMany[i] = "id=" + uuid.NewString()
	}
	for _, query := range []string{
		"", "?id=not-a-uuid", "?id=" + uuid.NewString() + "&limit=1", "?" + strings.Join(tooMany, "&"),
	} {
		response := serveArtifactRequest(handler, base+"/resolve"+query)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("resolve%.40s = %d %s", query, response.Code, response.Body)
		}
	}
	if len(stub.resolved) != 0 {
		t.Fatalf("service reached with invalid input: %v", stub.resolved)
	}
}

func TestParseConversationArtifactRunFilter(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	for _, tt := range []struct {
		name, query string
		wantID      uuid.UUID
		wantError   bool
	}{
		{name: "all runs", query: "limit=10"},
		{name: "one run", query: "runId=" + id.String() + "&limit=10", wantID: id},
		{name: "invalid run", query: "runId=invalid", wantError: true},
		{name: "empty run", query: "runId=", wantError: true},
		{name: "zero run", query: "runId=" + uuid.Nil.String(), wantError: true},
		{name: "repeated run", query: "runId=" + id.String() + "&runId=" + id.String(), wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseConversationArtifactListOptions(tt.query)
			if (err != nil) != tt.wantError || got.RunID != tt.wantID {
				t.Fatalf("options = %+v, error = %v; want run %s, error %v", got, err, tt.wantID, tt.wantError)
			}
		})
	}
	if _, err := parseArtifactListOptions("runId=" + id.String()); err == nil {
		t.Fatal("company-wide listing accepted a conversation-only filter")
	}
}

// chatWhiteboardArtifactsStub resolves the Whiteboard an Agent or Task chat
// just made, recording the Company it was resolved in.
type chatWhiteboardArtifactsStub struct {
	deliverableDownloadStub
	resolvedIn uuid.UUID
}

func (s *chatWhiteboardArtifactsStub) ResolveArtifacts(
	_ context.Context,
	principal auth.UserPrincipal,
	ids []uuid.UUID,
) ([]execution.ArtifactRunResult, error) {
	s.resolvedIn = principal.CompanyID
	items := make([]execution.ArtifactRunResult, 0, len(ids))
	for _, id := range ids {
		items = append(items, execution.ArtifactRunResult{Artifact: execution.DeliverableOutput{ID: id}})
	}
	return items, nil
}

// A Whiteboard made in an Agent or Task chat answers as the chat's Artifacts
// list it, resolved in the chat's Company, which the route does not name.
func TestChatWhiteboardAnswersWithTheNewArtifact(t *testing.T) {
	t.Parallel()
	userID, chatID, companyID, artifactID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	conversations := &conversationServiceStub{
		createWhiteboard: func(
			_ context.Context,
			_ auth.UserPrincipal,
			target conversation.MessageTarget,
			title string,
		) (conversation.CreatedWhiteboard, error) {
			if target.Kind != conversation.TargetConversation || target.ID != chatID || title != "Plan" {
				t.Errorf("CreateWhiteboard(%+v, %q)", target, title)
			}
			return conversation.CreatedWhiteboard{CompanyID: companyID, ArtifactID: artifactID}, nil
		},
	}
	artifacts := &chatWhiteboardArtifactsStub{}
	handler := New(Handler{
		AuthService:         authenticatedAuthStub(userID),
		ConversationService: conversations,
		DeliverableService:  artifacts,
	})
	response := serveBearerJSON(handler, http.MethodPost,
		"/api/v1/conversations/"+chatID.String()+"/whiteboards", []byte(`{"title":"Plan"}`))
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Artifact struct {
			ID uuid.UUID `json:"id"`
		} `json:"artifact"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Artifact.ID != artifactID {
		t.Fatalf("body = %s, %v", response.Body.String(), err)
	}
	if artifacts.resolvedIn != companyID {
		t.Fatalf("resolved in Company %s; want %s", artifacts.resolvedIn, companyID)
	}
	if missing := serveBearerJSON(handler, http.MethodPost,
		"/api/v1/conversations/"+uuid.NewString()+"/whiteboards", []byte(`{}`)); missing.Code < 400 ||
		missing.Code >= 500 {
		t.Fatalf("untitled status = %d; body=%s", missing.Code, missing.Body.String())
	}
}
