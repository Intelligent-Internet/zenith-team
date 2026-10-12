//go:build integration

package execution

import (
	"context"
	"errors"
	"testing"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// artifactRunFixture is the shared Company graph with a second member in
// it: UserA (Olivia, the owner, who holds every Project permission) is the
// Authorizer of every Run, and UserB (Mia) is a member who reads the chats
// those Runs answer into.
type artifactRunFixture struct {
	pool    *pgxpool.Pool
	f       dbtest.Fixture
	service *DefaultDeliverableService
	olivia  auth.UserPrincipal
	mia     auth.UserPrincipal
}

func newArtifactRunFixture(t *testing.T, ctx context.Context) artifactRunFixture {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewDeliverableService(DeliverableServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`, f.CompanyA, f.UserB)
	return artifactRunFixture{
		pool: pool, f: f, service: service,
		olivia: auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner},
		mia:    auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: auth.CompanyRoleMember},
	}
}

// insertPrivateOutput stores a private Output of RunA owned by owner, which
// output_access opens to the owner alone until someone shares it.
func (x artifactRunFixture) insertPrivateOutput(t *testing.T, ctx context.Context, owner uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := x.pool.QueryRow(ctx, `
		INSERT INTO outputs (company_id, run_id, title, summary, presentation_fingerprint,
			presentation_number, owner_user_id, visibility, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id)
		SELECT $1, $2, 'Report', 'Pinned', gen_random_uuid()::text,
			(SELECT COALESCE(max(presentation_number), 0) + 1 FROM outputs WHERE run_id = $2),
			$3, 'private', origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.id=origin.conversation_id
 WHERE origin.id=$2
		RETURNING id`, x.f.CompanyA, x.f.RunA, owner).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// runOrigins are Olivia's three kinds of Run that read Artifacts: one in her
// direct chat, one tagged in a channel Mia is in, and one in the Task chat of
// a restricted Project Mia reads. Mia reads both shared chats.
func (x artifactRunFixture) runOrigins(t *testing.T, ctx context.Context) map[string]RunOrigin {
	t.Helper()
	f := x.f
	dbtest.MustExec(t, ctx, x.pool, `UPDATE projects SET visibility = 'restricted' WHERE id = $1`, f.ProjectA)
	dbtest.GrantProject(t, ctx, x.pool, f.CompanyA, f.ProjectA, f.UserB, "project.read")
	channel := dbtest.SeedDiscussionChannel(t, ctx, x.pool, f.CompanyA, f.UserA, nil)
	dbtest.AddDiscussionMember(t, ctx, x.pool, f.CompanyA, channel, f.UserB)
	for _, chat := range []uuid.UUID{f.ConversationA, channel} {
		var reads bool
		err := x.pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM conversation_readers
				WHERE company_id = $1 AND conversation_id = $2 AND user_id = $3)`,
			f.CompanyA, chat, f.UserB).Scan(&reads)
		if err != nil || !reads {
			t.Fatalf("Mia reads chat %s = %v, %v; the scenario needs her in it", chat, reads, err)
		}
	}
	direct := RunOrigin{
		CompanyID: f.CompanyA, RunID: f.RunA, AgentID: f.AgentA,
		ConversationID: f.DirectConversationA, ConversationKind: "direct",
		AuthorizedByUserID: &f.UserA,
	}
	tagged := direct
	tagged.ConversationID, tagged.ConversationKind = channel, "channel_agent"
	tagged.InvocationChannelID = &channel
	task := direct
	task.ConversationID, task.ConversationKind = f.ConversationA, "task"
	task.TaskID, task.ProjectID = &f.TaskA, &f.ProjectA
	return map[string]RunOrigin{"direct": direct, "tagged": tagged, "task": task}
}

