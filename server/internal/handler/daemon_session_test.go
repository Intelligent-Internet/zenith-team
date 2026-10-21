package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func routeActorDaemonSession() auth.UserPrincipal {
	principal := routeActorMember()
	principal.CompanyID, principal.Role = uuid.Nil, ""
	principal.Client = auth.SessionClientDaemon
	return principal
}

// countingDaemonAuth answers every bearer with the daemon's own session and
// counts how often a route authenticated.
func countingDaemonAuth() (*authServiceStub, func() int) {
	var mu sync.Mutex
	calls := 0
	stub := &authServiceStub{
		authenticate: func(context.Context, string, auth.TokenKind) (auth.UserPrincipal, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			return routeActorDaemonSession(), nil
		},
	}
	return stub, func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
}

// The daemon's own session with no Run proof is
// refused with 403 run_proof_required on every route that authenticates,
// person, run and any alike, except the daemon plane. It is never its
// person.
func TestDaemonSessionWithoutAProofIsRefusedOffTheDaemonPlane(t *testing.T) {
	t.Parallel()

	stub, authenticated := countingDaemonAuth()
	resolver := &countingRunProofResolver{}
	wired := fullyWiredHandler()
	wired.AuthService = stub
	wired.CompanyService = routeActorCompanyService()
	wired.Queries = generated.New(&daemonRuntimeAccessDBTX{runtimeErr: pgx.ErrNoRows})
	wired.RunProofResolver = resolver
	handler := New(wired)

	refused := 0
	for route := range routeActorTypes {
		t.Run(route, func(t *testing.T) {
			method, path := routeActorPath(route)
			var request *http.Request
			if method == http.MethodGet {
				request = httptest.NewRequest(method, path, nil)
			} else {
				request = httptest.NewRequest(method, path, strings.NewReader("{}"))
				request.Header.Set("Content-Type", "application/json")
			}
			request.Header.Set("Authorization", "Bearer daemon-access-token")
			before := authenticated()
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if authenticated() == before {
				// A public route (sign-in, refresh, invitations) never reads
				// the credential, so it is no person's to refuse.
				return
			}
			if strings.HasPrefix(path, "/api/v1/daemon/") {
				if strings.Contains(response.Body.String(), `"run_proof_required"`) {
					t.Fatalf(
						"the daemon plane refused the daemon's own session: %s",
						response.Body.String(),
					)
				}
				return
			}
			refused++
			assertErrorResponse(t, response, http.StatusForbidden, "run_proof_required")
		})
	}
	if refused == 0 || resolver.count() != 0 {
		t.Fatalf(
			"refused %d routes, resolved %d proofs; want refusals and no resolution",
			refused,
			resolver.count(),
		)
	}
	for _, route := range append(append([]string{}, runActorRoutes...), anyActorRoutes...) {
		if strings.Contains(route, " /api/v1/daemon/") {
			continue
		}
		method, path := routeActorPath(route)
		request := httptest.NewRequest(method, path, nil)
		request.Header.Set("Authorization", "Bearer daemon-access-token")
		before := authenticated()
		handler.ServeHTTP(httptest.NewRecorder(), request)
		if authenticated() == before {
			t.Errorf("%s accepts a Run but never authenticated the daemon's session", route)
		}
	}
}

// With its person's live proof the daemon's session reaches
// every run route, and the proof is resolved once, by the credential guard;
// the handler reads the guard's answer instead of resolving it again. The
// Run here is in another Company, so each handler still refuses it on its
// own check (runProofBearer).
func TestDaemonSessionReachesRunRoutesWithItsPersonsLiveProof(t *testing.T) {
	t.Parallel()

	stub, _ := countingDaemonAuth()
	authorizer := routeActorUser
	resolver := &countingRunProofResolver{origin: execution.RunOrigin{
		RunID: uuid.New(), CompanyID: uuid.New(), AgentID: uuid.New(),
		ConversationID: uuid.New(), ConversationKind: "direct", AuthorizedByUserID: &authorizer,
	}}
	wired := fullyWiredHandler()
	wired.AuthService = stub
	wired.CompanyService = routeActorCompanyService()
	wired.ProjectService = &projectServiceStub{authorize: allProjectPermissions}
	wired.Queries = generated.New(&daemonRuntimeAccessDBTX{member: true})
	wired.RunProofResolver = resolver
	handler := New(wired)

	probes := runRouteProbes()
	for _, route := range runActorRoutes {
		probe := probes[route]
		t.Run(route, func(t *testing.T) {
			before := resolver.count()
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, probe.request(t, route))
			if strings.Contains(response.Body.String(), `"run_proof_required"`) {
				t.Fatalf(
					"the guard refused a live proof of the session's person: %s",
					response.Body.String(),
				)
			}
			if resolved := resolver.count() - before; resolved != 1 {
				t.Fatalf("the proof was resolved %d times, want once; status %d, body %s",
					resolved, response.Code, response.Body.String())
			}
			if response.Code < 400 {
				t.Fatalf(
					"a Run of another Company answered %d; body %s",
					response.Code,
					response.Body.String(),
				)
			}
		})
	}
}

