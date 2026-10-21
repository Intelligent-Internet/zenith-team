package handler

// A UserPrincipal carries a zero CompanyID and an empty Role until
// requireCompanyMember or requireCompanyOwner resolves access for a
// :companyId route, so the middleware is the only thing that puts a company on
// the principal a handler reads. A company-scoped route mounted without it
// would scope its queries to the nil UUID instead of failing, which no type
// check can catch.
//
// These tests pin both halves of that fence: the middleware stamps the
// resolved company and refuses to run the handler otherwise, and every mounted
// :companyId route either runs the middleware or authorizes the path company
// inside its own service.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
)

// companyAccessFenceResult captures what each stage of the chain observed for
// one request: the principal as it stood before the company middleware ran,
// and the principal that reached the guarded handler.
type companyAccessFenceResult struct {
	response *httptest.ResponseRecorder
	before   []auth.UserPrincipal
	reached  []auth.UserPrincipal
}

// serveCompanyAccessFence runs one request through the real credential
// middleware, then the real company access middleware, then a terminal handler
// that records the principal it was given.
func serveCompanyAccessFence(
	service CompanyService,
	permission auth.Permission,
	authenticated bool,
	companyID string,
) companyAccessFenceResult {
	result := companyAccessFenceResult{response: httptest.NewRecorder()}
	router := gin.New()
	router.Use(middleware.RequestID())
	routes := router.Group("/companies/:companyId",
		middleware.RequireUserAuth(authenticatedAuthStub(crossCompanyUser)))
	routes.Use(func(c *gin.Context) {
		if principal, ok := auth.UserPrincipalFromContext(c.Request.Context()); ok {
			result.before = append(result.before, principal)
		}
		c.Next()
	})
	if permission != "" {
		routes.Use(middleware.RequireCompanyPermission(service, permission))
	} else {
		routes.Use(middleware.RequireCompanyMember(service))
	}
	routes.GET("/fence", func(c *gin.Context) {
		if principal, ok := auth.UserPrincipalFromContext(c.Request.Context()); ok {
			result.reached = append(result.reached, principal)
		}
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/companies/"+companyID+"/fence", nil)
	if authenticated {
		request.Header.Set("Authorization", "Bearer access-token")
	}
	router.ServeHTTP(result.response, request)
	return result
}

// grantCompanyAccess answers like the real service: it resolves the company
// for the caller's own principal and returns it with the role attached.
func grantCompanyAccess(
	role auth.CompanyRole,
) func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
	return func(_ context.Context, principal auth.UserPrincipal, companyID uuid.UUID) (auth.UserPrincipal, error) {
		principal.CompanyID = companyID
		principal.Role = role
		return principal, nil
	}
}

func denyCompanyAccess(
	_ context.Context,
	_ auth.UserPrincipal,
	_ uuid.UUID,
) (auth.UserPrincipal, error) {
	return auth.UserPrincipal{}, company.ErrNotFound
}

func TestCompanyAccessMiddlewareIsWhatPutsACompanyOnThePrincipal(t *testing.T) {
	t.Parallel()

	t.Run("credential middleware alone leaves the company unresolved", func(t *testing.T) {
		t.Parallel()

		result := serveCompanyAccessFence(
			&companyServiceStub{authorize: grantCompanyAccess(auth.CompanyRoleMember)},
			"", true, crossCompanyA.String(),
		)

		if len(result.before) != 1 {
			t.Fatalf("principals observed before the company middleware = %d, want 1", len(result.before))
		}
		if result.before[0].CompanyID != uuid.Nil {
			t.Errorf(
				"unresolved principal carries company %s, want the nil UUID",
				result.before[0].CompanyID,
			)
		}
		if result.before[0].Role != "" {
			t.Errorf("unresolved principal carries role %q, want empty", result.before[0].Role)
		}
	})

	t.Run("resolved access reaches the handler as company scope", func(t *testing.T) {
		t.Parallel()

		for _, testCase := range []struct {
			name       string
			permission auth.Permission
			role       auth.CompanyRole
		}{
			{name: "member", permission: "", role: auth.CompanyRoleMember},
			{name: "owner", permission: auth.PermissionCompanyDelete, role: auth.CompanyRoleOwner},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				t.Parallel()

				service := &companyServiceStub{
					authorize:         grantCompanyAccess(testCase.role),
					requirePermission: grantCompanyAccess(testCase.role),
				}
				result := serveCompanyAccessFence(
					service,
					testCase.permission,
					true,
					crossCompanyA.String(),
				)

				if result.response.Code != http.StatusNoContent {
					t.Fatalf("status = %d, want %d", result.response.Code, http.StatusNoContent)
				}
				if len(result.reached) != 1 {
					t.Fatalf("principals reaching the handler = %d, want 1", len(result.reached))
				}
				reached := result.reached[0]
				if reached.CompanyID != crossCompanyA {
					t.Errorf("handler company = %s, want %s", reached.CompanyID, crossCompanyA)
				}
				if reached.Role != testCase.role {
					t.Errorf("handler role = %q, want %q", reached.Role, testCase.role)
				}
				if reached.UserID != crossCompanyUser {
					t.Errorf("handler user = %s, want %s", reached.UserID, crossCompanyUser)
				}
			})
		}
	})

	t.Run("identity stays the authenticated caller, not the service result", func(t *testing.T) {
		t.Parallel()

		impostor := uuid.MustParse("0198aa00-0000-7000-8000-0000000000ff")
		service := &companyServiceStub{
			authorize: func(_ context.Context, _ auth.UserPrincipal, companyID uuid.UUID) (auth.UserPrincipal, error) {
				return auth.UserPrincipal{
					UserID:    impostor,
					SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
					AuthKind:  auth.TokenKindBearer,
					CompanyID: companyID,
					Role:      auth.CompanyRoleOwner,
				}, nil
			},
		}
		result := serveCompanyAccessFence(service, "", true, crossCompanyA.String())

		if len(result.reached) != 1 {
			t.Fatalf("principals reaching the handler = %d, want 1", len(result.reached))
		}
		if result.reached[0].UserID != crossCompanyUser {
			t.Errorf(
				"handler user = %s, want the authenticated caller %s",
				result.reached[0].UserID,
				crossCompanyUser,
			)
		}
	})

	t.Run("unresolved access never reaches the handler", func(t *testing.T) {
		t.Parallel()

		for _, testCase := range []struct {
			name          string
			service       CompanyService
			authenticated bool
			companyID     string
			wantStatus    int
			wantCode      string
		}{
			{
				name:          "denied company",
				service:       &companyServiceStub{authorize: denyCompanyAccess},
				authenticated: true,
				companyID:     crossCompanyB.String(),
				wantStatus:    http.StatusNotFound,
				wantCode:      "company_not_found",
			},
			{
				name:          "unparseable company",
				service:       &companyServiceStub{},
				authenticated: true,
				companyID:     "not-a-uuid",
				wantStatus:    http.StatusNotFound,
				wantCode:      "company_not_found",
			},
			{
				name:          "anonymous caller",
				service:       &companyServiceStub{},
				authenticated: false,
				companyID:     crossCompanyA.String(),
				wantStatus:    http.StatusUnauthorized,
				wantCode:      "authentication_required",
			},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				t.Parallel()

				result := serveCompanyAccessFence(
					testCase.service, "", testCase.authenticated, testCase.companyID,
				)

				assertErrorResponse(t, result.response, testCase.wantStatus, testCase.wantCode)
				if len(result.reached) != 0 {
					t.Fatalf(
						"handler ran with company %s; an unresolved principal must never reach a company-scoped handler",
						result.reached[0].CompanyID,
					)
				}
			})
		}
	})
}

