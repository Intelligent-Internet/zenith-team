package handler

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/middleware"
)

func TestConnectorListHandlerForwardsFiltersAndReturnsWireShape(t *testing.T) {
	t.Parallel()

	principal := connectorTestPrincipal()
	connectorID := uuid.New()
	runtimeID := uuid.New()
	cursor, err := agent.FormatConnectorCursor(agent.ConnectorCursor{Name: "github", ID: connectorID})
	if err != nil {
		t.Fatal(err)
	}
	service := &connectorServiceStub{}
	service.list = func(
		_ context.Context,
		got auth.UserPrincipal,
		input agent.ConnectorListInput,
	) (agent.ConnectorCatalogResult, error) {
		if got != principal || input.Query != "git" || input.Category != "Developer tools" ||
			input.Status != agent.ConnectorStatusConnected || input.Source != agent.ConnectorSourceClaudeMCP || input.Limit != 25 ||
			input.Cursor == nil || input.Cursor.Name != "github" || input.Cursor.ID != connectorID ||
			input.Provider != agent.ConnectorProviderClaude || !input.ReviewRequired ||
			input.RuntimeID == nil || *input.RuntimeID != runtimeID {
			t.Fatalf("List() input = %+v / %+v", got, input)
		}
		return agent.ConnectorCatalogResult{
			SchemaVersion: 1, ActiveProvider: agent.ConnectorProviderCodex,
			Connectors: []agent.ConnectorItemV1{}, CategoryCounts: []agent.ConnectorCategoryCount{},
		}, nil
	}
	handler := connectorTestHandler(principal, service)
	path := "/api/v1/companies/" + principal.CompanyID.String() +
		"/connectors?query=%20git%20&category=Developer+tools&provider=claude&status=connected" +
		"&source=claude_mcp&review_required=true&limit=25&cursor=" + cursor + "&runtime_id=" + runtimeID.String()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() !=
		`{"schemaVersion":1,"activeProvider":"codex","runtime":null,"connectors":[],"nextCursor":null,"totalCount":0,"connectedCount":0,"categoryCounts":[],"stale":false}` {
		t.Fatalf("Connector list response = %d %s", response.Code, response.Body.String())
	}
}

// A runtime_id that is not one of the caller's runtimes answers like every
// runtime route (ADR 0014): 404 runtime_not_found, never a provider conflict
// that would read as "this computer exists but serves no catalog".
func TestConnectorListHandlerAnswersAnotherMembersRuntimeAsNotFound(t *testing.T) {
	t.Parallel()

	principal := connectorTestPrincipal()
	service := &connectorServiceStub{}
	service.list = func(
		context.Context,
		auth.UserPrincipal,
		agent.ConnectorListInput,
	) (agent.ConnectorCatalogResult, error) {
		return agent.ConnectorCatalogResult{}, agent.ErrRuntimeNotFound
	}
	path := "/api/v1/companies/" + principal.CompanyID.String() +
		"/connectors?runtime_id=" + uuid.NewString()
	response := httptest.NewRecorder()
	connectorTestHandler(principal, service).ServeHTTP(
		response, httptest.NewRequest(http.MethodGet, path, nil),
	)
	assertErrorResponse(t, response, http.StatusNotFound, "runtime_not_found")
}

