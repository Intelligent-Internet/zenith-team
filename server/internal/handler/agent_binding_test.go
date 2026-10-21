package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
)

type agentBindingServiceStub struct {
	bind   func(context.Context, auth.UserPrincipal, uuid.UUID, agent.BindInput) (agent.Response, error)
	unbind func(context.Context, auth.UserPrincipal, uuid.UUID) error
	list   func(context.Context, auth.UserPrincipal, agent.BindingListOptions) ([]agent.Binding, error)
}

func (s *agentBindingServiceStub) Bind(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	input agent.BindInput,
) (agent.Response, error) {
	if s.bind == nil {
		return agent.Response{}, errors.New("unexpected Agent Bind call")
	}
	return s.bind(ctx, principal, agentID, input)
}

func (s *agentBindingServiceStub) Unbind(ctx context.Context, principal auth.UserPrincipal, agentID uuid.UUID) error {
	if s.unbind == nil {
		return errors.New("unexpected Agent Unbind call")
	}
	return s.unbind(ctx, principal, agentID)
}

func (s *agentBindingServiceStub) ListForViewer(
	ctx context.Context,
	principal auth.UserPrincipal,
	options agent.BindingListOptions,
) ([]agent.Binding, error) {
	if s.list == nil {
		return nil, errors.New("unexpected Agent ListForViewer call")
	}
	return s.list(ctx, principal, options)
}

var _ AgentBindingService = (*agentBindingServiceStub)(nil)

