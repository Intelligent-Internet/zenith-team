package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestToolReportRequiresDaemonIdentityEvenDuringLegacyHeaderRollout(t *testing.T) {
	t.Parallel()
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/daemon/runtimes/11111111-1111-4111-8111-111111111111/tools",
		strings.NewReader(`{"items":[]}`),
	)
	// No dependencies: a missing header must be rejected before a DB read.
	(&Handler{}).daemonReportTools(c)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestToolReportBoundaryAcceptsInstallationAndAgentProfiles(t *testing.T) {
	t.Parallel()
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(
		http.MethodPost,
		"/tools",
		strings.NewReader(
			`{"items":[{"key":"zenith-studio","authState":"unknown"},{"key":"hyperframes","authState":"not_required"},{"key":"zenith-studio","agentId":"11111111-1111-4111-8111-111111111111","authState":"authenticated"},{"key":"zenith-studio","agentId":"22222222-2222-4222-8222-222222222222","authState":"needs_auth"}]}`,
		),
	)
	c.Request.Header.Set("Content-Type", "application/json")
	request, err := bindRequest[daemonToolReportRequest](c, 2<<20)
	if err != nil || len(request.Items) != 4 {
		t.Fatalf("scoped report=%+v, %v", request, err)
	}
}

// The daemon's own session reads its person's bindings on one of its
// runtimes on the daemon plane, through the same runtime fence as every
// runtime-scoped daemon route; a runtime of another daemon or another person
// is the plain 404 and lists nothing.
func TestDaemonRuntimeBindingsListsThePersonsBindingsOnItsOwnRuntime(t *testing.T) {
	t.Parallel()

	principal := daemonTestPrincipal()
	principal.Client = auth.SessionClientDaemon
	runtimeID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2773")
	companyID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2774")
	agentID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2775")
	owned := uuid.NullUUID{UUID: principal.UserID, Valid: true}
	for _, test := range []struct {
		name     string
		daemonID string
		owner    uuid.NullUUID
		want     int
	}{
		{name: "its own runtime", daemonID: "daemon-a", owner: owned, want: http.StatusOK},
		{name: "another daemon's runtime", daemonID: "daemon-m", owner: owned, want: http.StatusNotFound},
		{
			name: "another person's runtime", daemonID: "daemon-a",
			owner: uuid.NullUUID{UUID: uuid.New(), Valid: true}, want: http.StatusNotFound,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var listed []agent.BindingListOptions
			var listedFor []auth.UserPrincipal
			handler := &Handler{
				Queries: db.New(&daemonRuntimeAccessDBTX{
					runtime: db.Runtime{
						ID:          runtimeID,
						CompanyID:   companyID,
						DaemonID:    "daemon-a",
						OwnerUserID: test.owner,
					},
					member: true,
				}),
				AgentBindingService: &agentBindingServiceStub{
					list: func(_ context.Context, caller auth.UserPrincipal, options agent.BindingListOptions) ([]agent.Binding, error) {
						listed = append(listed, options)
						listedFor = append(listedFor, caller)
						return []agent.Binding{
							{
								ID:      uuid.New(),
								AgentID: agentID,
								Runtime: agent.RuntimeRef{ID: runtimeID},
							},
						}, nil
					},
				},
			}
			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			c.Request = httptest.NewRequest(
				http.MethodGet, "/api/v1/daemon/runtimes/"+runtimeID.String()+"/agent-bindings", nil,
			)
			c.Request.Header.Set("X-Zenith-Team-Daemon-ID", test.daemonID)
			c.Request = c.Request.WithContext(auth.WithUserPrincipal(c.Request.Context(), principal))
			c.Params = gin.Params{{Key: "runtimeId", Value: runtimeID.String()}}
			handler.daemonRuntimeBindings(c)
			if response.Code != test.want {
				t.Fatalf(
					"status = %d, want %d; body = %s",
					response.Code,
					test.want,
					response.Body.String(),
				)
			}
			if test.want != http.StatusOK {
				if len(listed) != 0 {
					t.Fatalf("listed bindings for a runtime that is not its own: %+v", listed)
				}
				return
			}
			if len(listed) != 1 || listed[0].RuntimeID == nil || *listed[0].RuntimeID != runtimeID ||
				listedFor[0].CompanyID != companyID || listedFor[0].UserID != principal.UserID {
				t.Fatalf(
					"listed %+v for %+v; want this runtime in its Company for its person",
					listed,
					listedFor,
				)
			}
			var body struct {
				Runtime struct {
					ID        string `json:"id"`
					CompanyID string `json:"companyId"`
					DaemonID  string `json:"daemonId"`
				} `json:"runtime"`
				Items []struct {
					AgentID string `json:"agentId"`
					Runtime struct {
						ID string `json:"id"`
					} `json:"runtime"`
				} `json:"items"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Runtime.ID != runtimeID.String() || body.Runtime.CompanyID != companyID.String() ||
				body.Runtime.DaemonID != "daemon-a" || len(body.Items) != 1 ||
				body.Items[0].AgentID != agentID.String() || body.Items[0].Runtime.ID != runtimeID.String() {
				t.Fatalf("answer = %s", response.Body.String())
			}
		})
	}
}
