package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/middleware"
)

// The catalog read is any member's (ADR 0014): the route takes the member
// gate, and the service hides every machine the caller did not register.
func TestPluginListRouteUsesMemberPrincipalAndReturnsWireShape(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	runtimeID := uuid.Must(uuid.NewV7())
	cursorID := uuid.Must(uuid.NewV7())
	cursor, err := agent.FormatPluginCursor(agent.PluginCursor{Rank: 7, ID: cursorID})
	if err != nil {
		t.Fatalf("format Plugin cursor: %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
		AuthKind: auth.TokenKindBearer, CompanyID: companyID, Role: auth.CompanyRoleMember,
	}
	observedAt := time.Date(2026, 9, 25, 17, 27, 4, 123456000, time.UTC)
	service := &pluginServiceStub{list: func(
		_ context.Context,
		gotPrincipal auth.UserPrincipal,
		input agent.PluginListInput,
	) (agent.PluginCatalogResult, error) {
		if gotPrincipal != principal {
			t.Fatalf("List() principal = %+v, want %+v", gotPrincipal, principal)
		}
		if input.Provider != agent.ConnectorProviderClaude || input.Query != "calendar" ||
			input.Category != "Productivity" || input.Status != agent.PluginStatusInstalled ||
			!input.Featured || input.Limit != 25 || input.Cursor == nil ||
			input.Cursor.Rank != 7 || input.Cursor.ID != cursorID ||
			input.RuntimeID == nil || *input.RuntimeID != runtimeID {
			t.Fatalf("List() input = %+v", input)
		}
		return agent.PluginCatalogResult{
			SchemaVersion: 1, ActiveProvider: agent.ConnectorProviderClaude,
			Runtime:    &agent.PluginRuntimeSummary{ID: runtimeID, Name: "Claude workstation", Online: true},
			ObservedAt: &observedAt,
		}, nil
	}}
	handler := New(Handler{
		AuthService: authenticatedAuthStub(userID),
		CompanyService: &companyServiceStub{authorize: func(
			_ context.Context,
			gotPrincipal auth.UserPrincipal,
			gotCompanyID uuid.UUID,
		) (auth.UserPrincipal, error) {
			if gotPrincipal.UserID != userID || gotPrincipal.CompanyID != uuid.Nil ||
				gotPrincipal.Role != "" || gotCompanyID != companyID {
				t.Fatalf("Authorize() = %+v / %s", gotPrincipal, gotCompanyID)
			}
			return principal, nil
		}},
		PluginService: service,
	})
	path := "/api/v1/companies/" + companyID.String() +
		"/plugins?provider=claude&query=%20calendar%20&category=Productivity" +
		"&status=installed&featured=true&cursor=" + cursor + "&limit=25&runtime_id=" + runtimeID.String()
	response := serveBearerRequest(handler, http.MethodGet, path, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	want := `{"schemaVersion":1,"activeProvider":"claude","runtime":{"id":"` +
		runtimeID.String() +
		`","name":"Claude workstation","online":true},"plugins":[],"nextCursor":null,` +
		`"totalCount":0,"installedCount":0,"categoryCounts":[],"stale":false,` +
		`"observedAt":"2026-09-25T17:27:04.123456Z"}`
	if got := response.Body.String(); got != want {
		t.Fatalf("response body = %s, want %s", got, want)
	}
}

func TestParsePluginListInputRejectsNonCanonicalQueries(t *testing.T) {
	t.Parallel()

	for name, rawQuery := range map[string]string{
		"unknown field":         "extra=true",
		"duplicate field":       "provider=codex&provider=claude",
		"empty provider":        "provider=",
		"unknown provider":      "provider=openai",
		"noncanonical provider": "provider=Codex",
		"blank category":        "category=++",
		"unknown status":        "status=pending",
		"false featured":        "featured=false",
		"malformed cursor":      "cursor=not-a-cursor",
		"zero limit":            "limit=0",
		"oversized limit":       "limit=101",
		"noncanonical limit":    "limit=01",
		"malformed escape":      "query=%zz",
		"unescaped semicolon":   "query=a;b",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
			c.Request.URL.RawQuery = rawQuery
			if _, err := parsePluginListInput(c); !errors.Is(err, agent.ErrInvalidInput) {
				t.Fatalf("parsePluginListInput(%q) error = %v, want ErrInvalidInput", rawQuery, err)
			}
		})
	}
}

