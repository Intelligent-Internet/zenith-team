//go:build integration

package company

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/approval"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	taskdomain "github.com/Intelligent-Internet/zenith-team/server/internal/task"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// planCounts is what a PlanApplyResult says, compared as one value.
type planCounts struct {
	Created, Updated, Deleted, Skipped int
}

func countsOf(result PlanApplyResult) planCounts {
	return planCounts{
		Created: result.Created, Updated: result.Updated, Deleted: result.Deleted,
		Skipped: len(result.Skipped),
	}
}

// planAccessWorld is the fixture Company with a Company Plan service, for
// applies by people standing differently on its Projects (ADR 0017).
type planAccessWorld struct {
	ctx     context.Context
	pool    *pgxpool.Pool
	fixture dbtest.Fixture
	store   *db.Store
	bus     *events.Bus
	service *DefaultPlanService
	owner   auth.UserPrincipal
}

func newPlanAccessWorld(t *testing.T) planAccessWorld {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	// The fixture's task_a holds number 1.
	dbtest.MustExec(t, ctx, pool, `UPDATE companies SET next_task_number = 2 WHERE id = $1`, fixture.CompanyA)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewPlanService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	return planAccessWorld{
		ctx:     ctx,
		pool:    pool,
		fixture: fixture,
		store:   store,
		bus:     bus,
		service: service,
		owner: auth.UserPrincipal{
			UserID:    fixture.UserA,
			CompanyID: fixture.CompanyA,
			Role:      auth.CompanyRoleOwner,
		},
	}
}

