//go:build integration

package approval

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// projectDecisionWorld is a Company whose Task Runs raise Approvals and
// Questions in ProjectA's Task chat, with people standing differently on
// that Project (ADR 0017).
type projectDecisionWorld struct {
	ctx       context.Context
	pool      *pgxpool.Pool
	fixture   dbtest.Fixture
	store     *db.Store
	bus       *events.Bus
	owner     auth.UserPrincipal
	decider   auth.UserPrincipal // member: project.read + project.approvals.resolve
	reader    auth.UserPrincipal // admin: project.read only
	outsider  auth.UserPrincipal // member: no grant
	deciderID uuid.UUID
}

func newProjectDecisionWorld(t *testing.T) projectDecisionWorld {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	w := projectDecisionWorld{
		ctx:     ctx,
		pool:    pool,
		fixture: fixture,
		store:   store,
		bus:     bus,
		owner: auth.UserPrincipal{
			UserID:    fixture.UserA,
			CompanyID: fixture.CompanyA,
			Role:      auth.CompanyRoleOwner,
		},
	}
	w.deciderID, w.decider = w.seedMember(t, "decider@example.com", auth.CompanyRoleMember)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, w.deciderID, "project.approvals.resolve")
	var readerID uuid.UUID
	readerID, w.reader = w.seedMember(t, "reader@example.com", auth.CompanyRoleAdmin)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, readerID, "project.read")
	_, w.outsider = w.seedMember(t, "outsider@example.com", auth.CompanyRoleMember)
	return w
}

func (w projectDecisionWorld) seedMember(
	t *testing.T,
	email string,
	role auth.CompanyRole,
) (uuid.UUID, auth.UserPrincipal) {
	t.Helper()
	id := dbtest.NewUUID(t)
	dbtest.MustExec(t, w.ctx, w.pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, $2, $2)`, id, email)
	dbtest.MustExec(t, w.ctx, w.pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, $3)`,
		w.fixture.CompanyA, id, string(role))
	return id, auth.UserPrincipal{UserID: id, CompanyID: w.fixture.CompanyA, Role: role}
}

