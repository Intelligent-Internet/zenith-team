package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
)

// groupServiceStub answers each Group method from its func field; a method
// with no func panics through the embedded nil interface, which the router
// recovers as a 500, so a call no test expected fails it.
type groupServiceStub struct {
	company.GroupService
	list          func(context.Context, auth.UserPrincipal) ([]company.Group, error)
	get           func(context.Context, auth.UserPrincipal, uuid.UUID) (company.GroupDetail, error)
	create        func(context.Context, auth.UserPrincipal, company.GroupInput) (company.GroupDetail, error)
	update        func(context.Context, auth.UserPrincipal, uuid.UUID, company.GroupUpdateInput) (company.GroupDetail, error)
	deleteGroup   func(context.Context, auth.UserPrincipal, uuid.UUID, int64) error
	changeMembers func(context.Context, auth.UserPrincipal, uuid.UUID, company.GroupMembersInput) (company.GroupDetail, error)
	preview       func(context.Context, auth.UserPrincipal, uuid.UUID, company.GroupChange) (company.GroupPreview, error)
}

func (s *groupServiceStub) List(ctx context.Context, p auth.UserPrincipal) ([]company.Group, error) {
	return s.list(ctx, p)
}

func (s *groupServiceStub) Get(ctx context.Context, p auth.UserPrincipal, id uuid.UUID) (company.GroupDetail, error) {
	return s.get(ctx, p, id)
}

func (s *groupServiceStub) Create(
	ctx context.Context,
	p auth.UserPrincipal,
	input company.GroupInput,
) (company.GroupDetail, error) {
	return s.create(ctx, p, input)
}

func (s *groupServiceStub) Update(
	ctx context.Context, p auth.UserPrincipal, id uuid.UUID, input company.GroupUpdateInput,
) (company.GroupDetail, error) {
	return s.update(ctx, p, id, input)
}

func (s *groupServiceStub) Delete(ctx context.Context, p auth.UserPrincipal, id uuid.UUID, revision int64) error {
	return s.deleteGroup(ctx, p, id, revision)
}

func (s *groupServiceStub) ChangeMembers(
	ctx context.Context, p auth.UserPrincipal, id uuid.UUID, input company.GroupMembersInput,
) (company.GroupDetail, error) {
	return s.changeMembers(ctx, p, id, input)
}

func (s *groupServiceStub) Preview(
	ctx context.Context, p auth.UserPrincipal, id uuid.UUID, change company.GroupChange,
) (company.GroupPreview, error) {
	return s.preview(ctx, p, id, change)
}

// groupHandler serves the Group routes for Company A's member with the given
// role; reached counts the service calls.
func groupHandler(role auth.CompanyRole, reached *int) *Handler {
	principal := crossCompanyUserPrincipal(role)
	detail := company.GroupDetail{
		Group:   company.Group{ID: crossCompanyResource, Name: "Finance", Projects: []company.GroupProject{}},
		Members: []company.GroupMember{},
	}
	count := func() { *reached++ }
	return New(Handler{
		RunProofResolver: runProofResolverStub{},
		AuthService:      authenticatedAuthStub(crossCompanyUser),
		CompanyService: &companyServiceStub{
			authorize: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
				return principal, nil
			},
			requirePermission: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
				if !role.Can(auth.PermissionMembersManage) {
					return auth.UserPrincipal{}, company.ErrForbidden
				}
				return principal, nil
			},
		},
		GroupService: &groupServiceStub{
			list: func(context.Context, auth.UserPrincipal) ([]company.Group, error) {
				count()
				return []company.Group{detail.Group}, nil
			},
			get: func(context.Context, auth.UserPrincipal, uuid.UUID) (company.GroupDetail, error) {
				count()
				return detail, nil
			},
			create: func(context.Context, auth.UserPrincipal, company.GroupInput) (company.GroupDetail, error) {
				count()
				return detail, nil
			},
			update: func(context.Context, auth.UserPrincipal, uuid.UUID, company.GroupUpdateInput) (company.GroupDetail, error) {
				count()
				return detail, nil
			},
			deleteGroup: func(context.Context, auth.UserPrincipal, uuid.UUID, int64) error {
				count()
				return nil
			},
			changeMembers: func(context.Context, auth.UserPrincipal, uuid.UUID, company.GroupMembersInput) (company.GroupDetail, error) {
				count()
				return detail, nil
			},
			preview: func(context.Context, auth.UserPrincipal, uuid.UUID, company.GroupChange) (company.GroupPreview, error) {
				count()
				return company.GroupPreview{}, nil
			},
		},
	})
}

type groupRoute struct {
	method, path, body string
	status             int
}

func groupRoutes() []groupRoute {
	id := "/" + crossCompanyResource.String()
	return []groupRoute{
		{http.MethodGet, "", "", http.StatusOK},
		{http.MethodPost, "", `{"name":"Finance","description":""}`, http.StatusCreated},
		{http.MethodGet, id, "", http.StatusOK},
		{http.MethodPatch, id, `{"expectedRevision":1,"name":"Money"}`, http.StatusOK},
		{http.MethodDelete, id, `{"expectedRevision":1}`, http.StatusNoContent},
		{
			http.MethodPost,
			id + "/members",
			`{"addUserIds":[],"removeUserIds":[],"expectedRevision":1}`,
			http.StatusOK,
		},
		{http.MethodPost, id + "/preview", `{"addUserIds":[],"removeUserIds":[],"delete":true}`, http.StatusOK},
	}
}

