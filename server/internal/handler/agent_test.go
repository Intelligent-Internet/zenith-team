package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
)

func TestAgentCreateRouteReturnsDatabaseGeneratedAgent(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	parentID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	now := time.Date(2026, 7, 12, 3, 4, 5, 0, time.UTC)
	ownerPrincipal := auth.UserPrincipal{
		UserID:    userID,
		SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
		AuthKind:  auth.TokenKindBearer,
		CompanyID: companyID,
		Role:      auth.CompanyRoleOwner,
	}
	companies := &companyServiceStub{
		authorize: func(_ context.Context, _ auth.UserPrincipal, gotCompanyID uuid.UUID) (auth.UserPrincipal, error) {
			if gotCompanyID != companyID {
				t.Fatalf("Authorize() Company = %s, want %s", gotCompanyID, companyID)
			}
			return ownerPrincipal, nil
		},
	}
	runtimeID := uuid.Must(uuid.NewV7())
	agents := &agentServiceStub{
		create: func(_ context.Context, principal auth.UserPrincipal, input agent.CreateInput) (agent.Response, error) {
			if principal.CompanyID != companyID || input.ReportsToAgentID == nil ||
				*input.ReportsToAgentID != parentID ||
				input.Provider != "codex" ||
				input.RuntimeID == nil || *input.RuntimeID != runtimeID ||
				input.Instructions != "" ||
				input.Model != nil ||
				input.ThinkingLevel != nil ||
				input.MaxConcurrentRuns == nil || *input.MaxConcurrentRuns != 5 {
				t.Fatalf("Create() principal/input = %+v / %+v", principal, input)
			}
			return agent.Response{
				ID: agentID, CompanyID: companyID, Name: input.Name, Title: input.Title,
				Instructions: input.Instructions, ReportsToAgentID: &parentID,
				Provider:  "codex",
				CreatedAt: now, UpdatedAt: now, Revision: 1,
			}, nil
		},
	}
	handler := New(
		Handler{AuthService: authenticatedAuthStub(userID), CompanyService: companies, AgentService: agents},
	)
	body := validAgentCreateBody(t, parentID, runtimeID)
	// The cap is optional at creation: a supplied value reaches the domain
	// input as-is, under the same 1..10 rule as an update.
	body = []byte(strings.TrimSuffix(string(body), "}") + `,"maxConcurrentRuns":5}`)

	response := serveBearerJSON(
		handler,
		http.MethodPost,
		"/api/v1/companies/"+companyID.String()+"/agents",
		body,
	)
	if response.Code != http.StatusCreated {
		t.Fatalf("created status = %d, want 201; body = %s", response.Code, response.Body.String())
	}
	var got agent.Response
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode created Agent: %v", err)
	}
	if got.ID != agentID || got.CompanyID != companyID || got.Provider != "codex" {
		t.Fatalf("created Agent = %+v", got)
	}
}