// companyAccessResolutions records every company the access middleware asked
// the company service to resolve.
type companyAccessResolutions struct {
	mu        sync.Mutex
	companyID []uuid.UUID
}

func (r *companyAccessResolutions) record(companyID uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.companyID = append(r.companyID, companyID)
}

func (r *companyAccessResolutions) since(index int) []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uuid.UUID(nil), r.companyID[index:]...)
}

func (r *companyAccessResolutions) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.companyID)
}

// routesAuthorizingCompanyInService are the mounted :companyId routes that
// deliberately skip the access middleware. Each one passes the path company to
// its service as an explicit argument and is authorized there against the
// caller's membership; none of them reads CompanyID off the principal, so the
// unresolved nil UUID cannot reach a query.
func routesAuthorizingCompanyInService() map[string]struct{} {
	return map[string]struct{}{
		"GET /api/v1/companies/:companyId":                          {},
		"GET /api/v1/companies/:companyId/resources":                {},
		"POST /api/v1/companies/:companyId/resources":               {},
		"GET /api/v1/companies/:companyId/resources/:resourceId":    {},
		"PATCH /api/v1/companies/:companyId/resources/:resourceId":  {},
		"DELETE /api/v1/companies/:companyId/resources/:resourceId": {},
	}
}

// companyAccessFenceHandler mounts every company-scoped route with a company
// service that records each resolution and denies it. Every domain service is
// left unstubbed, so a route that skipped the access middleware would fall
// through to its domain service and answer 500 instead of the non-disclosing
// denial.
func companyAccessFenceHandler(t *testing.T) (*Handler, *companyAccessResolutions) {
	t.Helper()
	resolutions := &companyAccessResolutions{}
	deny := func(_ context.Context, _ auth.UserPrincipal, companyID uuid.UUID) (auth.UserPrincipal, error) {
		resolutions.record(companyID)
		return auth.UserPrincipal{}, company.ErrNotFound
	}
	companyService := &companyServiceStub{
		authorize:         deny,
		requirePermission: deny,
		get: func(context.Context, auth.UserPrincipal, uuid.UUID) (company.Response, error) {
			return company.Response{}, company.ErrNotFound
		},
	}
	resourceService := &companyResourceServiceStub{
		list: func(
			context.Context,
			auth.UserPrincipal,
			uuid.UUID,
			company.ResourceListOptions,
		) ([]company.ResourceResponse, error) {
			return nil, company.ErrResourceNotFound
		},
		get: func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (company.ResourceResponse, error) {
			return company.ResourceResponse{}, company.ErrResourceNotFound
		},
		update: func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, company.ResourceUpdateInput) (company.ResourceResponse, error) {
			return company.ResourceResponse{}, company.ErrResourceNotFound
		},
		delete: func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, company.RevisionInput) error {
			return company.ErrResourceNotFound
		},
		create: func(
			context.Context,
			uuid.UUID,
			uuid.UUID,
			company.CreateLocalDirectoryResourceInput,
		) (company.ResourceCreateResult, error) {
			return company.ResourceCreateResult{}, company.ErrResourceNotFound
		},
	}
	return New(Handler{
		AuthService:            authenticatedAuthStub(crossCompanyUser),
		ActivityFeed:           activityFeedStub{},
		CompanyService:         companyService,
		AgentService:           &agentServiceStub{},
		UsageService:           &usageServiceStub{},
		TaskService:            &taskServiceStub{},
		ConversationService:    &conversationServiceStub{},
		CompanyResourceService: resourceService,
	}), resolutions
}

