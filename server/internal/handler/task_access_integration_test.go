//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// Every Task route answers from Project access (ADR 0017), through the real
// services and database: a member who cannot read the Task's Project, and
// an admin holding only the management bridge, get 404 on the Task by id and
// by key, on its workspace, dependencies and results, on a change and a
// start, and on a list filtered by its Project; the Company-wide lists leave
// the Task out. A reader without project.tasks.write sees the Task and is
// refused the change with 403, the only case a 403 is allowed.
func TestTaskRoutesAnswerFromProjectAccess(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	passwords, err := auth.NewPasswordHasher(auth.PasswordHasherOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := auth.NewTokenManager(auth.TokenManagerOptions{
		Secret:     []byte("task-access-synthetic-secret-32-bytes!"),
		AccessTTL:  15 * time.Minute,
		RefreshTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	authService, err := auth.NewAuthService(auth.ServiceOptions{
		Store: store, Bus: bus, Passwords: passwords, Tokens: tokens,
		SessionInvalidator: auth.SessionInvalidatorFunc(func(context.Context, []auth.SessionRevocation) {}),
	})
	if err != nil {
		t.Fatal(err)
	}
	companies, err := company.NewCompanyService(company.ServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := task.NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	deliverables, err := execution.NewDeliverableService(
		execution.DeliverableServiceOptions{Store: store, Bus: bus},
	)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(New(Handler{
		AuthService:        authService,
		CompanyService:     companies,
		TaskService:        tasks,
		DeliverableService: deliverables,
		RunProofResolver:   runProofResolverStub{},
	}))
	defer server.Close()

	signIn := func(email string, role auth.CompanyRole, grants ...string) string {
		t.Helper()
		userID := dbtest.NewUUID(t)
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`INSERT INTO users (id, email, display_name) VALUES ($1, $2, $2)`,
			userID,
			email,
		)
		dbtest.MustExec(t, ctx, pool,
			`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, $3)`,
			fixture.CompanyA, userID, string(role))
		if len(grants) > 0 {
			dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, userID, grants...)
		}
		var sessionID uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO auth_sessions (user_id, token_prefix, token_hash, expires_at)
			VALUES ($1, 'test', gen_random_bytes(32), now() + interval '1 hour') RETURNING id`, userID,
		).Scan(&sessionID); err != nil {
			t.Fatal(err)
		}
		token, _, err := tokens.IssueAccess(userID, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	call := func(token, method, path, body string) (int, string, string) {
		t.Helper()
		url := server.URL + "/api/v1/companies/" + fixture.CompanyA.String() + path
		request, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(raw, &envelope)
		return response.StatusCode, envelope.Code, string(raw)
	}

	taskPath := "/tasks/" + fixture.TaskA.String()
	change := `{"expectedRevision":1,"title":"Renamed"}`
	start := `{"expectedRevision":1}`
	create := `{"projectId":"` + fixture.ProjectA.String() + `","milestoneType":"product","title":"Hidden"}`
	type expect struct {
		status int
		code   string
	}
	notFound := expect{http.StatusNotFound, "task_not_found"}
	routes := []struct {
		method, path, body string
	}{
		{http.MethodGet, taskPath, ""},
		{http.MethodGet, "/tasks/task_a", ""},
		{http.MethodGet, taskPath + "/workspace", ""},
		{http.MethodGet, taskPath + "/dependencies", ""},
		{http.MethodGet, taskPath + "/results", ""},
		{http.MethodPatch, taskPath, change},
		{http.MethodPost, taskPath + "/runs", start},
		{http.MethodGet, "/tasks?project_id=" + fixture.ProjectA.String(), ""},
		// A create names the Project, so a Project the caller cannot read
		// answers as the filtered list does.
		{http.MethodPost, "/tasks", create},
	}
	for _, scenario := range []struct {
		name  string
		token string
		want  map[string]expect
	}{
		{name: "member without a grant", token: signIn("outsider@example.com", auth.CompanyRoleMember)},
		{name: "admin holding only the bridge", token: signIn("admin@example.com", auth.CompanyRoleAdmin)},
		{
			name:  "reader without tasks.write",
			token: signIn("reader@example.com", auth.CompanyRoleMember, "project.read"),
			want: map[string]expect{
				"GET " + taskPath:                   {status: http.StatusOK},
				"GET /tasks/task_a":                 {status: http.StatusOK},
				"GET " + taskPath + "/workspace":    {status: http.StatusOK},
				"GET " + taskPath + "/dependencies": {status: http.StatusOK},
				"GET " + taskPath + "/results":      {status: http.StatusOK},
				"PATCH " + taskPath:                 {http.StatusForbidden, "project_permission_required"},
				// Only the assignee starts a Task; the reader is not it.
				"POST " + taskPath + "/runs":                         {http.StatusForbidden, "task_assignee_required"},
				"GET /tasks?project_id=" + fixture.ProjectA.String(): {status: http.StatusOK},
				"POST /tasks": {http.StatusForbidden, "project_permission_required"},
			},
		},
	} {
		for _, route := range routes {
			key := route.method + " " + route.path
			want, listed := scenario.want[key]
			if !listed {
				want = notFound
				switch {
				case strings.HasPrefix(route.path, "/tasks?"), key == "POST /tasks":
					want = expect{http.StatusNotFound, "project_not_found"}
				case strings.HasSuffix(route.path, "/results"):
					// The results route keeps its own not-found code, the
					// same one a missing Task gets.
					want = expect{http.StatusNotFound, "not_found"}
				}
			}
			status, code, body := call(scenario.token, route.method, route.path, route.body)
			if status != want.status || (want.code != "" && code != want.code) {
				t.Errorf(
					"%s: %s = %d %s, want %d %s",
					scenario.name,
					key,
					status,
					body,
					want.status,
					want.code,
				)
			}
		}
		// The Company-wide reads leave the hidden Task out instead of
		// refusing the list.
		if len(scenario.want) == 0 {
			for _, path := range []string{"/tasks", "/task-dependencies"} {
				status, _, body := call(scenario.token, http.MethodGet, path, "")
				if status != http.StatusOK || strings.Contains(body, fixture.TaskA.String()) {
					t.Errorf(
						"%s: GET %s = %d %s; want 200 without the hidden Task",
						scenario.name,
						path,
						status,
						body,
					)
				}
			}
		}
	}
}
