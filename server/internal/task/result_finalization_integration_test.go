//go:build integration

package task

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"boardly.dev/boardly/server/internal/wiki"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

type finalizationTestCase struct {
	name                                                                     string
	reviewRequired, priorRun, pendingSuggestion, proposalOnly, revokedAccess bool
}

func TestAgentFinalizesExplicitTaskResultsWithoutApproving(t *testing.T) {
	for _, test := range []finalizationTestCase{
		{name: "review required", reviewRequired: true},
		{name: "optional review"},
		{name: "revoked delivery access", revokedAccess: true},
		{name: "earlier Run selection", priorRun: true},
		{name: "required application", pendingSuggestion: true},
		{name: "proposal only", pendingSuggestion: true, proposalOnly: true},
	} {
		t.Run(test.name, func(t *testing.T) { testAgentFinalization(t, test) })
	}
}

func testAgentFinalization(t *testing.T, test finalizationTestCase) {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	service, _ := NewTaskService(store, newTestBus(t), nil)
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE tasks SET status='in_progress',result_review_required=$2,result_proposal_only=$3 WHERE id=$1`,
		f.TaskA,
		test.reviewRequired,
		test.proposalOnly,
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE runs SET status='running', execution_authorized_by_user_id=$2, task_execution_generation=(SELECT execution_generation FROM tasks WHERE id=$3) WHERE id=$1`,
		f.RunA,
		f.UserA,
		f.TaskA,
	)
	var outputs []uuid.UUID
	for _, number := range []int{1, 2} {
		var id uuid.UUID
		err := pool.QueryRow(ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id,presentation_number,conversation_id,task_id,project_id,task_execution_generation,actor_agent_id)
   SELECT r.company_id,r.id,'Report','Results',$3::bigint::text,$2,$3::bigint,r.conversation_id,r.task_id,c.project_id,r.task_execution_generation,r.agent_id
   FROM runs r JOIN conversations c ON c.id=r.conversation_id WHERE r.id=$1 RETURNING id`, f.RunA, f.UserA, number).
			Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, id)
	}
	if test.pendingSuggestion {
		pages := canvas.NewPageService(store, newTestBus(t))
		presentations := canvas.NewPresentationService(store, newTestBus(t), nil)
		page, err := pages.CreatePage(
			ctx,
			p,
			f.ProjectA,
			canvas.PageCreateInput{
				RequestKey: uuid.New(),
				Title:      "Proposed report",
				Markdown:   "Shared text",
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		read, err := pages.ReadPageForRun(ctx, p, f.RunA, page.PageID)
		if err != nil {
			t.Fatal(err)
		}
		addition := wiki.BlocksFromMarkdown("Pending update").Content[0]
		change, err := pages.EditPageForRun(
			ctx,
			p,
			f.RunA,
			page.PageID,
			canvas.PageEditInput{
				RequestKey: uuid.New(),
				Sequence:   read.Sequence,
				Summary:    "Proposed update",
				Operations: []wiki.BlockOperation{{Type: "insert", Content: &addition}},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		preview, err := presentations.PreviewProposalForRun(ctx, p, f.RunA, page.PageID, change.ID)
		if err != nil {
			t.Fatal(err)
		}
		capture, err := presentations.CapturePresentation(
			ctx,
			p,
			f.ProjectA,
			canvas.PresentationInput{
				RequestKey:  uuid.New(),
				PageID:      page.PageID,
				ChangeID:    &change.ID,
				Sequence:    preview.Sequence,
				Fingerprint: preview.Fingerprint,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`UPDATE project_presentations SET output_id=$2 WHERE id=$1`,
			capture.ID,
			outputs[0],
		)
	}
	current, err := service.Get(ctx, p, f.TaskA, nil)
	if err != nil {
		t.Fatal(err)
	}
	origin := execution.RunOrigin{
		CompanyID:        f.CompanyA,
		RunID:            f.RunA,
		AgentID:          f.AgentA,
		TaskID:           &f.TaskA,
		ConversationID:   f.ConversationA,
		ConversationKind: "task",
	}
	in := FinalizeResultsInput{
		RequestKey:       uuid.New(),
		ExpectedRevision: current.Revision,
		OutputIDs:        []uuid.UUID{outputs[1], outputs[0]},
	}
	result, err := service.FinalizeResults(ctx, p, origin, in)
	if err != nil || result.Status != StatusInProgress {
		t.Fatalf("finalize = %+v, %v", result, err)
	}
	var actorType, actorID string
	var actorRun, actorUser uuid.UUID
	err = pool.QueryRow(ctx, `SELECT actor_type,actor_id,run_id,on_behalf_of_user_id
 FROM activity_events WHERE company_id=$1 AND action='task.updated' ORDER BY created_at DESC,id DESC LIMIT 1`, f.CompanyA).
		Scan(&actorType, &actorID, &actorRun, &actorUser)
	if err != nil || actorType != "agent" || actorID != f.AgentA.String() || actorRun != f.RunA ||
		actorUser != f.UserA {
		t.Fatalf(
			"finalization attribution = %s/%s Run %s for %s, %v",
			actorType,
			actorID,
			actorRun,
			actorUser,
			err,
		)
	}
	selection, err := service.ResultSelection(ctx, p, f.TaskA)
	if err != nil || !slices.Equal(selection.OutputIDs, in.OutputIDs) || selection.ApprovedAt != nil {
		t.Fatalf("selection = %+v, %v", selection, err)
	}
	replay, err := service.FinalizeResults(ctx, p, origin, in)
	if err != nil || replay.Revision != result.Revision {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	changed := in
	changed.OutputIDs = outputs
	if _, err := service.FinalizeResults(ctx, p, origin, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay = %v", err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE tasks SET execution_generation=execution_generation+1 WHERE id=$1`,
		f.TaskA,
	)
	in.RequestKey = uuid.New()
	in.ExpectedRevision = result.Revision
	if _, err := service.FinalizeResults(ctx, p, origin, in); !errors.Is(err, ErrConflict) {
		t.Fatalf("old generation = %v", err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE tasks SET execution_generation=execution_generation-1 WHERE id=$1`,
		f.TaskA,
	)
	if test.priorRun {
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`UPDATE task_result_sets SET source_run_id=$2 WHERE task_id=$1`,
			f.TaskA,
			uuid.New(),
		)
	}
	if test.revokedAccess {
		dbtest.MustExec(t, ctx, pool, `UPDATE outputs SET owner_user_id=$2 WHERE task_id=$1`, f.TaskA, f.UserB)
		state, err := service.ResultSelection(ctx, p, f.TaskA)
		if err != nil || !state.HasUnavailable {
			t.Fatalf("revocation fixture = %+v, %v", state, err)
		}
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, f.RunA)
	run, err := store.Queries().GetRun(ctx, f.RunA)
	if err != nil {
		t.Fatal(err)
	}
	var pending []events.Event
	if err = store.WithinTx(
		ctx,
		pgx.TxOptions{},
		func(q *generated.Queries) error { return ApplyRunTerminalInTx(ctx, q, &pending, run) },
	); err != nil {
		t.Fatal(err)
	}
	completed, err := service.Get(ctx, p, f.TaskA, nil)
	want := StatusDone
	if test.reviewRequired || test.priorRun || test.revokedAccess || test.pendingSuggestion && !test.proposalOnly {
		want = StatusInReview
	}
	if err != nil || completed.Status != want {
		t.Fatalf("terminal Task = %+v, %v; want %s", completed, err, want)
	}
	selection, err = service.ResultSelection(ctx, p, f.TaskA)
	if err != nil || selection.ApprovedAt != nil {
		t.Fatalf("Run authored human approval: %+v, %v", selection, err)
	}
	if want == StatusDone {
		title := "Edited completed Task"
		updated, err := service.Update(ctx, p, f.TaskA, UpdateInput{ExpectedRevision: completed.Revision, Title: &title})
		if err != nil {
			t.Fatal(err)
		}
		selection, err = service.ResultSelection(ctx, p, f.TaskA)
		if err != nil || selection.ApprovedAt != nil {
			t.Fatalf("metadata edit authored approval: %+v, %v", selection, err)
		}
		dbtest.MustExec(t, ctx, pool, `INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`, f.CompanyA, f.UserB)
		dbtest.GrantProject(t, ctx, pool, f.CompanyA, f.ProjectA, f.UserB, "project.tasks.write")
		editor := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: auth.CompanyRoleMember}
		title = "Edited by another member"
		updated, err = service.Update(ctx, editor, f.TaskA, UpdateInput{ExpectedRevision: updated.Task.Revision, Title: &title})
		if err != nil {
			t.Fatal(err)
		}
		selection, err = service.ResultSelection(ctx, p, f.TaskA)
		if err != nil || selection.ApprovedAt != nil {
			t.Fatalf("Project editor authored approval: %+v, %v", selection, err)
		}
		done := StatusDone
		_, err = service.Update(ctx, editor, f.TaskA, UpdateInput{ExpectedRevision: updated.Task.Revision, Status: &done})
		if !errors.Is(err, ErrReviewPermissionRequired) {
			t.Fatalf("Project editor approval = %v, want permission error", err)
		}
		_, err = service.Update(ctx, p, f.TaskA, UpdateInput{ExpectedRevision: updated.Task.Revision, Status: &done})
		if err != nil {
			t.Fatal(err)
		}
		selection, err = service.ResultSelection(ctx, p, f.TaskA)
		if err != nil || selection.ApprovedAt == nil {
			t.Fatalf("explicit approval missing: %+v, %v", selection, err)
		}
	}

}
