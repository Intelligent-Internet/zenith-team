//go:build integration

package activity_test

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// Who reads each Activity row (ADR 0017): a Company-wide row about a
// Project reads for its readers, a project_grant row also for the members
// whose role holds projects.manage, a row addressed to one person reads for
// them only while they read its Project (the actor of a grant change who
// later loses the Project loses the row too), and a Company-wide row about
// Task work that names no Project reads for nobody. The calendar counts the
// same rows.
func TestActivityFeedFollowsProjectAccess(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)
	newMember := func(name, role string) uuid.UUID {
		id := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool, `INSERT INTO users (id, email, display_name) VALUES ($1, $2, $3)`,
			id, name+"@example.com", name)
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, $3)`,
			fixture.CompanyA,
			id,
			role,
		)
		return id
	}
	member := newMember("member", "member")
	admin := newMember("admin", "admin")
	reader := newMember("reader", "member")
	former := newMember("former", "member")
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, reader, "project.read")

	projectA := uuid.NullUUID{UUID: fixture.ProjectA, Valid: true}
	insert := func(action, entityType string, entityID uuid.UUID, visibility string, addressee uuid.UUID,
		project uuid.NullUUID,
	) string {
		t.Helper()
		row, err := queries.CreateActivity(ctx, generated.CreateActivityParams{
			CompanyID: fixture.CompanyA, ActorType: "system", Action: action,
			EntityType: entityType, EntityID: entityID.String(), Details: []byte(`{}`),
			Visibility:       pgtype.Text{String: visibility, Valid: visibility != ""},
			VisibilityUserID: uuid.NullUUID{UUID: addressee, Valid: addressee != uuid.Nil},
			ProjectID:        project,
		})
		if err != nil {
			t.Fatalf("CreateActivity(%s) error = %v", action, err)
		}
		return row.ID.String()
	}
	taskRow := insert("task.updated", "task", fixture.TaskA, "company", uuid.Nil, projectA)
	insert("task.updated", "task", fixture.TaskA, "company", uuid.Nil, uuid.NullUUID{})
	insert("attachment.deleted", "attachment", uuid.New(), "company", uuid.Nil, uuid.NullUUID{})
	agentRow := insert("agent.updated", "agent", fixture.AgentA, "company", uuid.Nil, uuid.NullUUID{})
	grantRow := insert("project_grant.changed", "project_grant", fixture.ProjectA, "user", fixture.UserA, projectA)
	// Rows addressed to one person: about no Project, it stays theirs; about
	// a Project, only while they read it.
	insert("run.failed", "run", fixture.RunA, "user", member, projectA)
	memberDirectRow := insert("conversation.read", "conversation", fixture.DirectConversationA, "user", member,
		uuid.NullUUID{})
	readerRow := insert("run.failed", "run", fixture.RunA, "user", reader, projectA)
	// A grant change by someone who has since lost the Project.
	formerGrantRow := insert("project_grant.changed", "project_grant", fixture.ProjectA, "user", former, projectA)

	feed := activity.NewFeed(queries)
	now := time.Now().UTC()
	for name, want := range map[string]struct {
		user uuid.UUID
		rows []string
	}{
		"owner (grant addressee and reader)": {fixture.UserA, []string{taskRow, agentRow, grantRow, formerGrantRow}},
		"reader":                             {reader, []string{taskRow, agentRow, grantRow, formerGrantRow, readerRow}},
		"admin (projects.manage, no grant)":  {admin, []string{agentRow, grantRow, formerGrantRow}},
		"member (no grant)":                  {member, []string{agentRow, memberDirectRow}},
		"former grant actor (no grant)":      {former, []string{agentRow}},
	} {
		entries, _, err := feed.List(ctx, fixture.CompanyA, activity.ListOptions{UserID: want.user, Limit: 50})
		if err != nil {
			t.Fatalf("%s: List() error = %v", name, err)
		}
		got := make([]string, 0, len(entries))
		for _, entry := range entries {
			got = append(got, entry.ID)
		}
		slices.Sort(got)
		slices.Sort(want.rows)
		if !slices.Equal(got, want.rows) {
			t.Errorf("%s reads %v, want %v", name, got, want.rows)
		}
		calendar, err := feed.Calendar(ctx, fixture.CompanyA, activity.CalendarOptions{
			UserID: want.user, Month: now.Format("2006-01"), TimeZone: "UTC", Location: time.UTC,
			StartAt: now.Add(-time.Hour), EndAt: now.Add(time.Hour),
		})
		if err != nil || calendar.TotalCount != int64(len(want.rows)) {
			t.Errorf("%s calendar = %d, %v; want %d", name, calendar.TotalCount, err, len(want.rows))
		}
	}
}

// taskScopedWithoutProject lists the rows the read predicate hides because
// they are about Task work and name no Project.
const taskScopedWithoutProject = `
	SELECT action || ' ' || entity_type || ' ' || entity_id
	FROM activity_events
	WHERE company_id = $1 AND project_id IS NULL
	  AND (task_id IS NOT NULL
	       OR (visibility = 'company'
	           AND entity_type IN ('task', 'conversation', 'run', 'message', 'attachment', 'approval', 'question')))
	ORDER BY created_at, id`

// Every Activity row written from now on about a Task, its chat, a Run, a
// message, a file, an Approval or a Question names the Project, whichever
// recorder writes it: the owner snapshots (including a deletion, whose only
// observation is the one before it), the bus recorder (from the chat or Task
// on the event envelope, or from its subject) and domain receipts. The read
// predicate hides such a row without a Project, so none may be written.
func TestActivityRowsAboutTaskWorkNameTheirProject(t *testing.T) {
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
	bus := historyTestBus(t)
	if err := activity.RegisterListeners(bus, store.Queries()); err != nil {
		t.Fatal(err)
	}
	tasks, err := task.NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture seeds Task number 1 without advancing the counter.
	dbtest.MustExec(t, ctx, pool, `UPDATE companies SET next_task_number = 2 WHERE id = $1`, fixture.CompanyA)
	owner := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	created, err := tasks.Create(ctx, owner, task.CreateInput{
		ProjectID: fixture.ProjectA, Key: "activity_project", Title: "Activity names its Project",
		MilestoneType: task.MilestoneTypeProduct, Status: task.StatusTodo, Priority: task.PriorityMedium,
		Assignee: task.ActorRef{Kind: task.ActorUser, ID: fixture.UserA},
	})
	if err != nil {
		t.Fatal(err)
	}
	title := "Renamed"
	if _, err := tasks.Update(ctx, owner, created.Task.ID, task.UpdateInput{
		ExpectedRevision: created.Task.Revision, Title: &title,
	}); err != nil {
		t.Fatal(err)
	}

	// Bus evidence: a file deletion names no file a chat still holds, only
	// the chat on its envelope; a message and a Run receipt name their
	// subject alone.
	publish := func(topic, action string, subject, conversationID, taskID uuid.UUID) {
		bus.Publish(ctx, topic, events.Event{
			EntityType: topic, Name: action, CompanyID: fixture.CompanyA, CreatedAt: time.Now().UTC(),
			ConversationID: conversationID, TaskID: taskID, Actor: events.UserActor(fixture.UserA.String()),
			Payload: map[string]any{"id": subject.String()},
		})
	}
	publish("attachment", "attachment.deleted", uuid.New(), created.Task.ConversationID, uuid.Nil)
	publish("attachment", "attachment.created", uuid.New(), uuid.Nil, created.Task.ID)
	publish("message", "message.created", fixture.MessageA, uuid.Nil, uuid.Nil)

	if err := store.WithinRawTx(ctx, pgx.TxOptions{}, func(raw generated.DBTX, q *generated.Queries) error {
		var pending []events.Event
		if err := activity.RecordEvidenceInTx(ctx, q, &pending, activity.EvidenceInput{
			CompanyID: fixture.CompanyA, EntityID: fixture.RunA, Action: "run.evidence_recorded",
			Actor: events.SystemActor(), Payload: map[string]any{},
		}); err != nil {
			return err
		}
		if err := activity.RecordInTx(ctx, q, &pending, events.SystemActor(), "run.queued", nil,
			"run", fixture.RunA); err != nil {
			return err
		}
		before, err := activity.ObserveInTx(ctx, q, "task", created.Task.ID)
		if err != nil {
			return err
		}
		if _, err := raw.Exec(ctx, `DELETE FROM tasks WHERE id = $1`, created.Task.ID); err != nil {
			return err
		}
		return activity.RecordInTx(ctx, q, &pending, events.UserActor(fixture.UserA.String()),
			"task.deleted", before, "task", created.Task.ID)
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := pool.Query(ctx, taskScopedWithoutProject, fixture.CompanyA)
	if err != nil {
		t.Fatal(err)
	}
	missing, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("Activity rows about Task work name no Project:\n%s", strings.Join(missing, "\n"))
	}
	// The assertion above proves something only if each recorder wrote.
	rows, err = pool.Query(ctx, `
		SELECT DISTINCT action FROM activity_events
		WHERE company_id = $1 AND project_id = $2 ORDER BY action`, fixture.CompanyA, fixture.ProjectA)
	if err != nil {
		t.Fatal(err)
	}
	named, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{
		"task.created", "task.updated", "task.deleted", "run.queued", "run.evidence_recorded",
		"attachment.deleted", "attachment.created", "message.created",
	} {
		if !slices.Contains(named, action) {
			t.Errorf("no %s row names the Project; rows that do: %v", action, named)
		}
	}
	var envelope int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM activity_events
		WHERE action = 'attachment.deleted' AND conversation_id = $1`, created.Task.ConversationID,
	).Scan(&envelope); err != nil || envelope != 1 {
		t.Errorf("attachment.deleted rows keeping the envelope's chat = %d, %v; want 1", envelope, err)
	}
}