func TestPluginMutationRoutesUseOwnerPrincipalAndReturnOperationState(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	pluginID := uuid.New()
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
		AuthKind: auth.TokenKindBearer, CompanyID: companyID, Role: auth.CompanyRoleOwner,
	}

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
		configure  func(*testing.T, *pluginServiceStub)
	}{
		{
			name: "refresh", path: "/plugins/refresh", wantStatus: http.StatusAccepted,
			wantBody: `{"state":"queued"}`,
			configure: func(t *testing.T, service *pluginServiceStub) {
				t.Helper()
				service.refresh = func(_ context.Context, got auth.UserPrincipal) error {
					if got != principal {
						t.Fatalf("Refresh() principal = %+v, want %+v", got, principal)
					}
					return nil
				}
			},
		},
		{
			name: "install", path: "/plugins/" + pluginID.String() + "/install",
			wantStatus: http.StatusOK, wantBody: `{"state":"complete"}`,
			configure: func(t *testing.T, service *pluginServiceStub) {
				t.Helper()
				service.install = func(_ context.Context, got auth.UserPrincipal, gotPluginID uuid.UUID) error {
					if got != principal || gotPluginID != pluginID {
						t.Fatalf("Install() = %+v / %s", got, gotPluginID)
					}
					return nil
				}
			},
		},
		{
			name: "uninstall", path: "/plugins/" + pluginID.String() + "/uninstall",
			wantStatus: http.StatusOK, wantBody: `{"state":"complete"}`,
			configure: func(t *testing.T, service *pluginServiceStub) {
				t.Helper()
				service.uninstall = func(_ context.Context, got auth.UserPrincipal, gotPluginID uuid.UUID) error {
					if got != principal || gotPluginID != pluginID {
						t.Fatalf("Uninstall() = %+v / %s", got, gotPluginID)
					}
					return nil
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			service := &pluginServiceStub{}
			test.configure(t, service)
			handler := newPluginHandlerForTest(t, principal, service)
			response := serveBearerJSON(
				handler,
				http.MethodPost,
				"/api/v1/companies/"+companyID.String()+test.path,
				[]byte(`{"schemaVersion":1}`),
			)
			if response.Code != test.wantStatus || response.Body.String() != test.wantBody {
				t.Fatalf(
					"response = %d %s, want %d %s",
					response.Code,
					response.Body.String(),
					test.wantStatus,
					test.wantBody,
				)
			}
		})
	}
}

func TestPluginMutationRoutesRequireCompanyOwner(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	calls := 0
	service := &pluginServiceStub{install: func(context.Context, auth.UserPrincipal, uuid.UUID) error {
		calls++
		return nil
	}}
	handler := New(Handler{
		AuthService: authenticatedAuthStub(userID),
		CompanyService: &companyServiceStub{requirePermission: func(
			_ context.Context,
			principal auth.UserPrincipal,
			gotCompanyID uuid.UUID,
		) (auth.UserPrincipal, error) {
			if principal.UserID != userID || gotCompanyID != companyID {
				t.Fatalf("RequireOwner() = %+v / %s", principal, gotCompanyID)
			}
			return auth.UserPrincipal{}, company.ErrForbidden
		}},
		PluginService: service,
	})
	response := serveBearerJSON(
		handler,
		http.MethodPost,
		"/api/v1/companies/"+companyID.String()+"/plugins/"+uuid.NewString()+"/install",
		[]byte(`{"schemaVersion":1}`),
	)
	assertErrorResponse(t, response, http.StatusForbidden, "company_permission_required")
	if calls != 0 {
		t.Fatalf("PluginService.Install() calls = %d, want 0", calls)
	}
}

