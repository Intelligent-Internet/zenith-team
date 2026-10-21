package handler

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
)

// projectServiceStub answers the route guard from authorize and every other
// method from its func field; a method with no func fails the test that
// reached it through the embedded nil interface.
type projectServiceStub struct {
	company.ProjectService
	authorize    func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.ProjectAccess, error)
	create       func(context.Context, auth.UserPrincipal, company.ProjectCreateInput) (company.ProjectResponse, error)
	list         func(context.Context, auth.UserPrincipal, bool) ([]company.ProjectResponse, error)
	manageView   func(context.Context, auth.UserPrincipal, bool) ([]company.ProjectResponse, error)
	get          func(context.Context, auth.UserPrincipal, uuid.UUID) (company.ProjectResponse, error)
	update       func(context.Context, auth.UserPrincipal, uuid.UUID, company.ProjectUpdateInput) (company.ProjectResponse, error)
	archive      func(context.Context, auth.UserPrincipal, uuid.UUID, int64) (company.ProjectResponse, error)
	replaceFiles func(context.Context, auth.UserPrincipal, uuid.UUID, company.ProjectFilesInput, int64) (company.ProjectResponse, error)
	listGrants   func(context.Context, auth.UserPrincipal, uuid.UUID) (company.ProjectGrantList, error)
	setGrants    func(context.Context, auth.UserPrincipal, uuid.UUID, company.ProjectGrantInput) (company.ProjectGrantResult, error)
	removeGrants func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int64) (company.ProjectGrantResult, error)
	previewGrant func(
		context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, []string,
	) (company.ProjectGrantPreview, error)
	join         func(context.Context, auth.UserPrincipal, uuid.UUID, int64) (company.ProjectResponse, error)
	listForRun   func(context.Context, auth.UserPrincipal, bool, *task.RunScope) ([]company.ProjectResponse, error)
	getForRun    func(context.Context, auth.UserPrincipal, uuid.UUID, *task.RunScope) (company.ProjectResponse, error)
	accessReport func(context.Context, auth.UserPrincipal) (company.ProjectAccessReport, error)
	setGroup     func(
		context.Context, auth.UserPrincipal, uuid.UUID, company.ProjectGroupGrantInput,
	) (company.ProjectGroupGrantResult, error)
	removeGroup func(
		context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int64,
	) (company.ProjectGroupGrantResult, error)
	previewGroup func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, []string) (company.GroupPreview, error)
}

func (s *projectServiceStub) SetGroupGrant(
	ctx context.Context, p auth.UserPrincipal, id uuid.UUID, input company.ProjectGroupGrantInput,
) (company.ProjectGroupGrantResult, error) {
	return s.setGroup(ctx, p, id, input)
}

func (s *projectServiceStub) RemoveGroupGrant(
	ctx context.Context, p auth.UserPrincipal, id, groupID uuid.UUID, revision int64,
) (company.ProjectGroupGrantResult, error) {
	return s.removeGroup(ctx, p, id, groupID, revision)
}

func (s *projectServiceStub) PreviewGroupGrant(
	ctx context.Context, p auth.UserPrincipal, id, groupID uuid.UUID, permissions []string,
) (company.GroupPreview, error) {
	return s.previewGroup(ctx, p, id, groupID, permissions)
}

func (s *projectServiceStub) ListForRun(
	ctx context.Context, p auth.UserPrincipal, include bool, run *task.RunScope,
) ([]company.ProjectResponse, error) {
	return s.listForRun(ctx, p, include, run)
}

func (s *projectServiceStub) GetForRun(
	ctx context.Context, p auth.UserPrincipal, id uuid.UUID, run *task.RunScope,
) (company.ProjectResponse, error) {
	return s.getForRun(ctx, p, id, run)
}

func (s *projectServiceStub) Authorize(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
) (auth.ProjectAccess, error) {
	return s.authorize(ctx, p, id)
}

func (s *projectServiceStub) Create(
	ctx context.Context,
	p auth.UserPrincipal,
	input company.ProjectCreateInput,
) (company.ProjectResponse, error) {
	return s.create(ctx, p, input)
}

func (s *projectServiceStub) List(
	ctx context.Context,
	p auth.UserPrincipal,
	include bool,
) ([]company.ProjectResponse, error) {
	return s.list(ctx, p, include)
}