// Every subject a receipt can be about resolves to the Task chat's Project,
// each arm by its own key: the chat or Task on the envelope, and a Task,
// Project, grant, chat, Run, message, file in a chat, Approval or Question
// as the subject. A wrong join would store no Project and hide the row from
// everyone. Approval and Question events, which carry no chat on their
// envelope, are recorded with the Project too.
func TestActivityProjectResolvesEverySubject(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO approvals (company_id, rule_key, title, preview_markdown, requested_by_agent_id,
		                       origin_run_id, request_key, task_id, conversation_id, canonical_action,
		                       request_sha256, action_sha256, expires_at)
		SELECT runs.company_id, 'company_plan_apply', 'Apply', '', runs.agent_id,
		       runs.id, gen_random_uuid(), runs.task_id, runs.conversation_id, '{}'::jsonb,
		       decode(repeat('11', 32), 'hex'), decode(repeat('11', 32), 'hex'), now() + interval '1 day'
		FROM runs WHERE runs.id = $1`, fixture.RunA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO questions (company_id, title, asked_by_agent_id, origin_run_id, request_key,
		                       task_id, conversation_id, prompts, request_sha256, expires_at)
		SELECT runs.company_id, 'Choose', runs.agent_id, runs.id, gen_random_uuid(),
		       runs.task_id, runs.conversation_id, '[]'::jsonb, decode(repeat('11', 32), 'hex'),
		       now() + interval '1 day'
		FROM runs WHERE runs.id = $1`, fixture.RunA)
	var approvalID, questionID, attachmentID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM approvals WHERE origin_run_id = $1`, fixture.RunA).
		Scan(&approvalID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM questions WHERE origin_run_id = $1`, fixture.RunA).
		Scan(&questionID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO attachments (company_id, uploaded_by_user_id, request_key, filename, content_type,
		                         size_bytes, status)
		VALUES ($1, $2, gen_random_uuid(), 'notes.txt', 'text/plain', 1, 'ready') RETURNING id`,
		fixture.CompanyA, fixture.UserA).Scan(&attachmentID); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO message_attachments (company_id, message_id, attachment_id, position)
		VALUES ($1, $2, $3, 0)`, fixture.CompanyA, fixture.MessageA, attachmentID)
	queries := generated.New(pool)
	id := func(value uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: value, Valid: true} }
	for _, subject := range []struct {
		name   string
		params generated.ResolveActivityProjectParams
	}{
		{"envelope chat", generated.ResolveActivityProjectParams{
			ConversationID: id(fixture.ConversationA), EntityType: "agent", SubjectID: id(fixture.AgentA),
		}},
		{"envelope Task", generated.ResolveActivityProjectParams{
			TaskID: id(fixture.TaskA), EntityType: "agent", SubjectID: id(fixture.AgentA),
		}},
		{"Task", generated.ResolveActivityProjectParams{EntityType: "task", SubjectID: id(fixture.TaskA)}},
		{"Project", generated.ResolveActivityProjectParams{EntityType: "project", SubjectID: id(fixture.ProjectA)}},
		{"grant", generated.ResolveActivityProjectParams{EntityType: "project_grant", SubjectID: id(fixture.ProjectA)}},
		{"chat", generated.ResolveActivityProjectParams{EntityType: "conversation", SubjectID: id(fixture.ConversationA)}},
		{"Run", generated.ResolveActivityProjectParams{EntityType: "run", SubjectID: id(fixture.RunA)}},
		{"message", generated.ResolveActivityProjectParams{EntityType: "message", SubjectID: id(fixture.MessageA)}},
		{"file", generated.ResolveActivityProjectParams{EntityType: "attachment", SubjectID: id(attachmentID)}},
		{"Approval", generated.ResolveActivityProjectParams{EntityType: "approval", SubjectID: id(approvalID)}},
		{"Question", generated.ResolveActivityProjectParams{EntityType: "question", SubjectID: id(questionID)}},
	} {
		subject.params.CompanyID = fixture.CompanyA
		project, err := queries.ResolveActivityProject(ctx, subject.params)
		if err != nil || project != fixture.ProjectA {
			t.Errorf(
				"ResolveActivityProject(%s) = %s, %v; want %s",
				subject.name,
				project,
				err,
				fixture.ProjectA,
			)
		}
	}
	// A subject in the other Company resolves to nothing.
	if _, err := queries.ResolveActivityProject(ctx, generated.ResolveActivityProjectParams{
		CompanyID: fixture.CompanyA, EntityType: "task", SubjectID: id(fixture.TaskB),
	}); err == nil {
		t.Error("ResolveActivityProject(Task of another Company) found a Project")
	}

	bus := historyTestBus(t)
	if err := activity.RegisterListeners(bus, queries); err != nil {
		t.Fatal(err)
	}
	for topic, subject := range map[string]uuid.UUID{"approval": approvalID, "question": questionID} {
		bus.Publish(ctx, topic, events.Event{
			EntityType: topic,
			Name:       topic + ".created",
			CompanyID:  fixture.CompanyA,
			CreatedAt:  time.Now().UTC(),
			Actor: events.AgentActor(
				fixture.AgentA.String(),
			),
			Payload: map[string]any{"id": subject.String()},
		})
		var project uuid.NullUUID
		if err := pool.QueryRow(ctx, `
			SELECT project_id FROM activity_events WHERE action = $1 AND entity_id = $2`,
			topic+".created", subject.String()).Scan(&project); err != nil {
			t.Fatalf("%s.created row: %v", topic, err)
		}
		if !project.Valid || project.UUID != fixture.ProjectA {
			t.Errorf("%s.created row stores Project %v, want %s", topic, project, fixture.ProjectA)
		}
	}
}