// Asking your own computer for its plugin and connector lists again is the
// registrant's right (ADR 0014), not a Company permission: both refresh
// routes take the member gate, and a plain member reaches the service, which
// answers only for a runtime the caller registered.
func TestCatalogRefreshRoutesTakeTheMemberGate(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	member := auth.UserPrincipal{
		UserID: userID, SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
		AuthKind: auth.TokenKindBearer, CompanyID: companyID, Role: auth.CompanyRoleMember,
	}
	var pluginCalls, connectorCalls int
	handler := New(Handler{
		AuthService: authenticatedAuthStub(userID),
		CompanyService: &companyServiceStub{
			authorize: func(
				_ context.Context,
				principal auth.UserPrincipal,
				gotCompanyID uuid.UUID,
			) (auth.UserPrincipal, error) {
				if principal.UserID != userID || gotCompanyID != companyID {
					return auth.UserPrincipal{}, company.ErrNotFound
				}
				return member, nil
			},
			requirePermission: func(
				context.Context,
				auth.UserPrincipal,
				uuid.UUID,
			) (auth.UserPrincipal, error) {
				return auth.UserPrincipal{}, company.ErrForbidden
			},
		},
		PluginService: &pluginServiceStub{refresh: func(_ context.Context, got auth.UserPrincipal) error {
			pluginCalls++
			if got != member {
				t.Errorf("PluginService.Refresh() principal = %+v, want %+v", got, member)
			}
			return nil
		}},
		ConnectorService: &connectorServiceStub{refresh: func(_ context.Context, got auth.UserPrincipal) error {
			connectorCalls++
			if got != member {
				t.Errorf("ConnectorService.Refresh() principal = %+v, want %+v", got, member)
			}
			return nil
		}},
	})
	for _, catalog := range []string{"plugins", "connectors"} {
		response := serveBearerJSON(
			handler,
			http.MethodPost,
			"/api/v1/companies/"+companyID.String()+"/"+catalog+"/refresh",
			[]byte(`{"schemaVersion":1}`),
		)
		if response.Code != http.StatusAccepted || response.Body.String() != `{"state":"queued"}` {
			t.Fatalf("%s refresh = %d %s, want 202 queued", catalog, response.Code, response.Body.String())
		}
	}
	if pluginCalls != 1 || connectorCalls != 1 {
		t.Fatalf("Refresh() calls = plugins %d, connectors %d; want 1 each", pluginCalls, connectorCalls)
	}
}

func TestPluginMutationBodyIsStrictAndBounded(t *testing.T) {
	t.Parallel()

	principal := pluginTestPrincipal()
	tests := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
		wantCode    string
	}{
		{
			name: "unknown field", body: `{"schemaVersion":1,"pluginId":"ignored"}`,
			contentType: "application/json", wantStatus: http.StatusBadRequest, wantCode: "invalid_request",
		},
		{
			name:        "missing schema version",
			body:        `{}`,
			contentType: "application/json",
			wantStatus:  http.StatusUnprocessableEntity,
			wantCode:    "validation_failed",
		},
		{
			name:        "wrong schema version",
			body:        `{"schemaVersion":2}`,
			contentType: "application/json",
			wantStatus:  http.StatusUnprocessableEntity,
			wantCode:    "validation_failed",
		},
		{
			name: "malformed JSON", body: `{"schemaVersion":`,
			contentType: "application/json", wantStatus: http.StatusBadRequest, wantCode: "invalid_request",
		},
		{
			name:        "wrong media type",
			body:        `{"schemaVersion":1}`,
			contentType: "text/plain",
			wantStatus:  http.StatusUnsupportedMediaType,
			wantCode:    "unsupported_media_type",
		},
		{
			name: "oversized body",
			body: `{"schemaVersion":1,"padding":"` + strings.Repeat(
				"x",
				maxPluginMutationBodyBytes,
			) + `"}`,
			contentType: "application/json",
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantCode:    "request_too_large",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			calls := 0
			service := &pluginServiceStub{refresh: func(context.Context, auth.UserPrincipal) error {
				calls++
				return nil
			}}
			handler := pluginTestRouter(principal, service)
			request := httptest.NewRequest(
				http.MethodPost,
				"/companies/"+principal.CompanyID.String()+"/plugins/refresh",
				bytes.NewBufferString(test.body),
			)
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertErrorResponse(t, response, test.wantStatus, test.wantCode)
			if calls != 0 {
				t.Fatalf("PluginService.Refresh() calls = %d, want 0", calls)
			}
		})
	}
}

func TestPluginHandlersMapWrappedServiceErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"unauthenticated", agent.ErrUnauthenticated, http.StatusUnauthorized, "authentication_required"},
		{"forbidden", agent.ErrForbidden, http.StatusForbidden, "company_permission_required"},
		{"invalid input", agent.ErrInvalidInput, http.StatusUnprocessableEntity, "validation_failed"},
		{"not found", agent.ErrPluginNotFound, http.StatusNotFound, "plugin_not_found"},
		// Another member's or an unknown runtime_id answers like every runtime
		// route (ADR 0014), not as a provider conflict.
		{"runtime not found", agent.ErrRuntimeNotFound, http.StatusNotFound, "runtime_not_found"},
		{
			"provider unavailable", agent.ErrPluginProviderUnavailable,
			http.StatusConflict, "plugin_provider_unavailable",
		},
		{
			"action unavailable", agent.ErrPluginActionUnavailable,
			http.StatusServiceUnavailable, "plugin_action_unavailable",
		},
		{
			"unexpected",
			errors.New("database unavailable"),
			http.StatusInternalServerError,
			"plugin_request_failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			principal := pluginTestPrincipal()
			service := &pluginServiceStub{list: func(
				context.Context,
				auth.UserPrincipal,
				agent.PluginListInput,
			) (agent.PluginCatalogResult, error) {
				return agent.PluginCatalogResult{}, fmt.Errorf("list plugins: %w", test.err)
			}}
			request := httptest.NewRequest(
				http.MethodGet,
				"/companies/"+principal.CompanyID.String()+"/plugins",
				nil,
			)
			response := httptest.NewRecorder()
			pluginTestRouter(principal, service).ServeHTTP(response, request)
			assertErrorResponse(t, response, test.wantStatus, test.wantCode)
		})
	}
}

type pluginServiceStub struct {
	publish   func(context.Context, auth.UserPrincipal, agent.PluginDiscoveryInput) (agent.PluginDiscoveryResult, error)
	list      func(context.Context, auth.UserPrincipal, agent.PluginListInput) (agent.PluginCatalogResult, error)
	get       func(context.Context, auth.UserPrincipal, agent.PluginDetailInput) (agent.PluginDetailResult, error)
	refresh   func(context.Context, auth.UserPrincipal) error
	install   func(context.Context, auth.UserPrincipal, uuid.UUID) error
	uninstall func(context.Context, auth.UserPrincipal, uuid.UUID) error
}

func (s *pluginServiceStub) PublishDiscovery(
	ctx context.Context,
	principal auth.UserPrincipal,
	input agent.PluginDiscoveryInput,
) (agent.PluginDiscoveryResult, error) {
	if s.publish == nil {
		return agent.PluginDiscoveryResult{}, errors.New("unexpected PublishDiscovery call")
	}
	return s.publish(ctx, principal, input)
}

func (s *pluginServiceStub) List(
	ctx context.Context,
	principal auth.UserPrincipal,
	input agent.PluginListInput,
) (agent.PluginCatalogResult, error) {
	if s.list == nil {
		return agent.PluginCatalogResult{}, errors.New("unexpected List call")
	}
	return s.list(ctx, principal, input)
}