// Every Run reads an Artifact exactly when its Authorizer reads it, with no
// chat-audience limit (D31, Access TECH B10). Main's #493 required a Task
// Run's whole Task chat to read the Artifact; D31 supersedes that, so Mia,
// who reads the Task chat and the channel but not the Output, changes no
// answer. The Authorizer's own access is rechecked on every read.
func TestEveryRunReadsAnArtifactWithItsAuthorizersAccess(t *testing.T) {
	ctx := t.Context()
	x := newArtifactRunFixture(t, ctx)
	origins := x.runOrigins(t, ctx)
	own := x.insertPrivateOutput(t, ctx, x.f.UserA)
	if _, err := x.service.GetArtifact(ctx, x.mia, own); !errors.Is(err, ErrDeliverableNotFound) {
		t.Fatalf("Mia's own read of Olivia's private Output = %v; the scenario needs her refused", err)
	}
	for kind, origin := range origins {
		got, err := x.service.ReadArtifactForRun(ctx, x.olivia, origin, own)
		if err != nil || got.ID != own {
			t.Fatalf("%s Run read = %+v, %v; want its Authorizer's access", kind, got, err)
		}
	}

	// Mia's Output, shared with Olivia and then unshared: each Run follows
	// Olivia's grant as it stands at the read.
	shared := x.insertPrivateOutput(t, ctx, x.f.UserB)
	dbtest.MustExec(t, ctx, x.pool, `
		INSERT INTO output_grants (company_id, output_id, user_id, permission, granted_by_user_id)
		VALUES ($1, $2, $3, 'view', $4)`, x.f.CompanyA, shared, x.f.UserA, x.f.UserB)
	for kind, origin := range origins {
		got, err := x.service.ReadArtifactForRun(ctx, x.olivia, origin, shared)
		if err != nil || got.ID != shared {
			t.Fatalf("%s Run read of a shared Output = %+v, %v", kind, got, err)
		}
	}
	dbtest.MustExec(t, ctx, x.pool, `DELETE FROM output_grants WHERE output_id = $1`, shared)
	for kind, origin := range origins {
		_, err := x.service.ReadArtifactForRun(ctx, x.olivia, origin, shared)
		if !errors.Is(err, ErrDeliverableNotFound) {
			t.Fatalf("%s Run read after the share was revoked = %v, want ErrDeliverableNotFound", kind, err)
		}
	}

	// A scheduled Run reads no Artifact, and a proof for another person or
	// Company is refused before anything is read.
	scheduled := origins["direct"]
	scheduled.ConversationKind = "schedule"
	if _, err := x.service.ReadArtifactForRun(ctx, x.olivia, scheduled, own); !errors.Is(err, ErrRunProofInvalid) {
		t.Fatalf("scheduled Run read = %v, want ErrRunProofInvalid", err)
	}
	someoneElse := origins["task"]
	someoneElse.AuthorizedByUserID = &x.f.UserB
	_, err := x.service.ReadArtifactForRun(ctx, x.olivia, someoneElse, own)
	if !errors.Is(err, ErrRunProofInvalid) {
		t.Fatalf("read with another Authorizer's proof = %v, want ErrRunProofInvalid", err)
	}
	otherCompany, otherOrigin := x.olivia, origins["task"]
	otherCompany.CompanyID, otherOrigin.CompanyID = x.f.CompanyB, x.f.CompanyB
	_, err = x.service.ReadArtifactForRun(ctx, otherCompany, otherOrigin, own)
	if !errors.Is(err, ErrDeliverableNotFound) {
		t.Fatalf("cross-company read = %v, want ErrDeliverableNotFound", err)
	}
}

// A Run never reads more than its Authorizer: another person's private
// Output is not found for every kind of Run, as it is for the Authorizer.
// Both agent-artifacts routes, inspect and download, read the Artifact for
// the Run first, so both answer 404.
func TestRunCannotReadAnArtifactItsAuthorizerCannotRead(t *testing.T) {
	ctx := t.Context()
	x := newArtifactRunFixture(t, ctx)
	origins := x.runOrigins(t, ctx)
	theirs := x.insertPrivateOutput(t, ctx, x.f.UserB)
	if _, err := x.service.GetArtifact(ctx, x.olivia, theirs); !errors.Is(err, ErrDeliverableNotFound) {
		t.Fatalf("Olivia's own read of Mia's private Output = %v; the scenario needs her refused", err)
	}
	for kind, origin := range origins {
		_, err := x.service.ReadArtifactForRun(ctx, x.olivia, origin, theirs)
		if !errors.Is(err, ErrDeliverableNotFound) {
			t.Fatalf("%s Run read of Mia's private Output = %v, want ErrDeliverableNotFound", kind, err)
		}
	}
}

