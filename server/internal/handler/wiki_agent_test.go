package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/wiki"
)

const wikiAgentRunToken = "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// wikiAgentReadServiceStub records what reached the service so a test can
// prove a refused request never got that far.
type wikiAgentReadServiceStub struct {
	wiki.AgentReadService
	searchErr error
	searches  []wikiAgentSearchCall
	reads     int
	runReads  []uuid.UUID
	pageReads []int
}

type wikiAgentSearchCall struct {
	runID uuid.UUID
	query string
	limit int
}

func (s *wikiAgentReadServiceStub) Search(_ context.Context, _ auth.UserPrincipal, runID uuid.UUID, query string, limit int) (wiki.AgentSearchResponse, error) {
	s.searches = append(s.searches, wikiAgentSearchCall{runID, query, limit})
	return wiki.AgentSearchResponse{Items: []wiki.AgentSearchItem{}}, s.searchErr
}

func (s *wikiAgentReadServiceStub) Read(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int) (wiki.AgentReadResponse, error) {
	s.reads++
	return wiki.AgentReadResponse{}, nil
}

func (s *wikiAgentReadServiceStub) ListRunReads(_ context.Context, _ auth.UserPrincipal, runID uuid.UUID) (wiki.RunWikiReadList, error) {
	s.runReads = append(s.runReads, runID)
	return wiki.RunWikiReadList{Items: []wiki.RunWikiRead{}}, nil
}

func (s *wikiAgentReadServiceStub) ListPageAgentReads(_ context.Context, _ auth.UserPrincipal, _ uuid.UUID, limit int) (wiki.PageAgentReadList, error) {
	s.pageReads = append(s.pageReads, limit)
	return wiki.PageAgentReadList{Items: []wiki.PageAgentRead{}}, nil
}

type wikiAgentFixture struct {
	principal auth.UserPrincipal
	runID     uuid.UUID
	origin    execution.RunOrigin
	service   *wikiAgentReadServiceStub
}

func newWikiAgentFixture(kind string) wikiAgentFixture {
	principal := phase4Principal(uuid.New(), uuid.New())
	runID := uuid.New()
	user := principal.UserID
	return wikiAgentFixture{
		principal: principal, runID: runID, service: &wikiAgentReadServiceStub{},
		origin: execution.RunOrigin{
			RunID: runID, CompanyID: principal.CompanyID, AgentID: uuid.New(),
			ConversationID: uuid.New(), ConversationKind: kind,
			OriginUserID: &user, AuthorizedByUserID: &user,
		},
	}
}

func (f wikiAgentFixture) handler() *Handler {
	h := wikiTestHandler(f.principal, nil, nil)
	h.RunProofResolver = runProofResolverStub{origin: f.origin}
	h.WikiAgentReadService = f.service
	return h
}

func (f wikiAgentFixture) get(h *Handler, runID uuid.UUID, suffix, token string) *httptest.ResponseRecorder {
	path := "/api/v1/companies/" + f.principal.CompanyID.String() + "/runs/" + runID.String() + "/agent-wiki" + suffix
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer access-token")
	if token != "" {
		request.Header.Set(runTokenHeader, token)
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
}

func TestWikiAgentRoutesAdmitOnlyTheRunsOwnProof(t *testing.T) {
	t.Parallel()
	otherUser := uuid.New()
	for _, tc := range []struct {
		name   string
		mutate func(*wikiAgentFixture, *uuid.UUID, *string)
		status int
		code   string
	}{
		{"own task Run", func(*wikiAgentFixture, *uuid.UUID, *string) {}, http.StatusOK, ""},
		{"own direct Run", func(f *wikiAgentFixture, _ *uuid.UUID, _ *string) { f.origin.ConversationKind = "direct" }, http.StatusOK, ""},
		{"no Run token", func(_ *wikiAgentFixture, _ *uuid.UUID, token *string) { *token = "" }, http.StatusUnauthorized, "run_token_invalid"},
		{"unknown Run token", func(_ *wikiAgentFixture, _ *uuid.UUID, token *string) { *token = "brt_wrong" }, http.StatusUnauthorized, "run_token_invalid"},
		{"another Run's token", func(_ *wikiAgentFixture, runID *uuid.UUID, _ *string) { *runID = uuid.New() }, http.StatusUnauthorized, "run_token_invalid"},
		{"another Company's Run", func(f *wikiAgentFixture, _ *uuid.UUID, _ *string) { f.origin.CompanyID = uuid.New() }, http.StatusUnauthorized, "run_token_invalid"},
		{"Schedule Run", func(f *wikiAgentFixture, _ *uuid.UUID, _ *string) { f.origin.ConversationKind = "schedule" }, http.StatusUnauthorized, "run_token_invalid"},
		{"Run of another member", func(f *wikiAgentFixture, _ *uuid.UUID, _ *string) {
			f.origin.AuthorizedByUserID, f.origin.OriginUserID = &otherUser, &otherUser
		}, http.StatusUnauthorized, "run_token_invalid"},
		{"authorizer wins over opener", func(f *wikiAgentFixture, _ *uuid.UUID, _ *string) {
			f.origin.OriginUserID = &otherUser
		}, http.StatusOK, ""},
		// The proof's bearer is the Run's authorizer (runProofBearer, ADR
		// 0017): the opener of its chat never stands in for a missing one.
		{"a Run with no authorizer", func(f *wikiAgentFixture, _ *uuid.UUID, _ *string) {
			f.origin.AuthorizedByUserID = nil
		}, http.StatusUnauthorized, "run_token_invalid"},
		{"opener does not stand in for a named authorizer", func(f *wikiAgentFixture, _ *uuid.UUID, _ *string) {
			f.origin.AuthorizedByUserID = &otherUser
			f.origin.OriginUserID = &f.principal.UserID
		}, http.StatusUnauthorized, "run_token_invalid"},
		{"Run with no user", func(f *wikiAgentFixture, _ *uuid.UUID, _ *string) {
			f.origin.AuthorizedByUserID, f.origin.OriginUserID = nil, nil
		}, http.StatusUnauthorized, "run_token_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newWikiAgentFixture("task")
			runID, token := fixture.runID, wikiAgentRunToken
			tc.mutate(&fixture, &runID, &token)
			h := fixture.handler()
			for _, route := range []struct {
				suffix string
				served func() int
			}{
				{"/search?q=refunds", func() int { return len(fixture.service.searches) }},
				{"/pages/" + uuid.NewString(), func() int { return fixture.service.reads }},
			} {
				result := fixture.get(h, runID, route.suffix, token)
				if result.Code != tc.status || (tc.code != "" && !strings.Contains(result.Body.String(), `"code":"`+tc.code+`"`)) {
					t.Fatalf("%s = %d %s, want %d %s", route.suffix, result.Code, result.Body.String(), tc.status, tc.code)
				}
				if tc.status != http.StatusOK && route.served() != 0 {
					t.Fatalf("%s refused request reached the service", route.suffix)
				}
			}
		})
	}
}

