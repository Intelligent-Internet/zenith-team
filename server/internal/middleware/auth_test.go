package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
)

// sessionAuthenticator answers every bearer with one fixed principal. Only
// Authenticate is called by the credential guards.
type sessionAuthenticator struct {
	auth.AuthService
	principal auth.UserPrincipal
}

func (a sessionAuthenticator) Authenticate(context.Context, string, auth.TokenKind) (auth.UserPrincipal, error) {
	return a.principal, nil
}

// A daemon's own session acts on the daemon plane, and
// elsewhere only as a live Run of its person on a route that accepts a Run.
// Everything else is 403 run_proof_required, and a person's session is
// untouched.
func TestDaemonSessionPassesOnlyTheDaemonPlaneAndRunRoutesWithALiveProof(t *testing.T) {
	t.Parallel()

	person := uuid.New()
	stranger := uuid.New()
	const (
		ownProof      = "brt_own"
		strangerProof = "brt_stranger"
		deadProof     = "brt_dead"
		brokenProof   = "brt_broken"
	)
	ownRun := execution.RunOrigin{RunID: uuid.New(), AuthorizedByUserID: &person}
	resolve := func(_ context.Context, token string) (execution.RunOrigin, error) {
		switch token {
		case ownProof:
			return ownRun, nil
		case strangerProof:
			return execution.RunOrigin{RunID: uuid.New(), AuthorizedByUserID: &stranger}, nil
		case brokenProof:
			return execution.RunOrigin{}, errors.New("database is down")
		default:
			return execution.RunOrigin{}, execution.ErrRunProofInvalid
		}
	}
	types := map[string]RouteActorType{
		"GET /person":     RouteActorTypePerson,
		"GET /run/:runId": RouteActorTypeRun,
		"GET /any":        RouteActorTypeAny,
		"GET /optional":   RouteActorTypePerson,
	}

	type outcome struct {
		resolved     int64
		resolvedRun  uuid.UUID
		reachedRoute bool
	}
	newRouter := func(principal auth.UserPrincipal, resolver RunOriginResolver) (*gin.Engine, *outcome) {
		seen := &outcome{}
		var counted RunOriginResolver
		if resolver != nil {
			counted = func(ctx context.Context, token string) (execution.RunOrigin, error) {
				atomic.AddInt64(&seen.resolved, 1)
				return resolver(ctx, token)
			}
		}
		service := sessionAuthenticator{principal: principal}
		reached := func(c *gin.Context) {
			seen.reachedRoute = true
			if origin, ok := ResolvedRunOrigin(c, c.GetHeader(HeaderRunToken)); ok {
				seen.resolvedRun = origin.RunID
			}
			c.Status(http.StatusNoContent)
		}
		router := gin.New()
		router.Use(RunProofPolicy(types, counted))
		router.GET("/daemon/config", RequireDaemonAuth(service), reached)
		router.GET("/person", RequireUserAuth(service), reached)
		router.GET("/run/:runId", RequireUserAuth(service), reached)
		router.GET("/any", RequireUserAuth(service), reached)
		router.GET("/optional", OptionalUserAuth(service), reached)
		return router, seen
	}
	daemon := auth.UserPrincipal{
		UserID: person, SessionID: uuid.New(), AuthKind: auth.TokenKindBearer, Client: auth.SessionClientDaemon,
	}
	personal := daemon
	personal.Client = auth.SessionClientPerson

	for _, test := range []struct {
		name      string
		principal auth.UserPrincipal
		noResolve bool
		path      string
		proof     []string
		want      int
		code      string
		resolved  int64
	}{
		{name: "daemon plane", principal: daemon, path: "/daemon/config", want: http.StatusNoContent},
		{
			name: "run route with its person's live proof", principal: daemon, path: "/run/42",
			proof: []string{ownProof}, want: http.StatusNoContent, resolved: 1,
		},
		{
			name: "run route without a proof", principal: daemon, path: "/run/42",
			want: http.StatusForbidden, code: "run_proof_required",
		},
		{
			name: "run route with another person's Run", principal: daemon, path: "/run/42",
			proof: []string{strangerProof}, want: http.StatusForbidden, code: "run_proof_required", resolved: 1,
		},
		{
			name: "run route with a proof that is not live", principal: daemon, path: "/run/42",
			proof: []string{deadProof}, want: http.StatusForbidden, code: "run_proof_required", resolved: 1,
		},
		{
			name: "run route with a blank proof", principal: daemon, path: "/run/42",
			proof: []string{""}, want: http.StatusForbidden, code: "run_proof_required",
		},
		{
			name: "run route with a repeated proof", principal: daemon, path: "/run/42",
			proof: []string{ownProof, ownProof}, want: http.StatusForbidden, code: "run_proof_required",
		},
		{
			name: "run route when the proof cannot be checked", principal: daemon, path: "/run/42",
			proof: []string{brokenProof}, want: http.StatusInternalServerError, code: "internal_error", resolved: 1,
		},
		{
			name: "run route with no resolver", principal: daemon, noResolve: true, path: "/run/42",
			proof: []string{ownProof}, want: http.StatusForbidden, code: "run_proof_required",
		},
		{
			name: "any route without a proof", principal: daemon, path: "/any",
			want: http.StatusForbidden, code: "run_proof_required",
		},
		{
			name: "any route with its person's live proof", principal: daemon, path: "/any",
			proof: []string{ownProof}, want: http.StatusNoContent, resolved: 1,
		},
		{
			name: "person route without a proof", principal: daemon, path: "/person",
			want: http.StatusForbidden, code: "run_proof_required",
		},
		{
			name: "person route with a proof", principal: daemon, path: "/person",
			proof: []string{ownProof}, want: http.StatusForbidden, code: "human_actor_required",
		},
		{
			name: "optional sign-in route", principal: daemon, path: "/optional",
			want: http.StatusForbidden, code: "run_proof_required",
		},
		{name: "a person's session on a person route", principal: personal, path: "/person", want: http.StatusNoContent},
		{
			name: "a person's session on a run route leaves the proof to the handler", principal: personal,
			path: "/run/42", proof: []string{ownProof}, want: http.StatusNoContent,
		},
		{name: "a person's session on the daemon plane", principal: personal, path: "/daemon/config", want: http.StatusNoContent},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var resolver RunOriginResolver = resolve
			if test.noResolve {
				resolver = nil
			}
			router, seen := newRouter(test.principal, resolver)
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("Authorization", "Bearer access")
			for _, value := range test.proof {
				request.Header.Add(HeaderRunToken, value)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf(
					"status = %d, want %d; body = %s",
					response.Code,
					test.want,
					response.Body.String(),
				)
			}
			if got := atomic.LoadInt64(&seen.resolved); got != test.resolved {
				t.Fatalf("the guard resolved the proof %d times, want %d", got, test.resolved)
			}
			if test.code == "" {
				if !seen.reachedRoute {
					t.Fatal("the route was not reached")
				}
				wantRun := uuid.Nil
				if test.principal.DaemonSession() && len(test.proof) == 1 {
					wantRun = ownRun.RunID
				}
				if seen.resolvedRun != wantRun {
					t.Fatalf(
						"the handler reads Run %s from the guard, want %s",
						seen.resolvedRun,
						wantRun,
					)
				}
				return
			}
			if seen.reachedRoute {
				t.Fatal("a refused request reached the route")
			}
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode refusal: %v", err)
			}
			if body["code"] != test.code {
				t.Fatalf("code = %q, want %q", body["code"], test.code)
			}
		})
	}
}

