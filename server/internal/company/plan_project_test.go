package company

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/approval"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	taskdomain "github.com/Intelligent-Internet/zenith-team/server/internal/task"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// The Company Plan scope rules of ADR 0017, decided without a database.

func TestClassifyPlanProject(t *testing.T) {
	t.Parallel()
	project, other := uuid.New(), uuid.New()
	base := &PlanBase{}
	read := &PlanBaseEntity{ID: project.String(), Key: "mine"}
	readOnly := &PlanBaseEntity{ID: project.String(), Key: "mine", ReadOnly: true}
	taskBound := approval.CompanyPlanActor{TaskProjectID: uuid.NullUUID{UUID: other, Valid: true}}
	tests := []struct {
		name           string
		rule           planScopeRule
		canWrite       bool
		read           *PlanBaseEntity
		markedReadOnly bool
		want           planProjectStanding
	}{
		{
			"another Project than the Task chat's",
			planScopeRule{actor: taskBound},
			true,
			nil,
			false,
			planProjectOutsideTask,
		},
		{"raise, writable", planScopeRule{writeNeeded: true}, true, nil, false, planProjectInScope},
		{"raise, read-only", planScopeRule{writeNeeded: true}, false, nil, false, planProjectReadOnly},
		{"Task-chat raise previews without write", planScopeRule{}, false, nil, false, planProjectInScope},
		{"raise, marked read-only", planScopeRule{}, true, nil, true, planProjectReadOnly},
		{"apply, read writable, writable", planScopeRule{base: base}, true, read, false, planProjectInScope},
		{
			"apply, read writable, write lost",
			planScopeRule{base: base},
			false,
			read,
			false,
			planProjectWriteLost,
		},
		{
			"apply, read read-only, now writable",
			planScopeRule{base: base},
			true,
			readOnly,
			false,
			planProjectReadOnly,
		},
		{
			"apply, marked read-only in desired",
			planScopeRule{base: base},
			true,
			read,
			true,
			planProjectReadOnly,
		},
		{"apply, unread, writable", planScopeRule{base: base}, true, nil, false, planProjectUnlisted},
		{"apply, unread, read-only", planScopeRule{base: base}, false, nil, false, planProjectReadOnly},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := classifyPlanProject(test.rule, project, test.canWrite, test.read, test.markedReadOnly)
			if got != test.want {
				t.Fatalf("classifyPlanProject() = %d, want %d", got, test.want)
			}
		})
	}
}

// planWorld is a locked scope built by hand: mine in scope, shared read-only,
// secret unlisted, gone unreadable since the read.
type planWorld struct {
	mine, shared uuid.UUID
	locked       lockedPlan
}

func newPlanWorld(base *PlanBase) planWorld {
	mine, shared := uuid.New(), uuid.New()
	return planWorld{
		mine: mine, shared: shared,
		locked: lockedPlan{
			projects: map[string]generated.Project{
				"mine": {ID: mine, Key: "mine", Title: "mine", Version: 1},
			},
			access: map[string]auth.ProjectAccess{"mine": {
				ProjectID:   mine,
				Permissions: []auth.ProjectPermission{auth.ProjectPermissionTasksWrite},
			}},
			outside: map[string]planProjectStanding{
				"shared": planProjectReadOnly,
				"secret": planProjectUnlisted,
			},
			lost:       map[string]generated.Project{},
			unreadable: map[string]bool{"gone": true},
			tasks: map[string]persistedTask{
				"mine_task": {
					ID:        uuid.New(),
					ProjectID: mine,
					Key:       "mine_task",
					Status:    "todo",
					Version:   1,
				},
			},
			dependencies: map[string]persistedDependency{},
			base:         base,
		},
	}
}

func planDocument(projects []string, tasks ...PlanTask) PlanDocument {
	document := PlanDocument{
		FormatVersion: planFormatVersion,
		Company:       "acme",
		Projects:      []PlanProject{},
		Tasks:         tasks,
	}
	if document.Tasks == nil {
		document.Tasks = []PlanTask{}
	}
	for _, key := range projects {
		document.Projects = append(document.Projects, PlanProject{Key: key, Title: key})
	}
	return document
}

func plannedTask(key, projectKey string) PlanTask {
	return PlanTask{
		Key: key, ProjectKey: projectKey, MilestoneType: "product", Title: key,
		Status: "todo", Priority: "medium", DependsOn: []PlanTaskDependency{},
	}
}

func projectKeys(document PlanDocument) []string {
	var keys []string
	for _, project := range document.Projects {
		keys = append(keys, project.Key)
	}
	return keys
}