func TestWikiAgentSearchParsesItsQueryParameters(t *testing.T) {
	t.Parallel()
	fixture := newWikiAgentFixture("task")
	h := fixture.handler()

	if result := fixture.get(h, fixture.runID, "/search?q=refund+policy", wikiAgentRunToken); result.Code != http.StatusOK {
		t.Fatalf("default search = %d %s", result.Code, result.Body.String())
	}
	if result := fixture.get(h, fixture.runID, "/search?q=refund&limit=10", wikiAgentRunToken); result.Code != http.StatusOK {
		t.Fatalf("explicit limit = %d %s", result.Code, result.Body.String())
	}
	want := []wikiAgentSearchCall{
		{fixture.runID, "refund policy", wiki.DefaultAgentSearchLimit},
		{fixture.runID, "refund", 10},
	}
	if len(fixture.service.searches) != 2 || fixture.service.searches[0] != want[0] || fixture.service.searches[1] != want[1] {
		t.Fatalf("service calls = %+v, want %+v", fixture.service.searches, want)
	}

	for _, suffix := range []string{"/search?q=x&limit=abc", "/pages/not-a-uuid", "/pages/" + uuid.NewString() + "?offset=x"} {
		assertErrorResponse(t, fixture.get(h, fixture.runID, suffix, wikiAgentRunToken), http.StatusUnprocessableEntity, "validation_failed")
	}
	if len(fixture.service.searches) != 2 || fixture.service.reads != 0 {
		t.Fatalf("invalid request reached the service")
	}
}

func TestWikiAgentRoutesReportDisabledAccessAndPrivateCaching(t *testing.T) {
	t.Parallel()
	fixture := newWikiAgentFixture("task")
	h := fixture.handler()

	for _, suffix := range []string{"/search?q=refunds", "/pages/" + uuid.NewString()} {
		result := fixture.get(h, fixture.runID, suffix, wikiAgentRunToken)
		if result.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", suffix, result.Code, result.Body.String())
		}
		if got := result.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Fatalf("%s Cache-Control = %q, want private, no-store", suffix, got)
		}
	}

	fixture.service.searchErr = wiki.ErrAgentAccessDisabled
	search := fixture.get(h, fixture.runID, "/search?q=refunds", wikiAgentRunToken)
	assertErrorResponse(t, search, http.StatusForbidden, "wiki_agent_access_disabled")
}

func TestWikiEvidenceRoutesAreHumanOnlyAndNameMissingRunsAsNotFound(t *testing.T) {
	t.Parallel()
	fixture := newWikiAgentFixture("task")
	h := fixture.handler()
	root := "/api/v1/companies/" + fixture.principal.CompanyID.String()

	result := serveBearerJSON(h, http.MethodGet, root+"/runs/not-a-uuid/wiki-reads", nil)
	assertErrorResponse(t, result, http.StatusNotFound, "run_not_found")
	if len(fixture.service.runReads) != 0 {
		t.Fatalf("malformed Run id reached the service: %v", fixture.service.runReads)
	}
	result = serveBearerJSON(h, http.MethodGet, root+"/runs/"+fixture.runID.String()+"/wiki-reads", nil)
	if result.Code != http.StatusOK {
		t.Fatalf("run reads = %d %s", result.Code, result.Body.String())
	}
	if len(fixture.service.runReads) != 1 || fixture.service.runReads[0] != fixture.runID {
		t.Fatalf("service run reads = %v, want [%s]", fixture.service.runReads, fixture.runID)
	}

	page := root + "/wiki/pages/" + uuid.NewString() + "/agent-reads"
	if result := serveBearerJSON(h, http.MethodGet, page, nil); result.Code != http.StatusOK {
		t.Fatalf("default limit = %d %s", result.Code, result.Body.String())
	}
	if result := serveBearerJSON(h, http.MethodGet, page+"?limit=abc", nil); result.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad limit = %d %s", result.Code, result.Body.String())
	}
	if len(fixture.service.pageReads) != 1 || fixture.service.pageReads[0] != wiki.DefaultPageAgentReadsLimit {
		t.Fatalf("page read limits = %v", fixture.service.pageReads)
	}

	request := httptest.NewRequest(http.MethodGet, root+"/runs/"+fixture.runID.String()+"/wiki-reads", nil)
	request.Header.Set("Authorization", "Bearer access-token")
	request.Header.Set(runTokenHeader, wikiAgentRunToken)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	assertErrorResponse(t, response, http.StatusForbidden, "human_actor_required")
}