// recipients lists who received an Inbox item about the entity.
func (w projectDecisionWorld) recipients(t *testing.T, entityID uuid.UUID) []uuid.UUID {
	t.Helper()
	rows, err := w.pool.Query(w.ctx,
		`SELECT recipient_user_id FROM inbox_items WHERE entity_id = $1 ORDER BY recipient_user_id`,
		entityID.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// A Task-origin Approval is decided inside its Project: a member holding
// project.approvals.resolve and no Company permission may reject it, an admin
// who only reads the Project may not decide it, and someone who cannot read
// the Project cannot see it at all. Approving runs the plan in that Project
// as the decider, so it also needs project.tasks.write there, and never the
// Company company.plan.apply; the Approval is addressed to the Project's
// deciders who may approve it.
func TestTaskApprovalIsDecidedInsideItsProject(t *testing.T) {
	w := newProjectDecisionWorld(t)
	participant := &planActionParticipantStub{}
	service, err := NewApprovalService(w.store, w.bus, participant, nil)
	if err != nil {
		t.Fatal(err)
	}
	origin := seedTaskRunningOrigin(t, w.ctx, w.pool, w.fixture)
	raised, _, err := service.RequestCompanyPlanApply(
		w.ctx, w.owner, origin, uuid.Must(uuid.NewV7()),
		[]byte(`{"artifactVersion":1,"desired":{"tasks":[]}}`),
	)
	if err != nil {
		t.Fatalf("RequestCompanyPlanApply() error = %v", err)
	}
	if got := w.recipients(t, raised.ID); !slices.Equal(got, []uuid.UUID{w.fixture.UserA}) {
		t.Errorf("Approval recipients = %v, want the one decider who may approve, %v", got, w.fixture.UserA)
	}

	// Outside the Project the Approval does not exist.
	if _, err := service.Get(w.ctx, w.outsider, raised.ID); !errors.Is(err, ErrApprovalNotFound) {
		t.Errorf("Get(outsider) error = %v, want ErrApprovalNotFound", err)
	}
	if page, err := service.List(w.ctx, w.outsider, ApprovalListOptions{}); err != nil || len(page.Items) != 0 {
		t.Errorf("List(outsider) = %d items, %v; want none", len(page.Items), err)
	}
	decision := ApprovalDecisionInput{ExpectedRevision: raised.Revision}
	if _, err := service.Reject(w.ctx, w.outsider, raised.ID, decision); !errors.Is(err, ErrApprovalNotFound) {
		t.Errorf("Reject(outsider) error = %v, want ErrApprovalNotFound", err)
	}

	// An admin who reads the Project sees it but may not decide it: the
	// Company role qualifies nobody inside a Project.
	if got, err := service.Get(w.ctx, w.reader, raised.ID); err != nil || got.ID != raised.ID {
		t.Errorf("Get(reader) = %+v, %v", got, err)
	}
	if _, err := service.Reject(
		w.ctx,
		w.reader,
		raised.ID,
		decision,
	); !errors.Is(
		err,
		auth.ErrProjectPermissionRequired,
	) {
		t.Errorf("Reject(admin with project.read) error = %v, want ErrProjectPermissionRequired", err)
	}

	// The member decider may not approve without project.tasks.write, the
	// permission the apply writes with; rejecting needs only theirs.
	if _, err := service.Approve(
		w.ctx,
		w.decider,
		raised.ID,
		decision,
	); !errors.Is(
		err,
		auth.ErrProjectPermissionRequired,
	) {
		t.Errorf(
			"Approve(member decider without project.tasks.write) error = %v, want ErrProjectPermissionRequired",
			err,
		)
	}
	rejected, err := service.Reject(w.ctx, w.decider, raised.ID, decision)
	if err != nil || rejected.Status != ApprovalStatusRejected {
		t.Fatalf("Reject(member decider) = %+v, %v", rejected, err)
	}

	// With project.tasks.write too, the member approves, although their
	// role holds no company.plan.apply, and the plan runs as them in the
	// Task chat's Project alone.
	dbtest.GrantProject(t, w.ctx, w.pool, w.fixture.CompanyA, w.fixture.ProjectA, w.deciderID,
		"project.approvals.resolve", "project.tasks.write")
	second, _, err := service.RequestCompanyPlanApply(
		w.ctx, w.owner, origin, uuid.Must(uuid.NewV7()),
		[]byte(`{"artifactVersion":1,"desired":{"tasks":[]}}`),
	)
	if err != nil {
		t.Fatalf("RequestCompanyPlanApply(second) error = %v", err)
	}
	if got := w.recipients(t, second.ID); !slices.Equal(got, sortedIDs(w.fixture.UserA, w.deciderID)) {
		t.Errorf("second Approval recipients = %v, want the owner and the member decider", got)
	}
	if want := (CompanyPlanActor{
		Principal:     auth.UserPrincipal{UserID: w.fixture.UserA, CompanyID: w.fixture.CompanyA},
		TaskProjectID: uuid.NullUUID{UUID: w.fixture.ProjectA, Valid: true},
	}); participant.prepared != want {
		t.Errorf(
			"prepared for %+v, want the requesting member bound to the Task's Project %+v",
			participant.prepared,
			want,
		)
	}
	approved, err := service.Approve(
		w.ctx,
		w.decider,
		second.ID,
		ApprovalDecisionInput{ExpectedRevision: second.Revision},
	)
	if err != nil || approved.Status != ApprovalStatusApproved {
		t.Fatalf("Approve(member decider) = %+v, %v", approved, err)
	}
	if participant.executed.Principal.UserID != w.deciderID ||
		participant.executed.TaskProjectID != (uuid.NullUUID{UUID: w.fixture.ProjectA, Valid: true}) {
		t.Errorf("executed for %+v, want the member decider in the Task's Project", participant.executed)
	}
}

// The Company Approvals page lists, counts and totals what the caller may
// decide (view=decide and the stats): a Task-origin Approval by
// project.approvals.resolve on its Project, a direct one by its origin
// member holding the Company approvals.resolve. A member decider with no
// Company permission sees and counts the Task Approval; an admin who only
// reads the Project counts nothing, though the plain list still shows them
// the Task Approval; a member outside the Project gets nothing, and none of
// them is refused.
func TestDecideViewAndStatsCoverWhatTheCallerMayDecide(t *testing.T) {
	w := newProjectDecisionWorld(t)
	approvals, err := NewApprovalService(w.store, w.bus, &planActionParticipantStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	questions, err := NewQuestionService(w.store, w.bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := []byte(`{"artifactVersion":1,"desired":{"tasks":[]}}`)
	taskApproval, _, err := approvals.RequestCompanyPlanApply(
		w.ctx, w.owner, seedTaskRunningOrigin(t, w.ctx, w.pool, w.fixture), uuid.Must(uuid.NewV7()), plan)
	if err != nil {
		t.Fatalf("raise the Task Approval: %v", err)
	}
	directApproval, _, err := approvals.RequestCompanyPlanApply(
		w.ctx, w.owner, seedDirectRunningOrigin(t, w.ctx, w.pool, w.fixture), uuid.Must(uuid.NewV7()), plan)
	if err != nil {
		t.Fatalf("raise the direct Approval: %v", err)
	}
	if _, _, err := questions.Ask(w.ctx, w.owner, seedTaskRunningOrigin(t, w.ctx, w.pool, w.fixture),
		uuid.Must(uuid.NewV7()), QuestionAskInput{Title: "Pick", Prompts: []QuestionPrompt{{
			ID: "pick", Prompt: "Which?", SelectionMode: QuestionSelectionSingle, Required: true,
			Options: []QuestionOption{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}},
		}}}); err != nil {
		t.Fatalf("ask the Task Question: %v", err)
	}

	approvalIDs := func(who auth.UserPrincipal, decide bool) []uuid.UUID {
		t.Helper()
		page, err := approvals.List(w.ctx, who, ApprovalListOptions{Decide: decide})
		if err != nil {
			t.Fatalf("List(%s, decide=%t) error = %v", who.Role, decide, err)
		}
		ids := make([]uuid.UUID, 0, len(page.Items))
		for _, item := range page.Items {
			ids = append(ids, item.ID)
		}
		return sortedIDs(ids...)
	}
	questionCount := func(who auth.UserPrincipal, decide bool) int {
		t.Helper()
		page, err := questions.List(w.ctx, who, QuestionListOptions{Decide: decide})
		if err != nil {
			t.Fatalf("List Questions(%s, decide=%t) error = %v", who.Role, decide, err)
		}
		return len(page.Items)
	}
	requested := func(who auth.UserPrincipal) int64 {
		t.Helper()
		stats, err := approvals.Stats(w.ctx, who, ApprovalStatsOptions{Since: time.Now().Add(-time.Hour)})
		if err != nil {
			t.Fatalf("Stats(%s) error = %v, want counts for everyone", who.Role, err)
		}
		return stats.Requested
	}

	for _, testCase := range []struct {
		name            string
		who             auth.UserPrincipal
		reads           []uuid.UUID
		decides         []uuid.UUID
		questionReads   int
		questionAnswers int
		counted         int64
	}{
		{"owner", w.owner, sortedIDs(taskApproval.ID, directApproval.ID), sortedIDs(taskApproval.ID, directApproval.ID), 1, 1, 2},
		{"member decider", w.decider, []uuid.UUID{taskApproval.ID}, []uuid.UUID{taskApproval.ID}, 1, 1, 1},
		// The admin holds the Company approvals.resolve, yet the direct
		// Approval is its origin member's alone to decide, so it counts
		// nowhere for them: the stats total the decide view.
		{"admin who only reads", w.reader, []uuid.UUID{taskApproval.ID}, []uuid.UUID{}, 1, 0, 0},
		{"member outside", w.outsider, []uuid.UUID{}, []uuid.UUID{}, 0, 0, 0},
	} {
		if got := approvalIDs(testCase.who, false); !slices.Equal(got, testCase.reads) {
			t.Errorf("%s reads Approvals %v, want %v", testCase.name, got, testCase.reads)
		}
		if got := approvalIDs(testCase.who, true); !slices.Equal(got, testCase.decides) {
			t.Errorf("%s may decide Approvals %v, want %v", testCase.name, got, testCase.decides)
		}
		if got := questionCount(testCase.who, false); got != testCase.questionReads {
			t.Errorf("%s reads %d Question(s), want %d", testCase.name, got, testCase.questionReads)
		}
		if got := questionCount(testCase.who, true); got != testCase.questionAnswers {
			t.Errorf("%s may answer %d Question(s), want %d", testCase.name, got, testCase.questionAnswers)
		}
		if got := requested(testCase.who); got != testCase.counted {
			t.Errorf("%s stats count %d Approval(s), want %d", testCase.name, got, testCase.counted)
		}
	}
}

// sortedIDs is ids in the order the recipient list reads them.
func sortedIDs(ids ...uuid.UUID) []uuid.UUID {
	sorted := slices.Clone(ids)
	slices.SortFunc(sorted, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	return sorted
}

// A Task-origin Question is answered inside its Project: only readers who
// hold project.approvals.resolve receive, answer and withdraw it.
func TestTaskQuestionIsAnsweredInsideItsProject(t *testing.T) {
	w := newProjectDecisionWorld(t)
	service, err := NewQuestionService(w.store, w.bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	origin := seedTaskRunningOrigin(t, w.ctx, w.pool, w.fixture)
	input := QuestionAskInput{Title: "Choose a target", Prompts: []QuestionPrompt{{
		ID: "target", Prompt: "Which target?", SelectionMode: QuestionSelectionSingle,
		Required: true, Options: []QuestionOption{{ID: "a", Label: "Target A"}, {ID: "b", Label: "Target B"}},
	}}}
	asked, _, err := service.Ask(w.ctx, w.owner, origin, uuid.Must(uuid.NewV7()), input)
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	want := []uuid.UUID{w.fixture.UserA, w.deciderID}
	slices.SortFunc(want, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	if got := w.recipients(t, asked.ID); !slices.Equal(got, want) {
		t.Errorf("Question recipients = %v, want the Project's deciders %v", got, want)
	}
	answer := QuestionAnswerInput{
		ExpectedRevision: asked.Revision,
		Answers:          []QuestionAnswer{{PromptID: "target", OptionIDs: []string{"a"}}},
	}
	if _, err := service.Get(w.ctx, w.outsider, asked.ID); !errors.Is(err, ErrQuestionNotFound) {
		t.Errorf("Get(outsider) error = %v, want ErrQuestionNotFound", err)
	}
	if page, err := service.List(w.ctx, w.outsider, QuestionListOptions{}); err != nil || len(page.Items) != 0 {
		t.Errorf("List(outsider) = %d items, %v; want none", len(page.Items), err)
	}
	if _, err := service.Answer(w.ctx, w.outsider, asked.ID, answer); !errors.Is(err, ErrQuestionNotFound) {
		t.Errorf("Answer(outsider) error = %v, want ErrQuestionNotFound", err)
	}
	if page, err := service.List(w.ctx, w.reader, QuestionListOptions{}); err != nil || len(page.Items) != 1 {
		t.Errorf("List(reader) = %d items, %v; want the Question", len(page.Items), err)
	}
	if _, err := service.Answer(
		w.ctx,
		w.reader,
		asked.ID,
		answer,
	); !errors.Is(
		err,
		auth.ErrProjectPermissionRequired,
	) {
		t.Errorf("Answer(reader) error = %v, want ErrProjectPermissionRequired", err)
	}
	answered, err := service.Answer(w.ctx, w.decider, asked.ID, answer)
	if err != nil || answered.Status != QuestionStatusAnswered {
		t.Fatalf("Answer(member decider) = %+v, %v", answered, err)
	}

	second, _, err := service.Ask(w.ctx, w.owner, origin, uuid.Must(uuid.NewV7()), input)
	if err != nil {
		t.Fatalf("Ask(second) error = %v", err)
	}
	if _, err := service.Withdraw(
		w.ctx,
		w.reader,
		second.ID,
		second.Revision,
	); !errors.Is(
		err,
		auth.ErrProjectPermissionRequired,
	) {
		t.Errorf("Withdraw(admin with project.read) error = %v, want ErrProjectPermissionRequired", err)
	}
	withdrawn, err := service.Withdraw(w.ctx, w.decider, second.ID, second.Revision)
	if err != nil || withdrawn.Status != QuestionStatusWithdrawn {
		t.Fatalf("Withdraw(member decider) = %+v, %v", withdrawn, err)
	}
}

// When no reader of the Task chat's Project holds project.approvals.resolve,
// nobody could ever decide what the Run raises: the raise is refused with
// ErrNoEligibleResolver, nothing is stored, and the Run fails with that
// reason: never retried, and its member is told why in a run_failed Inbox
// item worded for the Run's provider.
func TestTaskRaiseWithNoEligibleResolverStopsTheRun(t *testing.T) {
	w := newProjectDecisionWorld(t)
	dbtest.MustExec(t, w.ctx, w.pool, `
		DELETE FROM project_grants
		WHERE project_id = $1 AND permission = 'project.approvals.resolve'`, w.fixture.ProjectA)
	approvals, err := NewApprovalService(w.store, w.bus, &planActionParticipantStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	questions, err := NewQuestionService(w.store, w.bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, raise := range map[string]func(origin execution.RunOrigin) error{
		"approval": func(origin execution.RunOrigin) error {
			_, _, err := approvals.RequestCompanyPlanApply(
				w.ctx, w.owner, origin, uuid.Must(uuid.NewV7()),
				[]byte(`{"artifactVersion":1,"desired":{"tasks":[]}}`),
			)
			return err
		},
		"question": func(origin execution.RunOrigin) error {
			_, _, err := questions.Ask(w.ctx, w.owner, origin, uuid.Must(uuid.NewV7()), QuestionAskInput{
				Title: "Choose", Prompts: []QuestionPrompt{{
					ID: "p", Prompt: "Which?", SelectionMode: QuestionSelectionSingle, Required: true,
					Options: []QuestionOption{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}},
				}},
			})
			return err
		},
	} {
		dbtest.MustExec(t, w.ctx, w.pool,
			`UPDATE runs SET status = 'completed' WHERE conversation_id = $1 AND status = 'running'`,
			w.fixture.ConversationA)
		origin := seedTaskRunningOrigin(t, w.ctx, w.pool, w.fixture)
		if err := raise(origin); !errors.Is(err, auth.ErrNoEligibleResolver) {
			t.Fatalf("%s: raise error = %v, want ErrNoEligibleResolver", name, err)
		}
		var status, reason, runError, body string
		var children, tokens, notices int
		if err := w.pool.QueryRow(w.ctx, `
			SELECT status, COALESCE(failure_reason, ''), COALESCE(error, ''),
			       (SELECT count(*) FROM runs AS child WHERE child.parent_run_id = runs.id),
			       (SELECT count(*) FROM run_tokens WHERE run_tokens.run_id = runs.id),
			       (SELECT count(*) FROM inbox_items WHERE recipient_user_id = $2 AND kind = 'run_failed'
			          AND entity_type = 'run' AND entity_id = runs.id::text),
			       COALESCE((SELECT body FROM inbox_items WHERE recipient_user_id = $2 AND kind = 'run_failed'
			          AND entity_type = 'run' AND entity_id = runs.id::text), '')
			FROM runs WHERE id = $1`,
			origin.RunID, w.fixture.UserA,
		).Scan(&status, &reason, &runError, &children, &tokens, &notices, &body); err != nil {
			t.Fatal(err)
		}
		if status != "failed" || reason != "no_eligible_resolver" || runError != "" || children != 0 ||
			tokens != 0 {
			t.Errorf(
				"%s: Run = %s (%q, error %q, %d retries, %d tokens), want failed with no_eligible_resolver, "+
					"no provider text, no retry and no token",
				name,
				status,
				reason,
				runError,
				children,
				tokens,
			)
		}
		if notices != 1 || !strings.Contains(body, "nobody who can see this work may make") ||
			!strings.Contains(body, "Codex") {
			t.Errorf(
				"%s: run_failed items for the authorizer = %d (%q), want one naming the reason and the provider",
				name,
				notices,
				body,
			)
		}
		var stored int
		if err := w.pool.QueryRow(w.ctx, `
			SELECT (SELECT count(*) FROM approvals WHERE origin_run_id = $1)
			     + (SELECT count(*) FROM questions WHERE origin_run_id = $1)`,
			origin.RunID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != 0 {
			t.Errorf("%s: %d requests stored for a raise nobody may decide", name, stored)
		}
	}
}

// When the only reader holding project.approvals.resolve lacks
// project.tasks.write, a Company Plan Approval has nobody who may approve it
// (approving writes the plan in the Project), so the raise is refused with
// ErrNoEligibleResolver; a Question they can answer is still asked.
func TestTaskApprovalWithOnlyMemberDecidersIsRefused(t *testing.T) {
	w := newProjectDecisionWorld(t)
	dbtest.MustExec(t, w.ctx, w.pool, `
		DELETE FROM project_grants
		WHERE project_id = $1 AND user_id <> $2 AND permission = 'project.approvals.resolve'`,
		w.fixture.ProjectA, w.deciderID)
	approvals, err := NewApprovalService(w.store, w.bus, &planActionParticipantStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	questions, err := NewQuestionService(w.store, w.bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	origin := seedTaskRunningOrigin(t, w.ctx, w.pool, w.fixture)
	asked, _, err := questions.Ask(w.ctx, w.owner, origin, uuid.Must(uuid.NewV7()), QuestionAskInput{
		Title: "Choose", Prompts: []QuestionPrompt{{
			ID: "p", Prompt: "Which?", SelectionMode: QuestionSelectionSingle, Required: true,
			Options: []QuestionOption{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}},
		}},
	})
	if err != nil {
		t.Fatalf("Ask() with a member decider error = %v", err)
	}
	if got := w.recipients(t, asked.ID); !slices.Equal(got, []uuid.UUID{w.deciderID}) {
		t.Errorf("Question recipients = %v, want the member decider", got)
	}
	_, _, err = approvals.RequestCompanyPlanApply(
		w.ctx, w.owner, origin, uuid.Must(uuid.NewV7()),
		[]byte(`{"artifactVersion":1,"desired":{"tasks":[]}}`),
	)
	if !errors.Is(err, auth.ErrNoEligibleResolver) {
		t.Fatalf(
			"RequestCompanyPlanApply() with only a member decider error = %v, want ErrNoEligibleResolver",
			err,
		)
	}
}