func (w planAccessWorld) seedMember(t *testing.T, email string, role auth.CompanyRole) auth.UserPrincipal {
	t.Helper()
	id := dbtest.NewUUID(t)
	dbtest.MustExec(t, w.ctx, w.pool, `INSERT INTO users (id, email, display_name) VALUES ($1, $2, $2)`, id, email)
	dbtest.MustExec(t, w.ctx, w.pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, $3)`,
		w.fixture.CompanyA, id, string(role))
	return auth.UserPrincipal{UserID: id, CompanyID: w.fixture.CompanyA, Role: role}
}

// read is the plan as the person reads it, the way the export reads it:
// every Project they read, its Tasks and edges, and the base of the same,
// with each Project they may not change marked read-only.
func (w planAccessWorld) read(t *testing.T, principal auth.UserPrincipal) (PlanDocument, PlanBase) {
	t.Helper()
	var document PlanDocument
	var base PlanBase
	err := w.store.WithinTx(w.ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		locked, err := lockPlanForApply(w.ctx, q, planScopeRule{
			actor: approval.CompanyPlanActor{Principal: principal},
		}, PlanArtifact{CompanySlug: "schema-a"})
		if err != nil {
			return err
		}
		base = locked.snapshot()
		readOnly := func(key string) bool {
			return !locked.access[key].Can(auth.ProjectPermissionTasksWrite)
		}
		for index := range base.Projects {
			base.Projects[index].ReadOnly = readOnly(base.Projects[index].Key)
		}
		document = PlanDocument{
			FormatVersion: planFormatVersion, Company: "schema-a",
			Projects: []PlanProject{}, Tasks: []PlanTask{},
		}
		keys := map[uuid.UUID]string{}
		for _, key := range sortedKeys(locked.projects) {
			project := locked.projects[key]
			keys[project.ID] = key
			document.Projects = append(document.Projects, PlanProject{
				Key: key, Title: project.Title, Description: project.Description,
				Archived: project.ArchivedAt.Valid, ReadOnly: readOnly(key),
			})
		}
		taskKeys := map[uuid.UUID]string{}
		for key, value := range locked.tasks {
			taskKeys[value.ID] = key
		}
		for _, key := range sortedPersistedTaskKeys(locked.tasks) {
			value := locked.tasks[key]
			planned := PlanTask{
				Key: key, ProjectKey: keys[value.ProjectID],
				MilestoneType: taskdomain.MilestoneType(value.MilestoneType), Title: value.Title,
				Description: value.Description, Status: value.Status, Priority: value.Priority,
				DependsOn: []PlanTaskDependency{},
			}
			if value.ExecutedByAgentID.Valid {
				planned.AgentID = value.ExecutedByAgentID.UUID.String()
			}
			for _, edge := range locked.dependencies {
				if edge.DependentTaskID == value.ID {
					planned.DependsOn = append(
						planned.DependsOn,
						PlanTaskDependency{TaskKey: taskKeys[edge.PrerequisiteTaskID]},
					)
				}
			}
			document.Tasks = append(document.Tasks, planned)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read the plan: %v", err)
	}
	return document, base
}

func (w planAccessWorld) artifact(base PlanBase, desired PlanDocument) PlanArtifact {
	return PlanArtifact{
		ArtifactVersion: 1, CompanyID: w.fixture.CompanyA.String(), CompanySlug: "schema-a",
		CreatedAt: time.Now().UTC(), Base: base, Desired: desired,
	}
}

// apply is the person's own apply of the plan they read, changed by edit.
func (w planAccessWorld) apply(
	t *testing.T,
	principal auth.UserPrincipal,
	edit func(*PlanDocument),
) (PlanApplyResult, error) {
	t.Helper()
	document, base := w.read(t, principal)
	edit(&document)
	return w.service.ApplyCompanyPlan(w.ctx, principal, w.artifact(base, document))
}

func (w planAccessWorld) projectID(t *testing.T, key string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := w.pool.QueryRow(w.ctx, `SELECT id FROM projects WHERE company_id = $1 AND key = $2`,
		w.fixture.CompanyA, key).Scan(&id); err != nil {
		t.Fatalf("read Project %q: %v", key, err)
	}
	return id
}

// taskKeys lists the Company's Task keys, whoever can read them.
func (w planAccessWorld) taskKeys(t *testing.T) []string {
	t.Helper()
	rows, err := w.pool.Query(w.ctx, `SELECT key FROM tasks WHERE company_id = $1 ORDER BY key`, w.fixture.CompanyA)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func openPlanTask(key, projectKey string) PlanTask {
	return PlanTask{
		Key: key, ProjectKey: projectKey, MilestoneType: "product", Title: key,
		Description: "", Status: "todo", Priority: "medium", DependsOn: []PlanTaskDependency{},
	}
}

func withoutTask(document *PlanDocument, key string) {
	document.Tasks = slices.DeleteFunc(document.Tasks, func(task PlanTask) bool { return task.Key == key })
}

// lay out adds three Projects with one open Task each, as the owner.
func (w planAccessWorld) layOut(t *testing.T) {
	t.Helper()
	result, err := w.apply(t, w.owner, func(document *PlanDocument) {
		for _, key := range []string{"secret", "shared", "mine"} {
			document.Projects = append(document.Projects, PlanProject{Key: key, Title: key})
			document.Tasks = append(document.Tasks, openPlanTask(key+"_task", key))
		}
	})
	if err != nil || countsOf(result) != (planCounts{Created: 6}) {
		t.Fatalf("lay out the Projects = %+v, %v", result, err)
	}
}

// A Company Plan apply reaches only the Projects its applier may write
// (ADR 0017): omitting the open Task of a Project they cannot read, or can
// only read, deletes nothing, while the Project they write applies in full.
func TestCompanyPlanApplyReachesOnlyTheApplierWritableProjects(t *testing.T) {
	w := newPlanAccessWorld(t)
	w.layOut(t)
	admin := w.seedMember(t, "admin@example.com", auth.CompanyRoleAdmin)
	dbtest.GrantProject(t, w.ctx, w.pool, w.fixture.CompanyA, w.projectID(t, "mine"), admin.UserID)
	dbtest.GrantProject(
		t,
		w.ctx,
		w.pool,
		w.fixture.CompanyA,
		w.projectID(t, "shared"),
		admin.UserID,
		"project.read",
	)

	document, _ := w.read(t, admin)
	var read []string
	for _, project := range document.Projects {
		read = append(read, project.Key)
	}
	if !slices.Equal(read, []string{"mine", "shared"}) {
		t.Fatalf("the admin reads Projects %v, want mine and shared", read)
	}

	// The admin's plan drops both Tasks they read and adds one where they
	// write. The hidden Projects are not in it at all.
	result, err := w.apply(t, admin, func(document *PlanDocument) {
		withoutTask(document, "mine_task")
		withoutTask(document, "shared_task")
		document.Tasks = append(document.Tasks, openPlanTask("mine_next", "mine"))
	})
	if err != nil || countsOf(result) != (planCounts{Created: 1, Deleted: 1}) {
		t.Fatalf("ApplyCompanyPlan(admin) = %+v, %v; want one create and one delete", result, err)
	}
	if got := w.taskKeys(t); !slices.Equal(got, []string{"mine_next", "secret_task", "shared_task", "task_a"}) {
		t.Fatalf("Tasks after the admin's apply = %v", got)
	}
	var assignee uuid.UUID
	if err := w.pool.QueryRow(w.ctx, `SELECT assignee_user_id FROM tasks WHERE key = 'mine_next'`).
		Scan(&assignee); err != nil ||
		assignee != admin.UserID {
		t.Fatalf("mine_next assignee = %v, %v; want the admin who applied", assignee, err)
	}

	// A Project the admin may write only after reading the plan is outside
	// it: a plan that does not name it applies and leaves its Task alone,
	// and one that names it is stale.
	document, base := w.read(t, admin)
	dbtest.GrantProject(t, w.ctx, w.pool, w.fixture.CompanyA, w.projectID(t, "secret"), admin.UserID)
	named := document
	named.Projects = append(slices.Clone(document.Projects), PlanProject{Key: "secret", Title: "secret"})
	if _, err := w.service.ApplyCompanyPlan(
		w.ctx,
		admin,
		w.artifact(base, named),
	); !errors.Is(
		err,
		ErrPlanConflict,
	) {
		t.Fatalf("a plan naming a Project granted after it was read error = %v, want ErrPlanConflict", err)
	}
	result, err = w.service.ApplyCompanyPlan(w.ctx, admin, w.artifact(base, document))
	if err != nil || countsOf(result) != (planCounts{}) {
		t.Fatalf("the plan read before the grant = %+v, %v; want no change", result, err)
	}
	if got := w.taskKeys(t); !slices.Contains(got, "secret_task") {
		t.Fatalf("Tasks after the older plan = %v, want secret_task kept", got)
	}
}

// A key naming a Project the applier cannot read behaves exactly like a key
// nobody has used (ADR 0017): archiving it is the same invalid input, and
// creating it fails on the taken key without touching the hidden Project.
func TestCompanyPlanApplyTreatsAHiddenKeyLikeAnUnknownKey(t *testing.T) {
	w := newPlanAccessWorld(t)
	w.layOut(t)
	admin := w.seedMember(t, "admin@example.com", auth.CompanyRoleAdmin)
	dbtest.GrantProject(t, w.ctx, w.pool, w.fixture.CompanyA, w.projectID(t, "mine"), admin.UserID)

	outcomes := map[string]error{}
	for _, key := range []string{"secret", "never_used"} {
		_, outcomes[key] = w.apply(t, admin, func(document *PlanDocument) {
			document.Projects = append(document.Projects, PlanProject{Key: key, Title: key, Archived: true})
		})
	}
	if !errors.Is(outcomes["secret"], ErrPlanInvalidInput) ||
		!errors.Is(outcomes["never_used"], ErrPlanInvalidInput) {
		t.Fatalf("archiving a hidden key = %v, an unused key = %v; want the same ErrPlanInvalidInput",
			outcomes["secret"], outcomes["never_used"])
	}

	_, err := w.apply(t, admin, func(document *PlanDocument) {
		document.Projects = append(document.Projects, PlanProject{Key: "secret", Title: "Taken over"})
		document.Tasks = append(document.Tasks, openPlanTask("takeover", "secret"))
	})
	if !errors.Is(err, ErrPlanConflict) {
		t.Fatalf("creating a hidden key error = %v, want ErrPlanConflict", err)
	}
	var title string
	var grants int
	if err := w.pool.QueryRow(
		w.ctx,
		`
		SELECT project.title,
		       (SELECT count(*) FROM project_grants WHERE project_id = project.id AND user_id = $2)
		FROM projects AS project WHERE project.company_id = $1 AND project.key = 'secret'`,
		w.fixture.CompanyA,
		admin.UserID,
	).Scan(&title, &grants); err != nil || title != "secret" || grants != 0 {
		t.Fatalf("hidden Project after the refused create = %q, %d grants, %v", title, grants, err)
	}
}

// Archiving a Project, or emptying it by leaving it out of the plan, needs
// project.archive (ADR 0017). A member who only writes its Tasks gets
// neither: the Project and every one of its Tasks stay as they are, and the
// apply lists the archive as skipped. With project.archive the same plans
// apply.
func TestCompanyPlanApplyHoldsAnArchiveWithoutProjectArchive(t *testing.T) {
	w := newPlanAccessWorld(t)
	w.layOut(t)
	writer := w.seedMember(t, "writer@example.com", auth.CompanyRoleMember)
	mine := w.projectID(t, "mine")
	dbtest.GrantProject(t, w.ctx, w.pool, w.fixture.CompanyA, mine, writer.UserID)
	// The writer is responsible for mine_task, so only project.archive
	// stands between them and deleting it.
	dbtest.MustExec(
		t,
		w.ctx,
		w.pool,
		`UPDATE tasks SET assignee_user_id = $1 WHERE key = 'mine_task'`,
		writer.UserID,
	)
	actor := approval.CompanyPlanActor{Principal: writer, TaskProjectID: uuid.NullUUID{UUID: mine, Valid: true}}
	archiveMine := func(document *PlanDocument) {
		withoutTask(document, "mine_task")
		for index := range document.Projects {
			if document.Projects[index].Key == "mine" {
				document.Projects[index].Archived = true
			}
		}
	}
	leaveMineOut := func(document *PlanDocument) {
		withoutTask(document, "mine_task")
		document.Projects = slices.DeleteFunc(document.Projects, func(project PlanProject) bool {
			return project.Key == "mine"
		})
	}
	archived := func() bool {
		t.Helper()
		var archived bool
		if err := w.pool.QueryRow(w.ctx, `SELECT archived_at IS NOT NULL FROM projects WHERE id = $1`, mine).
			Scan(&archived); err != nil {
			t.Fatal(err)
		}
		return archived
	}

	want := []PlanSkip{
		{EntityType: "project", Key: "mine", Operation: "archive", Permission: auth.ProjectPermissionArchive},
	}
	for name, edit := range map[string]func(*PlanDocument){"archived": archiveMine, "left out": leaveMineOut} {
		result := w.approveAs(t, actor, edit)
		if countsOf(result) != (planCounts{Skipped: 1}) || !slices.Equal(result.Skipped, want) {
			t.Fatalf(
				"%s without project.archive = %+v, want nothing changed and the archive skipped",
				name,
				result,
			)
		}
		if archived() || !slices.Contains(w.taskKeys(t), "mine_task") {
			t.Fatalf(
				"%s without project.archive: mine archived = %v, Tasks = %v; want both kept",
				name,
				archived(),
				w.taskKeys(t),
			)
		}
	}

	dbtest.GrantProject(
		t,
		w.ctx,
		w.pool,
		w.fixture.CompanyA,
		mine,
		writer.UserID,
		"project.tasks.write",
		"project.archive",
	)
	result := w.approveAs(t, actor, archiveMine)
	if countsOf(result) != (planCounts{Updated: 1, Deleted: 1}) {
		t.Fatalf("apply with project.archive = %+v, want mine_task deleted and mine archived", result)
	}
	if !archived() || slices.Contains(w.taskKeys(t), "mine_task") {
		t.Fatalf("with project.archive: mine archived = %v, Tasks = %v", archived(), w.taskKeys(t))
	}
}

// A Project the plan was read without being able to change is left alone,
// even when its applier may change it by the time they apply (ADR 0017): the
// plan dropped its Task only because the reader could not keep it
// otherwise.
func TestCompanyPlanApplyLeavesAProjectReadAsReadOnly(t *testing.T) {
	w := newPlanAccessWorld(t)
	w.layOut(t)
	admin := w.seedMember(t, "admin@example.com", auth.CompanyRoleAdmin)
	shared := w.projectID(t, "shared")
	dbtest.GrantProject(t, w.ctx, w.pool, w.fixture.CompanyA, shared, admin.UserID, "project.read")

	document, base := w.read(t, admin)
	withoutTask(&document, "shared_task")
	dbtest.GrantProject(t, w.ctx, w.pool, w.fixture.CompanyA, shared, admin.UserID, "project.tasks.write")
	result, err := w.service.ApplyCompanyPlan(w.ctx, admin, w.artifact(base, document))
	if err != nil || countsOf(result) != (planCounts{}) {
		t.Fatalf("a plan read while shared was read-only = %+v, %v; want no change", result, err)
	}
	if !slices.Contains(w.taskKeys(t), "shared_task") {
		t.Fatalf("Tasks = %v, want shared_task kept", w.taskKeys(t))
	}
}

// A Project the plan read as changeable, where the applier has since lost
// project.tasks.write or project.read, is left alone (ADR 0017). When the
// plan would change it the apply lists it as skipped, so the result never
// looks like a smaller plan that succeeded; restated as it is, it is left
// alone without a word.
func TestCompanyPlanApplySkipsAProjectWhoseWriteWasLost(t *testing.T) {
	w := newPlanAccessWorld(t)
	w.layOut(t)
	admin := w.seedMember(t, "admin@example.com", auth.CompanyRoleAdmin)
	mine, shared := w.projectID(t, "mine"), w.projectID(t, "shared")
	dbtest.GrantProject(t, w.ctx, w.pool, w.fixture.CompanyA, mine, admin.UserID)
	dbtest.GrantProject(t, w.ctx, w.pool, w.fixture.CompanyA, shared, admin.UserID)

	document, base := w.read(t, admin)
	restated := document
	restated.Projects = slices.Clone(document.Projects)
	restated.Tasks = slices.Clone(document.Tasks)
	withoutTask(&document, "mine_task")
	withoutTask(&document, "shared_task")
	dbtest.MustExec(t, w.ctx, w.pool, `
		DELETE FROM project_grants
		WHERE project_id = $1 AND user_id = $2 AND permission = 'project.tasks.write'`, mine, admin.UserID)
	dbtest.MustExec(
		t,
		w.ctx,
		w.pool,
		`DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2`,
		shared,
		admin.UserID,
	)

	result, err := w.service.ApplyCompanyPlan(w.ctx, admin, w.artifact(base, document))
	want := []PlanSkip{
		{EntityType: "project", Key: "mine", Operation: "tasks", Permission: auth.ProjectPermissionTasksWrite},
		{EntityType: "project", Key: "shared", Operation: "tasks", Permission: auth.ProjectPermissionRead},
	}
	if err != nil || countsOf(result) != (planCounts{Skipped: 2}) || !slices.Equal(result.Skipped, want) {
		t.Fatalf("a plan whose Projects lost write and read = %+v, %v; want both skipped", result, err)
	}
	if got := w.taskKeys(t); !slices.Contains(got, "mine_task") || !slices.Contains(got, "shared_task") {
		t.Fatalf("Tasks = %v, want mine_task and shared_task kept", got)
	}

	restated.Projects = slices.DeleteFunc(slices.Clone(restated.Projects), func(project PlanProject) bool {
		return project.Key == "shared"
	})
	withoutTask(&restated, "shared_task")
	base.Projects = slices.DeleteFunc(
		base.Projects,
		func(entity PlanBaseEntity) bool { return entity.Key == "shared" },
	)
	result, err = w.service.ApplyCompanyPlan(w.ctx, admin, w.artifact(base, restated))
	if err != nil || countsOf(result) != (planCounts{}) {
		t.Fatalf(
			"restating the Project whose write was lost = %+v, %v; want no change and no skip",
			result,
			err,
		)
	}
}

// A Company Plan Approval frozen with a wider base than its Task chat's
// Project, as every one raised before plans were bound to the Task's
// Project was, is stale when approved: its preview showed changes the apply
// would not make (ADR 0017).
func TestTaskPlanApprovalFrozenWiderThanItsProjectIsStale(t *testing.T) {
	w := newPlanAccessWorld(t)
	w.layOut(t)
	document, _ := w.read(t, w.owner)
	withoutTask(&document, "shared_task")
	encoded, err := json.Marshal(w.artifact(PlanBase{}, document))
	if err != nil {
		t.Fatal(err)
	}
	var executed approval.CompanyPlanActionResult
	var pending []events.Event
	err = w.store.WithinTx(w.ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		prepared, err := w.service.PrepareCompanyPlanApply(
			w.ctx,
			q,
			approval.CompanyPlanActor{Principal: w.owner},
			encoded,
		)
		if err != nil {
			return err
		}
		executed, err = w.service.ExecuteCompanyPlanApply(w.ctx, q, &pending, approval.CompanyPlanActor{
			Principal: w.owner, TaskProjectID: uuid.NullUUID{UUID: w.fixture.ProjectA, Valid: true},
		}, prepared.CanonicalAction)
		return err
	})
	if err != nil || executed.SkippedReason == nil {
		t.Fatalf(
			"executing a wider frozen base in a Task chat = %+v, %v; want a skipped execution",
			executed,
			err,
		)
	}
	if !slices.Contains(w.taskKeys(t), "shared_task") {
		t.Fatalf("Tasks = %v, want shared_task kept", w.taskKeys(t))
	}
}

// approveAs raises the person's plan for their Task chat's Project and
// applies it as them, as an approved Task-origin Approval does.
func (w planAccessWorld) approveAs(
	t *testing.T,
	actor approval.CompanyPlanActor,
	edit func(*PlanDocument),
) PlanApplyResult {
	t.Helper()
	executed, err := w.decide(t, actor, actor, edit)
	if err != nil {
		t.Fatalf("prepare and execute the plan: %v", err)
	}
	if executed.SkippedReason != nil {
		t.Fatalf("the plan was skipped: %s", *executed.SkippedReason)
	}
	var result PlanApplyResult
	if err := json.Unmarshal(executed.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// decide prepares the requester's plan, as raising its Approval does, and
// executes the frozen action as the applier, as approving it does, in one
// transaction that a refusal rolls back.
func (w planAccessWorld) decide(
	t *testing.T,
	requester, applier approval.CompanyPlanActor,
	edit func(*PlanDocument),
) (approval.CompanyPlanActionResult, error) {
	t.Helper()
	document, _ := w.read(t, requester.Principal)
	edit(&document)
	encoded, err := json.Marshal(w.artifact(PlanBase{}, document))
	if err != nil {
		t.Fatal(err)
	}
	var executed approval.CompanyPlanActionResult
	var pending []events.Event
	err = w.store.WithinTx(w.ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		prepared, err := w.service.PrepareCompanyPlanApply(w.ctx, q, requester, encoded)
		if err != nil {
			return err
		}
		executed, err = w.service.ExecuteCompanyPlanApply(w.ctx, q, &pending, applier, prepared.CanonicalAction)
		return err
	})
	return executed, err
}

// A member who approves a Task-chat plan with Project permissions alone
// keeps the Task rules a Task PATCH keeps (ADR 0014; ADR 0017 decision 5):
// on another member's Task that has run and waits in review, the plan may
// not hand it to another Agent or to its assignee's own hands, move it out
// of review, or delete it. The assignee may.
func TestTaskPlanApprovalKeepsTheTaskRulesOfAPatch(t *testing.T) {
	w := newPlanAccessWorld(t)
	decider := w.seedMember(t, "decider@example.com", auth.CompanyRoleMember)
	dbtest.GrantProject(t, w.ctx, w.pool, w.fixture.CompanyA, w.fixture.ProjectA, decider.UserID,
		"project.approvals.resolve", "project.tasks.write")
	dbtest.GrantProject(t, w.ctx, w.pool, w.fixture.CompanyA, w.fixture.ProjectA, w.owner.UserID,
		"project.approvals.resolve", "project.tasks.write")
	// task_a is the owner's, has run on AgentA and waits in review.
	dbtest.MustExec(t, w.ctx, w.pool, `
		UPDATE tasks SET status = 'in_review', executed_by_agent_id = $2, execution_generation = 1
		WHERE id = $1`, w.fixture.TaskA, w.fixture.AgentA)
	work := uuid.NullUUID{UUID: w.fixture.ProjectA, Valid: true}
	requester := approval.CompanyPlanActor{Principal: w.owner, TaskProjectID: work}
	member := approval.CompanyPlanActor{Principal: decider, TaskProjectID: work}
	editTaskA := func(change func(*PlanTask)) func(*PlanDocument) {
		return func(document *PlanDocument) {
			for index := range document.Tasks {
				if document.Tasks[index].Key == "task_a" {
					change(&document.Tasks[index])
				}
			}
		}
	}
	handOver := editTaskA(func(planned *PlanTask) { planned.AgentID = "" })
	markDone := editTaskA(func(planned *PlanTask) { planned.Status = "done" })
	remove := func(document *PlanDocument) { withoutTask(document, "task_a") }

	for _, refusal := range []struct {
		name string
		edit func(*PlanDocument)
		want error
	}{
		{"hand over", handOver, taskdomain.ErrHandoffPermissionRequired},
		{"out of review", markDone, taskdomain.ErrReviewPermissionRequired},
		{"delete", remove, ErrPlanTaskDeletePermissionRequired},
	} {
		if _, err := w.decide(t, requester, member, refusal.edit); !errors.Is(err, refusal.want) {
			t.Fatalf(
				"%s by a member who is not the assignee error = %v, want %v",
				refusal.name,
				err,
				refusal.want,
			)
		}
	}
	var status string
	var generation int64
	var agent uuid.NullUUID
	read := func() {
		t.Helper()
		if err := w.pool.QueryRow(w.ctx, `
			SELECT status, execution_generation, executed_by_agent_id FROM tasks WHERE id = $1`,
			w.fixture.TaskA).Scan(&status, &generation, &agent); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if status != "in_review" || generation != 1 || agent.UUID != w.fixture.AgentA {
		t.Fatalf(
			"task_a after the refusals = %s, generation %d, agent %v; want it untouched",
			status,
			generation,
			agent,
		)
	}

	executed, err := w.decide(t, requester, requester, markDone)
	if err != nil || executed.SkippedReason != nil {
		t.Fatalf("the assignee's apply = %+v, %v", executed, err)
	}
	if read(); status != "done" {
		t.Fatalf("task_a after the assignee's apply = %s, want done", status)
	}
}

// A Company Plan raised by a Task Run is approved inside its Task's Project
// (ADR 0017): a member holding project.approvals.resolve and
// project.tasks.write there approves it without company.plan.apply, one
// holding only project.approvals.resolve may not, and the plan reaches that
// Project alone, even for Projects the requester reads.
func TestTaskPlanApprovalIsApprovedWithProjectPermissions(t *testing.T) {
	w := newPlanAccessWorld(t)
	w.layOut(t)
	decider := w.seedMember(t, "decider@example.com", auth.CompanyRoleMember)
	dbtest.GrantProject(t, w.ctx, w.pool, w.fixture.CompanyA, w.fixture.ProjectA, decider.UserID,
		"project.approvals.resolve", "project.tasks.write")
	resolverOnly := w.seedMember(t, "resolver@example.com", auth.CompanyRoleMember)
	dbtest.GrantProject(t, w.ctx, w.pool, w.fixture.CompanyA, w.fixture.ProjectA, resolverOnly.UserID,
		"project.approvals.resolve")
	approvals, err := approval.NewApprovalService(w.store, w.bus, w.service, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The owner's Run in task_a's chat plans a follow-up in the work
	// Project and, in the same document, drops the Task of another Project
	// the owner reads.
	document, _ := w.read(t, w.owner)
	withoutTask(&document, "shared_task")
	document.Tasks = append(document.Tasks, openPlanTask("follow_up", "work"))
	encoded, err := json.Marshal(w.artifact(PlanBase{}, document))
	if err != nil {
		t.Fatal(err)
	}
	origin := w.seedTaskRun(t)
	raised, _, err := approvals.RequestCompanyPlanApply(w.ctx, w.owner, origin, uuid.Must(uuid.NewV7()), encoded)
	if err != nil {
		t.Fatalf("RequestCompanyPlanApply() error = %v", err)
	}
	var presentation PlanApprovalPresentation
	if err := json.Unmarshal(raised.Presentation, &presentation); err != nil {
		t.Fatal(err)
	}
	var shown []string
	for _, project := range presentation.Plan.Projects {
		shown = append(shown, project.Key)
	}
	if !slices.Equal(shown, []string{"work"}) ||
		!reflect.DeepEqual(
			presentation.Changes,
			[]PlanChange{{EntityType: "task", Operation: "create", Key: "follow_up"}},
		) {
		t.Fatalf(
			"Approval shows Projects %v and changes %+v; want the work Project and one create",
			shown,
			presentation.Changes,
		)
	}

	decision := approval.ApprovalDecisionInput{ExpectedRevision: raised.Revision}
	if _, err := approvals.Approve(
		w.ctx,
		resolverOnly,
		raised.ID,
		decision,
	); !errors.Is(
		err,
		auth.ErrProjectPermissionRequired,
	) {
		t.Fatalf("Approve(project.approvals.resolve only) error = %v, want ErrProjectPermissionRequired", err)
	}
	approved, err := approvals.Approve(w.ctx, decider, raised.ID, decision)
	if err != nil || approved.Execution == nil || approved.Execution.Status != approval.ApprovalExecutionExecuted {
		t.Fatalf("Approve(member decider) = %+v, %v", approved, err)
	}
	var result PlanApplyResult
	if err := json.Unmarshal(approved.Execution.Result, &result); err != nil {
		t.Fatal(err)
	}
	if countsOf(result) != (planCounts{Created: 1}) {
		t.Fatalf("approved apply = %+v, want only the follow-up", result)
	}
	if got := w.taskKeys(
		t,
	); !slices.Equal(
		got,
		[]string{"follow_up", "mine_task", "secret_task", "shared_task", "task_a"},
	) {
		t.Fatalf("Tasks after the approval = %v", got)
	}
	var assignee uuid.UUID
	if err := w.pool.QueryRow(w.ctx, `SELECT assignee_user_id FROM tasks WHERE key = 'follow_up'`).
		Scan(&assignee); err != nil ||
		assignee != decider.UserID {
		t.Fatalf("follow_up assignee = %v, %v; want the decider who applied", assignee, err)
	}
}

// seedTaskRun starts the owner's Run in task_a's chat, the origin of a
// Task-origin Approval.
func (w planAccessWorld) seedTaskRun(t *testing.T) execution.RunOrigin {
	t.Helper()
	var runID uuid.UUID
	if err := w.pool.QueryRow(w.ctx, `
		INSERT INTO runs (company_id, agent_id, task_id, conversation_id, runtime_id,
			execution_authorized_by_user_id, status, run_type)
		VALUES ($1, $2, $3, $4, $5, $6, 'running', 'task')
		RETURNING id`,
		w.fixture.CompanyA, w.fixture.AgentA, w.fixture.TaskA, w.fixture.ConversationA,
		w.fixture.RuntimeA, w.fixture.UserA,
	).Scan(&runID); err != nil {
		t.Fatalf("seed the Task Run: %v", err)
	}
	return execution.RunOrigin{
		RunID: runID, CompanyID: w.fixture.CompanyA, AgentID: w.fixture.AgentA,
		TaskID: &w.fixture.TaskA, ConversationID: w.fixture.ConversationA, ConversationKind: "task",
	}
}