// POST /auth/daemon-session: a person's bearer session starts the
// daemon's own session; a daemon session cannot start another, and a
// browser's cookie session gets no bearer pair.
func TestDaemonSessionRouteMintsOnlyForAPersonsBearerSession(t *testing.T) {
	t.Parallel()

	issuedAt := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	newHandler := func(principal auth.UserPrincipal) (*Handler, *int) {
		minted := 0
		wired := fullyWiredHandler()
		wired.AuthService = &authServiceStub{
			authenticate: func(_ context.Context, _ string, kind auth.TokenKind) (auth.UserPrincipal, error) {
				principal.AuthKind = kind
				return principal, nil
			},
			mintDaemon: func(_ context.Context, caller auth.UserPrincipal) (auth.Result, error) {
				minted++
				if caller.UserID != routeActorUser || caller.DaemonSession() {
					t.Errorf("minted for %+v", caller)
				}
				return auth.Result{
					User: auth.User{
						ID:          routeActorUser,
						Email:       "mia@example.com",
						DisplayName: "Mia",
					},
					Client: auth.ClientDesktop,
					Tokens: auth.IssuedTokens{
						SessionID:    uuid.New(),
						IssuedAt:     issuedAt,
						AccessToken:  "daemon-access",
						RefreshToken: "daemon-refresh",
						AccessExpiresAt: issuedAt.Add(
							15 * time.Minute,
						),
						RefreshExpiresAt: issuedAt.Add(720 * time.Hour),
					},
				}, nil
			},
		}
		return New(wired), &minted
	}
	person := routeActorMember()
	person.CompanyID, person.Role = uuid.Nil, ""

	t.Run("a person's bearer session", func(t *testing.T) {
		t.Parallel()
		handler, minted := newHandler(person)
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/daemon-session", strings.NewReader("{}"))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer person-access")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusCreated || *minted != 1 {
			t.Fatalf("status = %d, minted %d; body = %s", response.Code, *minted, response.Body.String())
		}
		var body struct {
			Client  string              `json:"client"`
			User    struct{ ID string } `json:"user"`
			Session struct {
				Transport    string `json:"transport"`
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
			} `json:"session"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Client != "daemon" || body.User.ID != routeActorUser.String() ||
			body.Session.Transport != "bearer" || body.Session.AccessToken != "daemon-access" ||
			body.Session.RefreshToken != "daemon-refresh" {
			t.Fatalf("answer = %s", response.Body.String())
		}
	})
	t.Run("an empty body", func(t *testing.T) {
		t.Parallel()
		handler, minted := newHandler(person)
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/daemon-session", nil)
		request.Header.Set("Authorization", "Bearer person-access")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusCreated || *minted != 1 {
			t.Fatalf("status = %d, minted %d; body = %s", response.Code, *minted, response.Body.String())
		}
	})
	for _, test := range []struct {
		name      string
		principal auth.UserPrincipal
		headers   map[string]string
		body      string
		status    int
		code      string
	}{
		{
			name: "a daemon session cannot start another", principal: routeActorDaemonSession(),
			status: http.StatusForbidden, code: "run_proof_required",
		},
		{
			name: "nor with a Run proof", principal: routeActorDaemonSession(),
			headers: map[string]string{runTokenHeader: routeActorProof},
			status:  http.StatusForbidden, code: "human_actor_required",
		},
		{
			name: "a browser gets no bearer pair", principal: person,
			headers: map[string]string{"Origin": "https://app.zenith-team.test"},
			status:  http.StatusUnprocessableEntity, code: "client_transport_mismatch",
		},
		{
			name: "the body takes no fields", principal: person, body: `{"client":"daemon"}`,
			status: http.StatusBadRequest, code: "invalid_request",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler, minted := newHandler(test.principal)
			body := test.body
			if body == "" {
				body = "{}"
			}
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/auth/daemon-session",
				strings.NewReader(body),
			)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer access")
			for key, value := range test.headers {
				request.Header.Set(key, value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertErrorResponse(t, response, test.status, test.code)
			if *minted != 0 {
				t.Fatalf("minted %d sessions on a refused request", *minted)
			}
		})
	}
}