func (s *projectServiceStub) ListForManageView(
	ctx context.Context,
	p auth.UserPrincipal,
	include bool,
) ([]company.ProjectResponse, error) {
	return s.manageView(ctx, p, include)
}

func (s *projectServiceStub) Get(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
) (company.ProjectResponse, error) {
	return s.get(ctx, p, id)
}

func (s *projectServiceStub) Update(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	input company.ProjectUpdateInput,
) (company.ProjectResponse, error) {
	return s.update(ctx, p, id, input)
}

func (s *projectServiceStub) Archive(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	revision int64,
) (company.ProjectResponse, error) {
	return s.archive(ctx, p, id, revision)
}

func (s *projectServiceStub) ReplaceFiles(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	input company.ProjectFilesInput,
	revision int64,
) (company.ProjectResponse, error) {
	return s.replaceFiles(ctx, p, id, input, revision)
}

func (s *projectServiceStub) ListGrants(
	ctx context.Context, p auth.UserPrincipal, id uuid.UUID,
) (company.ProjectGrantList, error) {
	return s.listGrants(ctx, p, id)
}

func (s *projectServiceStub) SetGrants(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	input company.ProjectGrantInput,
) (company.ProjectGrantResult, error) {
	return s.setGrants(ctx, p, id, input)
}

func (s *projectServiceStub) RemoveGrants(
	ctx context.Context,
	p auth.UserPrincipal,
	id, userID uuid.UUID,
	revision int64,
) (company.ProjectGrantResult, error) {
	return s.removeGrants(ctx, p, id, userID, revision)
}

func (s *projectServiceStub) PreviewGrant(
	ctx context.Context, p auth.UserPrincipal, id, userID uuid.UUID, permissions []string,
) (company.ProjectGrantPreview, error) {
	return s.previewGrant(ctx, p, id, userID, permissions)
}

func (s *projectServiceStub) AccessReport(
	ctx context.Context,
	p auth.UserPrincipal,
) (company.ProjectAccessReport, error) {
	return s.accessReport(ctx, p)
}

func (s *projectServiceStub) Join(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	revision int64,
) (company.ProjectResponse, error) {
	return s.join(ctx, p, id, revision)
}

// allProjectPermissions is a reader holding the whole catalog.
func allProjectPermissions(context.Context, auth.UserPrincipal, uuid.UUID) (auth.ProjectAccess, error) {
	return auth.ProjectAccess{Via: auth.ProjectAccessViaGrant, Permissions: auth.ProjectPermissions()}, nil
}

func TestProjectRoutesRejectMutableKeysAndServeCreate(t *testing.T) {
	t.Parallel()
	principal := phase4Principal(uuid.New(), uuid.New())
	calls := 0
	service := &projectServiceStub{
		authorize: allProjectPermissions,
		create: func(_ context.Context, p auth.UserPrincipal, input company.ProjectCreateInput) (company.ProjectResponse, error) {
			calls++
			if p != principal || input.Key != "research" {
				t.Fatalf("create = %+v / %+v", p, input)
			}
			return company.ProjectResponse{
				ID:        uuid.New(),
				CompanyID: p.CompanyID,
				Key:       input.Key,
				Title:     input.Title,
				Revision:  1,
			}, nil
		},
	}
	h := New(
		Handler{
			AuthService: authenticatedAuthStub(principal.UserID),
			CompanyService: &companyServiceStub{
				authorize: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
					return principal, nil
				},
				requirePermission: func(_ context.Context, _ auth.UserPrincipal, _ uuid.UUID) (auth.UserPrincipal, error) {
					return principal, nil
				},
			},
			ProjectService: service,
		},
	)
	root := "/api/v1/companies/" + principal.CompanyID.String() + "/projects"
	result := serveBearerJSON(h, http.MethodPost, root, []byte(`{"key":"research","title":"Research"}`))
	if result.Code != 201 || calls != 1 {
		t.Fatalf("create = %d %s", result.Code, result.Body.String())
	}
	for _, body := range []string{
		`{"expectedRevision":1,"key":"renamed"}`,
		`{"expectedRevision":1,"companyId":"` + uuid.NewString() + `"}`,
		`{"expectedRevision":1,"title":null}`,
		`{"expectedRevision":1}`,
		`{"expectedRevision":1,"visibility":"public"}`,
	} {
		result = serveBearerJSON(h, http.MethodPatch, root+"/"+uuid.NewString(), []byte(body))
		if result.Code != 422 && result.Code != 400 {
			t.Fatalf("body %s = %d %s", body, result.Code, result.Body.String())
		}
	}
	// Creating a Project needs projects.manage, declared on the route, so a
	// member is refused before the body is read.
	member := New(
		Handler{
			AuthService: authenticatedAuthStub(principal.UserID),
			CompanyService: &companyServiceStub{
				authorize: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
					return principal, nil
				},
				requirePermission: func(_ context.Context, _ auth.UserPrincipal, _ uuid.UUID) (auth.UserPrincipal, error) {
					return auth.UserPrincipal{}, company.ErrForbidden
				},
			},
			ProjectService: &projectServiceStub{
				create: func(context.Context, auth.UserPrincipal, company.ProjectCreateInput) (company.ProjectResponse, error) {
					t.Fatal("a member reached Create")
					return company.ProjectResponse{}, nil
				},
			},
		},
	)
	result = serveBearerJSON(member, http.MethodPost, root, []byte(`not json at all`))
	if result.Code != 403 || !strings.Contains(result.Body.String(), `"code":"company_permission_required"`) {
		t.Fatalf("member create = %d %s", result.Code, result.Body.String())
	}
	for _, row := range projectErrorMappings {
		assertMappedErrorResponse(
			t,
			func(c *gin.Context) { writeProjectError(c, row.target) },
			row.status,
			row.detail,
			row.code,
		)
	}
}