func TestEveryMountedCompanyScopedRouteResolvesCompanyAccess(t *testing.T) {
	t.Parallel()

	handler, resolutions := companyAccessFenceHandler(t)
	inService := routesAuthorizingCompanyInService()
	// Routes that authorize in their service validate the body before they
	// reach that seam, so they need the same well-formed bodies the
	// cross-company walk uses.
	matrix := crossCompanyMatrix()
	unmounted := make(map[string]struct{}, len(inService))
	for key := range inService {
		unmounted[key] = struct{}{}
	}

	walked := 0
	for _, route := range handler.router.Routes() {
		if !strings.Contains(route.Path, ":companyId") {
			continue
		}
		walked++
		key := route.Method + " " + route.Path
		body := matrix[key].body
		if body == "" && route.Method != http.MethodGet {
			body = "{}"
		}
		delete(unmounted, key)

		t.Run(key, func(t *testing.T) {
			before := resolutions.len()
			response := serveCrossCompany(
				handler, route.Method, crossCompanyPath(route.Path, crossCompanyA), body, true,
			)
			resolved := resolutions.since(before)

			// The denial is non-disclosing whichever seam produced it. A 500
			// here means the request fell through to an unstubbed domain
			// service, which is exactly the unguarded-route failure.
			if response.Code != http.StatusNotFound {
				t.Fatalf(
					"status = %d, want %d; body %s",
					response.Code,
					http.StatusNotFound,
					response.Body.String(),
				)
			}
			if _, authorizedInService := inService[key]; authorizedInService {
				if len(resolved) != 0 {
					t.Errorf(
						"route %q is listed as authorizing in its service but ran the access middleware; move it out of the list",
						key,
					)
				}
				return
			}
			if len(resolved) == 0 {
				t.Fatalf(
					"mounted company-scoped route %q never resolved company access; without requireCompanyMember or requireCompanyOwner its principal reaches the handler with the nil company",
					key,
				)
			}
			for _, companyID := range resolved {
				if companyID != crossCompanyA {
					t.Errorf(
						"access middleware resolved company %s, want the path company %s",
						companyID,
						crossCompanyA,
					)
				}
			}
		})
	}

	for key := range unmounted {
		t.Errorf("route %q is listed as authorizing in its service but is not mounted; update the list", key)
	}

	// This fence is only as wide as the walk above. Rename the path parameter
	// and the filter matches nothing, every subtest disappears, and a test
	// named "every mounted company-scoped route" passes having checked none.
	if walked == 0 {
		t.Fatal("walked no company-scoped routes; the :companyId filter no longer matches the mounted paths")
	}
}