func TestAgentReadRoutesReturnCompleteDerivedViews(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	runtimeID := uuid.Must(uuid.NewV7())
	now := time.Date(2026, 7, 12, 3, 4, 5, 0, time.UTC)
	view := agent.Response{
		ID:        agentID,
		CompanyID: companyID,
		Name:      "Researcher",
		Title:     "Senior Researcher",
		Binding: &agent.BindingRef{
			ID:      uuid.Must(uuid.NewV7()),
			Runtime: agent.RuntimeRef{ID: runtimeID, Name: "Codex workstation", Status: "online"},
		},
		RuntimeAvailable: true,
		Provider:         "codex",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	listCalls := 0
	agents := &agentServiceStub{
		list: func(_ context.Context, principal auth.UserPrincipal, options agent.ListOptions) ([]agent.Response, error) {
			listCalls++
			if principal.CompanyID != companyID {
				t.Fatalf("List() principal/options = %+v / %+v", principal, options)
			}
			if listCalls == 1 && (!options.IncludeArchived ||
				options.ReportsToAgentID == nil || *options.ReportsToAgentID != agentID) {
				t.Fatalf("filtered List() options = %+v", options)
			}
			if listCalls == 2 && (options.IncludeArchived || options.ReportsToAgentID != nil) {
				t.Fatalf("unfiltered List() options = %+v", options)
			}
			return []agent.Response{view}, nil
		},
		get: func(_ context.Context, principal auth.UserPrincipal, gotAgentID uuid.UUID) (agent.Response, error) {
			if principal.CompanyID != companyID || gotAgentID != agentID {
				t.Fatalf("Get() principal/Agent = %+v / %s", principal, gotAgentID)
			}
			return view, nil
		},
	}
	handler := New(Handler{
		AuthService: authenticatedAuthStub(
			userID,
		),
		CompanyService: memberCompanyStub(t, userID, companyID),
		AgentService:   agents,
	})
	response := serveBearerRequest(handler, http.MethodGet,
		"/api/v1/companies/"+companyID.String()+"/agents?include_archived=true"+
			"&reports_to_agent_id="+agentID.String(), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var page struct {
		Items []agent.Response `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode Agent page: %v", err)
	}
	if len(page.Items) != 1 || !page.Items[0].RuntimeAvailable {
		t.Fatalf("Agent page = %+v", page)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &keys); err != nil || len(keys) != 1 {
		t.Fatalf("Agent collection keys = %s, want items only", response.Body.String())
	}

	response = serveBearerRequest(handler, http.MethodGet,
		"/api/v1/companies/"+companyID.String()+"/agents", nil)
	if response.Code != http.StatusOK || listCalls != 2 {
		t.Fatalf(
			"unfiltered list response = %d / %s; calls = %d",
			response.Code,
			response.Body.String(),
			listCalls,
		)
	}

	response = serveBearerRequest(handler, http.MethodGet,
		"/api/v1/companies/"+companyID.String()+"/agents/"+agentID.String(), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var got agent.Response
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode Agent view: %v", err)
	}
	if got.ID != agentID || !got.RuntimeAvailable {
		t.Fatalf("Agent view = %+v", got)
	}
	// The flat canonical response carries the viewer's binding (ADR 0014)
	// and no runtime of its own: the template has no machine.
	var object map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &object); err != nil || object["agent"] != nil ||
		object["availability"] != nil || object["runtime"] != nil || object["runtimeId"] != nil ||
		object["binding"] == nil || object["runtimeAvailable"] == nil {
		t.Fatalf("Agent read is not the flat canonical response: %s", response.Body.String())
	}
}

func TestAgentReadRoutesRejectInvalidQueriesAndIDs(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	agents := &agentServiceStub{
		list: func(context.Context, auth.UserPrincipal, agent.ListOptions) ([]agent.Response, error) {
			t.Fatal("List() called for invalid query")
			return nil, nil
		},
		get: func(context.Context, auth.UserPrincipal, uuid.UUID) (agent.Response, error) {
			t.Fatal("Get() called for invalid Agent ID")
			return agent.Response{}, nil
		},
	}
	handler := New(Handler{
		AuthService: authenticatedAuthStub(
			userID,
		),
		CompanyService: memberCompanyStub(t, userID, companyID),
		AgentService:   agents,
	})
	for _, path := range []string{
		"/api/v1/companies/" + companyID.String() + "/agents?unknown=true",
		"/api/v1/companies/" + companyID.String() + "/agents?limit=25",
		"/api/v1/companies/" + companyID.String() + "/agents?cursor=retired",
		"/api/v1/companies/" + companyID.String() + "/agents?includeArchived=true",
		"/api/v1/companies/" + companyID.String() + "/agents?include_archived=",
		"/api/v1/companies/" + companyID.String() + "/agents?include_archived=True",
		"/api/v1/companies/" + companyID.String() + "/agents?include_archived=true&include_archived=false",
		// The runtime filter went with the machine: bindings are listed
		// per member under /agent-bindings instead.
		"/api/v1/companies/" + companyID.String() + "/agents?runtime_id=" + uuid.NewString(),
		"/api/v1/companies/" + companyID.String() + "/agents?reports_to_agent_id=",
		"/api/v1/companies/" + companyID.String() + "/agents?reports_to_agent_id=not-a-uuid",
		"/api/v1/companies/" + companyID.String() + "/agents?reports_to_agent_id=" + uuid.NewString() + "&reports_to_agent_id=" + uuid.NewString(),
	} {
		response := serveBearerRequest(handler, http.MethodGet, path, nil)
		assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
	}
	response := serveBearerRequest(handler, http.MethodGet,
		"/api/v1/companies/"+companyID.String()+"/agents/not-a-uuid", nil)
	assertErrorResponse(t, response, http.StatusNotFound, "agent_not_found")
}

func TestAgentLifecycleRoutesPreservePartialAndNullableInputs(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	parentID := uuid.Must(uuid.NewV7())
	now := time.Date(2026, 7, 12, 3, 4, 5, 0, time.UTC)
	want := agent.Response{
		ID: agentID, CompanyID: companyID, Name: "Principal Researcher", Title: "Senior Researcher",
		ReportsToAgentID: &parentID, Provider: "claude",
		CreatedAt: now, UpdatedAt: now.Add(time.Minute), Revision: 2,
	}
	agents := &agentServiceStub{
		update: func(_ context.Context, principal auth.UserPrincipal, gotAgentID uuid.UUID, input agent.UpdateInput) (agent.Response, error) {
			if principal.CompanyID != companyID || gotAgentID != agentID || input.ExpectedRevision != 1 ||
				input.Name == nil || *input.Name != " Principal Researcher " || input.ReportsToAgentID == nil ||
				*input.ReportsToAgentID != parentID ||
				!input.Model.Present || input.Model.Value != nil || !input.ThinkingLevel.Present ||
				input.ThinkingLevel.Value == nil || *input.ThinkingLevel.Value != "high" ||
				input.MaxConcurrentRuns == nil || *input.MaxConcurrentRuns != 5 {
				t.Fatalf(
					"Update() principal/Agent/input = %+v / %s / %+v",
					principal,
					gotAgentID,
					input,
				)
			}
			return want, nil
		},
		archive: func(_ context.Context, _ auth.UserPrincipal, gotAgentID uuid.UUID, input agent.RevisionInput) (agent.Response, error) {
			if gotAgentID != agentID || input.ExpectedRevision != 2 {
				t.Fatalf("Archive() Agent/input = %s / %+v", gotAgentID, input)
			}
			return want, nil
		},
		restore: func(_ context.Context, _ auth.UserPrincipal, gotAgentID uuid.UUID, input agent.RevisionInput) (agent.Response, error) {
			if gotAgentID != agentID || input.ExpectedRevision != 3 {
				t.Fatalf("Restore() Agent/input = %s / %+v", gotAgentID, input)
			}
			return want, nil
		},
	}
	handler := New(Handler{
		AuthService: authenticatedAuthStub(
			userID,
		),
		CompanyService: memberCompanyStub(t, userID, companyID),
		AgentService:   agents,
	})
	base := "/api/v1/companies/" + companyID.String() + "/agents/" + agentID.String()
	updateBody := []byte(
		`{"expectedRevision":1,"name":" Principal Researcher ","reportsToAgentId":"` + parentID.String() + `","model":null,"thinkingLevel":"high","maxConcurrentRuns":5}`,
	)
	for name, request := range map[string]struct {
		method string
		path   string
		body   []byte
	}{
		"update":  {http.MethodPatch, base, updateBody},
		"archive": {http.MethodPost, base + "/archive", []byte(`{"expectedRevision":2}`)},
		"restore": {http.MethodPost, base + "/restore", []byte(`{"expectedRevision":3}`)},
	} {
		t.Run(name, func(t *testing.T) {
			response := serveBearerJSON(handler, request.method, request.path, request.body)
			if response.Code != http.StatusOK {
				t.Fatalf(
					"%s status = %d, want 200; body = %s",
					name,
					response.Code,
					response.Body.String(),
				)
			}
			var got agent.Response
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode %s Agent: %v", name, err)
			}
			if got.ID != agentID || got.Revision != want.Revision {
				t.Fatalf("%s Agent = %+v", name, got)
			}
		})
	}
}

func TestAgentLifecycleRoutesRejectStrictInvalidBodies(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	calls := 0
	agents := &agentServiceStub{
		update: func(context.Context, auth.UserPrincipal, uuid.UUID, agent.UpdateInput) (agent.Response, error) {
			calls++
			return agent.Response{}, nil
		},
		archive: func(context.Context, auth.UserPrincipal, uuid.UUID, agent.RevisionInput) (agent.Response, error) {
			calls++
			return agent.Response{}, nil
		},
	}
	handler := New(Handler{
		AuthService: authenticatedAuthStub(
			userID,
		),
		CompanyService: memberCompanyStub(t, userID, companyID),
		AgentService:   agents,
	})
	base := "/api/v1/companies/" + companyID.String() + "/agents/" + agentID.String()
	for name, test := range map[string]struct {
		method string
		path   string
		body   string
		status int
		code   string
	}{
		"empty update":       {http.MethodPatch, base, `{"expectedRevision":1}`, http.StatusUnprocessableEntity, "validation_failed"},
		"null name":          {http.MethodPatch, base, `{"expectedRevision":1,"name":null}`, http.StatusBadRequest, "invalid_request"},
		"null capacity":      {http.MethodPatch, base, `{"expectedRevision":1,"maxConcurrentRuns":null}`, http.StatusBadRequest, "invalid_request"},
		"zero capacity":      {http.MethodPatch, base, `{"expectedRevision":1,"maxConcurrentRuns":0}`, http.StatusUnprocessableEntity, "validation_failed"},
		"capacity above cap": {http.MethodPatch, base, `{"expectedRevision":1,"maxConcurrentRuns":11}`, http.StatusUnprocessableEntity, "validation_failed"},
		"null parent":        {http.MethodPatch, base, `{"expectedRevision":1,"reportsToAgentId":null}`, http.StatusBadRequest, "invalid_request"},
		// The machine left the template (ADR 0014): a runtime on an update
		// is an unknown field, and the provider is immutable, which gets its
		// own 422 (TestAgentUpdateRouteExplainsTheFixedProvider).
		"runtime on update":   {http.MethodPatch, base, `{"expectedRevision":1,"runtimeId":"` + uuid.NewString() + `"}`, http.StatusBadRequest, "invalid_request"},
		"provider on update":  {http.MethodPatch, base, `{"expectedRevision":1,"provider":"codex"}`, http.StatusUnprocessableEntity, "validation_failed"},
		"wrong model type":    {http.MethodPatch, base, `{"expectedRevision":1,"model":7}`, http.StatusBadRequest, "invalid_request"},
		"unknown field":       {http.MethodPatch, base, `{"expectedRevision":1,"name":"Name","settings":{}}`, http.StatusBadRequest, "invalid_request"},
		"missing version":     {http.MethodPatch, base, `{"name":"Name"}`, http.StatusUnprocessableEntity, "validation_failed"},
		"empty archive":       {http.MethodPost, base + "/archive", `{}`, http.StatusUnprocessableEntity, "validation_failed"},
		"archive extra field": {http.MethodPost, base + "/archive", `{"expectedRevision":1,"force":true}`, http.StatusBadRequest, "invalid_request"},
	} {
		t.Run(name, func(t *testing.T) {
			response := serveBearerJSON(handler, test.method, test.path, []byte(test.body))
			assertErrorResponse(t, response, test.status, test.code)
		})
	}
	if calls != 0 {
		t.Fatalf("Agent lifecycle service calls for rejected bodies = %d, want 0", calls)
	}
}

// An Agent's provider is fixed (ADR 0014). Naming it on an update is a
// readable request with a rule to explain, not an unknown field: 422 with a
// detail that says to duplicate the agent, whatever else the body carries.
func TestAgentUpdateRouteExplainsTheFixedProvider(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	agents := &agentServiceStub{
		update: func(context.Context, auth.UserPrincipal, uuid.UUID, agent.UpdateInput) (agent.Response, error) {
			t.Fatal("AgentService.Update() called for a provider change")
			return agent.Response{}, nil
		},
	}
	handler := New(Handler{
		AuthService:    authenticatedAuthStub(userID),
		CompanyService: memberCompanyStub(t, userID, companyID),
		AgentService:   agents,
	})
	path := "/api/v1/companies/" + companyID.String() + "/agents/" + agentID.String()
	for name, body := range map[string]string{
		"provider alone":      `{"expectedRevision":2,"provider":"claude"}`,
		"provider with name":  `{"expectedRevision":2,"name":"Lyra","provider":"codex"}`,
		"provider as null":    `{"expectedRevision":2,"provider":null}`,
		"provider as garbage": `{"expectedRevision":2,"provider":7}`,
	} {
		t.Run(name, func(t *testing.T) {
			response := serveBearerJSON(handler, http.MethodPatch, path, []byte(body))
			assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
			var envelope struct {
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode error envelope: %v", err)
			}
			want := "An agent's provider is fixed; duplicate the agent to use the other provider"
			if envelope.Detail != want {
				t.Fatalf("detail = %q, want %q", envelope.Detail, want)
			}
		})
	}
}

func TestAgentLifecycleRoutesMapStableDomainErrors(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	base := "/api/v1/companies/" + companyID.String() + "/agents/" + agentID.String() + "/archive"
	for name, test := range map[string]struct {
		err  error
		code string
	}{
		"revision conflict": {agent.ErrRevisionConflict, "agent_revision_conflict"},
		"reports-to cycle":  {agent.ErrCycle, "agent_reports_to_cycle"},
		"root immutable":    {agent.ErrRootImmutable, "root_agent_topology_immutable"},
		"archive blocked":   {agent.ErrArchiveBlocked, "agent_archive_blocked"},
	} {
		t.Run(name, func(t *testing.T) {
			agents := &agentServiceStub{
				archive: func(context.Context, auth.UserPrincipal, uuid.UUID, agent.RevisionInput) (agent.Response, error) {
					return agent.Response{}, test.err
				},
			}
			handler := New(Handler{
				AuthService: authenticatedAuthStub(
					userID,
				),
				CompanyService: memberCompanyStub(t, userID, companyID),
				AgentService:   agents,
			})
			response := serveBearerJSON(handler, http.MethodPost, base, []byte(`{"expectedRevision":1}`))
			assertErrorResponse(t, response, http.StatusConflict, test.code)
		})
	}
}

func TestAgentCreateRouteRejectsInvalidAndOversizedBodies(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	parentID := uuid.Must(uuid.NewV7())
	createCalls := 0
	companies := memberCompanyStub(t, userID, companyID)
	agents := &agentServiceStub{
		create: func(context.Context, auth.UserPrincipal, agent.CreateInput) (agent.Response, error) {
			createCalls++
			return agent.Response{}, nil
		},
	}
	handler := New(
		Handler{AuthService: authenticatedAuthStub(userID), CompanyService: companies, AgentService: agents},
	)
	valid := string(validAgentCreateBody(t, parentID, uuid.Must(uuid.NewV7())))

	tests := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{
			"unknown field",
			strings.TrimSuffix(valid, "}") + `,"settings":{}}`,
			http.StatusBadRequest,
			"invalid_request",
		},
		{
			"forbidden status",
			strings.TrimSuffix(valid, "}") + `,"status":"running"}`,
			http.StatusBadRequest,
			"invalid_request",
		},
		{
			"missing model",
			strings.Replace(valid, `"model":null,`, "", 1),
			http.StatusUnprocessableEntity,
			"validation_failed",
		},
		{
			"missing thinking level",
			strings.Replace(valid, `,"thinkingLevel":null`, "", 1),
			http.StatusUnprocessableEntity,
			"validation_failed",
		},
		{
			"retired Agent id field",
			strings.TrimSuffix(valid, "}") + `,"id":"` + uuid.Must(uuid.NewV7()).String() + `"}`,
			http.StatusBadRequest,
			"invalid_request",
		},
		{
			"malformed parent ID",
			strings.Replace(valid, parentID.String(), "not-a-uuid", 1),
			http.StatusUnprocessableEntity,
			"validation_failed",
		},
		{
			"missing provider",
			strings.Replace(valid, `"provider":"codex",`, "", 1),
			http.StatusUnprocessableEntity,
			"validation_failed",
		},
		{
			"unknown provider",
			strings.Replace(valid, `"provider":"codex"`, `"provider":"gemini"`, 1),
			http.StatusUnprocessableEntity,
			"validation_failed",
		},
		{
			"malformed runtime ID",
			strings.Replace(valid, `"runtimeId":"`, `"runtimeId":"x`, 1),
			http.StatusUnprocessableEntity,
			"validation_failed",
		},
		{
			"null instructions",
			strings.TrimSuffix(valid, "}") + `,"instructions":null}`,
			http.StatusBadRequest,
			"invalid_request",
		},
		{
			"null capacity",
			strings.TrimSuffix(valid, "}") + `,"maxConcurrentRuns":null}`,
			http.StatusBadRequest,
			"invalid_request",
		},
		{
			"capacity above cap",
			strings.TrimSuffix(valid, "}") + `,"maxConcurrentRuns":11}`,
			http.StatusUnprocessableEntity,
			"validation_failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := serveBearerJSON(
				handler, http.MethodPost,
				"/api/v1/companies/"+companyID.String()+"/agents", []byte(test.body),
			)
			assertErrorResponse(t, response, test.status, test.code)
		})
	}
	if createCalls != 0 {
		t.Fatalf("Create() calls for rejected bodies = %d, want 0", createCalls)
	}

	oversized := []byte(`{"name":"` + strings.Repeat("a", maxAgentBodyBytes) + `"}`)
	response := serveBearerJSON(
		handler,
		http.MethodPost,
		"/api/v1/companies/"+companyID.String()+"/agents",
		oversized,
	)
	assertErrorResponse(t, response, http.StatusRequestEntityTooLarge, "request_too_large")

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/companies/"+companyID.String()+"/agents",
		bytes.NewReader(validAgentCreateBody(t, parentID, uuid.Must(uuid.NewV7()))),
	)
	request.Header.Set("Authorization", "Bearer access-token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertErrorResponse(t, response, http.StatusUnsupportedMediaType, "unsupported_media_type")
}

func TestAgentCreateRouteMapsAuthorizationAndDomainErrors(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	parentID := uuid.Must(uuid.NewV7())
	body := validAgentCreateBody(t, parentID, uuid.Must(uuid.NewV7()))

	t.Run("user authentication", func(t *testing.T) {
		handler := New(
			Handler{
				AuthService:    authenticatedAuthStub(userID),
				CompanyService: memberCompanyStub(t, userID, companyID),
				AgentService:   &agentServiceStub{},
			},
		)
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/companies/"+companyID.String()+"/agents",
			bytes.NewReader(body),
		)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertErrorResponse(t, response, http.StatusUnauthorized, "authentication_required")
	})

	for _, middlewareError := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"revoked or cross-Company actor", company.ErrNotFound, http.StatusNotFound, "company_not_found"},
	} {
		t.Run(middlewareError.name, func(t *testing.T) {
			companies := &companyServiceStub{
				authorize: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
					return auth.UserPrincipal{}, middlewareError.err
				},
			}
			agents := &agentServiceStub{
				create: func(context.Context, auth.UserPrincipal, agent.CreateInput) (agent.Response, error) {
					t.Fatal("Create() called after rejected Company authorization")
					return agent.Response{}, nil
				},
			}
			handler := New(
				Handler{
					AuthService:    authenticatedAuthStub(userID),
					CompanyService: companies,
					AgentService:   agents,
				},
			)
			response := serveBearerJSON(
				handler,
				http.MethodPost,
				"/api/v1/companies/"+companyID.String()+"/agents",
				body,
			)
			assertErrorResponse(t, response, middlewareError.status, middlewareError.code)
		})
	}

	for _, domainError := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"stale unauthenticated principal", agent.ErrUnauthenticated, http.StatusUnauthorized, "authentication_required"},
		{"invalid input", agent.ErrInvalidInput, http.StatusUnprocessableEntity, "validation_failed"},
		{"topology conflict", agent.ErrConflict, http.StatusConflict, "agent_create_conflict"},
		{"missing parent or revoked actor", agent.ErrNotFound, http.StatusNotFound, "agent_not_found"},
		{"member creating the root agent", agent.ErrForbidden, http.StatusForbidden, "agent_creator_required"},
		{"internal failure", errors.New("database unavailable"), http.StatusInternalServerError, "internal_error"},
	} {
		t.Run(domainError.name, func(t *testing.T) {
			agents := &agentServiceStub{
				create: func(context.Context, auth.UserPrincipal, agent.CreateInput) (agent.Response, error) {
					return agent.Response{}, domainError.err
				},
			}
			handler := New(
				Handler{
					AuthService:    authenticatedAuthStub(userID),
					CompanyService: memberCompanyStub(t, userID, companyID),
					AgentService:   agents,
				},
			)
			response := serveBearerJSON(
				handler, http.MethodPost,
				"/api/v1/companies/"+companyID.String()+"/agents", body,
			)
			assertErrorResponse(t, response, domainError.status, domainError.code)
			if strings.Contains(response.Body.String(), "database unavailable") {
				t.Fatal("Agent error response leaked internal failure")
			}
		})
	}
}

func TestAgentCreateRouteRequiresCompleteDependencies(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	parentID := uuid.Must(uuid.NewV7())
	body := validAgentCreateBody(t, parentID, uuid.Must(uuid.NewV7()))
	authService := authenticatedAuthStub(userID)
	companyService := memberCompanyStub(t, userID, companyID)
	agentService := &agentServiceStub{
		create: func(context.Context, auth.UserPrincipal, agent.CreateInput) (agent.Response, error) {
			return agent.Response{Provider: "claude"}, nil
		},
	}

	for name, options := range map[string]Handler{
		"missing Auth":    {CompanyService: companyService, AgentService: agentService},
		"missing Company": {AuthService: authService, AgentService: agentService},
		"missing Agent":   {AuthService: authService, CompanyService: companyService},
	} {
		t.Run(name, func(t *testing.T) {
			response := serveBearerJSON(
				New(options),
				http.MethodPost,
				"/api/v1/companies/"+companyID.String()+"/agents",
				body,
			)
			if response.Code != http.StatusNotFound {
				t.Fatalf(
					"incomplete dependency route status = %d, want 404; body = %s",
					response.Code,
					response.Body.String(),
				)
			}
		})
	}
	response := serveBearerJSON(
		New(Handler{AuthService: authService, CompanyService: companyService, AgentService: agentService}),
		http.MethodPost,
		"/api/v1/companies/"+companyID.String()+"/agents",
		body,
	)
	if response.Code != http.StatusCreated {
		t.Fatalf(
			"complete dependency route status = %d, want 201; body = %s",
			response.Code,
			response.Body.String(),
		)
	}
}

type agentServiceStub struct {
	create  func(context.Context, auth.UserPrincipal, agent.CreateInput) (agent.Response, error)
	list    func(context.Context, auth.UserPrincipal, agent.ListOptions) ([]agent.Response, error)
	get     func(context.Context, auth.UserPrincipal, uuid.UUID) (agent.Response, error)
	update  func(context.Context, auth.UserPrincipal, uuid.UUID, agent.UpdateInput) (agent.Response, error)
	archive func(context.Context, auth.UserPrincipal, uuid.UUID, agent.RevisionInput) (agent.Response, error)
	restore func(context.Context, auth.UserPrincipal, uuid.UUID, agent.RevisionInput) (agent.Response, error)
}

func (s *agentServiceStub) List(
	ctx context.Context,
	principal auth.UserPrincipal,
	options agent.ListOptions,
) ([]agent.Response, error) {
	if s.list == nil {
		return nil, errors.New("unexpected Agent List call")
	}
	return s.list(ctx, principal, options)
}

func (s *agentServiceStub) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
) (agent.Response, error) {
	if s.get == nil {
		return agent.Response{}, errors.New("unexpected Agent Get call")
	}
	return s.get(ctx, principal, agentID)
}

func (s *agentServiceStub) Create(
	ctx context.Context,
	principal auth.UserPrincipal,
	input agent.CreateInput,
) (agent.Response, error) {
	if s.create == nil {
		return agent.Response{}, errors.New("unexpected Agent Create call")
	}
	return s.create(ctx, principal, input)
}

func (s *agentServiceStub) Update(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	input agent.UpdateInput,
) (agent.Response, error) {
	if s.update == nil {
		return agent.Response{}, errors.New("unexpected Agent Update call")
	}
	return s.update(ctx, principal, agentID, input)
}

func (s *agentServiceStub) Archive(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	input agent.RevisionInput,
) (agent.Response, error) {
	if s.archive == nil {
		return agent.Response{}, errors.New("unexpected Agent Archive call")
	}
	return s.archive(ctx, principal, agentID, input)
}

func (s *agentServiceStub) Restore(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	input agent.RevisionInput,
) (agent.Response, error) {
	if s.restore == nil {
		return agent.Response{}, errors.New("unexpected Agent Restore call")
	}
	return s.restore(ctx, principal, agentID, input)
}

// validAgentCreateBody is a codex template the caller also sets up on their
// runtime at creation (ADR 0014); the key order is what the strict-body
// cases splice on.
func validAgentCreateBody(t *testing.T, parentID, runtimeID uuid.UUID) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"name": "Researcher", "title": "Senior Researcher",
		"reportsToAgentId": parentID, "provider": "codex", "runtimeId": runtimeID,
		"model": nil, "thinkingLevel": nil,
	})
	if err != nil {
		t.Fatalf("marshal Agent create body: %v", err)
	}
	return body
}

var _ AgentService = (*agentServiceStub)(nil)

func TestAgentCreateRequestFormattingAssumption(t *testing.T) {
	// Guard the literal replacements used by strict-body cases above.
	body := string(
		validAgentCreateBody(t, uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())),
	)
	for _, field := range []string{`"name":"Researcher"`, `"model":null`, `"thinkingLevel":null`, `"provider":"codex",`, `"runtimeId":"`} {
		if !strings.Contains(body, field) {
			t.Fatalf("JSON fixture %s does not contain %s", body, fmt.Sprintf("%q", field))
		}
	}
}
