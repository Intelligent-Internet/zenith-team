package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

var (
	pluginDiscoveryTestRuntimeID  = uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f27a1")
	pluginDiscoveryTestCompanyID  = uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f27a2")
	pluginDiscoveryTestGeneration = uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f27a3")
)

func pluginDiscoveryTestHandler(service agent.PluginService) http.Handler {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID(), func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithUserPrincipal(c.Request.Context(), daemonTestPrincipal()))
		c.Next()
	})
	h := &Handler{
		Queries: db.New(&daemonRuntimeAccessDBTX{
			runtime: db.Runtime{
				ID:        pluginDiscoveryTestRuntimeID,
				CompanyID: pluginDiscoveryTestCompanyID,
				DaemonID:  "daemon-a",
				// ADR 0014: only the registrant's daemon reaches its runtime.
				OwnerUserID: uuid.NullUUID{UUID: daemonTestPrincipal().UserID, Valid: true},
			},
			member: true,
		}),
		PluginService: service,
	}
	router.PUT(
		"/api/v1/daemon/companies/:companyId/runtimes/:runtimeId/plugins/discovery",
		h.publishPluginDiscovery,
	)
	return router
}

func putPluginDiscovery(handler http.Handler, item string) *httptest.ResponseRecorder {
	body := `{"schemaVersion":1,"generation":"` + pluginDiscoveryTestGeneration.String() +
		`","manifestDigest":"` + strings.Repeat("a", 64) + `","observedAt":"2026-09-25T08:00:00Z",` +
		`"items":[` + item + `]}`
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/daemon/companies/"+pluginDiscoveryTestCompanyID.String()+"/runtimes/"+
			pluginDiscoveryTestRuntimeID.String()+"/plugins/discovery",
		bytes.NewBufferString(body),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Zenith-Team-Daemon-ID", "daemon-a")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// A daemon that saw plugin-presentation-v1 sends the Codex presentation; the
// handler carries every field into the domain unchanged, where the manifest
// digest is recomputed over them.
func TestPublishPluginDiscoveryCarriesThePresentationFields(t *testing.T) {
	t.Parallel()

	var got agent.PluginDiscoveryInput
	service := &pluginServiceStub{publish: func(
		_ context.Context,
		_ auth.UserPrincipal,
		input agent.PluginDiscoveryInput,
	) (agent.PluginDiscoveryResult, error) {
		got = input
		return agent.PluginDiscoveryResult{Applied: true}, nil
	}}
	response := putPluginDiscovery(pluginDiscoveryTestHandler(service), `{
		"nativeId":"gmail@openai-curated-remote","name":"Gmail","status":"installed",
		"installed":true,"enabled":true,"featured":true,"action":"uninstall","catalogRank":0,
		"componentCounts":{"apps":1,"mcpServers":0,"skills":2},
		"longDescription":"Read mail.\n\nDraft replies.","defaultPrompts":["Summarize my inbox"],
		"brandColor":"#EA4335","developerName":"Google","websiteUrl":"https://mail.google.com",
		"privacyPolicyUrl":"https://policies.google.com/privacy",
		"termsOfServiceUrl":"https://policies.google.com/terms"}`)
	if response.Code != http.StatusOK || response.Body.String() != `{"applied":true}` {
		t.Fatalf("publish = %d %s", response.Code, response.Body.String())
	}
	if got.RuntimeID != pluginDiscoveryTestRuntimeID || got.CompanyID != pluginDiscoveryTestCompanyID ||
		len(got.Items) != 1 {
		t.Fatalf("PublishDiscovery() input = %+v", got)
	}
	item := got.Items[0]
	if item.LongDescription != "Read mail.\n\nDraft replies." ||
		!reflect.DeepEqual(item.DefaultPrompts, []string{"Summarize my inbox"}) ||
		item.BrandColor != "#EA4335" || item.DeveloperName != "Google" ||
		item.WebsiteURL != "https://mail.google.com" ||
		item.PrivacyPolicyURL != "https://policies.google.com/privacy" ||
		item.TermsOfServiceURL != "https://policies.google.com/terms" {
		t.Fatalf("presentation = %+v", item)
	}
}

// The binder bounds each presentation field before the domain runs; the
// service's validators then decide content and Codex gating.
func TestPublishPluginDiscoveryBoundsThePresentationFields(t *testing.T) {
	t.Parallel()

	base := `"nativeId":"plugin","name":"Plugin","status":"available","action":"install",` +
		`"catalogRank":0,"componentCounts":{"apps":0,"mcpServers":0,"skills":0}`
	quoted := func(value string) string { return `"` + value + `"` }
	for name, field := range map[string]string{
		"four prompts":          `"defaultPrompts":["a","b","c","d"]`,
		"empty prompt":          `"defaultPrompts":[""]`,
		"long prompt":           `"defaultPrompts":[` + quoted(strings.Repeat("p", 129)) + `]`,
		"short brand color":     `"brandColor":"#FFF"`,
		"long description":      `"longDescription":` + quoted(strings.Repeat("d", 4001)),
		"long developer":        `"developerName":` + quoted(strings.Repeat("n", 121)),
		"long website":          `"websiteUrl":` + quoted("https://example.test/"+strings.Repeat("w", 2048)),
		"long privacy policy":   `"privacyPolicyUrl":` + quoted("https://example.test/"+strings.Repeat("w", 2048)),
		"long terms of service": `"termsOfServiceUrl":` + quoted("https://example.test/"+strings.Repeat("w", 2048)),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			service := &pluginServiceStub{publish: func(
				context.Context,
				auth.UserPrincipal,
				agent.PluginDiscoveryInput,
			) (agent.PluginDiscoveryResult, error) {
				t.Fatal("PublishDiscovery() reached with an out-of-bounds field")
				return agent.PluginDiscoveryResult{}, nil
			}}
			response := putPluginDiscovery(pluginDiscoveryTestHandler(service), "{"+base+","+field+"}")
			assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
		})
	}
}