// A Codex computer signed in to ChatGPT publishes the whole hosted app
// directory. At the 10,000-item cap it is about 4.4 MiB, well over the old
// 2 MiB. The publish route's 8 MiB body cap and its max=10000 binding must
// take it, as the daemon's own caps do, or the catalog stays stale.
func TestConnectorDiscoveryBodyCapTakesAHostedAppDirectory(t *testing.T) {
	t.Parallel()

	items := make([]string, 10000)
	for index := range items {
		items[index] = fmt.Sprintf(
			`{"source":"codex_app","nativeId":"asdk_app_%032x","name":"Directory app %04d",`+
				`"description":"Search, read and update your workspace.","category":"Productivity",`+
				`"iconUrl":"https://files.openai.com/content?id=file-%022d&sig=%064x&ts=1790000000`+
				`&p=fs&v=0",`+
				`"status":"available","action":"install_and_auth","canExecute":false}`,
			index, index, index, index,
		)
	}
	body := `{"schemaVersion":1,"generation":"` + uuid.NewString() + `","manifestDigest":"` +
		strings.Repeat("a", 64) + `","observedAt":"2026-09-28T06:14:28Z","items":[` +
		strings.Join(items, ",") + `]}`
	if len(body) <= 2<<20 {
		t.Fatalf("directory body is %d bytes, want over 2 MiB", len(body))
	}
	router := gin.New()
	router.PUT("/discovery", func(c *gin.Context) {
		request, err := bindRequest[connectorDiscoveryRequest](c, maxConnectorDiscoveryBodyBytes)
		if err != nil {
			writeRequestError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": len(request.Items)})
	})
	request := httptest.NewRequest(http.MethodPut, "/discovery", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != `{"items":10000}` {
		t.Fatalf("bind directory status = %d, body = %.200s", response.Code, response.Body.String())
	}
}

func TestConnectorUnavailableActionHandlersAreStrict(t *testing.T) {
	t.Parallel()

	principal := connectorTestPrincipal()
	connectorID := uuid.New()
	service := &connectorServiceStub{}
	service.refresh = func(context.Context, auth.UserPrincipal) error {
		return agent.ErrConnectorActionUnavailable
	}
	service.connect = func(context.Context, auth.UserPrincipal, uuid.UUID) error {
		return agent.ErrConnectorActionUnavailable
	}
	handler := connectorTestHandler(principal, service)
	base := "/api/v1/companies/" + principal.CompanyID.String() + "/connectors"
	for _, path := range []string{base + "/refresh", base + "/" + connectorID.String() + "/connect"} {
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{"schemaVersion":1}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertErrorResponse(t, response, http.StatusServiceUnavailable, "connector_action_unavailable")
	}
}

type connectorServiceStub struct {
	publish func(
		context.Context,
		auth.UserPrincipal,
		agent.ConnectorDiscoveryInput,
	) (agent.ConnectorDiscoveryResult, error)
	reaffirm func(
		context.Context,
		auth.UserPrincipal,
		agent.ConnectorDiscoveryReaffirmInput,
	) (agent.ConnectorDiscoveryResult, error)
	readiness func(
		context.Context,
		auth.UserPrincipal,
		agent.ConnectorAppReadinessInput,
	) (agent.ConnectorAppReadinessResult, error)
	list    func(context.Context, auth.UserPrincipal, agent.ConnectorListInput) (agent.ConnectorCatalogResult, error)
	resolve func(context.Context, auth.UserPrincipal, agent.ConnectorResolveInput) (agent.ConnectorResolveResult, error)
	refresh func(context.Context, auth.UserPrincipal) error
	connect func(context.Context, auth.UserPrincipal, uuid.UUID) error
}

func (s *connectorServiceStub) ReaffirmDiscovery(
	ctx context.Context,
	principal auth.UserPrincipal,
	input agent.ConnectorDiscoveryReaffirmInput,
) (agent.ConnectorDiscoveryResult, error) {
	return s.reaffirm(ctx, principal, input)
}

func (s *connectorServiceStub) ReadAppReadiness(
	ctx context.Context,
	principal auth.UserPrincipal,
	input agent.ConnectorAppReadinessInput,
) (agent.ConnectorAppReadinessResult, error) {
	return s.readiness(ctx, principal, input)
}

func (s *connectorServiceStub) PublishDiscovery(
	ctx context.Context,
	principal auth.UserPrincipal,
	input agent.ConnectorDiscoveryInput,
) (agent.ConnectorDiscoveryResult, error) {
	return s.publish(ctx, principal, input)
}

func (s *connectorServiceStub) Resolve(
	ctx context.Context,
	principal auth.UserPrincipal,
	input agent.ConnectorResolveInput,
) (agent.ConnectorResolveResult, error) {
	return s.resolve(ctx, principal, input)
}

func (s *connectorServiceStub) List(
	ctx context.Context,
	principal auth.UserPrincipal,
	input agent.ConnectorListInput,
) (agent.ConnectorCatalogResult, error) {
	return s.list(ctx, principal, input)
}

func (s *connectorServiceStub) Refresh(
	ctx context.Context,
	principal auth.UserPrincipal,
) error {
	return s.refresh(ctx, principal)
}

func (s *connectorServiceStub) Connect(
	ctx context.Context,
	principal auth.UserPrincipal,
	connectorID uuid.UUID,
) error {
	return s.connect(ctx, principal, connectorID)
}

func connectorTestPrincipal() auth.UserPrincipal {
	return auth.UserPrincipal{
		UserID: uuid.New(), SessionID: uuid.New(), AuthKind: auth.TokenKindBearer,
		CompanyID: uuid.New(), Role: auth.CompanyRoleOwner,
	}
}

func connectorTestHandler(principal auth.UserPrincipal, service agent.ConnectorService) http.Handler {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID(), func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithUserPrincipal(c.Request.Context(), principal))
		c.Next()
	})
	h := &Handler{ConnectorService: service}
	base := "/api/v1/companies/:companyId/connectors"
	router.GET(base, h.listConnectors)
	router.POST(base+"/refresh", h.refreshConnectors)
	router.POST(base+"/resolve", h.resolveConnectors)
	router.POST(base+"/:connectorId/connect", h.connectConnector)
	return router
}

func TestParseConnectorListInputRejectsInvalidRuntimeSelection(t *testing.T) {
	t.Parallel()
	for _, query := range []string{"runtime_id=", "runtime_id=invalid", "runtime_id=00000000-0000-0000-0000-000000000000", "runtime_id=" + uuid.NewString() + "&runtime_id=" + uuid.NewString()} {
		t.Run(query, func(t *testing.T) {
			t.Parallel()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/?"+query, nil)
			if _, err := parseConnectorListInput(c); err == nil {
				t.Fatalf("parseConnectorListInput(%q) error = nil, want invalid input", query)
			}
		})
	}
}

func TestParseConnectorListInputSourceFilter(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		query     string
		wantValid bool
	}{
		{query: "source=codex_mcp", wantValid: true},
		{query: "source=claude_mcp", wantValid: true},
		{query: "source=codex_app", wantValid: true},
		{query: "source=claude_plugin", wantValid: true},
		{query: "source=claude_cloud", wantValid: true},
		{query: "provider=codex&source=codex_mcp", wantValid: true},
		{query: "provider=claude&source=claude_mcp", wantValid: true},
		{query: "provider=codex&source=claude_mcp"},
		{query: "provider=claude&source=codex_mcp"},
		{query: "source="},
		{query: "source=mcp"},
		{query: "source=CODEX_MCP"},
		{query: "source=codex_mcp&source=claude_mcp"},
	} {
		t.Run(test.query, func(t *testing.T) {
			t.Parallel()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/?"+test.query, nil)
			_, err := parseConnectorListInput(c)
			if (err == nil) != test.wantValid {
				t.Fatalf(
					"parseConnectorListInput(%q) error = %v, want valid = %v",
					test.query,
					err,
					test.wantValid,
				)
			}
		})
	}
}