// The 2026-10-09 desktop finding (S1g): Payroll is restricted, Olivia holds
// every permission there and Mia holds project.read. Eli runs Olivia's Task
// and presents an Output, which is private to her, the Task's creator. In
// the Task chat Olivia asks Eli to inspect and fetch it, and both must
// answer though Mia, who reads the Task chat, cannot open the Output. This
// is the path the agent-artifacts inspect and download routes take.
func TestTaskRunReadsItsOwnTasksPrivateOutputInARestrictedProject(t *testing.T) {
	ctx := t.Context()
	x := newArtifactRunFixture(t, ctx)
	f := x.f
	task := x.runOrigins(t, ctx)["task"]
	dbtest.MustExec(t, ctx, x.pool, `UPDATE tasks SET execution_generation = 1 WHERE id = $1`, f.TaskA)
	dbtest.MustExec(t, ctx, x.pool, `
		UPDATE runs
		SET status = 'running', task_execution_generation = 1,
			execution_authorized_by_user_id = $2, updated_at = statement_timestamp()
		WHERE id = $1`, f.RunA, f.UserA)
	attachmentID := insertReadyDeliverableAttachment(t, ctx, x.pool, f.CompanyA, f.UserA, "payroll.pdf")
	presented, err := x.service.Present(ctx, x.olivia, task, DeliverablePresentRequest{
		Title: "Payroll summary", Summary: "October payroll.",
		Items: []DeliverablePresentItemRequest{{
			Role: DeliverableItemRolePrimary, Label: "Summary", AttachmentID: &attachmentID,
		}},
	})
	if err != nil || !presented.Created {
		t.Fatalf("Present() = %+v, %v", presented, err)
	}
	owned, err := x.service.GetArtifact(ctx, x.olivia, presented.OutputID)
	if err != nil || owned.Access.Visibility != "private" || len(owned.Access.Grants) != 0 {
		t.Fatalf("Olivia's Output = %+v, %v; want private to her", owned.Access, err)
	}
	if _, err := x.service.GetArtifact(ctx, x.mia, presented.OutputID); !errors.Is(err, ErrDeliverableNotFound) {
		t.Fatalf("Mia's read of the Output = %v; the scenario needs her refused", err)
	}

	// artifact_inspect.
	inspected, err := x.service.ReadArtifactForRun(ctx, x.olivia, task, presented.OutputID)
	if err != nil || inspected.ID != presented.OutputID || len(inspected.Items) != 1 {
		t.Fatalf("Task Run inspect = %+v, %v; want its Authorizer's Output", inspected, err)
	}
	// artifact_fetch: downloadArtifactForRun reads the Artifact for the Run,
	// then resolves the item's file under the Artifact's own source Run.
	detail, err := x.service.GetArtifact(ctx, x.olivia, presented.OutputID)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := x.service.ResolveArtifactFileAttachment(
		ctx, x.olivia, *detail.RunID, presented.OutputID, inspected.Items[0].Ordinal,
	)
	if err != nil || resolved != attachmentID {
		t.Fatalf("Task Run fetch = %s, %v; want %s", resolved, err, attachmentID)
	}
}

// Retained deliveries keep their frozen Project audience after Run deletion.
func TestRetainedOutputPreservesProjectAccessAfterRunDeletion(t *testing.T) {
	ctx := t.Context()
	x := newArtifactRunFixture(t, ctx)
	output := x.insertPrivateOutput(t, ctx, x.f.UserA)
	dbtest.MustExec(t, ctx, x.pool, `UPDATE projects SET visibility='restricted' WHERE id=$1`, x.f.ProjectA)
	dbtest.MustExec(t, ctx, x.pool, `UPDATE outputs SET visibility='company' WHERE id=$1`, output)
	dbtest.GrantProject(t, ctx, x.pool, x.f.CompanyA, x.f.ProjectA, x.f.UserB, "project.read")
	dbtest.MustExec(t, ctx, x.pool, `DELETE FROM runs WHERE id=$1`, x.f.RunA)
	detail, err := x.service.GetArtifact(ctx, x.mia, output)
	if err != nil || detail.RunID != nil {
		t.Fatalf("retained Project output = %+v, %v", detail, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		x.pool,
		`DELETE FROM project_grants WHERE project_id=$1 AND user_id=$2`,
		x.f.ProjectA,
		x.f.UserB,
	)
	if _, err := x.service.GetArtifact(ctx, x.mia, output); !errors.Is(err, ErrDeliverableNotFound) {
		t.Fatalf("retained output after Project access revocation = %v", err)
	}
}
