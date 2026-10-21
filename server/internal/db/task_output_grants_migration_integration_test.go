//go:build integration

package db

import (
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// A Task Run executes on its authorizer's computer (ADR 0014), yet 000036
// made a Task Output its creator's alone, so the member who ran the Task
// could not see it. Before ADR 0014, though, a Run's authorizer was whoever
// started it — a message sender, a question answerer — and the Run ran on
// the Agent's runtime, usually someone else's computer. So 000060 grants
// view on an earlier Task Output only when its Run clearly ran for its
// authorizer: on a runtime the authorizer registered, for a Task the
// authorizer is assigned, while they are an active member who was one when
// the Output was saved and are not its owner. It takes nothing away — the
// owner and any grant already held stay as they were — and its down file
// keeps the grants, so running it again changes nothing.
func TestTaskOutputAuthorizerGrantsMigrationOnlyAddsAccess(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := runMigration(ctx, pool, "prepare Task Outputs", func(m *migrate.Migrate) error {
		return m.Migrate(59)
	}); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	// UserA created every Task here and owns its Outputs; TaskA is also
	// assigned to UserA, who runs it on RuntimeA. The colleague is assigned
	// a Task of their own; so is the leaver, who has since left, and the
	// returner, who left and came back after the Output was saved. Each of
	// them registered a computer of their own.
	colleagueID, leaverID, returnerID := f.UserB, dbtest.NewUUID(t), dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO users (id, email, display_name) VALUES
			($1, 'task-leaver@example.com', 'Task Leaver'),
			($2, 'task-returner@example.com', 'Task Returner')`, leaverID, returnerID)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role) VALUES
			($1, $2, 'member'), ($1, $3, 'member'), ($1, $4, 'member')`,
		f.CompanyA, colleagueID, leaverID, returnerID)

	seedRuntime := func(ownerID uuid.UUID, daemonID string) uuid.UUID {
		t.Helper()
		runtimeID := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO runtimes
				(id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
			VALUES ($1, $2, $3, $3, 'codex', 'online', $4, now())`,
			runtimeID, f.CompanyA, daemonID, ownerID)
		return runtimeID
	}
	colleagueRuntime := seedRuntime(colleagueID, "daemon-colleague")
	leaverRuntime := seedRuntime(leaverID, "daemon-leaver")
	returnerRuntime := seedRuntime(returnerID, "daemon-returner")

	type seededTask struct{ id, conversationID uuid.UUID }
	seedTask := func(number int64, key string, assigneeID uuid.UUID) seededTask {
		t.Helper()
		task := seededTask{dbtest.NewUUID(t), dbtest.NewUUID(t)}
		dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
			dbtest.MustExecTx(t, ctx, tx, `
				INSERT INTO conversations (id, company_id, kind, task_id, title)
				VALUES ($1, $2, 'task', $3, $4)`,
				task.conversationID, f.CompanyA, task.id, key)
			dbtest.MustExecTx(t, ctx, tx, `
				INSERT INTO tasks
					(id, company_id, conversation_id, project_id, milestone_type,
					 number, key, title, assignee_user_id, executed_by_agent_id, creator_user_id)
				VALUES ($1, $2, $3, $4, 'product', $5, $6, $6, $7, $8, $9)`,
				task.id, f.CompanyA, task.conversationID, f.ProjectA,
				number, key, assigneeID, f.AgentA, f.UserA)
		})
		return task
	}
	creatorTask := seededTask{f.TaskA, f.ConversationA}
	colleagueTask := seedTask(2, "colleague_task", colleagueID)
	leaverTask := seedTask(3, "leaver_task", leaverID)
	returnerTask := seedTask(4, "returner_task", returnerID)

	// seedOutput writes a completed Run that authorizerID started on
	// runtimeID and the Output it presented. A nil task is a direct chat.
	seedOutput := func(task *seededTask, runtimeID, authorizerID uuid.UUID, ownerID *uuid.UUID) uuid.UUID {
		t.Helper()
		runType, conversationID, taskID := "direct_chat", f.DirectConversationA, (*uuid.UUID)(nil)
		if task != nil {
			runType, conversationID, taskID = "task", task.conversationID, &task.id
		}
		var outputID uuid.UUID
		if err := pool.QueryRow(ctx, `
			WITH run AS (
				INSERT INTO runs
					(company_id, agent_id, task_id, conversation_id, runtime_id,
					 status, run_type, revision, execution_authorized_by_user_id)
				VALUES ($1, $2, $3, $4, $5, 'completed', $6, 1, $7)
				RETURNING id
			)
			INSERT INTO outputs (company_id, run_id, title, summary, presentation_fingerprint, owner_user_id)
			SELECT $1, run.id, 'Result', 'Summary', gen_random_uuid()::text, $8
			FROM run
			RETURNING id`,
			f.CompanyA, f.AgentA, taskID, conversationID, runtimeID, runType, authorizerID, ownerID,
		).Scan(&outputID); err != nil {
			t.Fatalf("seed Output: %v", err)
		}
		return outputID
	}
	colleagueRun := seedOutput(&colleagueTask, colleagueRuntime, colleagueID, &f.UserA)
	creatorRun := seedOutput(&creatorTask, f.RuntimeA, f.UserA, &f.UserA)
	unownedRun := seedOutput(&colleagueTask, colleagueRuntime, colleagueID, nil)
	alreadyManaged := seedOutput(&colleagueTask, colleagueRuntime, colleagueID, &f.UserA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO output_grants (company_id, output_id, user_id, permission, granted_by_user_id)
		VALUES ($1, $2, $3, 'manage', $4)`, f.CompanyA, alreadyManaged, colleagueID, f.UserA)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE outputs SET visibility = 'shared', access_version = 2 WHERE id = $1`, alreadyManaged)
	directRun := seedOutput(nil, colleagueRuntime, colleagueID, &f.UserA)
	// Before ADR 0014: the colleague answered the Agent's question in UserA's
	// Task, and the Run that continued it ran on UserA's computer.
	answeredRun := seedOutput(&creatorTask, f.RuntimeA, colleagueID, &f.UserA)
	onAnotherComputer := seedOutput(&colleagueTask, f.RuntimeA, colleagueID, &f.UserA)
	forAnotherAssignee := seedOutput(&creatorTask, colleagueRuntime, colleagueID, &f.UserA)
	leaverRun := seedOutput(&leaverTask, leaverRuntime, leaverID, &f.UserA)
	returnerRun := seedOutput(&returnerTask, returnerRuntime, returnerID, &f.UserA)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE company_members SET status = 'revoked', revoked_at = clock_timestamp()
		WHERE company_id = $1 AND user_id = $2`, f.CompanyA, leaverID)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE company_members SET reactivated_at = clock_timestamp()
		WHERE company_id = $1 AND user_id = $2`, f.CompanyA, returnerID)

	type access struct {
		visibility string
		revision   int64
		grant      string
	}
	want := map[string]struct {
		outputID uuid.UUID
		userID   uuid.UUID
		access   access
	}{
		"colleague's run of their own task on their own computer": {
			colleagueRun,
			colleagueID,
			access{"shared", 2, "view"},
		},
		"creator's own run": {
			creatorRun,
			f.UserA,
			access{"private", 1, ""},
		},
		"run of an Agent-created task": {
			unownedRun,
			colleagueID,
			access{"shared", 2, "view"},
		},
		"run already shared to manage": {
			alreadyManaged,
			colleagueID,
			access{"shared", 2, "manage"},
		},
		"direct conversation run": {
			directRun,
			colleagueID,
			access{"private", 1, ""},
		},
		"colleague's answer on the creator's task and computer": {
			answeredRun,
			colleagueID,
			access{"private", 1, ""},
		},
		"run on a computer the authorizer did not register": {
			onAnotherComputer,
			colleagueID,
			access{"private", 1, ""},
		},
		"run for a task the authorizer is not assigned": {
			forAnotherAssignee,
			colleagueID,
			access{"private", 1, ""},
		},
		"run by a member who left": {
			leaverRun,
			leaverID,
			access{"private", 1, ""},
		},
		"run by a member who came back": {
			returnerRun,
			returnerID,
			access{"private", 1, ""},
		},
	}
	assertAccess := func(step string) {
		t.Helper()
		for name, tc := range want {
			var got access
			if err := pool.QueryRow(ctx, `
				SELECT outputs.visibility, outputs.access_version, COALESCE(output_grants.permission, '')
				FROM outputs
				LEFT JOIN output_grants
				  ON output_grants.company_id = outputs.company_id
				 AND output_grants.output_id = outputs.id
				 AND output_grants.user_id = $2
				WHERE outputs.id = $1`, tc.outputID, tc.userID).
				Scan(&got.visibility, &got.revision, &got.grant); err != nil {
				t.Fatalf("%s: %s: read access: %v", step, name, err)
			}
			if got != tc.access {
				t.Errorf("%s: %s: access = %+v, want %+v", step, name, got, tc.access)
			}
			var owner uuid.NullUUID
			if err := pool.QueryRow(ctx, `SELECT owner_user_id FROM outputs WHERE id = $1`, tc.outputID).
				Scan(&owner); err != nil {
				t.Fatalf("%s: %s: read owner: %v", step, name, err)
			}
			if tc.outputID == unownedRun && owner.Valid ||
				tc.outputID != unownedRun && owner.UUID != f.UserA {
				t.Errorf("%s: %s: owner = %v, want it unchanged", step, name, owner)
			}
		}
	}

	if err := runMigration(ctx, pool, "grant Task Output authorizers", func(m *migrate.Migrate) error {
		return m.Migrate(60)
	}); err != nil {
		t.Fatal(err)
	}
	assertAccess("up")
	var viewers int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM output_access
		WHERE output_id = $1 AND user_id = $2`, colleagueRun, colleagueID).Scan(&viewers); err != nil {
		t.Fatalf("read colleague's access: %v", err)
	}
	if viewers != 1 {
		t.Fatalf("colleague's access rows = %d, want 1", viewers)
	}

	if err := runMigration(ctx, pool, "roll back and reapply", func(m *migrate.Migrate) error {
		if err := m.Migrate(59); err != nil {
			return err
		}
		return m.Migrate(60)
	}); err != nil {
		t.Fatal(err)
	}
	assertAccess("down and up again")
}