// projectGrantRoutesHandler serves the grant routes for a caller holding
// every Project permission; each grant service method normalizes its set as
// the real service does first, and answers what the test gives it.
func projectGrantRoutesHandler(principal auth.UserPrincipal, preview *company.ProjectGrantPreview) *Handler {
	normalize := func(names []string) error {
		_, err := auth.NormalizeProjectGrant(names)
		return err
	}
	service := &projectServiceStub{
		authorize: allProjectPermissions,
		setGrants: func(
			_ context.Context, _ auth.UserPrincipal, _ uuid.UUID, input company.ProjectGrantInput,
		) (company.ProjectGrantResult, error) {
			return company.ProjectGrantResult{Revision: 2}, normalize(input.Permissions)
		},
		setGroup: func(
			_ context.Context, _ auth.UserPrincipal, _ uuid.UUID, input company.ProjectGroupGrantInput,
		) (company.ProjectGroupGrantResult, error) {
			return company.ProjectGroupGrantResult{Revision: 2}, normalize(input.Permissions)
		},
		previewGroup: func(
			_ context.Context, _ auth.UserPrincipal, _, _ uuid.UUID, names []string,
		) (company.GroupPreview, error) {
			return company.GroupPreview{}, normalize(names)
		},
		previewGrant: func(
			_ context.Context, p auth.UserPrincipal, _, userID uuid.UUID, names []string,
		) (company.ProjectGrantPreview, error) {
			if err := normalize(names); err != nil {
				return company.ProjectGrantPreview{}, err
			}
			if p != principal || userID != principal.UserID || len(names) != 0 {
				return company.ProjectGrantPreview{}, company.ErrProjectInvalidInput
			}
			return *preview, nil
		},
	}
	return New(Handler{
		AuthService: authenticatedAuthStub(principal.UserID),
		CompanyService: &companyServiceStub{
			authorize: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
				return principal, nil
			},
		},
		ProjectService: service,
	})
}

// A grant, a Group grant or either preview naming a permission outside the
// catalog answers 422 project_permission_unknown with the permission in the
// detail. An older desktop still sends the removed project.results.review,
// and is told which permission it was.
func TestGrantRoutesNameAnUnknownPermission(t *testing.T) {
	t.Parallel()
	principal := phase4Principal(uuid.New(), uuid.New())
	h := projectGrantRoutesHandler(principal, &company.ProjectGrantPreview{})
	root := "/api/v1/companies/" + principal.CompanyID.String() + "/projects/" + uuid.NewString()
	removed := `["project.read","project.results.review"]`
	for _, route := range []struct{ method, path, body string }{
		{http.MethodPut, "/grants/" + principal.UserID.String(), `{"permissions":` + removed + `,"expectedRevision":1}`},
		{http.MethodPost, "/grants/" + principal.UserID.String() + "/preview", `{"permissions":` + removed + `}`},
		{http.MethodPut, "/group-grants/" + uuid.NewString(), `{"permissions":` + removed + `,"expectedRevision":1}`},
		{http.MethodPost, "/group-grants/" + uuid.NewString() + "/preview", `{"permissions":` + removed + `}`},
	} {
		response := serveBearerJSON(h, route.method, root+route.path, []byte(route.body))
		assertErrorResponse(t, response, http.StatusUnprocessableEntity, "project_permission_unknown")
		if !strings.Contains(response.Body.String(), `project.results.review`) {
			t.Errorf("%s %s detail does not name the permission: %s",
				route.method, route.path, response.Body.String())
		}
	}
}

