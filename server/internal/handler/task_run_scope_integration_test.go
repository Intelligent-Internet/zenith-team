//go:build integration

package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// The reads a Run sends through its channel carry its proof beside its
// member's session, through the real services and database (ADR 0017): its
// own Task in a Project the member reads answers 200 and so does that
// Project; `zen task create` without the member's project.tasks.write is 403;
// after the member loses project.read the Task and the Project are 404, and
// a Project the member only manages through projects.manage is 404 to the Run
// though not to the member; and the same proof carried by another member's
// session is 401 run_token_invalid before anything is read.
func TestRunProofReadsFollowTheAuthorizersProjectAccess(t *testing.T) {
	t.Parallel()
	h := newRunScopeHarness(t)
	ctx, fixture, pool := h.ctx, h.fixture, h.pool
	owner := h.session(fixture.UserA)
	colleagueID := h.member("colleague@example.com")
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, colleagueID)
	colleague := h.session(colleagueID)

	// The owner's Task Run, live, with its proof.
	proof := h.liveRun(fixture.AgentA, fixture.ConversationA, &fixture.TaskA, nil, "task")

	expect := func(name string, status int, code string, gotStatus int, gotCode string) {
		t.Helper()
		if gotStatus != status || gotCode != code {
			t.Errorf("%s = %d %q, want %d %q", name, gotStatus, gotCode, status, code)
		}
	}
	taskPath := "/tasks/" + fixture.TaskA.String()
	projectPath := "/projects/" + fixture.ProjectA.String()
	create := `{"projectId":"` + fixture.ProjectA.String() + `","milestoneType":"product","title":"Follow up"}`

	status, code, _ := h.call(owner, proof, http.MethodGet, taskPath, "")
	expect("own Task", http.StatusOK, "", status, code)
	status, code, _ = h.call(owner, proof, http.MethodGet, projectPath, "")
	expect("own Task's Project", http.StatusOK, "", status, code)
	status, code, _ = h.call(owner, proof, http.MethodGet, "/projects", "")
	expect("Project list", http.StatusOK, "", status, code)

	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, taskPath, ""},
		{http.MethodGet, "/tasks", ""},
		{http.MethodGet, projectPath, ""},
		{http.MethodPost, "/tasks", create},
	} {
		status, code, _ = h.call(colleague, proof, route.method, route.path, route.body)
		expect("another member's session with the proof: "+route.method+" "+route.path,
			http.StatusUnauthorized, "run_token_invalid", status, code)
	}

	dbtest.MustExec(t, ctx, pool, `
		DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2 AND permission = 'project.tasks.write'`,
		fixture.ProjectA, fixture.UserA)
	status, code, _ = h.call(owner, proof, http.MethodPost, "/tasks", create)
	expect("create without project.tasks.write", http.StatusForbidden, "project_permission_required", status, code)

	dbtest.MustExec(t, ctx, pool, `DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2`,
		fixture.ProjectA, fixture.UserA)
	status, code, _ = h.call(owner, proof, http.MethodGet, taskPath, "")
	expect("own Task after losing project.read", http.StatusNotFound, "task_not_found", status, code)
	status, code, _ = h.call(owner, proof, http.MethodGet, projectPath, "")
	expect("the Project after losing project.read, through a Run", http.StatusNotFound, "project_not_found",
		status, code)
	status, code, _ = h.call(owner, "", http.MethodGet, projectPath, "")
	expect("the Project after losing project.read, as its manager", http.StatusOK, "", status, code)
}

