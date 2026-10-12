package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/mcp/tools"
	"boardly.dev/boardly/server/internal/workflow"
)

func workflowTestHandler(t *testing.T) (http.Handler, string) {
	t.Helper()
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	member := auth.UserPrincipal{
		UserID: userID, SessionID: uuid.Must(uuid.NewV7()), AuthKind: auth.TokenKindBearer,
		CompanyID: companyID, Role: auth.CompanyRoleMember,
	}
	companies := &companyServiceStub{
		authorize: func(_ context.Context, _ auth.UserPrincipal, gotCompanyID uuid.UUID) (auth.UserPrincipal, error) {
			if gotCompanyID != companyID {
				t.Fatalf("Authorize() Company = %s, want %s", gotCompanyID, companyID)
			}
			return member, nil
		},
	}
	handler := New(Handler{AuthService: authenticatedAuthStub(userID), CompanyService: companies})
	return handler, "/api/v1/companies/" + companyID.String() + "/workflows"
}

// Any member reads the Workflow catalog: the cards in name order, without
// the forms, which the launch dialog reads one Workflow at a time.
func TestWorkflowsListTheCatalogForMembers(t *testing.T) {
	handler, path := workflowTestHandler(t)

	response := serveBearerJSON(handler, http.MethodGet, path, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var got struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want, err := workflow.List()
	if err != nil {
		t.Fatalf("workflow.List() error = %v", err)
	}
	if len(got.Items) != len(want) || got.Items[0]["key"] != want[0].Key {
		t.Fatalf("items = %d (first %v), want %d (first %s)", len(got.Items), got.Items[0]["key"], len(want), want[0].Key)
	}
	if _, hasForm := got.Items[0]["pages"]; hasForm {
		t.Fatalf("list item carries the form: %v", got.Items[0])
	}

	// The read takes no query parameters.
	response = serveBearerJSON(handler, http.MethodGet, path+"?category=legal", nil)
	assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
}

// One Workflow carries its form, brief template and the playbook reference a
// Task Run loads; an unknown key is a named 404.
func TestWorkflowDetailCarriesTheFormAndPlaybookReference(t *testing.T) {
	handler, path := workflowTestHandler(t)
	summaries, err := workflow.List()
	if err != nil {
		t.Fatalf("workflow.List() error = %v", err)
	}
	key := summaries[0].Key

	response := serveBearerJSON(handler, http.MethodGet, path+"/"+key, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var got workflow.Workflow
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Key != key || len(got.Pages) == 0 || got.Brief == "" || got.SubmitLabel == "" ||
		got.Playbook.SkillKey != tools.ZenithWorkflowsSkillKey || got.Playbook.Path != key+"/playbook.md" {
		t.Fatalf("detail = %+v", got)
	}

	response = serveBearerJSON(handler, http.MethodGet, path+"/no-such-workflow", nil)
	assertErrorResponse(t, response, http.StatusNotFound, "workflow_not_found")

	response = serveBearerJSON(handler, http.MethodGet, path+"/"+key+"?preview=1", nil)
	assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
}

func workflowReadinessTestHandler(
	t *testing.T,
	readiness func(context.Context, auth.UserPrincipal, agent.ConnectorAppReadinessInput) (agent.ConnectorAppReadinessResult, error),
) (http.Handler, string, auth.UserPrincipal) {
	t.Helper()
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	member := auth.UserPrincipal{
		UserID: userID, SessionID: uuid.Must(uuid.NewV7()), AuthKind: auth.TokenKindBearer,
		CompanyID: companyID, Role: auth.CompanyRoleMember,
	}
	companies := &companyServiceStub{
		authorize: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
			return member, nil
		},
	}
	handler := New(Handler{
		AuthService: authenticatedAuthStub(userID), CompanyService: companies,
		ConnectorService: &connectorServiceStub{readiness: readiness},
	})
	return handler, "/api/v1/companies/" + companyID.String() + "/workflows", member
}

func workflowWithApps(t *testing.T) workflow.Workflow {
	t.Helper()
	summaries, err := workflow.List()
	if err != nil {
		t.Fatalf("workflow.List() error = %v", err)
	}
	for _, summary := range summaries {
		if len(summary.Connectors) > 1 {
			item, err := workflow.Get(summary.Key)
			if err != nil {
				t.Fatalf("workflow.Get(%q) error = %v", summary.Key, err)
			}
			return item
		}
	}
	t.Fatal("no shipped Workflow carries two or more app hints")
	return workflow.Workflow{}
}

// Any member reads a Workflow's app readiness for one Agent: the handler
// passes the Workflow's hints in order and answers the WorkflowReadiness
// contract without ids the domain chose not to reveal.
func TestWorkflowReadinessIsMemberReadable(t *testing.T) {
	item := workflowWithApps(t)
	agentID := uuid.Must(uuid.NewV7())
	runtimeID := uuid.Must(uuid.NewV7())
	observedAt := "2026-09-24T09:30:00Z"
	var member auth.UserPrincipal
	handler, path, member := workflowReadinessTestHandler(t, func(
		_ context.Context,
		principal auth.UserPrincipal,
		input agent.ConnectorAppReadinessInput,
	) (agent.ConnectorAppReadinessResult, error) {
		if principal.UserID != member.UserID || principal.Role != auth.CompanyRoleMember ||
			input.AgentID != agentID || len(input.Apps) != len(item.Connectors) {
			t.Fatalf("ReadAppReadiness() principal = %+v input = %+v", principal, input)
		}
		apps := make([]agent.ConnectorAppReadiness, len(input.Apps))
		for index, app := range input.Apps {
			if string(app) != string(item.Connectors[index]) {
				t.Fatalf("app hints = %v, want the Workflow's %v", input.Apps, item.Connectors)
			}
			apps[index] = agent.ConnectorAppReadiness{App: app, State: agent.ConnectorAppStateReady}
		}
		return agent.ConnectorAppReadinessResult{
			AgentID: agentID, Provider: agent.ConnectorProviderCodex,
			RuntimeID: uuid.NullUUID{UUID: runtimeID, Valid: true},
			Discovery: agent.ConnectorAppDiscovery{State: agent.ConnectorDiscoveryStateFresh, ObservedAt: &observedAt},
			Apps:      apps,
		}, nil
	})

	response := serveBearerJSON(handler, http.MethodGet, path+"/"+item.Key+"/readiness?agent_id="+agentID.String(), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	apps, _ := got["apps"].([]any)
	first, _ := apps[0].(map[string]any)
	discovery, _ := got["discovery"].(map[string]any)
	if got["schemaVersion"] != float64(1) || got["workflowKey"] != item.Key ||
		got["agentId"] != agentID.String() || got["provider"] != "codex" ||
		got["runtimeId"] != runtimeID.String() || discovery["state"] != "fresh" ||
		discovery["observedAt"] != observedAt || len(apps) != len(item.Connectors) ||
		first["app"] != string(item.Connectors[0]) || first["state"] != "ready" {
		t.Fatalf("readiness = %s", response.Body.String())
	}
	if _, disclosed := first["connector"]; disclosed {
		t.Fatalf("readiness disclosed a Connector the domain withheld: %s", response.Body.String())
	}
}

// A viewer who has not set the Agent up reads it `unbound`: the Agent's
// provider, a null runtimeId (written, not omitted: the contract requires
// it) and every app unknown.
func TestWorkflowReadinessOfAnAgentTheViewerHasNotSetUp(t *testing.T) {
	item := workflowWithApps(t)
	agentID := uuid.Must(uuid.NewV7())
	handler, path, _ := workflowReadinessTestHandler(t, func(
		_ context.Context,
		_ auth.UserPrincipal,
		input agent.ConnectorAppReadinessInput,
	) (agent.ConnectorAppReadinessResult, error) {
		apps := make([]agent.ConnectorAppReadiness, len(input.Apps))
		for index, app := range input.Apps {
			apps[index] = agent.ConnectorAppReadiness{App: app, State: agent.ConnectorAppStateUnknown}
		}
		return agent.ConnectorAppReadinessResult{
			AgentID: agentID, Provider: agent.ConnectorProviderClaude,
			Discovery: agent.ConnectorAppDiscovery{State: agent.ConnectorDiscoveryStateUnbound},
			Apps:      apps,
		}, nil
	})

	response := serveBearerJSON(handler, http.MethodGet, path+"/"+item.Key+"/readiness?agent_id="+agentID.String(), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	runtimeID, present := got["runtimeId"]
	discovery, _ := got["discovery"].(map[string]any)
	apps, _ := got["apps"].([]any)
	first, _ := apps[0].(map[string]any)
	if !present || runtimeID != nil || got["provider"] != "claude" ||
		discovery["state"] != "unbound" || discovery["observedAt"] != nil || first["state"] != "unknown" {
		t.Fatalf("readiness = %s", response.Body.String())
	}
}

// A Connector the domain reveals carries its tool counts, and a count of zero
// is written rather than omitted: both are required by the contract.
func TestWorkflowReadinessConnectorCarriesToolCounts(t *testing.T) {
	item := workflowWithApps(t)
	agentID := uuid.Must(uuid.NewV7())
	observedAt := "2026-09-24T09:30:00Z"
	handler, path, _ := workflowReadinessTestHandler(t, func(
		_ context.Context,
		_ auth.UserPrincipal,
		input agent.ConnectorAppReadinessInput,
	) (agent.ConnectorAppReadinessResult, error) {
		apps := make([]agent.ConnectorAppReadiness, len(input.Apps))
		for index, app := range input.Apps {
			apps[index] = agent.ConnectorAppReadiness{
				App: app, State: agent.ConnectorAppStateNeedsConnection,
				Connector: &agent.ConnectorAppConnector{
					ID: uuid.Must(uuid.NewV7()), Source: agent.ConnectorSourceCodexApp,
					NativeID: "connector-" + string(app), Name: string(app),
					Status: agent.ConnectorStatusNeedsAuth, Action: agent.ConnectorActionAuthenticate,
				},
			}
		}
		apps[0].State = agent.ConnectorAppStateReady
		apps[0].Connector.Status = agent.ConnectorStatusConnected
		apps[0].Connector.Action = agent.ConnectorActionNone
		apps[0].Connector.ToolCount, apps[0].Connector.WriteToolCount = 3, 1
		return agent.ConnectorAppReadinessResult{
			AgentID: agentID, Provider: agent.ConnectorProviderCodex,
			RuntimeID: uuid.NullUUID{UUID: uuid.Must(uuid.NewV7()), Valid: true},
			Discovery: agent.ConnectorAppDiscovery{State: agent.ConnectorDiscoveryStateFresh, ObservedAt: &observedAt},
			Apps:      apps,
		}, nil
	})

	response := serveBearerJSON(handler, http.MethodGet, path+"/"+item.Key+"/readiness?agent_id="+agentID.String(), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var got struct {
		Apps []struct {
			Connector map[string]any `json:"connector"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	connected, unconnected := got.Apps[0].Connector, got.Apps[1].Connector
	if connected["toolCount"] != float64(3) || connected["writeToolCount"] != float64(1) {
		t.Fatalf("connected app connector = %v", connected)
	}
	if count, present := unconnected["toolCount"]; !present || count != float64(0) {
		t.Fatalf("unconnected app toolCount = %v (present %v)", count, present)
	}
	if count, present := unconnected["writeToolCount"]; !present || count != float64(0) {
		t.Fatalf("unconnected app writeToolCount = %v (present %v)", count, present)
	}
}

// The query is exactly one valid agent_id; anything else is refused before
// the Workflow or the Agent is read.
func TestWorkflowReadinessQueryIsExactlyOneAgentID(t *testing.T) {
	item := workflowWithApps(t)
	handler, path, _ := workflowReadinessTestHandler(t, func(
		context.Context,
		auth.UserPrincipal,
		agent.ConnectorAppReadinessInput,
	) (agent.ConnectorAppReadinessResult, error) {
		t.Fatal("ReadAppReadiness() reached with an invalid query")
		return agent.ConnectorAppReadinessResult{}, nil
	})
	agentID := uuid.Must(uuid.NewV7()).String()
	for _, query := range []string{
		"",
		"?agent_id=",
		"?agent_id=not-a-uuid",
		"?agent_id=00000000-0000-0000-0000-000000000000",
		"?agent_id=" + agentID + "&agent_id=" + agentID,
		"?agent_id=" + agentID + "&provider=codex",
		"?agentId=" + agentID,
	} {
		t.Run(query, func(t *testing.T) {
			response := serveBearerJSON(handler, http.MethodGet, path+"/"+item.Key+"/readiness"+query, nil)
			assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
		})
	}
}

// An unknown Workflow is its own 404 before any Agent read; the Agent's
// absence (archived, foreign or unknown) is the Agent surface's 404.
func TestWorkflowReadinessNamesWhatWasNotFound(t *testing.T) {
	item := workflowWithApps(t)
	calls := 0
	handler, path, _ := workflowReadinessTestHandler(t, func(
		context.Context,
		auth.UserPrincipal,
		agent.ConnectorAppReadinessInput,
	) (agent.ConnectorAppReadinessResult, error) {
		calls++
		return agent.ConnectorAppReadinessResult{}, agent.ErrNotFound
	})
	query := "/readiness?agent_id=" + uuid.Must(uuid.NewV7()).String()

	response := serveBearerJSON(handler, http.MethodGet, path+"/no-such-workflow"+query, nil)
	assertErrorResponse(t, response, http.StatusNotFound, "workflow_not_found")
	if calls != 0 {
		t.Fatalf("ReadAppReadiness() called %d times for an unknown Workflow", calls)
	}

	response = serveBearerJSON(handler, http.MethodGet, path+"/"+item.Key+query, nil)
	assertErrorResponse(t, response, http.StatusNotFound, "agent_not_found")
	if calls != 1 {
		t.Fatalf("ReadAppReadiness() called %d times, want 1", calls)
	}
}