// The binding routes are the member's own setup of an Agent on their
// machine (ADR 0014): member-gated, strictly bound, exact company scope,
// the agent projection back from a bind, no body from an unbind, and every
// service verdict mapped to its code.
func TestAgentBindingRoutesBindUnbindAndListForTheCaller(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	runtimeID := uuid.Must(uuid.NewV7())
	now := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	bound := agent.Response{
		ID: agentID, CompanyID: companyID, Name: "Lyra", Title: "Engineer", Provider: "codex",
		Binding: &agent.BindingRef{
			ID:      uuid.Must(uuid.NewV7()),
			AgentID: agentID,
			Runtime: agent.RuntimeRef{
				ID:       runtimeID,
				Name:     "Codex (MacBook)",
				Status:   "online",
				Provider: "codex",
			},
			CreatedAt: now,
		},
		RuntimeAvailable: true, Access: agent.Access{Configure: true, Execute: true},
		CreatedAt: now, UpdatedAt: now, Revision: 1,
	}
	var unbound []uuid.UUID
	var listOptions []agent.BindingListOptions
	handler := New(Handler{
		AuthService:    authenticatedAuthStub(userID),
		CompanyService: memberCompanyStub(t, userID, companyID),
		AgentService:   &agentServiceStub{},
		AgentBindingService: &agentBindingServiceStub{
			bind: func(_ context.Context, principal auth.UserPrincipal, gotAgentID uuid.UUID, input agent.BindInput) (agent.Response, error) {
				if principal.CompanyID != companyID || principal.UserID != userID ||
					gotAgentID != agentID || input.RuntimeID != runtimeID {
					t.Fatalf("Bind() scope/input = %+v / %s / %+v", principal, gotAgentID, input)
				}
				return bound, nil
			},
			unbind: func(_ context.Context, principal auth.UserPrincipal, gotAgentID uuid.UUID) error {
				if principal.CompanyID != companyID || principal.UserID != userID {
					t.Fatalf("Unbind() scope = %+v", principal)
				}
				unbound = append(unbound, gotAgentID)
				return nil
			},
			list: func(_ context.Context, principal auth.UserPrincipal, options agent.BindingListOptions) ([]agent.Binding, error) {
				if principal.CompanyID != companyID || principal.UserID != userID {
					t.Fatalf("ListForViewer() scope = %+v", principal)
				}
				listOptions = append(listOptions, options)
				if options.RuntimeID != nil {
					return nil, nil
				}
				return []agent.Binding{
					{
						ID:        bound.Binding.ID,
						AgentID:   agentID,
						Runtime:   bound.Binding.Runtime,
						CreatedAt: now,
					},
				}, nil
			},
		},
	})
	base := "/api/v1/companies/" + companyID.String()

	response := serveBearerJSON(handler, http.MethodPut, base+"/agents/"+agentID.String()+"/binding",
		[]byte(`{"runtimeId":"`+runtimeID.String()+`"}`))
	if response.Code != http.StatusOK {
		t.Fatalf("bind status = %d, body = %s", response.Code, response.Body.String())
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || got["binding"] == nil ||
		got["runtime"] != nil || string(got["runtimeAvailable"]) != "true" {
		t.Fatalf(
			"bind response = %s, %v; want the agent with the caller's binding",
			response.Body.String(),
			err,
		)
	}
	var binding struct {
		AgentID string `json:"agentId"`
		Runtime struct {
			ID string `json:"id"`
		} `json:"runtime"`
	}
	if err := json.Unmarshal(got["binding"], &binding); err != nil ||
		binding.AgentID != agentID.String() || binding.Runtime.ID != runtimeID.String() {
		t.Fatalf("binding = %s, %v", got["binding"], err)
	}

	response = serveBearerRequest(handler, http.MethodDelete, base+"/agents/"+agentID.String()+"/binding", nil)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 || len(unbound) != 1 ||
		unbound[0] != agentID {
		t.Fatalf(
			"unbind = %d %q (unbound %v), want 204 with no body",
			response.Code,
			response.Body.String(),
			unbound,
		)
	}

	response = serveBearerRequest(handler, http.MethodGet, base+"/agent-bindings", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", response.Code, response.Body.String())
	}
	var page struct {
		Items []agent.Binding `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || len(page.Items) != 1 ||
		page.Items[0].AgentID != agentID || page.Items[0].Runtime.ID != runtimeID {
		t.Fatalf("list = %s, %v", response.Body.String(), err)
	}
	response = serveBearerRequest(
		handler,
		http.MethodGet,
		base+"/agent-bindings?runtime_id="+runtimeID.String(),
		nil,
	)
	if response.Code != http.StatusOK || response.Body.String() != `{"items":[]}` {
		t.Fatalf("filtered list = %d %s, want an empty items array", response.Code, response.Body.String())
	}
	if len(listOptions) != 2 || listOptions[0].RuntimeID != nil ||
		listOptions[1].RuntimeID == nil || *listOptions[1].RuntimeID != runtimeID {
		t.Fatalf("ListForViewer() options = %+v", listOptions)
	}

	t.Run("binding", func(t *testing.T) {
		t.Parallel()
		refusing := New(Handler{
			AuthService:         authenticatedAuthStub(userID),
			CompanyService:      memberCompanyStub(t, userID, companyID),
			AgentService:        &agentServiceStub{},
			AgentBindingService: &agentBindingServiceStub{},
		})
		for name, test := range map[string]struct {
			method string
			path   string
			body   []byte
			status int
			code   string
		}{
			"missing runtime": {
				http.MethodPut, base + "/agents/" + agentID.String() + "/binding", []byte(`{}`),
				http.StatusUnprocessableEntity, "validation_failed",
			},
			"malformed runtime": {
				http.MethodPut, base + "/agents/" + agentID.String() + "/binding",
				[]byte(`{"runtimeId":"not-a-uuid"}`), http.StatusUnprocessableEntity, "validation_failed",
			},
			"unknown field": {
				http.MethodPut, base + "/agents/" + agentID.String() + "/binding",
				[]byte(`{"runtimeId":"` + runtimeID.String() + `","force":true}`), http.StatusBadRequest, "invalid_request",
			},
			"malformed agent id": {
				http.MethodPut, base + "/agents/not-a-uuid/binding",
				[]byte(`{"runtimeId":"` + runtimeID.String() + `"}`), http.StatusNotFound, "agent_not_found",
			},
			"malformed agent id on unbind": {
				http.MethodDelete, base + "/agents/not-a-uuid/binding", nil,
				http.StatusNotFound, "agent_not_found",
			},
			"unknown list filter": {
				http.MethodGet, base + "/agent-bindings?agent_id=" + agentID.String(), nil,
				http.StatusUnprocessableEntity, "validation_failed",
			},
			"malformed list filter": {
				http.MethodGet, base + "/agent-bindings?runtime_id=x", nil,
				http.StatusUnprocessableEntity, "validation_failed",
			},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				response := serveBearerRequest(refusing, test.method, test.path, nil)
				if test.body != nil {
					response = serveBearerJSON(refusing, test.method, test.path, test.body)
				}
				assertErrorResponse(t, response, test.status, test.code)
			})
		}
	})

	t.Run("verdicts", func(t *testing.T) {
		t.Parallel()
		for name, test := range map[string]struct {
			err    error
			status int
			code   string
		}{
			"agent unknown":        {agent.ErrNotFound, http.StatusNotFound, "agent_not_found"},
			"runtime not mine":     {agent.ErrRuntimeNotFound, http.StatusNotFound, "runtime_not_found"},
			"provider mismatch":    {agent.ErrBindingProviderMismatch, http.StatusUnprocessableEntity, "agent_binding_provider_mismatch"},
			"archived agent":       {agent.ErrBindingAgentArchived, http.StatusConflict, "agent_archived"},
			"failure":              {errors.New("boom"), http.StatusInternalServerError, "internal_error"},
			"membership not found": {agent.ErrNotFound, http.StatusNotFound, "agent_not_found"},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				failing := New(Handler{
					AuthService:    authenticatedAuthStub(userID),
					CompanyService: memberCompanyStub(t, userID, companyID),
					AgentService:   &agentServiceStub{},
					AgentBindingService: &agentBindingServiceStub{
						bind: func(context.Context, auth.UserPrincipal, uuid.UUID, agent.BindInput) (agent.Response, error) {
							return agent.Response{}, test.err
						},
					},
				})
				assertErrorResponse(t, serveBearerJSON(
					failing,
					http.MethodPut,
					base+"/agents/"+agentID.String()+"/binding",
					[]byte(`{"runtimeId":"`+runtimeID.String()+`"}`),
				),
					test.status, test.code)
			})
		}
	})
}
