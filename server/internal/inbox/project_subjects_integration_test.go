//go:build integration

package inbox

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// Every subject an Inbox item can name resolves to the Task chat's Project
// (ADR 0017): the chat it links to, a Task, a Run, an Approval, a Question
// and a Deliverable, and the Project itself (project_access_granted, which
// also reads for a member whose Company role holds projects.manage). A wrong
// join in any arm would store no Project, and an item with no Project reads
// for its recipient whatever they can read, so each arm is proven here. The
// counts that ride item frames and the Run notice's wait check then follow
// the recipient losing the Project, and a
// replayed create of an item they no longer read is a skip.
func TestInboxItemsNameTheProjectOfEverySubject(t *testing.T) {
	pool := startInboxPostgres(t)
	ctx := t.Context()
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
	var approvalID, questionID, deliverableID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM approvals WHERE origin_run_id = $1`, fixture.RunA).
		Scan(&approvalID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM questions WHERE origin_run_id = $1`, fixture.RunA).
		Scan(&questionID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		WITH output AS (
			INSERT INTO outputs (company_id, run_id, owner_user_id, title, summary, presentation_fingerprint, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1, $2, $3, 'Result', 'Result', repeat('a', 64), origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id
		)
		INSERT INTO deliverables (company_id, output_id, submitted_at)
		SELECT $1, output.id, statement_timestamp() FROM output RETURNING id`,
		fixture.CompanyA, fixture.RunA, fixture.UserA).Scan(&deliverableID); err != nil {
		t.Fatal(err)
	}
	member := dbtest.NewUUID(t)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, 'member@example.com', 'Member')`,
		member,
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		fixture.CompanyA,
		member,
	)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	create := func(recipient uuid.UUID, key string, conversation *uuid.UUID, entity Entity) CreateResult {
		t.Helper()
		var result CreateResult
		if err := store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			var pending []events.Event
			var err error
			result, err = CreateInTx(ctx, q, &pending, CreateInput{
				CompanyID: fixture.CompanyA, RecipientUserID: recipient, ConversationID: conversation,
				Type: TypeBlocker, Entity: entity, Title: key, Body: key, DedupeKey: key,
			})
			return err
		}); err != nil {
			t.Fatalf("CreateInTx(%s) error = %v", key, err)
		}
		return result
	}
	counts := func(recipient uuid.UUID) int64 {
		t.Helper()
		var unread int64
		if err := store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			counts, err := ReadCountsInTx(ctx, q, fixture.CompanyA, recipient)
			unread = counts.UnreadCount
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return unread
	}
	notified := func() map[string]bool {
		t.Helper()
		waits, err := store.Queries().ListRunHumanInputWaits(ctx, generated.ListRunHumanInputWaitsParams{
			RecipientUserID: fixture.UserA, RunID: fixture.RunA,
		})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, wait := range waits {
			got[wait.EntityType] = wait.RecipientNotified
		}
		return got
	}

	before := counts(fixture.UserA)
	conversationA := fixture.ConversationA
	for _, subject := range []struct {
		key          string
		conversation *uuid.UUID
		entity       Entity
	}{
		{"chat", &conversationA, Entity{Type: protocol.EntityConversation, ID: fixture.ConversationA.String()}},
		{"task", nil, Entity{Type: protocol.EntityTask, ID: fixture.TaskA.String()}},
		{"run", nil, Entity{Type: protocol.EntityRun, ID: fixture.RunA.String()}},
		{"approval", nil, Entity{Type: protocol.EntityApproval, ID: approvalID.String()}},
		{"question", nil, Entity{Type: protocol.EntityQuestion, ID: questionID.String()}},
		{"deliverable", nil, Entity{Type: protocol.EntityDeliverable, ID: deliverableID.String()}},
		// The Project itself (project_access_granted, 000101).
		{"project", nil, Entity{Type: protocol.EntityProject, ID: fixture.ProjectA.String()}},
	} {
		created := create(fixture.UserA, "owner-"+subject.key, subject.conversation, subject.entity)
		if !created.Created {
			t.Fatalf("CreateInTx(%s) for a reader = %+v, want a new item", subject.key, created)
		}
		var project uuid.NullUUID
		if err := pool.QueryRow(ctx, `SELECT project_id FROM inbox_items WHERE id = $1`, created.Item.ID).
			Scan(&project); err != nil {
			t.Fatal(err)
		}
		if !project.Valid || project.UUID != fixture.ProjectA {
			t.Errorf("item about a %s stores Project %v, want %s", subject.key, project, fixture.ProjectA)
		}
		if skipped := create(
			member,
			"member-"+subject.key,
			subject.conversation,
			subject.entity,
		); skipped.Created ||
			skipped.Item.ID != uuid.Nil {
			t.Errorf("CreateInTx(%s) for a non-reader = %+v, want nothing", subject.key, skipped)
		}
	}
	if got := counts(fixture.UserA); got != before+7 {
		t.Fatalf("reader unread count = %d, want %d", got, before+7)
	}
	// An admin who does not read the Project manages it through the
	// management bridge: an item about the Project itself reaches them, an
	// item about work inside it does not.
	admin := dbtest.NewUUID(t)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, 'admin@example.com', 'Admin')`,
		admin,
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'admin')`,
		fixture.CompanyA,
		admin,
	)
	if managed := create(admin, "admin-project", nil,
		Entity{Type: protocol.EntityProject, ID: fixture.ProjectA.String()}); !managed.Created {
		t.Errorf("CreateInTx(project) for a bridge-only admin = %+v, want a new item", managed)
	}
	if skipped := create(admin, "admin-task", nil,
		Entity{Type: protocol.EntityTask, ID: fixture.TaskA.String()}); skipped.Created {
		t.Errorf("CreateInTx(task) for a bridge-only admin = %+v, want nothing", skipped)
	}
	if got := counts(admin); got != 1 {
		t.Errorf("bridge-only admin unread count = %d, want 1 (the Project item)", got)
	}
	if got := notified(); !got["approval"] || !got["question"] {
		t.Fatalf("Run waits notified = %v, want both held by the reader", got)
	}

	// The reader loses the Project.
	dbtest.MustExec(t, ctx, pool, `DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2`,
		fixture.ProjectA, fixture.UserA)
	// The owner still manages the Project through the management bridge, so
	// the item about the Project itself still reads; the other six hide.
	if got := counts(fixture.UserA); got != before+1 {
		t.Errorf("unread count after losing the Project = %d, want %d", got, before+1)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET role = 'member' WHERE company_id = $1 AND user_id = $2`,
		fixture.CompanyA,
		admin,
	)
	if got := counts(admin); got != 0 {
		t.Errorf("unread count of an admin made member = %d, want 0", got)
	}
	if got := notified(); got["approval"] || got["question"] {
		t.Errorf("Run waits notified after losing the Project = %v, want neither", got)
	}
	// Read-all across Companies picks only Companies holding an item the
	// person still reads.
	var readable bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM inbox_items WHERE company_id = $1 AND recipient_user_id = $2
		                                        AND (project_id IS NULL OR entity_type = 'project'))`,
		fixture.CompanyA, fixture.UserA).Scan(&readable); err != nil {
		t.Fatal(err)
	}
	companies, err := store.Queries().ListInboxCompaniesForUser(ctx, generated.ListInboxCompaniesForUserParams{
		RecipientUserID: fixture.UserA, ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := slices.Contains(companies, fixture.CompanyA); got != readable {
		t.Errorf("read-all Companies include Company A = %t, want %t (an item it still reads)", got, readable)
	}
	// A replay of a stored item the recipient no longer reads, raised from a
	// subject that names no Project, is a skip and not an error.
	if replay := create(fixture.UserA, "owner-task", nil,
		Entity{Type: protocol.EntityCompany, ID: fixture.CompanyA.String()}); replay.Created ||
		replay.Item.ID != uuid.Nil {
		t.Errorf("replay of a hidden item = %+v, want nothing", replay)
	}
}