func serveGroupRoute(h *Handler, route groupRoute, runProof bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(route.method,
		"/api/v1/companies/"+crossCompanyA.String()+"/groups"+route.path, strings.NewReader(route.body))
	request.Header.Set("Authorization", "Bearer access-token")
	if route.body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if runProof {
		request.Header.Set(runTokenHeader, routeActorProof)
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
}

// IA-FR27: every active member reads the Groups; only members.manage
// holders change them, and a Run never does.
func TestGroupRoutesAnswerFromTheCompanyRole(t *testing.T) {
	t.Parallel()
	for _, route := range groupRoutes() {
		read := route.method == http.MethodGet
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			t.Parallel()
			reached := 0
			response := serveGroupRoute(groupHandler(auth.CompanyRoleAdmin, &reached), route, false)
			if response.Code != route.status || reached != 1 {
				t.Fatalf(
					"admin: %d (calls %d), want %d; body %s",
					response.Code,
					reached,
					route.status,
					response.Body.String(),
				)
			}
			reached = 0
			response = serveGroupRoute(groupHandler(auth.CompanyRoleMember, &reached), route, false)
			if read {
				if response.Code != route.status || reached != 1 {
					t.Fatalf(
						"member read: %d (calls %d); body %s",
						response.Code,
						reached,
						response.Body.String(),
					)
				}
			} else {
				assertErrorResponse(t, response, http.StatusForbidden, "company_permission_required")
				if reached != 0 {
					t.Fatalf("a member's change reached the service %d times", reached)
				}
			}
			reached = 0
			response = serveGroupRoute(groupHandler(auth.CompanyRoleOwner, &reached), route, true)
			assertErrorResponse(t, response, http.StatusForbidden, "human_actor_required")
			if reached != 0 {
				t.Fatalf("a Run proof reached the service %d times", reached)
			}
		})
	}
}

func TestGroupRoutesRefuseBadInputBeforeTheService(t *testing.T) {
	t.Parallel()
	id := "/" + crossCompanyResource.String()
	tooMany := `["` + strings.Repeat(uuid.NewString()+`","`, 500) + uuid.NewString() + `"]`
	for _, test := range []struct {
		name   string
		route  groupRoute
		status int
		code   string
	}{
		{"a malformed Group id", groupRoute{http.MethodGet, "/not-a-group", "", 0}, http.StatusNotFound, "group_not_found"},
		{
			"a malformed id on a change",
			groupRoute{http.MethodDelete, "/not-a-group", `{"expectedRevision":1}`, 0},
			http.StatusNotFound, "group_not_found",
		},
		{"no name", groupRoute{http.MethodPost, "", `{"description":"x"}`, 0}, http.StatusUnprocessableEntity, "validation_failed"},
		{
			"a rename naming nothing",
			groupRoute{http.MethodPatch, id, `{"expectedRevision":1}`, 0},
			http.StatusUnprocessableEntity, "validation_failed",
		},
		{
			"no revision",
			groupRoute{http.MethodPost, id + "/members", `{"addUserIds":[],"removeUserIds":[]}`, 0},
			http.StatusUnprocessableEntity, "validation_failed",
		},
		{
			"more than 500 ids",
			groupRoute{
				http.MethodPost, id + "/members", `{"addUserIds":` + tooMany + `,"removeUserIds":[],"expectedRevision":1}`, 0,
			},
			http.StatusUnprocessableEntity, "validation_failed",
		},
		{
			"an unknown field",
			groupRoute{
				http.MethodPost, id + "/preview", `{"addUserIds":[],"removeUserIds":[],"expectedRevision":1}`, 0,
			},
			http.StatusBadRequest, "invalid_request",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reached := 0
			assertErrorResponse(
				t,
				serveGroupRoute(groupHandler(auth.CompanyRoleOwner, &reached), test.route, false),
				test.status,
				test.code,
			)
			if reached != 0 {
				t.Fatalf("bad input reached the service %d times", reached)
			}
		})
	}
}

// The members route hands the service exactly the change it was sent.
func TestGroupMembersRouteSendsTheChange(t *testing.T) {
	t.Parallel()
	principal := crossCompanyUserPrincipal(auth.CompanyRoleAdmin)
	added, removed := uuid.New(), uuid.New()
	var got company.GroupMembersInput
	h := New(Handler{
		RunProofResolver: runProofResolverStub{},
		AuthService:      authenticatedAuthStub(crossCompanyUser),
		CompanyService: &companyServiceStub{
			requirePermission: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
				return principal, nil
			},
		},
		GroupService: &groupServiceStub{
			changeMembers: func(_ context.Context, p auth.UserPrincipal, id uuid.UUID, input company.GroupMembersInput) (company.GroupDetail, error) {
				if p.UserID != crossCompanyUser || id != crossCompanyResource {
					t.Errorf("principal %v, Group %v", p.UserID, id)
				}
				got = input
				return company.GroupDetail{}, nil
			},
		},
	})
	response := serveGroupRoute(h, groupRoute{
		method: http.MethodPost,
		path:   "/" + crossCompanyResource.String() + "/members",
		body:   `{"addUserIds":["` + added.String() + `"],"removeUserIds":["` + removed.String() + `"],"expectedRevision":7}`,
	}, false)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d; body %s", response.Code, response.Body.String())
	}
	if len(got.AddUserIDs) != 1 || got.AddUserIDs[0] != added || len(got.RemoveUserIDs) != 1 ||
		got.RemoveUserIDs[0] != removed || got.ExpectedRevision != 7 {
		t.Fatalf("input = %+v", got)
	}
}