// The person grant preview answers the service's counts as they are.
func TestPersonGrantPreviewAnswersRunsAndLoss(t *testing.T) {
	t.Parallel()
	principal := phase4Principal(uuid.New(), uuid.New())
	h := projectGrantRoutesHandler(principal, &company.ProjectGrantPreview{
		Revision: 7, RunsStopping: 2, LosesProject: true,
	})
	path := "/api/v1/companies/" + principal.CompanyID.String() + "/projects/" + uuid.NewString() +
		"/grants/" + principal.UserID.String() + "/preview"
	response := serveBearerJSON(h, http.MethodPost, path, []byte(`{"permissions":[]}`))
	if response.Code != http.StatusOK ||
		strings.TrimSpace(response.Body.String()) != `{"revision":7,"runsStopping":2,"losesProject":true}` {
		t.Fatalf("preview = %d %s", response.Code, response.Body.String())
	}
	for _, body := range []string{`{}`, `{"permissions":[],"expectedRevision":1}`} {
		response = serveBearerJSON(h, http.MethodPost, path, []byte(body))
		if response.Code != http.StatusBadRequest && response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("preview body %s = %d %s", body, response.Code, response.Body.String())
		}
	}
}

func TestProjectListSelectsTheManageView(t *testing.T) {
	t.Parallel()
	principal := phase4Principal(uuid.New(), uuid.New())
	var readable, managed int
	service := &projectServiceStub{
		list: func(_ context.Context, _ auth.UserPrincipal, include bool) ([]company.ProjectResponse, error) {
			readable++
			if include {
				t.Fatal("include_archived was not asked for")
			}
			return []company.ProjectResponse{}, nil
		},
		manageView: func(_ context.Context, _ auth.UserPrincipal, include bool) ([]company.ProjectResponse, error) {
			managed++
			if !include {
				t.Fatal("include_archived was dropped from the manage view")
			}
			return []company.ProjectResponse{}, nil
		},
	}
	h := New(Handler{
		AuthService: authenticatedAuthStub(principal.UserID),
		CompanyService: &companyServiceStub{
			authorize: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
				return principal, nil
			},
		},
		ProjectService: service,
	})
	root := "/api/v1/companies/" + principal.CompanyID.String() + "/projects"
	if result := serveBearerJSON(h, http.MethodGet, root, nil); result.Code != 200 {
		t.Fatalf("list = %d %s", result.Code, result.Body.String())
	}
	if result := serveBearerJSON(
		h,
		http.MethodGet,
		root+"?view=manage&include_archived=true",
		nil,
	); result.Code != 200 {
		t.Fatalf("manage view = %d %s", result.Code, result.Body.String())
	}
	for _, query := range []string{"?view=all", "?view=manage&view=manage", "?include_archived=yes"} {
		result := serveBearerJSON(h, http.MethodGet, root+query, nil)
		assertErrorResponse(t, result, http.StatusUnprocessableEntity, "validation_failed")
	}
	if readable != 1 || managed != 1 {
		t.Fatalf("list calls = %d readable, %d managed", readable, managed)
	}
}

