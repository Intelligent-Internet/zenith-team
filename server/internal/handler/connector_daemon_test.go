package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

const reaffirmTestDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var (
	reaffirmTestRuntimeID  = uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2781")
	reaffirmTestCompanyID  = uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2782")
	reaffirmTestGeneration = uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2783")
)

func reaffirmTestHandler(service agent.ConnectorService) http.Handler {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID(), func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithUserPrincipal(c.Request.Context(), daemonTestPrincipal()))
		c.Next()
	})
	h := &Handler{
		Queries: db.New(&daemonRuntimeAccessDBTX{
			runtime: db.Runtime{
				ID: reaffirmTestRuntimeID, CompanyID: reaffirmTestCompanyID, DaemonID: "daemon-a",
				// ADR 0014: only the registrant's daemon reaches its runtime.
				OwnerUserID: uuid.NullUUID{UUID: daemonTestPrincipal().UserID, Valid: true},
			},
			member: true,
		}),
		ConnectorService: service,
	}
	router.POST(
		"/api/v1/daemon/companies/:companyId/runtimes/:runtimeId/connectors/discovery/reaffirm",
		h.reaffirmConnectorDiscovery,
	)
	return router
}

func postReaffirm(handler http.Handler, companyID uuid.UUID, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/daemon/companies/"+companyID.String()+"/runtimes/"+reaffirmTestRuntimeID.String()+
			"/connectors/discovery/reaffirm",
		bytes.NewBufferString(body),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Zenith-Team-Daemon-ID", "daemon-a")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// A reaffirmation carries the daemon-fenced Runtime identity and the exact
// generation into the domain and answers the shared DiscoveryResponse.
func TestReaffirmConnectorDiscoveryForwardsTheFencedGeneration(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, time.September, 24, 9, 30, 0, 0, time.UTC)
	calls := 0
	service := &connectorServiceStub{
		reaffirm: func(
			_ context.Context,
			principal auth.UserPrincipal,
			input agent.ConnectorDiscoveryReaffirmInput,
		) (agent.ConnectorDiscoveryResult, error) {
			calls++
			if principal.UserID != daemonTestPrincipal().UserID ||
				principal.CompanyID != reaffirmTestCompanyID ||
				input.CompanyID != reaffirmTestCompanyID ||
				input.RuntimeID != reaffirmTestRuntimeID ||
				input.DaemonID != "daemon-a" ||
				input.Generation != reaffirmTestGeneration ||
				input.ManifestDigest != reaffirmTestDigest ||
				!input.ObservedAt.Equal(observedAt) {
				t.Fatalf("ReaffirmDiscovery() principal = %+v input = %+v", principal, input)
			}
			return agent.ConnectorDiscoveryResult{Applied: true}, nil
		},
	}
	response := postReaffirm(
		reaffirmTestHandler(service),
		reaffirmTestCompanyID,
		`{"schemaVersion":1,"generation":"`+
			reaffirmTestGeneration.String()+`","manifestDigest":"`+reaffirmTestDigest+
			`","observedAt":"2026-09-24T09:30:00Z"}`,
	)
	if response.Code != http.StatusOK || response.Body.String() != `{"applied":true}` || calls != 1 {
		t.Fatalf("reaffirm = %d %s after %d calls", response.Code, response.Body.String(), calls)
	}
}

// The body is a strict, tiny DTO: every malformed field is refused before the
// domain runs, and a conflicting generation is the named 409 the daemon falls
// back from.
func TestReaffirmConnectorDiscoveryRejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	valid := map[string]string{
		"schemaVersion": `1`, "generation": `"` + reaffirmTestGeneration.String() + `"`,
		"manifestDigest": `"` + reaffirmTestDigest + `"`, "observedAt": `"2026-09-24T09:30:00Z"`,
	}
	body := func(change func(map[string]string)) string {
		fields := map[string]string{}
		for key, value := range valid {
			fields[key] = value
		}
		change(fields)
		parts := make([]string, 0, len(fields))
		for _, key := range []string{"schemaVersion", "generation", "manifestDigest", "observedAt", "items"} {
			if value, present := fields[key]; present {
				parts = append(parts, `"`+key+`":`+value)
			}
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	unreachable := &connectorServiceStub{
		reaffirm: func(
			context.Context,
			auth.UserPrincipal,
			agent.ConnectorDiscoveryReaffirmInput,
		) (agent.ConnectorDiscoveryResult, error) {
			t.Fatal("ReaffirmDiscovery() reached with an invalid request")
			return agent.ConnectorDiscoveryResult{}, nil
		},
	}
	cases := []struct {
		name      string
		body      string
		status    int
		code      string
		companyID uuid.UUID
	}{
		{
			"schema version 2", body(func(f map[string]string) { f["schemaVersion"] = `2` }),
			http.StatusUnprocessableEntity, "validation_failed", reaffirmTestCompanyID,
		},
		{
			"missing generation", body(func(f map[string]string) { delete(f, "generation") }),
			http.StatusUnprocessableEntity, "validation_failed", reaffirmTestCompanyID,
		},
		{
			"generation is not a uuid",
			body(func(f map[string]string) { f["generation"] = `"generation-1"` }),
			http.StatusUnprocessableEntity,
			"validation_failed",
			reaffirmTestCompanyID,
		},
		{
			"short digest", body(func(f map[string]string) { f["manifestDigest"] = `"abc"` }),
			http.StatusUnprocessableEntity, "validation_failed", reaffirmTestCompanyID,
		},
		{
			"observedAt is not a wire time",
			body(func(f map[string]string) { f["observedAt"] = `"yesterday"` }),
			http.StatusUnprocessableEntity,
			"validation_failed",
			reaffirmTestCompanyID,
		},
		{
			"unknown field", body(func(f map[string]string) { f["items"] = `[]` }),
			http.StatusBadRequest, "invalid_request", reaffirmTestCompanyID,
		},
		{"body over 1 KiB", body(func(f map[string]string) {
			f["observedAt"] = `"` + strings.Repeat("9", 1<<10) + `"`
		}), http.StatusRequestEntityTooLarge, "request_too_large", reaffirmTestCompanyID},
		{
			"another Company's path",
			body(func(map[string]string) {}),
			http.StatusNotFound,
			"runtime_not_found",
			uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2799"),
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			response := postReaffirm(reaffirmTestHandler(unreachable), testCase.companyID, testCase.body)
			assertErrorResponse(t, response, testCase.status, testCase.code)
		})
	}

	conflicting := &connectorServiceStub{
		reaffirm: func(
			context.Context,
			auth.UserPrincipal,
			agent.ConnectorDiscoveryReaffirmInput,
		) (agent.ConnectorDiscoveryResult, error) {
			return agent.ConnectorDiscoveryResult{}, agent.ErrConnectorDiscoveryConflict
		},
	}
	response := postReaffirm(
		reaffirmTestHandler(conflicting),
		reaffirmTestCompanyID,
		body(func(map[string]string) {}),
	)
	assertErrorResponse(t, response, http.StatusConflict, "connector_discovery_conflict")
}
