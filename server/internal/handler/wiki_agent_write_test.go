package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/wiki"
)

type wikiAgentWriteServiceStub struct {
	wiki.AgentWriteService
	calls []wiki.AgentWriteInput
	runs  []uuid.UUID
	users []uuid.UUID
	err   error
}

func (s *wikiAgentWriteServiceStub) Write(
	_ context.Context,
	principal auth.UserPrincipal,
	runID uuid.UUID,
	input wiki.AgentWriteInput,
) (wiki.AgentWriteResponse, error) {
	s.calls = append(s.calls, input)
	s.runs = append(s.runs, runID)
	s.users = append(s.users, principal.UserID)
	return wiki.AgentWriteResponse{
		Pages:      []wiki.AgentWriteResult{{Key: "a", PageID: uuid.New(), Title: "A", Revision: 1, Status: wiki.AgentWriteCreated}},
		Warnings:   []string{},
		TotalPages: 1,
	}, s.err
}

func (f wikiAgentFixture) writeHandler(service *wikiAgentWriteServiceStub) *Handler {
	principal := f.principal
	h := New(Handler{
		AuthService: authenticatedAuthStub(principal.UserID),
		CompanyService: &companyServiceStub{
			authorize: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
				return principal, nil
			},
		},
		WikiService:           &wikiServiceStub{},
		WikiAgentReadService:  f.service,
		WikiAgentWriteService: service,
		RunProofResolver:      runProofResolverStub{origin: f.origin},
	})
	return h
}

func (f wikiAgentFixture) post(
	h *Handler, runID uuid.UUID, token, key, contentType string, body []byte,
) *httptest.ResponseRecorder {
	path := "/api/v1/companies/" + f.principal.CompanyID.String() + "/runs/" + runID.String() + "/agent-wiki/pages"
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer access-token")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		request.Header.Set(runTokenHeader, token)
	}
	if key != "" {
		request.Header.Set(idempotencyHeader, key)
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
}

const validWikiAgentWrite = `{"pages":[{"key":"company-overview","title":"Company overview","summary":"s","markdown":"See [[products]]"}]}`

func TestWikiAgentWriteAdmitsOnlyTheRunsOwnProof(t *testing.T) {
	t.Parallel()
	otherUser := uuid.New()
	for _, tc := range []struct {
		name   string
		mutate func(*wikiAgentFixture, *uuid.UUID, *string)
		status int
		code   string
	}{
		{"own Run", func(*wikiAgentFixture, *uuid.UUID, *string) {}, http.StatusOK, ""},
		{"no Run token", func(_ *wikiAgentFixture, _ *uuid.UUID, token *string) { *token = "" }, http.StatusUnauthorized, "run_token_invalid"},
		{"another Run's token", func(_ *wikiAgentFixture, runID *uuid.UUID, _ *string) { *runID = uuid.New() }, http.StatusUnauthorized, "run_token_invalid"},
		{"Run of another member", func(f *wikiAgentFixture, _ *uuid.UUID, _ *string) {
			f.origin.AuthorizedByUserID, f.origin.OriginUserID = &otherUser, &otherUser
		}, http.StatusUnauthorized, "run_token_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newWikiAgentFixture("direct")
			runID, token := fixture.runID, wikiAgentRunToken
			tc.mutate(&fixture, &runID, &token)
			service := &wikiAgentWriteServiceStub{}
			result := fixture.post(fixture.writeHandler(service), runID, token, uuid.NewString(), "application/json", []byte(validWikiAgentWrite))
			if result.Code != tc.status || (tc.code != "" && !strings.Contains(result.Body.String(), `"code":"`+tc.code+`"`)) {
				t.Fatalf("write = %d %s, want %d %s", result.Code, result.Body.String(), tc.status, tc.code)
			}
			if tc.status != http.StatusOK && len(service.calls) != 0 {
				t.Fatal("a refused request reached the service")
			}
		})
	}
}

func TestWikiAgentWritePassesTheRunAndThePagesToTheService(t *testing.T) {
	t.Parallel()
	fixture := newWikiAgentFixture("direct")
	service := &wikiAgentWriteServiceStub{}
	h := fixture.writeHandler(service)
	result := fixture.post(h, fixture.runID, wikiAgentRunToken, uuid.NewString(), "application/json", []byte(validWikiAgentWrite))
	if result.Code != http.StatusOK {
		t.Fatalf("write = %d %s", result.Code, result.Body.String())
	}
	if got := result.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var body struct {
		Pages      []map[string]any `json:"pages"`
		Warnings   []string         `json:"warnings"`
		TotalPages int              `json:"totalPages"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil || len(body.Pages) != 1 || body.TotalPages != 1 ||
		body.Pages[0]["status"] != "created" || body.Pages[0]["pageId"] == nil || body.Warnings == nil {
		t.Fatalf("response = %s, %v", result.Body.String(), err)
	}
	if len(service.calls) != 1 || service.runs[0] != fixture.runID || service.users[0] != fixture.principal.UserID {
		t.Fatalf("service saw runs %v users %v", service.runs, service.users)
	}
	got := service.calls[0]
	if len(got.Pages) != 1 || got.Pages[0] != (wiki.AgentWritePageInput{
		Key: "company-overview", Title: "Company overview", Summary: "s", Markdown: "See [[products]]",
	}) {
		t.Fatalf("input = %+v", got)
	}
}

func TestWikiAgentWriteRefusesMalformedRequestsBeforeTheService(t *testing.T) {
	t.Parallel()
	fixture := newWikiAgentFixture("direct")
	service := &wikiAgentWriteServiceStub{}
	h := fixture.writeHandler(service)
	key := uuid.NewString()
	thirteen := `{"pages":[` + strings.TrimSuffix(strings.Repeat(`{"key":"a","title":"T","markdown":"x"},`, 13), ",") + `]}`
	for _, tc := range []struct {
		name        string
		key         string
		contentType string
		body        string
		status      int
		code        string
	}{
		{"no idempotency key", "", "application/json", validWikiAgentWrite, http.StatusUnprocessableEntity, "validation_failed"},
		{"no pages", key, "application/json", `{"pages":[]}`, http.StatusUnprocessableEntity, "validation_failed"},
		{"thirteen pages", key, "application/json", thirteen, http.StatusUnprocessableEntity, "validation_failed"},
		{"body over the cap", key, "application/json", `{"pages":[{"key":"a","title":"T","markdown":"` + strings.Repeat("x", maxWikiAgentWriteBytes) + `"}]}`, http.StatusRequestEntityTooLarge, "request_too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := fixture.post(h, fixture.runID, wikiAgentRunToken, tc.key, tc.contentType, []byte(tc.body))
			if result.Code != tc.status || (tc.code != "" && !strings.Contains(result.Body.String(), `"code":"`+tc.code+`"`)) {
				t.Fatalf("write = %d %s, want %d %s", result.Code, result.Body.String(), tc.status, tc.code)
			}
		})
	}
	if len(service.calls) != 0 {
		t.Fatalf("%d malformed requests reached the service", len(service.calls))
	}
}