func (s *pluginServiceStub) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	input agent.PluginDetailInput,
) (agent.PluginDetailResult, error) {
	if s.get == nil {
		return agent.PluginDetailResult{}, errors.New("unexpected Get call")
	}
	return s.get(ctx, principal, input)
}

func (s *pluginServiceStub) Refresh(ctx context.Context, principal auth.UserPrincipal) error {
	if s.refresh == nil {
		return errors.New("unexpected Refresh call")
	}
	return s.refresh(ctx, principal)
}

func (s *pluginServiceStub) Install(
	ctx context.Context,
	principal auth.UserPrincipal,
	pluginID uuid.UUID,
) error {
	if s.install == nil {
		return errors.New("unexpected Install call")
	}
	return s.install(ctx, principal, pluginID)
}

func (s *pluginServiceStub) Uninstall(
	ctx context.Context,
	principal auth.UserPrincipal,
	pluginID uuid.UUID,
) error {
	if s.uninstall == nil {
		return errors.New("unexpected Uninstall call")
	}
	return s.uninstall(ctx, principal, pluginID)
}

func newPluginHandlerForTest(
	t *testing.T,
	principal auth.UserPrincipal,
	service agent.PluginService,
) http.Handler {
	t.Helper()
	resolve := func(
		_ context.Context,
		gotPrincipal auth.UserPrincipal,
		companyID uuid.UUID,
	) (auth.UserPrincipal, error) {
		if gotPrincipal.UserID != principal.UserID || companyID != principal.CompanyID {
			return auth.UserPrincipal{}, company.ErrNotFound
		}
		return principal, nil
	}
	return New(Handler{
		AuthService:    authenticatedAuthStub(principal.UserID),
		CompanyService: &companyServiceStub{authorize: resolve, requirePermission: resolve},
		PluginService:  service,
	})
}

func pluginTestPrincipal() auth.UserPrincipal {
	return auth.UserPrincipal{
		UserID: uuid.Must(uuid.NewV7()), SessionID: uuid.Must(uuid.NewV7()),
		AuthKind: auth.TokenKindBearer, CompanyID: uuid.Must(uuid.NewV7()), Role: auth.CompanyRoleOwner,
	}
}

func pluginTestRouter(principal auth.UserPrincipal, service agent.PluginService) http.Handler {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID(), func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithUserPrincipal(c.Request.Context(), principal))
		c.Next()
	})
	h := &Handler{PluginService: service}
	base := "/companies/:companyId/plugins"
	router.GET(base, h.listPlugins)
	router.GET(base+"/:pluginId", h.getPlugin)
	router.POST(base+"/refresh", h.refreshPlugins)
	router.POST(base+"/:pluginId/install", h.installPlugin)
	router.POST(base+"/:pluginId/uninstall", h.uninstallPlugin)
	return router
}

var _ agent.PluginService = (*pluginServiceStub)(nil)

func TestParsePluginListInputRejectsInvalidRuntimeSelection(t *testing.T) {
	t.Parallel()
	for _, query := range []string{"runtime_id=", "runtime_id=invalid", "runtime_id=00000000-0000-0000-0000-000000000000", "runtime_id=" + uuid.NewString() + "&runtime_id=" + uuid.NewString()} {
		t.Run(query, func(t *testing.T) {
			t.Parallel()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/?"+query, nil)
			if _, err := parsePluginListInput(c); err == nil {
				t.Fatalf("parsePluginListInput(%q) error = nil, want invalid input", query)
			}
		})
	}
}