// activityExplainRecorder runs every statement against the pool and keeps
// the last one's text and arguments, so a test can EXPLAIN what sqlc sent.
type activityExplainRecorder struct {
	pool *pgxpool.Pool
	sql  string
	args []any
}

func (r *activityExplainRecorder) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	r.sql, r.args = sql, args
	return r.pool.Exec(ctx, sql, args...)
}

func (r *activityExplainRecorder) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	r.sql, r.args = sql, args
	return r.pool.Query(ctx, sql, args...)
}

func (r *activityExplainRecorder) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	r.sql, r.args = sql, args
	return r.pool.QueryRow(ctx, sql, args...)
}

// The feed and the calendar decide Project access per row: one probe of the
// person's grants by index, never a hashed subplan that builds their whole
// readable set, and never a scan of every grant or every Activity row of the
// Company outside the requested window.
func TestActivityReadsProbeProjectAccessPerRow(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO projects (company_id, key, title)
		SELECT $1, 'bulk_' || n, 'Bulk ' || n FROM generate_series(1, 40) AS n`, fixture.CompanyA)
	// Colleagues with grants on every Project, so a scan of project_grants
	// would show.
	dbtest.MustExec(t, ctx, pool, `
		WITH colleague AS (
			INSERT INTO users (email, display_name)
			SELECT 'colleague-' || n || '@example.com', 'Colleague ' || n FROM generate_series(1, 40) AS n
			RETURNING id
		), membership AS (
			INSERT INTO company_members (company_id, user_id, role)
			SELECT $1, colleague.id, 'member' FROM colleague
			RETURNING user_id
		)
		INSERT INTO project_grants (company_id, project_id, user_id, permission, granted_by_user_id)
		SELECT $1, project.id, membership.user_id, 'project.read', $2
		FROM membership CROSS JOIN projects AS project
		WHERE project.company_id = $1`, fixture.CompanyA, fixture.UserA)
	// Forty days of Activity across the Projects, a tenth of it grant rows.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO activity_events (company_id, actor_type, action, entity_type, entity_id,
		                             visibility, visibility_user_id, details, project_id, created_at)
		SELECT $1, 'system',
		       CASE WHEN n % 10 = 0 THEN 'project_grant.changed' ELSE 'task.updated' END,
		       CASE WHEN n % 10 = 0 THEN 'project_grant' ELSE 'task' END,
		       project.id::text,
		       CASE WHEN n % 10 = 0 THEN 'user' ELSE 'company' END,
		       CASE WHEN n % 10 = 0 THEN $2::uuid END,
		       '{}'::jsonb, project.id, now() - (n * interval '10 minutes')
		FROM projects AS project CROSS JOIN generate_series(1, 150) AS n
		WHERE project.company_id = $1`, fixture.CompanyA, fixture.UserA)
	memberID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, 'explain@example.com', 'Explain')`, memberID)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		fixture.CompanyA,
		memberID,
	)
	var readable uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM projects WHERE company_id = $1 AND key = 'bulk_7'`,
		fixture.CompanyA).Scan(&readable); err != nil {
		t.Fatal(err)
	}
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, readable, memberID, "project.read")
	// Forty Groups of forty more colleagues, each Group holding grants on
	// every Project, so a scan of company_group_members or
	// project_group_grants would show too.
	dbtest.SeedGroupColleagues(t, ctx, pool, fixture.CompanyA, "group-colleague", 40)
	dbtest.MustExec(t, ctx, pool, `ANALYZE`)

	recorder := &activityExplainRecorder{pool: pool}
	feed := activity.NewFeed(generated.New(recorder))
	grantScan := regexp.MustCompile(
		`(?m)Seq Scan on (project_grants|company_group_members|project_group_grants)( |$)`,
	)
	activityScan := regexp.MustCompile(`(?m)Seq Scan on activity_events( |$)`)
	assertPerRow := func(name string) {
		t.Helper()
		plan, err := pool.Query(ctx, `EXPLAIN (COSTS OFF) `+recorder.sql, recorder.args...)
		if err != nil {
			t.Fatalf("%s explain: %v", name, err)
		}
		lines, err := pgx.CollectRows(plan, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(lines, "\n")
		if grantScan.MatchString(text) {
			t.Errorf("%s scans all of a grant or Group table:\n%s", name, text)
		}
		if strings.Contains(text, "hashed SubPlan") {
			t.Errorf("%s builds the readable set instead of probing per row:\n%s", name, text)
		}
		if activityScan.MatchString(text) {
			t.Errorf("%s scans all of activity_events:\n%s", name, text)
		}
	}

	entries, _, err := feed.List(ctx, fixture.CompanyA, activity.ListOptions{UserID: memberID, Limit: 50})
	if err != nil || len(entries) != 50 {
		t.Fatalf("feed = %d rows, %v; want a full page from the readable Project", len(entries), err)
	}
	assertPerRow("the Activity feed")
	for _, entry := range entries {
		if entry.Entity.ID != readable.String() {
			t.Fatalf("the feed shows a row of another Project: %+v", entry)
		}
	}
	now := time.Now().UTC()
	calendar, err := feed.Calendar(ctx, fixture.CompanyA, activity.CalendarOptions{
		UserID: memberID, Month: now.Format("2006-01"), TimeZone: "UTC", Location: time.UTC,
		StartAt: now.Add(-185 * time.Minute), EndAt: now.Add(time.Hour),
	})
	// The readable Project's rows of the last three hours (one every ten
	// minutes): its Company-wide rows, and its grant rows (addressed to the
	// owner), which the Project's readers read. A narrow window, so the
	// plan must reach the rows by time.
	if want := int64(18); err != nil || calendar.TotalCount != want {
		t.Fatalf("calendar = %d, %v; want %d", calendar.TotalCount, err, want)
	}
	assertPerRow("the Activity calendar")
}