// The server alone keeps the Company Plan export to the root Agent's direct
// chat. The root Agent's Run there, with its Company Plan
// tools on, lists every Task its person reads, a colleague's Task included,
// and reads that Task and its dependencies; a Task in a Project the person
// cannot read stays out. With planning switched off, in a Task Run, tagged in
// a channel, or as another Agent, the same person's Run lists only its own
// Tasks and is refused the colleague's Task.
func TestPlannerProofReadsEveryTaskItsPersonReads(t *testing.T) {
	t.Parallel()
	h := newRunScopeHarness(t)
	ctx, fixture, pool := h.ctx, h.fixture, h.pool
	owner := h.session(fixture.UserA)
	colleagueID := h.member("planner-colleague@example.com")
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, colleagueID)

	// A non-root Agent the owner runs, with one Task of its own.
	helperID := dbtest.NewUUID(t)
	dbtest.SeedBoundAgent(t, ctx, pool, helperID, fixture.CompanyA, "Helper", "codex", &fixture.AgentA,
		fixture.UserA, fixture.RuntimeA)
	helpersTask := h.task(fixture.ProjectA, fixture.UserA, &helperID, 2, "helper_task")
	// A colleague's own Task in a Project the owner reads, and one in a
	// Project the owner only manages (projects.manage, no project.read).
	colleaguesTask := h.task(fixture.ProjectA, colleagueID, nil, 3, "colleague_task")
	hiddenProject := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO projects (id, company_id, key, title) VALUES ($1, $2, 'hidden', 'Hidden')`,
		hiddenProject, fixture.CompanyA)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, hiddenProject, colleagueID)
	hiddenTask := h.task(hiddenProject, colleagueID, nil, 4, "hidden_task")
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO task_dependencies (project_id, company_id, dependent_task_id, prerequisite_task_id, created_by_user_id)
		VALUES ($1, $2, $3, $4, $5)`,
		fixture.ProjectA, fixture.CompanyA, colleaguesTask, fixture.TaskA, colleagueID)

	var helperChat uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO conversations (company_id, kind, agent_id, created_by_user_id, title)
		VALUES ($1, 'direct', $2, $3, 'Helper') RETURNING id`, fixture.CompanyA, helperID, fixture.UserA,
	).Scan(&helperChat); err != nil {
		t.Fatal(err)
	}
	planner := h.liveRun(fixture.AgentA, fixture.DirectConversationA, nil, nil, "direct_chat")
	taskRun := h.liveRun(fixture.AgentA, fixture.ConversationA, &fixture.TaskA, nil, "task")
	tagged := h.taggedRun()
	helper := h.liveRun(helperID, helperChat, nil, nil, "direct_chat")

	everyTask, status := h.listTasks(owner, "")
	if status != http.StatusOK || !sameTasks(everyTask, fixture.TaskA, helpersTask, colleaguesTask) {
		t.Fatalf(
			"the owner's own list = %d %v, want the three Tasks in the Project they read",
			status,
			everyTask,
		)
	}
	listed, status := h.listTasks(owner, planner)
	if status != http.StatusOK || !sameTasks(listed, everyTask...) {
		t.Fatalf("the Planner's list = %d %v, want every Task its person reads %v", status, listed, everyTask)
	}
	if slices.Contains(listed, hiddenTask) {
		t.Fatal("the Planner listed a Task in a Project its person cannot read")
	}
	colleaguePath := "/tasks/" + colleaguesTask.String()
	if status, code, _ := h.call(owner, planner, http.MethodGet, colleaguePath, ""); status != http.StatusOK {
		t.Fatalf("the Planner's read of a colleague's Task = %d %q, want 200", status, code)
	}
	dependentsPath := "/tasks/" + fixture.TaskA.String() + "/dependencies"
	status, code, body := h.call(owner, planner, http.MethodGet, dependentsPath, "")
	if status != http.StatusOK || !strings.Contains(body, colleaguesTask.String()) {
		t.Fatalf("the Planner's dependencies = %d %q, want the colleague's dependent: %s", status, code, body)
	}

	ownOnly := func(name, proof string, own ...uuid.UUID) {
		t.Helper()
		listed, status := h.listTasks(owner, proof)
		if status != http.StatusOK || !sameTasks(listed, own...) {
			t.Errorf("%s: list = %d %v, want only its own %v", name, status, listed, own)
		}
		if status, code, _ := h.call(
			owner, proof, http.MethodGet, colleaguePath, "",
		); status != http.StatusForbidden || code != "task_outside_run_scope" {
			t.Errorf(
				"%s: read of a colleague's Task = %d %q, want 403 task_outside_run_scope",
				name,
				status,
				code,
			)
		}
	}
	ownOnly("a Task Run", taskRun, fixture.TaskA)
	ownOnly("a Run tagged in a channel", tagged, fixture.TaskA)
	ownOnly("another Agent in its direct chat", helper, helpersTask)
	status, code, body = h.call(owner, taskRun, http.MethodGet, dependentsPath, "")
	if status != http.StatusOK || strings.Contains(body, colleaguesTask.String()) {
		t.Errorf("a Task Run's dependencies = %d %q, want no colleague's dependent: %s", status, code, body)
	}

	// The switch is read with every proof: turned off mid-Run, the next
	// read is narrowed at once.
	dbtest.MustExec(t, ctx, pool, `
		UPDATE agents SET settings = settings || '{"capabilities":{"disabledBuiltIns":["planning"]}}'::jsonb
		WHERE id = $1`, fixture.AgentA)
	ownOnly("the root Agent with planning switched off", planner, fixture.TaskA)
}

// The daemon's own session reads Tasks only as a live
// Run of its person, through the real services and database. With no proof
// it is 403 run_proof_required, never the person; with its person's live
// proof it reads exactly what that Run reads beside the person's session;
// another person's proof, or the proof of a Run that ended, is refused the
// same way.
func TestDaemonSessionReadsTasksOnlyAsALiveRunOfItsPerson(t *testing.T) {
	t.Parallel()
	h := newRunScopeHarness(t)
	ctx, fixture, pool := h.ctx, h.fixture, h.pool
	owner := h.session(fixture.UserA)
	daemon := h.daemonSession(fixture.UserA)
	colleagueID := h.member("daemon-colleague@example.com")
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, colleagueID)
	colleaguesDaemon := h.daemonSession(colleagueID)
	colleaguesTask := h.task(fixture.ProjectA, colleagueID, nil, 2, "daemon_colleague_task")
	taskRun := h.liveRun(fixture.AgentA, fixture.ConversationA, &fixture.TaskA, nil, "task")

	refused := func(name, bearer, proof, path string) {
		t.Helper()
		if status, code, _ := h.call(bearer, proof, http.MethodGet, path, ""); status != http.StatusForbidden ||
			code != "run_proof_required" {
			t.Errorf("%s = %d %q, want 403 run_proof_required", name, status, code)
		}
	}
	refused("the daemon's session with no proof", daemon, "", "/tasks")
	refused("the daemon's session with no proof on one Task", daemon, "", "/tasks/"+fixture.TaskA.String())

	viaPerson, status := h.listTasks(owner, taskRun)
	if status != http.StatusOK || !sameTasks(viaPerson, fixture.TaskA) {
		t.Fatalf("the Run's list beside its person's session = %d %v, want its own Task", status, viaPerson)
	}
	viaDaemon, status := h.listTasks(daemon, taskRun)
	if status != http.StatusOK || !sameTasks(viaDaemon, viaPerson...) {
		t.Fatalf("the Run's list beside the daemon's session = %d %v, want %v", status, viaDaemon, viaPerson)
	}
	status, code, _ := h.call(daemon, taskRun, http.MethodGet, "/tasks/"+colleaguesTask.String(), "")
	if status != http.StatusForbidden || code != "task_outside_run_scope" {
		t.Errorf("the daemon's Run reading a colleague's Task = %d %q, want 403 task_outside_run_scope",
			status, code)
	}

	refused("another person's daemon session with the proof", colleaguesDaemon, taskRun, "/tasks")
	digest := sha256.Sum256([]byte(taskRun))
	dbtest.MustExec(t, ctx, pool, `DELETE FROM run_tokens WHERE token_hash = $1`, digest[:])
	refused("the daemon's session with an ended Run's proof", daemon, taskRun, "/tasks")
	listed, status := h.listTasks(owner, "")
	if status != http.StatusOK || !sameTasks(listed, fixture.TaskA, colleaguesTask) {
		t.Fatalf("the person's own list = %d %v, want every Task they read", status, listed)
	}
}

// runScopeHarness serves the real Task, Project and Run services over the
// test database, with the handler that turns a Run proof into a scope.
type runScopeHarness struct {
	t       *testing.T
	ctx     context.Context
	pool    *pgxpool.Pool
	store   *db.Store
	tokens  *auth.TokenManager
	server  *httptest.Server
	fixture dbtest.Fixture
	proofs  int
}

func newRunScopeHarness(t *testing.T) *runScopeHarness {
	t.Helper()
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
		Secret:     []byte("run-scope-synthetic-secret-32-bytes!!!"),
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
	projects, err := company.NewProjectService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := task.NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	runs := execution.NewRunService(
		execution.RunServiceOptions{Queries: store.Queries(), Transactor: store, Bus: bus},
	)
	server := httptest.NewServer(New(Handler{
		AuthService:      authService,
		CompanyService:   companies,
		ProjectService:   projects,
		TaskService:      tasks,
		RunProofResolver: runs,
	}))
	t.Cleanup(server.Close)
	return &runScopeHarness{
		t: t, ctx: ctx, pool: pool, store: store, tokens: tokens, server: server, fixture: fixture,
	}
}

// session signs a person in and returns their access token.
func (h *runScopeHarness) session(userID uuid.UUID) string {
	h.t.Helper()
	var sessionID uuid.UUID
	if err := h.pool.QueryRow(h.ctx, `
		INSERT INTO auth_sessions (user_id, token_prefix, token_hash, expires_at)
		VALUES ($1, 'test', gen_random_bytes(32), now() + interval '1 hour') RETURNING id`, userID,
	).Scan(&sessionID); err != nil {
		h.t.Fatal(err)
	}
	token, _, err := h.tokens.IssueAccess(userID, sessionID)
	if err != nil {
		h.t.Fatal(err)
	}
	return token
}

// daemonSession starts a person's daemon session and returns its
// access token.
func (h *runScopeHarness) daemonSession(userID uuid.UUID) string {
	h.t.Helper()
	var sessionID uuid.UUID
	if err := h.pool.QueryRow(h.ctx, `
		INSERT INTO auth_sessions (user_id, token_prefix, token_hash, expires_at, client)
		VALUES ($1, 'daemon', gen_random_bytes(32), now() + interval '1 hour', 'daemon') RETURNING id`, userID,
	).Scan(&sessionID); err != nil {
		h.t.Fatal(err)
	}
	token, _, err := h.tokens.IssueAccess(userID, sessionID)
	if err != nil {
		h.t.Fatal(err)
	}
	return token
}

// member adds a person to the fixture's Company as a member.
func (h *runScopeHarness) member(email string) uuid.UUID {
	h.t.Helper()
	id := dbtest.NewUUID(h.t)
	dbtest.MustExec(h.t, h.ctx, h.pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Colleague')`, id, email)
	dbtest.MustExec(h.t, h.ctx, h.pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		h.fixture.CompanyA, id)
	return id
}