// The plugin page is a member read (ADR 0014), like the catalog, and answers
// the detail projection: the catalog row, the Codex presentation, the
// resolved apps and the Agents the caller runs that can use the Plugin.
func TestPluginDetailRouteUsesMemberPrincipalAndReturnsWireShape(t *testing.T) {
	t.Parallel()

	principal := pluginTestPrincipal()
	principal.Role = auth.CompanyRoleMember
	pluginID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2791")
	runtimeID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2792")
	connectorID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2793")
	agentID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2794")
	text := func(value string) *string { return &value }
	status, action := agent.ConnectorStatusNeedsAuth, agent.ConnectorActionAuthenticate
	service := &pluginServiceStub{get: func(
		_ context.Context,
		gotPrincipal auth.UserPrincipal,
		input agent.PluginDetailInput,
	) (agent.PluginDetailResult, error) {
		if gotPrincipal.UserID != principal.UserID || gotPrincipal.CompanyID != principal.CompanyID ||
			gotPrincipal.Role != auth.CompanyRoleMember || input.PluginID != pluginID ||
			input.RuntimeID == nil || *input.RuntimeID != runtimeID {
			t.Fatalf("Get() principal = %+v input = %+v", gotPrincipal, input)
		}
		return agent.PluginDetailResult{
			SchemaVersion: 1,
			Plugin: agent.PluginItemV1{
				ID: pluginID, Provider: agent.ConnectorProviderCodex, NativeID: "gmail@openai-curated-remote",
				Name: "Gmail", Status: agent.PluginStatusInstalled, Installed: true, Enabled: true,
				Action: agent.PluginActionUninstall, ComponentCounts: agent.PluginComponentCounts{Apps: 1},
				ConnectorIdentities: []agent.PluginConnectorIdentity{{
					Source: agent.ConnectorSourceCodexApp, NativeID: "connector_gmail",
				}},
				ObservedAt: "2026-09-25T08:00:00Z",
			},
			Runtime:         &agent.PluginRuntimeSummary{ID: runtimeID, Name: "Studio Mac", Online: true},
			LongDescription: text("Read mail.\n\nDraft replies."),
			DefaultPrompts:  []string{"Summarize my inbox"},
			BrandColor:      text("#EA4335"),
			WebsiteURL:      text("https://mail.google.com"),
			Apps: []agent.PluginDetailApp{
				{
					Source: agent.ConnectorSourceCodexApp, NativeID: "connector_gmail",
					ConnectorID: &connectorID, Name: "Gmail", IconURL: text("https://chatgpt.com/gmail.png"),
					Status: &status, Action: &action,
				},
				{Source: agent.ConnectorSourceCodexMCP, NativeID: "gmail-mcp", Name: "gmail-mcp"},
			},
			UsedBy: []agent.PluginAgentUse{{AgentID: agentID, Name: "CEO"}},
		}, nil
	}}
	response := serveBearerRequest(
		newPluginHandlerForTest(t, principal, service),
		http.MethodGet,
		"/api/v1/companies/"+principal.CompanyID.String()+"/plugins/"+pluginID.String()+
			"?runtime_id="+runtimeID.String(),
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
	want := `{"schemaVersion":1,"plugin":{"id":"` + pluginID.String() + `","provider":"codex",` +
		`"nativeId":"gmail@openai-curated-remote","name":"Gmail","status":"installed","installed":true,` +
		`"enabled":true,"featured":false,"action":"uninstall",` +
		`"componentCounts":{"apps":1,"mcpServers":0,"skills":0},` +
		`"connectorIdentities":[{"source":"codex_app","nativeId":"connector_gmail"}],` +
		`"observedAt":"2026-09-25T08:00:00Z"},` +
		`"runtime":{"id":"` + runtimeID.String() + `","name":"Studio Mac","online":true},"stale":false,` +
		`"longDescription":"Read mail.\n\nDraft replies.","defaultPrompts":["Summarize my inbox"],` +
		`"brandColor":"#EA4335","websiteUrl":"https://mail.google.com",` +
		`"apps":[{"source":"codex_app","nativeId":"connector_gmail","connectorId":"` + connectorID.String() +
		`","name":"Gmail","iconUrl":"https://chatgpt.com/gmail.png","status":"needs_auth",` +
		`"action":"authenticate"},{"source":"codex_mcp","nativeId":"gmail-mcp","name":"gmail-mcp"}],` +
		`"usedBy":[{"agentId":"` + agentID.String() + `","name":"CEO"}]}`
	if got := response.Body.String(); got != want {
		t.Fatalf("response body = %s\nwant %s", got, want)
	}
}

// Empty lists stay arrays on the wire, as the schema requires.
func TestPluginDetailRouteKeepsEmptyListsAsArrays(t *testing.T) {
	t.Parallel()

	principal := pluginTestPrincipal()
	pluginID := uuid.New()
	service := &pluginServiceStub{get: func(
		context.Context,
		auth.UserPrincipal,
		agent.PluginDetailInput,
	) (agent.PluginDetailResult, error) {
		return agent.PluginDetailResult{SchemaVersion: 1}, nil
	}}
	request := httptest.NewRequest(
		http.MethodGet,
		"/companies/"+principal.CompanyID.String()+"/plugins/"+pluginID.String(),
		nil,
	)
	response := httptest.NewRecorder()
	pluginTestRouter(principal, service).ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"runtime":null`) ||
		!strings.Contains(body, `"defaultPrompts":[]`) || !strings.Contains(body, `"apps":[]`) ||
		!strings.Contains(body, `"usedBy":[]`) {
		t.Fatalf("response = %d %s", response.Code, body)
	}
}

func TestPluginDetailRouteMapsErrorsAndRejectsNonCanonicalRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		pluginID   string
		rawQuery   string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "not found", err: agent.ErrPluginNotFound, wantStatus: http.StatusNotFound, wantCode: "plugin_not_found"},
		{
			name: "forbidden", err: agent.ErrForbidden,
			wantStatus: http.StatusForbidden, wantCode: "company_permission_required",
		},
		{
			name: "invalid input", err: agent.ErrInvalidInput,
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation_failed",
		},
		{
			name: "unexpected", err: errors.New("database unavailable"),
			wantStatus: http.StatusInternalServerError, wantCode: "plugin_request_failed",
		},
		{name: "malformed plugin id", pluginID: "not-a-uuid", wantStatus: http.StatusNotFound, wantCode: "plugin_not_found"},
		{name: "unknown query", rawQuery: "provider=codex", wantStatus: http.StatusUnprocessableEntity, wantCode: "validation_failed"},
		{name: "empty runtime", rawQuery: "runtime_id=", wantStatus: http.StatusUnprocessableEntity, wantCode: "validation_failed"},
		{name: "malformed runtime", rawQuery: "runtime_id=abc", wantStatus: http.StatusUnprocessableEntity, wantCode: "validation_failed"},
		{
			name: "repeated runtime", rawQuery: "runtime_id=" + uuid.NewString() + "&runtime_id=" + uuid.NewString(),
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation_failed",
		},
		{name: "malformed escape", rawQuery: "runtime_id=%zz", wantStatus: http.StatusUnprocessableEntity, wantCode: "validation_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			principal := pluginTestPrincipal()
			calls := 0
			service := &pluginServiceStub{get: func(
				context.Context,
				auth.UserPrincipal,
				agent.PluginDetailInput,
			) (agent.PluginDetailResult, error) {
				calls++
				return agent.PluginDetailResult{}, fmt.Errorf("get plugin: %w", test.err)
			}}
			pluginID := test.pluginID
			if pluginID == "" {
				pluginID = uuid.NewString()
			}
			request := httptest.NewRequest(
				http.MethodGet,
				"/companies/"+principal.CompanyID.String()+"/plugins/"+pluginID,
				nil,
			)
			request.URL.RawQuery = test.rawQuery
			response := httptest.NewRecorder()
			pluginTestRouter(principal, service).ServeHTTP(response, request)
			assertErrorResponse(t, response, test.wantStatus, test.wantCode)
			if wantCalls := map[bool]int{true: 1, false: 0}[test.err != nil]; calls != wantCalls {
				t.Fatalf("PluginService.Get() calls = %d, want %d", calls, wantCalls)
			}
		})
	}
}