func taskKeys(document PlanDocument) []string {
	var keys []string
	for _, planned := range document.Tasks {
		keys = append(keys, planned.Key)
	}
	return keys
}

func TestScopeDesired(t *testing.T) {
	t.Parallel()
	t.Run("keeps the scope and new keys, drops Projects left alone", func(t *testing.T) {
		t.Parallel()
		world := newPlanWorld(&PlanBase{})
		marked := planDocument([]string{"mine", "shared", "brand_new", "flagged"},
			plannedTask("mine_task", "mine"), plannedTask("shared_task", "shared"),
			plannedTask("new_task", "brand_new"), plannedTask("flagged_task", "flagged"))
		marked.Projects[3].ReadOnly = true
		scoped, err := world.locked.scopeDesired(marked, false)
		if err != nil {
			t.Fatal(err)
		}
		if got := projectKeys(scoped); !slices.Equal(got, []string{"mine", "brand_new"}) {
			t.Fatalf("Projects = %v, want mine and the new key", got)
		}
		if got := taskKeys(scoped); !slices.Equal(got, []string{"mine_task", "new_task"}) {
			t.Fatalf("Tasks = %v, want those of mine and the new key", got)
		}
		if len(world.locked.skipped) != 0 {
			t.Fatalf("skipped = %+v, want none", world.locked.skipped)
		}
	})
	t.Run("a Task-chat plan creates no Project", func(t *testing.T) {
		t.Parallel()
		world := newPlanWorld(nil)
		scoped, err := world.locked.scopeDesired(planDocument([]string{"mine", "brand_new"}), true)
		if err != nil || !slices.Equal(projectKeys(scoped), []string{"mine"}) {
			t.Fatalf("scopeDesired() = %v, %v; want mine alone", projectKeys(scoped), err)
		}
	})
	t.Run("naming an unlisted Project is stale", func(t *testing.T) {
		t.Parallel()
		world := newPlanWorld(&PlanBase{})
		if _, err := world.locked.scopeDesired(
			planDocument([]string{"secret"}),
			false,
		); !errors.Is(
			err,
			errPlanStale,
		) {
			t.Fatalf("scopeDesired() error = %v, want errPlanStale", err)
		}
	})
	t.Run("naming a Project no longer readable is skipped", func(t *testing.T) {
		t.Parallel()
		world := newPlanWorld(&PlanBase{})
		scoped, err := world.locked.scopeDesired(
			planDocument([]string{"gone"}, plannedTask("gone_task", "gone")),
			false,
		)
		want := []PlanSkip{
			{
				EntityType: "project",
				Key:        "gone",
				Operation:  "tasks",
				Permission: auth.ProjectPermissionRead,
			},
		}
		if err != nil || len(scoped.Projects) != 0 || len(scoped.Tasks) != 0 ||
			!slices.Equal(world.locked.skipped, want) {
			t.Fatalf("scopeDesired() = %+v, skipped %+v, %v", scoped, world.locked.skipped, err)
		}
	})
	t.Run("restating a Task the base read that has gone is stale", func(t *testing.T) {
		t.Parallel()
		world := newPlanWorld(&PlanBase{Tasks: []PlanBaseEntity{{ID: uuid.NewString(), Key: "old_task"}}})
		_, err := world.locked.scopeDesired(
			planDocument([]string{"mine"}, plannedTask("old_task", "mine")),
			false,
		)
		if !errors.Is(err, errPlanStale) {
			t.Fatalf("scopeDesired() error = %v, want errPlanStale", err)
		}
	})
	t.Run("moving a Task to another Project is invalid", func(t *testing.T) {
		t.Parallel()
		world := newPlanWorld(&PlanBase{})
		_, err := world.locked.scopeDesired(
			planDocument([]string{"mine", "brand_new"}, plannedTask("mine_task", "brand_new")),
			false,
		)
		if !errors.Is(err, ErrPlanInvalidInput) {
			t.Fatalf("scopeDesired() error = %v, want ErrPlanInvalidInput", err)
		}
	})
}

