package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
)

// `zen project list` and `get` reach the Project reads through a Run's
// channel (ADR 0017). A proof is resolved like a Task read's: it must be
// live in this Company and carried by the Run's authorizer's own session,
// else run_token_invalid before anything is read. A live proof reads through
// ListForRun / GetForRun under the Run's scope, which names the Run whose
// row the delegate rule reads; the Run-scope refusal is the one fixed
// sentence.
// A member's own read carries no proof and is unchanged.
func TestProjectReadsThroughARunAreScoped(t *testing.T) {
	t.Parallel()

	alice, companyID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	bob := uuid.Must(uuid.NewV7())
	agentID, channelID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	projectID := uuid.Must(uuid.NewV7())
	principal := phase4Principal(alice, companyID)
	tagged := execution.RunOrigin{
		RunID: uuid.Must(uuid.NewV7()), CompanyID: companyID, AgentID: agentID,
		ConversationID: uuid.Must(uuid.NewV7()), ConversationKind: "channel_agent",
		AuthorizedByUserID: &alice, InvocationChannelID: &channelID,
	}
	othersRun := tagged
	othersRun.AuthorizedByUserID = &bob
	foreign := tagged
	foreign.CompanyID = uuid.Must(uuid.NewV7())
	wantScope := task.RunScope{AgentID: agentID, UserID: alice, RunID: tagged.RunID}
	const token = "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	project := company.ProjectResponse{ID: projectID, CompanyID: companyID, Key: "work"}

	var calls []string
	var scopes []*task.RunScope
	refuse := false
	service := &projectServiceStub{
		authorize: allProjectPermissions,
		list: func(context.Context, auth.UserPrincipal, bool) ([]company.ProjectResponse, error) {
			calls = append(calls, "list")
			return []company.ProjectResponse{project}, nil
		},
		get: func(context.Context, auth.UserPrincipal, uuid.UUID) (company.ProjectResponse, error) {
			calls = append(calls, "get")
			return project, nil
		},
		listForRun: func(_ context.Context, _ auth.UserPrincipal, _ bool, run *task.RunScope) ([]company.ProjectResponse, error) {
			calls, scopes = append(calls, "listForRun"), append(scopes, run)
			return []company.ProjectResponse{project}, nil
		},
		getForRun: func(_ context.Context, _ auth.UserPrincipal, _ uuid.UUID, run *task.RunScope) (company.ProjectResponse, error) {
			calls, scopes = append(calls, "getForRun"), append(scopes, run)
			if refuse {
				return company.ProjectResponse{}, fmt.Errorf(
					"Project work, which Bob cannot read: %w",
					task.ErrOutsideRunScope,
				)
			}
			return project, nil
		},
	}
	serve := func(origin execution.RunOrigin, path, proof string) *httptest.ResponseRecorder {
		t.Helper()
		handler := New(Handler{
			AuthService: authenticatedAuthStub(alice),
			CompanyService: &companyServiceStub{
				authorize: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
					return principal, nil
				},
			},
			ProjectService:   service,
			RunProofResolver: runProofResolverStub{origin: origin},
		})
		request := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/companies/"+companyID.String()+"/projects"+path,
			nil,
		)
		request.Header.Set("Authorization", "Bearer access-token")
		if proof != "" {
			request.Header.Set(runTokenHeader, proof)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	assertCalls := func(name string, want ...string) {
		t.Helper()
		if fmt.Sprint(calls) != fmt.Sprint(want) {
			t.Fatalf("%s: service calls %v, want %v", name, calls, want)
		}
		// The scope names the Run; the delegate rule reads its chat and
		// audience from the Run's own row.
		for _, run := range scopes {
			if run == nil || run.AgentID != wantScope.AgentID || run.UserID != wantScope.UserID ||
				run.RunID != wantScope.RunID {
				t.Fatalf("%s: read under %+v, want %+v", name, run, wantScope)
			}
		}
		calls, scopes = nil, nil
	}

	if response := serve(tagged, "", token); response.Code != http.StatusOK {
		t.Fatalf("Run list = %d %s", response.Code, response.Body.String())
	}
	assertCalls("Run list", "listForRun")
	if response := serve(tagged, "/"+projectID.String(), token); response.Code != http.StatusOK {
		t.Fatalf("Run get = %d %s", response.Code, response.Body.String())
	}
	assertCalls("Run get", "getForRun")

	refuse = true
	refused := serve(tagged, "/"+projectID.String(), token)
	assertErrorResponse(t, refused, http.StatusForbidden, "task_outside_run_scope")
	assertRefusalNamesNoOne(t, refused)
	assertCalls("refused Run get", "getForRun")
	refuse = false

	for name, test := range map[string]struct {
		origin execution.RunOrigin
		proof  string
	}{
		"finished Run":          {proof: token},
		"another Company's Run": {origin: foreign, proof: token},
		"another member's Run":  {origin: othersRun, proof: token},
	} {
		for _, path := range []string{"", "/" + projectID.String()} {
			assertErrorResponse(
				t,
				serve(test.origin, path, test.proof),
				http.StatusUnauthorized,
				"run_token_invalid",
			)
			assertCalls(name + " GET " + path)
		}
	}

	if response := serve(tagged, "", ""); response.Code != http.StatusOK {
		t.Fatalf("member list = %d", response.Code)
	}
	assertCalls("member list", "list")
	if response := serve(tagged, "/"+projectID.String(), ""); response.Code != http.StatusOK {
		t.Fatalf("member get = %d", response.Code)
	}
	assertCalls("member get", "get")
}
