//go:build integration

package task

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestTaskCompletionRequiresDeliveredSuggestionsApplied(t *testing.T) {
	for _, atomic := range []bool{false, true} {
		name := "separate decisions"
		if atomic {
			name = "atomic acceptance"
		}
		t.Run(name, func(t *testing.T) { testTaskResultApplication(t, atomic, false, false) })
	}
}

func TestProposalOnlyTaskCompletesWithoutApplying(t *testing.T) {
	testTaskResultApplication(t, false, true, false)
}

func TestUnassignedMemberCannotSelectTaskResults(t *testing.T) {
	testTaskResultApplication(t, true, false, true)
}

func testTaskResultApplication(t *testing.T, atomic, proposalOnly, reviewer bool) {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus := newTestBus(t)
	tasks, _ := NewTaskService(store, bus, nil)
	pages := canvas.NewPageService(store, bus)
	presentations := canvas.NewPresentationService(store, bus, nil)
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	if proposalOnly {
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`UPDATE tasks SET result_proposal_only=true,result_review_required=false WHERE id=$1`,
			f.TaskA,
		)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE runs SET status='running',execution_authorized_by_user_id=$2,task_execution_generation=(SELECT execution_generation FROM tasks WHERE id=$3) WHERE id=$1`,
		f.RunA,
		f.UserA,
		f.TaskA,
	)
	createProposal := func(title string, number int) (canvas.PageCreateResult, canvas.PageEditResult, uuid.UUID) {
		page, err := pages.CreatePage(
			ctx,
			p,
			f.ProjectA,
			canvas.PageCreateInput{RequestKey: uuid.New(), Title: title, Markdown: "Shared text"},
		)
		if err != nil {
			t.Fatal(err)
		}
		read, err := pages.ReadPageForRun(ctx, p, f.RunA, page.PageID)
		if err != nil {
			t.Fatal(err)
		}
		addition := wiki.BlocksFromMarkdown("Required update").Content[0]
		change, err := pages.EditPageForRun(
			ctx,
			p,
			f.RunA,
			page.PageID,
			canvas.PageEditInput{
				RequestKey: uuid.New(),
				Sequence:   read.Sequence,
				Summary:    "Update report",
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
		var output uuid.UUID
		if err = pool.QueryRow(ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id,presentation_number, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,$4,'Proposed update',$4,$3,$5, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`, f.CompanyA, f.RunA, f.UserA, title, number).
			Scan(&output); err != nil {
			t.Fatal(err)
		}
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`UPDATE project_presentations SET output_id=$2 WHERE id=$1`,
			capture.ID,
			output,
		)
		return page, change, output
	}
	page, change, output := createProposal("Report", 1)
	outputs := []uuid.UUID{output}
	var second canvas.PageChangeDecision
	if atomic {
		otherPage, otherChange, otherOutput := createProposal("Appendix", 2)
		otherDetail, e := pages.PageChangeDetail(ctx, p, f.ProjectA, otherPage.PageID, otherChange.ID)
		if e != nil {
			t.Fatal(e)
		}
		second = canvas.PageChangeDecision{
			ProjectID: f.ProjectA,
			PageID:    otherPage.PageID,
			ChangeID:  otherChange.ID,
			Input: canvas.PageChangeReviewInput{
				RequestKey:  uuid.New(),
				Fingerprint: otherDetail.Fingerprint,
				Action:      "apply",
			},
		}
		outputs = append(outputs, otherOutput)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, f.RunA)
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET status='in_review' WHERE id=$1`, f.TaskA)
	if reviewer {
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
			f.CompanyA,
			f.UserB,
		)
		dbtest.GrantProject(
			t,
			ctx,
			pool,
			f.CompanyA,
			f.ProjectA,
			f.UserB,
			"project.tasks.write",
			"project.files.manage",
		)
		dbtest.MustExec(t, ctx, pool, `UPDATE outputs SET visibility='company' WHERE company_id=$1`, f.CompanyA)
		p = auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: auth.CompanyRoleMember}
	}
	current, err := tasks.Get(ctx, p, f.TaskA, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reviewer {
		_, err = tasks.Update(
			ctx,
			p,
			f.TaskA,
			UpdateInput{
				ExpectedRevision: current.Revision,
				ResultPolicy:     &ResultPolicy{ProposalOnly: true},
			},
		)
		if !errors.Is(err, ErrReviewPermissionRequired) {
			t.Fatalf("reviewer changed result policy: %v", err)
		}
		_, err = tasks.Update(ctx, p, f.TaskA, UpdateInput{
			ExpectedRevision: current.Revision,
			ResultSelection:  &ResultSelectionInput{RequestKey: uuid.New(), OutputIDs: outputs},
		})
		if !errors.Is(err, ErrReviewPermissionRequired) {
			t.Fatalf("unassigned member selected results: %v", err)
		}
		return
	}
	done := StatusDone
	_, err = tasks.Update(ctx, p, f.TaskA, UpdateInput{ExpectedRevision: current.Revision, Status: &done})
	if !errors.Is(err, ErrResultSelectionRequired) {
		t.Fatalf("completion before application error = %v, want conflict", err)
	}
	selected, err := tasks.Update(
		ctx,
		p,
		f.TaskA,
		UpdateInput{
			ExpectedRevision: current.Revision,
			ResultSelection:  &ResultSelectionInput{RequestKey: uuid.New(), OutputIDs: outputs},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	current = selected.Task
	if proposalOnly {
		if current.Status != StatusDone {
			t.Fatalf("proposal-only optional review Task = %s, want done", current.Status)
		}
		unchanged, e := pages.Page(ctx, p, f.ProjectA, page.PageID)
		if e != nil || strings.Contains(string(unchanged.Content), "Required update") {
			t.Fatalf("proposal-only completion applied content: %+v, %v", unchanged, e)
		}
		return
	}

	_, err = tasks.Update(ctx, p, f.TaskA, UpdateInput{ExpectedRevision: current.Revision, Status: &done})
	if !errors.Is(err, ErrResultApplicationRequired) {
		t.Fatalf("unapplied selected result approval error = %v", err)
	}

	detail, err := pages.PageChangeDetail(ctx, p, f.ProjectA, page.PageID, change.ID)
	if err != nil {
		t.Fatal(err)
	}
	var result MutationResult
	if atomic {
		decision := canvas.PageChangeDecision{
			ProjectID: f.ProjectA,
			PageID:    page.PageID,
			ChangeID:  change.ID,
			Input: canvas.PageChangeReviewInput{
				RequestKey:  uuid.New(),
				Fingerprint: detail.Fingerprint,
				Action:      "apply",
			},
		}
		selection := &ResultSelectionInput{
			RequestKey:    uuid.New(),
			OutputIDs:     outputs,
			AcceptChanges: []canvas.PageChangeDecision{decision, second},
		}
		slices.SortFunc(
			selection.AcceptChanges,
			func(a, b canvas.PageChangeDecision) int { return strings.Compare(a.PageID.String(), b.PageID.String()) },
		)
		originalFingerprint := selection.AcceptChanges[1].Input.Fingerprint
		selection.AcceptChanges[1].Input.Fingerprint = strings.Repeat("0", 64)
		_, err = tasks.Update(
			ctx,
			p,
			f.TaskA,
			UpdateInput{ExpectedRevision: current.Revision, Status: &done, ResultSelection: selection},
		)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("stale combined review error = %v", err)
		}
		unchanged, e := tasks.ResultSelection(ctx, p, f.TaskA)
		if e != nil || unchanged.TaskRevision != current.Revision {
			t.Fatalf("failed acceptance changed selection: %+v, %v", unchanged, e)
		}
		for _, review := range selection.AcceptChanges {
			after, e := pages.PageChangeDetail(ctx, p, f.ProjectA, review.PageID, review.ChangeID)
			if e != nil || after.Status != "proposed" {
				t.Fatalf("failed combined action applied a document: %+v, %v", after, e)
			}
		}
		selection.AcceptChanges[1].Input.Fingerprint = originalFingerprint
		result, err = tasks.Update(
			ctx,
			p,
			f.TaskA,
			UpdateInput{ExpectedRevision: current.Revision, Status: &done, ResultSelection: selection},
		)
		if err != nil {
			t.Fatal(err)
		}
		replay, e := tasks.Update(
			ctx,
			p,
			f.TaskA,
			UpdateInput{ExpectedRevision: current.Revision, Status: &done, ResultSelection: selection},
		)
		if e != nil || replay.Task.Revision != result.Task.Revision {
			t.Fatalf("acceptance replay = %+v, %v", replay, e)
		}

	} else {
		_, err = pages.ReviewPageChange(
			ctx,
			p,
			f.ProjectA,
			page.PageID,
			change.ID,
			canvas.PageChangeReviewInput{
				RequestKey:  uuid.New(),
				Fingerprint: detail.Fingerprint,
				Action:      "apply",
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		result, err = tasks.Update(
			ctx,
			p,
			f.TaskA,
			UpdateInput{ExpectedRevision: current.Revision, Status: &done},
		)
	}

	if err != nil || result.Task.Status != StatusDone {
		t.Fatalf("completion after application = %+v, %v", result, err)
	}
	state, err := tasks.ResultSelection(ctx, p, f.TaskA)
	if err != nil || len(state.OutputIDs) != len(outputs) || state.OutputIDs[0] != output ||
		state.ApprovedAt == nil {
		t.Fatalf("approved selection = %+v, %v", state, err)
	}
}

func TestTaskResultPolicyIsAssignedBeforeExecution(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	service, _ := NewTaskService(store, newTestBus(t), nil)
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	dbtest.MustExec(t, ctx, pool, `UPDATE companies SET next_task_number=2 WHERE id=$1`, f.CompanyA)
	created, err := service.Create(
		ctx,
		p,
		CreateInput{
			Key:           "proposal_review",
			ProjectID:     f.ProjectA,
			MilestoneType: "product",
			Title:         "Review a proposal",
			ResultPolicy:  &ResultPolicy{ProposalOnly: true, ReviewRequired: false},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := service.ResultSelection(ctx, p, created.Task.ID)
	if err != nil || !policy.ProposalOnly || policy.ReviewRequired || !policy.PolicyEditable {
		t.Fatalf("assigned policy = %+v, %v", policy, err)
	}
	existing, err := service.Get(ctx, p, f.TaskA, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Update(
		ctx,
		p,
		f.TaskA,
		UpdateInput{ExpectedRevision: existing.Revision, ResultPolicy: &ResultPolicy{ProposalOnly: true}},
	)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("changing policy after execution = %v, want conflict", err)
	}
}

func TestRetainedDeliveryResultSelection(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"completed", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			pool, _ := dbtest.StartPostgres(t)
			ctx := t.Context()
			if err := db.MigrateUp(ctx, pool); err != nil {
				t.Fatal(err)
			}
			f := dbtest.SeedFixture(t, ctx, pool)
			dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status=$2, execution_authorized_by_user_id=$3,
    task_execution_generation=(SELECT execution_generation FROM tasks WHERE id=$4) WHERE id=$1`, f.RunA, status, f.UserA, f.TaskA)
			q := generated.New(pool)
			output, err := q.InsertOutput(ctx, generated.InsertOutputParams{
				CompanyID:               f.CompanyA,
				RunID:                   f.RunA,
				PresentationNumber:      1,
				Title:                   "Retained result",
				Summary:                 "Keep evidence",
				PresentationFingerprint: "retained-result",
				Type:                    "file",
				Payload:                 []byte(`{}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			dbtest.MustExec(t, ctx, pool, `DELETE FROM runs WHERE id=$1`, f.RunA)
			store, _ := db.NewStore(pool)
			service, _ := NewTaskService(store, newTestBus(t), nil)
			p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
			current, err := service.Get(ctx, p, f.TaskA, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Update(ctx, p, f.TaskA, UpdateInput{
				ExpectedRevision: current.Revision,
				ResultSelection: &ResultSelectionInput{
					RequestKey: uuid.New(),
					OutputIDs:  []uuid.UUID{output.ID},
				},
			})
			if status != "completed" {
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("select retained %s delivery = %v, want conflict", status, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			selected, err := service.ResultSelection(ctx, p, f.TaskA)
			if err != nil || len(selected.OutputIDs) != 1 || selected.OutputIDs[0] != output.ID {
				t.Fatalf("retained selection = %+v, %v, want output %s", selected, err, output.ID)
			}
		})
	}
}