// Only the Project list takes a query string. Every other Project route
// answers any query, an unknown or a repeated one, with 422 validation_failed
// before a guard or the service runs (conventions.md), and the ready results
// list does the same.
func TestProjectRoutesRejectAnyQueryString(t *testing.T) {
	t.Parallel()
	principal := phase4Principal(uuid.New(), uuid.New())
	h := New(Handler{
		AuthService: authenticatedAuthStub(principal.UserID),
		CompanyService: &companyServiceStub{
			authorize: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
				return principal, nil
			},
			requirePermission: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
				return principal, nil
			},
		},
		// Every method is the embedded nil interface: reaching one panics.
		ProjectService:     &projectServiceStub{},
		DeliverableService: &deliverableDownloadStub{},
		RunProofResolver:   runProofResolverStub{},
	})
	root := "/api/v1/companies/" + principal.CompanyID.String()
	project := root + "/projects/" + uuid.NewString()
	grant := project + "/grants/" + uuid.NewString()
	for _, route := range []struct {
		method, path, body string
	}{
		{http.MethodPost, root + "/projects", `{"key":"research","title":"Research"}`},
		{http.MethodGet, root + "/projects/access-report", ""},
		{http.MethodGet, project, ""},
		{http.MethodPatch, project, `{"expectedRevision":1,"title":"x"}`},
		{http.MethodPost, project + "/archive", `{"expectedRevision":1}`},
		{http.MethodPut, project + "/files", `{"documentIds":[],"expectedRevision":1}`},
		{http.MethodGet, project + "/grants", ""},
		{http.MethodPut, grant, `{"permissions":["project.read"],"expectedRevision":1}`},
		{http.MethodDelete, grant, `{"expectedRevision":1}`},
		{http.MethodPost, project + "/join", `{"expectedRevision":1}`},
	} {
		for _, query := range []string{"?foo=1", "?since=2020-01-01T00:00:00Z", "?a=1&a=2", "?empty="} {
			var body []byte
			if route.body != "" {
				body = []byte(route.body)
			}
			result := serveBearerJSON(h, route.method, route.path+query, body)
			if result.Code != http.StatusUnprocessableEntity ||
				!strings.Contains(result.Body.String(), `"code":"validation_failed"`) {
				t.Errorf("%s %s%s = %d %s, want 422 validation_failed", route.method, route.path, query,
					result.Code, result.Body.String())
			}
		}
	}
}

// The client derives ProjectPermission and ProjectVisibility from the
// OpenAPI contract, so the contract's enums must be the server's catalogs.
func TestProjectPermissionContractMatchesCatalog(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(specRelativePath), "projects.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Schemas map[string]struct {
			Enum []string `yaml:"enum"`
		} `yaml:"schemas"`
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	permissions := auth.ProjectPermissionStrings(auth.ProjectPermissions())
	if got := document.Schemas["ProjectPermission"].Enum; !slices.Equal(got, permissions) {
		t.Errorf("projects.yaml ProjectPermission = %v, want auth.ProjectPermissions() %v", got, permissions)
	}
	visibilities := []string{string(auth.ProjectVisibilityCompany), string(auth.ProjectVisibilityRestricted)}
	if got := document.Schemas["ProjectVisibility"].Enum; !slices.Equal(got, visibilities) {
		t.Errorf("projects.yaml ProjectVisibility = %v, want %v", got, visibilities)
	}
}

func TestTaskProjectBindingIsRequiredAndImmutable(t *testing.T) {
	t.Parallel()
	principal := phase4Principal(uuid.New(), uuid.New())
	projectID := uuid.New()
	service := &taskServiceStub{
		list: func(_ context.Context, p auth.UserPrincipal, input task.ListOptions, _ *task.RunScope) (task.Page, error) {
			if p != principal || input.ProjectID == nil || *input.ProjectID != projectID {
				t.Fatalf("Project filter = %+v", input)
			}
			return task.Page{Tasks: []task.Response{}}, nil
		},
	}
	h := phase4TaskHandler(t, principal, service)
	root := "/api/v1/companies/" + principal.CompanyID.String() + "/tasks"
	result := serveBearerJSON(
		h,
		http.MethodPost,
		root,
		[]byte(`{"key":"no_project","milestoneType":"product","title":"Task"}`),
	)
	if result.Code != 422 {
		t.Fatalf("missing Project = %d %s", result.Code, result.Body.String())
	}
	for _, value := range []string{`null`, `"` + projectID.String() + `"`} {
		result = serveBearerJSON(
			h,
			http.MethodPatch,
			root+"/"+uuid.NewString(),
			[]byte(`{"expectedRevision":1,"projectId":`+value+`}`),
		)
		if result.Code != 400 && result.Code != 422 {
			t.Fatalf("Project mutation = %d %s", result.Code, result.Body.String())
		}
	}
	result = serveBearerJSON(h, http.MethodGet, root+"?project_id="+projectID.String(), nil)
	if result.Code != 200 {
		t.Fatalf("filter = %d %s", result.Code, result.Body.String())
	}
}