// ResolvedRunOrigin answers only for the proof the guard resolved.
func TestResolvedRunOriginAnswersOnlyForTheResolvedProof(t *testing.T) {
	t.Parallel()
	person := uuid.New()
	origin := execution.RunOrigin{RunID: uuid.New(), AuthorizedByUserID: &person}
	router := gin.New()
	router.Use(RunProofPolicy(
		map[string]RouteActorType{"GET /run": RouteActorTypeRun},
		func(context.Context, string) (execution.RunOrigin, error) { return origin, nil },
	))
	service := sessionAuthenticator{principal: auth.UserPrincipal{
		UserID: person, SessionID: uuid.New(), AuthKind: auth.TokenKindBearer, Client: auth.SessionClientDaemon,
	}}
	var sameToken, otherToken, emptyToken bool
	router.GET("/run", RequireUserAuth(service), func(c *gin.Context) {
		_, sameToken = ResolvedRunOrigin(c, "brt_proof")
		_, otherToken = ResolvedRunOrigin(c, "brt_other")
		_, emptyToken = ResolvedRunOrigin(c, "")
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/run", nil)
	request.Header.Set("Authorization", "Bearer access")
	request.Header.Set(HeaderRunToken, "brt_proof")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || !sameToken || otherToken || emptyToken {
		t.Fatalf("status %d; same %v, other %v, empty %v; want 204, true, false, false",
			response.Code, sameToken, otherToken, emptyToken)
	}
}