// task seeds a Task and its Task chat in a Project of the fixture's
// Company: done by agentID for its assignee, or by the assignee themself
// when agentID is nil.
func (h *runScopeHarness) task(projectID, assignee uuid.UUID, agentID *uuid.UUID, number int, key string) uuid.UUID {
	h.t.Helper()
	taskID, chatID := dbtest.NewUUID(h.t), dbtest.NewUUID(h.t)
	dbtest.WithinSeedTx(h.t, h.ctx, h.pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(h.t, h.ctx, tx, `
			INSERT INTO conversations (id, company_id, kind, task_id, project_id, title)
			VALUES ($1, $2, 'task', $3, $4, $5)`, chatID, h.fixture.CompanyA, taskID, projectID, key)
		dbtest.MustExecTx(h.t, h.ctx, tx, `
			INSERT INTO tasks (id, company_id, conversation_id, project_id, milestone_type, number, key, title,
				assignee_user_id, executed_by_agent_id, creator_user_id)
			VALUES ($1, $2, $3, $4, 'product', $5, $6, $6, $7, $8, $7)`,
			taskID, h.fixture.CompanyA, chatID, projectID, number, key, assignee, agentID)
	})
	return taskID
}

// liveRun starts a running Run of agentID for the fixture's owner in the
// given chat and returns its proof.
func (h *runScopeHarness) liveRun(
	agentID, conversationID uuid.UUID, taskID, invocationID *uuid.UUID, runType string,
) string {
	h.t.Helper()
	h.proofs++
	proof := fmt.Sprintf("brt_%064x", h.proofs)
	var runID uuid.UUID
	if err := h.pool.QueryRow(h.ctx, `
		INSERT INTO runs (company_id, agent_id, task_id, conversation_id, runtime_id,
			execution_authorized_by_user_id, status, run_type, invocation_id)
		VALUES ($1, $2, $3, $4, $5, $6, 'running', $7, $8) RETURNING id`,
		h.fixture.CompanyA, agentID, taskID, conversationID, h.fixture.RuntimeA, h.fixture.UserA,
		runType, invocationID,
	).Scan(&runID); err != nil {
		h.t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(proof))
	if _, err := h.store.Queries().CreateRunToken(h.ctx, generated.CreateRunTokenParams{
		RunID:     runID,
		TokenHash: digest[:],
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		h.t.Fatal(err)
	}
	return proof
}

// taggedRun starts the root Agent's Run for the fixture's owner, tagged in a
// channel the owner created, and returns its proof.
func (h *runScopeHarness) taggedRun() string {
	h.t.Helper()
	f := h.fixture
	channelID := dbtest.SeedDiscussionChannel(h.t, h.ctx, h.pool, f.CompanyA, f.UserA, []uuid.UUID{f.AgentA})
	var messageID uuid.UUID
	if err := h.pool.QueryRow(h.ctx, `
		INSERT INTO conversation_items
			(company_id, conversation_id, seq, type, author_kind, author_id,
			 content, body, request_key, send_fingerprint)
		VALUES ($1, $2, 1, 'user_message', 'user', $3, '@Agent list the work',
			'{"markdown":"@Agent list the work"}'::jsonb, gen_random_uuid(),
			decode(repeat('11', 32), 'hex'))
		RETURNING id`, f.CompanyA, channelID, f.UserA).Scan(&messageID); err != nil {
		h.t.Fatal(err)
	}
	chat, err := h.store.Queries().EnsureChannelAgentConversation(
		h.ctx, generated.EnsureChannelAgentConversationParams{
			ChannelID: channelID, AgentID: f.AgentA, CompanyID: f.CompanyA,
		},
	)
	if err != nil {
		h.t.Fatal(err)
	}
	invocation, err := h.store.Queries().InsertChannelInvocation(h.ctx, generated.InsertChannelInvocationParams{
		ConversationID: chat.ID, AgentID: f.AgentA, CallerUserID: f.UserA,
		MessageID: messageID, ChannelID: channelID, CompanyID: f.CompanyA,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	dbtest.MustExec(h.t, h.ctx, h.pool, `UPDATE channel_invocations SET status = 'active' WHERE id = $1`,
		invocation.ID)
	return h.liveRun(f.AgentA, chat.ID, nil, &invocation.ID, "channel_agent")
}

// call sends one request to the fixture's Company with the bearer's session
// and, when proof is set, the Run's proof beside it.
func (h *runScopeHarness) call(bearer, proof, method, path, body string) (status int, code, raw string) {
	h.t.Helper()
	url := h.server.URL + "/api/v1/companies/" + h.fixture.CompanyA.String() + path
	request, err := http.NewRequestWithContext(h.ctx, method, url, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if proof != "" {
		request.Header.Set(runTokenHeader, proof)
	}
	response, err := h.server.Client().Do(request)
	if err != nil {
		h.t.Fatal(err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	var envelope struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(content, &envelope)
	return response.StatusCode, envelope.Code, string(content)
}

// listTasks lists the Company's Tasks and returns their ids.
func (h *runScopeHarness) listTasks(bearer, proof string) ([]uuid.UUID, int) {
	h.t.Helper()
	status, code, raw := h.call(bearer, proof, http.MethodGet, "/tasks", "")
	if status != http.StatusOK {
		h.t.Logf("GET /tasks = %d %q", status, code)
		return nil, status
	}
	var page struct {
		Items []struct {
			ID uuid.UUID `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		h.t.Fatal(err)
	}
	ids := make([]uuid.UUID, len(page.Items))
	for i, item := range page.Items {
		ids[i] = item.ID
	}
	return ids, status
}

// sameTasks reports whether got holds exactly want, in any order.
func sameTasks(got []uuid.UUID, want ...uuid.UUID) bool {
	return len(got) == len(want) && !slices.ContainsFunc(want, func(id uuid.UUID) bool {
		return !slices.Contains(got, id)
	})
}
