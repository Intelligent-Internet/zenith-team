//go:build integration

package company

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

// The access report (ADR 0017) lists, per restricted Project that is not
// archived, the active members who cannot read it but hold one of its open
// Tasks or authorized a Run in its Task chats in the last 30 days. Readers,
// Company-visible and archived Projects, closed Tasks, old Runs and
// disabled accounts stay out, and only projects.manage holders may ask.
func TestProjectAccessReportListsPeopleOutsideTheirWork(t *testing.T) {
	t.Parallel()
	ctx, w := newProjectAccessWorld(t)
	pool, f := w.pool, w.f

	person := func(name string) uuid.UUID {
		id := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool, `INSERT INTO users (id, email, display_name) VALUES ($1, $2, $2)`,
			id, name+"-"+id.String()+"@report.test")
		dbtest.MustExec(t, ctx, pool, `INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
			f.CompanyA, id)
		return id
	}
	veteran, disabled, closer, stalled := person("veteran"), person("disabled"), person("closer"), person("stalled")
	dbtest.MustExec(t, ctx, pool, `UPDATE users SET disabled_at = now() WHERE id = $1`, disabled)

	// A Task in its own Project, in the given state, and its chat.
	seedProjectTask := func(key, visibility string, archived bool, assignee uuid.UUID, status string, number int) (uuid.UUID, uuid.UUID) {
		projectID, taskID, conversationID := dbtest.NewUUID(t), dbtest.NewUUID(t), dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool, `INSERT INTO projects (id, company_id, key, title, visibility, archived_at)
			VALUES ($1, $2, $3, $3, $4, CASE WHEN $5 THEN now() END)`, projectID, f.CompanyA, key, visibility, archived)
		dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
			dbtest.MustExecTx(t, ctx, tx, `INSERT INTO conversations (id, company_id, kind, task_id, title, project_id)
				VALUES ($1, $2, 'task', $3, $4, $5)`, conversationID, f.CompanyA, taskID, key, projectID)
			dbtest.MustExecTx(t, ctx, tx, `INSERT INTO tasks
				(id, company_id, conversation_id, project_id, milestone_type, number, key, title,
				 assignee_user_id, executed_by_agent_id, creator_user_id, status)
				VALUES ($1, $2, $3, $4, 'product', $5, $6, $6, $7, $8, $7, $9)`,
				taskID, f.CompanyA, conversationID, projectID, number, key, assignee, f.AgentA, status)
		})
		return projectID, conversationID
	}
	run := func(conversationID, authorizer uuid.UUID, age time.Duration) {
		dbtest.MustExec(t, ctx, pool, `INSERT INTO runs
			(company_id, agent_id, conversation_id, runtime_id, execution_authorized_by_user_id, status, run_type,
			 created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, 'completed', 'task', now() - make_interval(secs => $6::double precision),
			 now() - make_interval(secs => $6::double precision))`,
			f.CompanyA, f.AgentA, conversationID, w.memberRuntime, authorizer, age.Seconds())
	}

	// Project A (restricted, the owner reads it): the member holds its Task
	// and ran work there; the admin ran work there; the owner is a reader.
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET assignee_user_id = $1 WHERE id = $2`, w.member.UserID, f.TaskA)
	run(f.ConversationA, w.member.UserID, time.Hour)
	run(f.ConversationA, w.admin.UserID, 24*time.Hour)
	run(f.ConversationA, f.UserA, time.Hour)
	run(f.ConversationA, veteran, 40*24*time.Hour)
	run(f.ConversationA, disabled, time.Hour)
	// A Run queued 40 days ago that the upgrade cancelled an hour ago still
	// counts: it ended inside the window.
	dbtest.MustExec(t, ctx, pool, `INSERT INTO runs
		(company_id, agent_id, conversation_id, runtime_id, execution_authorized_by_user_id, status, error,
		 run_type, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'cancelled', 'project_access_revoked', 'task',
		 now() - interval '40 days', now() - interval '1 hour')`,
		f.CompanyA, f.AgentA, f.ConversationA, w.memberRuntime, stalled)
	// A restricted Project whose only Task is done: the closer is out of it,
	// but has no open work there.
	_, closedChat := seedProjectTask("closed", "restricted", false, closer, "done", 10)
	run(closedChat, veteran, time.Hour)
	// A Company-visible Project and an archived one list nobody.
	_, openChat := seedProjectTask("open", "company", false, closer, "todo", 11)
	run(openChat, w.admin.UserID, time.Hour)
	_, archivedChat := seedProjectTask("shelved", "restricted", true, closer, "todo", 12)
	run(archivedChat, w.admin.UserID, time.Hour)

	type listed struct {
		key     string
		userID  uuid.UUID
		reasons []ProjectAccessReportReason
	}
	flatten := func(report ProjectAccessReport) []listed {
		var out []listed
		for _, entry := range report.Items {
			for _, p := range entry.People {
				out = append(out, listed{entry.Key, p.UserID, p.Reasons})
			}
		}
		return out
	}
	want := []listed{
		{"closed", veteran, []ProjectAccessReportReason{ProjectAccessReportReasonRunAuthorizer}},
		{"work", w.admin.UserID, []ProjectAccessReportReason{ProjectAccessReportReasonRunAuthorizer}},
		{"work", w.member.UserID, []ProjectAccessReportReason{ProjectAccessReportReasonRunAuthorizer, ProjectAccessReportReasonTaskAssignee}},
		{"work", stalled, []ProjectAccessReportReason{ProjectAccessReportReasonRunAuthorizer}},
	}
	slices.SortFunc(want[1:], func(a, b listed) int { return slices.Compare(a.userID[:], b.userID[:]) })
	assertReport := func(principal auth.UserPrincipal, want []listed) {
		t.Helper()
		report, err := w.projects.AccessReport(ctx, principal)
		if err != nil {
			t.Fatalf("AccessReport(%s) error = %v", principal.Role, err)
		}
		if time.Since(report.Since) < 30*24*time.Hour-time.Minute || report.Items == nil {
			t.Errorf("report since = %v, items = %v", report.Since, report.Items)
		}
		got := flatten(report)
		if !slices.EqualFunc(got, want, func(a, b listed) bool {
			return a.key == b.key && a.userID == b.userID && slices.Equal(a.reasons, b.reasons)
		}) {
			t.Errorf("report = %+v, want %+v", got, want)
		}
	}
	// The admin reads no Project and still gets the whole report: it is
	// management, and names no Task or chat.
	assertReport(w.admin, want)
	assertReport(w.owner, want)
	if _, err := w.projects.AccessReport(ctx, w.member); !errors.Is(err, ErrProjectForbidden) {
		t.Fatalf("member AccessReport error = %v, want ErrProjectForbidden", err)
	}

	// Granting the member read takes them off the report.
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT access_version FROM projects WHERE id = $1`, f.ProjectA).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if _, err := w.projects.SetGrants(ctx, w.owner, f.ProjectA, ProjectGrantInput{
		UserID: w.member.UserID, Permissions: []string{string(auth.ProjectPermissionRead)}, ExpectedRevision: revision,
	}); err != nil {
		t.Fatalf("grant read: %v", err)
	}
	assertReport(w.owner, slices.DeleteFunc(slices.Clone(want), func(l listed) bool { return l.userID == w.member.UserID }))
}