func TestLockedPlanMatchesBase(t *testing.T) {
	t.Parallel()
	world := newPlanWorld(nil)
	current := world.locked.tasks["mine_task"]
	projectEntity := PlanBaseEntity{ID: world.mine.String(), Key: "mine", Revision: 1}
	taskEntity := PlanBaseEntity{ID: current.ID.String(), Key: "mine_task", Revision: 1}
	outside := PlanBaseEntity{ID: uuid.NewString(), Key: "shared_task", Revision: 9}
	tests := []struct {
		name string
		base PlanBase
		want bool
	}{
		{
			"as read",
			PlanBase{Projects: []PlanBaseEntity{projectEntity}, Tasks: []PlanBaseEntity{taskEntity}},
			true,
		},
		{
			"base entries outside the scope are ignored",
			PlanBase{
				Projects: []PlanBaseEntity{
					projectEntity,
					{ID: world.shared.String(), Key: "shared", Revision: 7},
				},
				Tasks: []PlanBaseEntity{taskEntity, outside},
			},
			true,
		},
		{
			"a Task added since",
			PlanBase{Projects: []PlanBaseEntity{projectEntity}, Tasks: []PlanBaseEntity{}},
			false,
		},
		{"a Task changed since", PlanBase{
			Projects: []PlanBaseEntity{projectEntity},
			Tasks:    []PlanBaseEntity{{ID: current.ID.String(), Key: "mine_task", Revision: 2}},
		}, false},
		{
			"a gone Task the plan omits",
			PlanBase{
				Projects: []PlanBaseEntity{projectEntity},
				Tasks: []PlanBaseEntity{
					taskEntity,
					{ID: uuid.NewString(), Key: "deleted_task", Revision: 1},
				},
			},
			true,
		},
		{"the Project changed since", PlanBase{
			Projects: []PlanBaseEntity{{ID: world.mine.String(), Key: "mine", Revision: 2}},
			Tasks:    []PlanBaseEntity{taskEntity},
		}, false},
		{
			"an edge added since",
			PlanBase{
				Projects: []PlanBaseEntity{projectEntity},
				Tasks:    []PlanBaseEntity{taskEntity},
				Dependencies: []PlanBaseDependency{
					{DependentTaskID: current.ID.String(), PrerequisiteTaskID: uuid.NewString()},
				},
			},
			false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			locked := newPlanWorld(nil).locked
			locked.projects["mine"] = world.locked.projects["mine"]
			locked.tasks = map[string]persistedTask{"mine_task": current}
			locked.base = &test.base
			if got := locked.matchesBase(); got != test.want {
				t.Fatalf("matchesBase() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSetAsideLostProjects(t *testing.T) {
	t.Parallel()
	lostWorld := func() planWorld {
		world := newPlanWorld(&PlanBase{})
		world.locked.lost["mine"] = world.locked.projects["mine"]
		delete(world.locked.projects, "mine")
		world.locked.outside["mine"] = planProjectWriteLost
		return world
	}
	restated := planDocument([]string{"mine"}, PlanTask{
		Key: "mine_task", ProjectKey: "mine", Title: "", Status: "todo", DependsOn: []PlanTaskDependency{},
	})
	world := lostWorld()
	world.locked.setAsideLostProjects(restated)
	if len(world.locked.skipped) != 0 || len(world.locked.tasks) != 0 {
		t.Fatalf(
			"restated: skipped %+v, Tasks %v; want none skipped and the Task set aside",
			world.locked.skipped,
			world.locked.tasks,
		)
	}

	world = lostWorld()
	world.locked.setAsideLostProjects(planDocument([]string{"mine"}))
	want := []PlanSkip{
		{EntityType: "project", Key: "mine", Operation: "tasks", Permission: auth.ProjectPermissionTasksWrite},
	}
	if !slices.Equal(world.locked.skipped, want) || len(world.locked.tasks) != 0 {
		t.Fatalf(
			"changed: skipped %+v, Tasks %v; want the Project skipped and its Task set aside",
			world.locked.skipped,
			world.locked.tasks,
		)
	}
}

func TestHoldUnpermittedArchives(t *testing.T) {
	t.Parallel()
	archiveHeld := []PlanSkip{
		{EntityType: "project", Key: "mine", Operation: "archive", Permission: auth.ProjectPermissionArchive},
	}
	archivedMine := planDocument([]string{"mine"})
	archivedMine.Projects[0].Archived = true
	tests := []struct {
		name     string
		desired  PlanDocument
		archive  bool
		finished bool
		held     bool
	}{
		{"archived without project.archive", archivedMine, false, false, true},
		{"left out with open Tasks", planDocument(nil), false, false, true},
		{"left out with finished Tasks only", planDocument(nil), false, true, false},
		{"restated", planDocument([]string{"mine"}, plannedTask("mine_task", "mine")), false, false, false},
		{"archived with project.archive", archivedMine, true, false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			world := newPlanWorld(&PlanBase{})
			if test.archive {
				access := world.locked.access["mine"]
				access.Permissions = append(access.Permissions, auth.ProjectPermissionArchive)
				world.locked.access["mine"] = access
			}
			if test.finished {
				value := world.locked.tasks["mine_task"]
				value.Status = "done"
				world.locked.tasks["mine_task"] = value
			}
			world.locked.desired = test.desired
			world.locked.holdUnpermittedArchives()
			_, inScope := world.locked.projects["mine"]
			if test.held {
				if !slices.Equal(world.locked.skipped, archiveHeld) || inScope ||
					len(world.locked.tasks) != 0 ||
					len(world.locked.desired.Projects) != 0 {
					t.Fatalf("held: skipped %+v, in scope %v, Tasks %v, desired %+v",
						world.locked.skipped, inScope, world.locked.tasks, world.locked.desired)
				}
				return
			}
			if len(world.locked.skipped) != 0 || !inScope || len(world.locked.tasks) != 1 {
				t.Fatalf(
					"kept: skipped %+v, in scope %v, Tasks %v",
					world.locked.skipped,
					inScope,
					world.locked.tasks,
				)
			}
		})
	}
}

// Renaming without project.settings.update is skipped before any write, so
// it needs no database.
func TestApplyPlanProjectsSkipsARenameWithoutSettingsUpdate(t *testing.T) {
	t.Parallel()
	world := newPlanWorld(&PlanBase{})
	desired := planDocument([]string{"mine"})
	desired.Projects[0].Title = "Renamed"
	var result PlanApplyResult
	if err := applyPlanProjects(
		context.Background(), nil, nil, planApplier{}, desired, world.locked, &result,
	); err != nil {
		t.Fatal(err)
	}
	want := []PlanSkip{
		{
			EntityType: "project",
			Key:        "mine",
			Operation:  "update",
			Permission: auth.ProjectPermissionSettingsUpdate,
		},
	}
	if !slices.Equal(result.Skipped, want) || result.Updated != 0 {
		t.Fatalf("result = %+v, want the rename skipped", result)
	}
}

func TestRequirePlanTaskAuthority(t *testing.T) {
	t.Parallel()
	applier, assignee, agent := uuid.New(), uuid.New(), uuid.New()
	member := planApplier{principal: auth.UserPrincipal{UserID: applier}, role: auth.CompanyRoleMember}
	admin := planApplier{principal: auth.UserPrincipal{UserID: applier}, role: auth.CompanyRoleAdmin}
	inReview := persistedTask{
		AssigneeUserID: assignee, Status: string(taskdomain.StatusInReview),
		ExecutedByAgentID: uuid.NullUUID{UUID: agent, Valid: true}, ExecutionGeneration: 1,
	}
	restated := desiredTask{PlanTask: PlanTask{Status: string(taskdomain.StatusInReview), AgentID: agent.String()}}
	done := desiredTask{PlanTask: PlanTask{Status: "done", AgentID: agent.String()}}
	handedOver := desiredTask{PlanTask: PlanTask{Status: string(taskdomain.StatusInReview)}}
	own := inReview
	own.AssigneeUserID = applier
	finished := inReview
	finished.Status = "done"
	tests := []struct {
		name     string
		applier  planApplier
		current  persistedTask
		target   desiredTask
		retained bool
		agent    uuid.UUID
		want     error
	}{
		{"restated", member, inReview, restated, true, agent, nil},
		{
			"handed to the assignee's own hands",
			member,
			inReview,
			handedOver,
			true,
			uuid.Nil,
			taskdomain.ErrHandoffPermissionRequired,
		},
		{
			"handed to another Agent",
			member,
			inReview,
			restated,
			true,
			uuid.New(),
			taskdomain.ErrHandoffPermissionRequired,
		},
		{"out of review", member, inReview, done, true, agent, taskdomain.ErrReviewPermissionRequired},
		{"deleted", member, inReview, desiredTask{}, false, uuid.Nil, ErrPlanTaskDeletePermissionRequired},
		{"the assignee's own Task", member, own, done, true, agent, nil},
		{"a tasks.assign holder", admin, inReview, desiredTask{}, false, uuid.Nil, nil},
		{"a finished Task", member, finished, desiredTask{}, false, uuid.Nil, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := requirePlanTaskAuthority(
				test.applier,
				test.current,
				test.target,
				test.retained,
				test.agent,
			)
			if !errors.Is(err, test.want) || (test.want == nil && err != nil) {
				t.Fatalf("requirePlanTaskAuthority() = %v, want %v", err, test.want)
			}
		})
	}
}
