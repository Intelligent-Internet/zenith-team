//go:build integration

package company

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	taskdomain "github.com/Intelligent-Internet/zenith-team/server/internal/task"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// planInboxPresenter records the transient presentation a Plan apply raises
// for each Inbox item it created, the way the Task service does for a write.
type planInboxPresenter struct {
	mu        sync.Mutex
	presented []inbox.Notification
}

func (p *planInboxPresenter) Present(notification inbox.Notification) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.presented = append(p.presented, notification)
}

func (p *planInboxPresenter) presentations() []inbox.Notification {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]inbox.Notification(nil), p.presented...)
}

func TestCompanyPlanApplyCreatesDirectTasks(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
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
	presenter := &planInboxPresenter{}
	service, err := NewPlanService(store, bus, presenter)
	if err != nil {
		t.Fatalf("NewPlanService() error = %v", err)
	}
	taskEvents := recordTopic(t, bus, taskdomain.Topic)
	inboxEvents := recordTopic(t, bus, inbox.Topic)
	dbtest.MustExec(t, ctx, pool, `DELETE FROM tasks WHERE company_id=$1`, fixture.CompanyA)
	engineeringID := dbtest.NewUUID(t)
	dbtest.SeedBoundAgent(t, ctx, pool, engineeringID, fixture.CompanyA,
		"Engineering", "codex", &fixture.AgentA, fixture.UserA, fixture.RuntimeA)
	slug := "schema-a"
	desired := PlanDocument{
		Projects:      []PlanProject{{Key: "work", Title: "Work"}},
		FormatVersion: planFormatVersion,
		Company:       slug,
		Tasks: []PlanTask{{
			ProjectKey: "work",
			Key:        "website", AgentID: engineeringID.String(), MilestoneType: "product",
			Title:       "Build and deploy the launch website",
			Description: "Produce the deployed customer-facing launch website.",
			Status:      "backlog", Priority: "high", DependsOn: []PlanTaskDependency{},
		}, {
			ProjectKey: "work",
			Key:        "buy_domain", MilestoneType: "product",
			Title:       "Purchase the launch domain",
			Description: "Purchase the approved domain with the founder payment method.",
			Status:      "todo", Priority: "high", DependsOn: []PlanTaskDependency{},
		}, {
			ProjectKey: "work",
			Key:        "launch_review", AgentID: engineeringID.String(), MilestoneType: "launch",
			Title:       "Prepare launch and obtain founder approval",
			Description: "Prepare launch assets, request approval, and hand off for release.",
			Status:      "backlog", Priority: "high", DependsOn: []PlanTaskDependency{},
		}},
	}
	result, err := service.ApplyCompanyPlan(
		ctx,
		auth.UserPrincipal{
			UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
		},
		PlanArtifact{
			ArtifactVersion: 1,
			CompanyID:       fixture.CompanyA.String(),
			CompanySlug:     slug,
			CreatedAt:       time.Now().UTC(),
			Base: PlanBase{
				Projects: []PlanBaseEntity{
					{ID: fixture.ProjectA.String(), Key: "work", Revision: 1},
				},
				Tasks:        []PlanBaseEntity{},
				Dependencies: []PlanBaseDependency{},
			},
			Desired: desired,
		},
	)
	if err != nil {
		t.Fatalf("ApplyCompanyPlan() error = %v", err)
	}
	if result.Created != 3 {
		t.Fatalf("ApplyCompanyPlan() Created = %d, want 3", result.Created)
	}
	createdEvents := taskEvents.named(taskdomain.EventCreated)
	if len(createdEvents) != 3 {
		t.Fatalf("published task.created events = %d, want 3", len(createdEvents))
	}
	createdTasks := make(map[string]taskdomain.Response, len(createdEvents))
	for _, event := range createdEvents {
		created := payloadOf[taskdomain.Response](t, event)
		createdTasks[created.Key] = created
		if event.CompanyID != fixture.CompanyA || event.TaskID != created.ID ||
			event.ConversationID != created.ConversationID {
			t.Fatalf("task.created envelope for %q = %#v", created.Key, event)
		}
	}
	for _, key := range []string{"buy_domain", "launch_review", "website"} {
		if _, exists := createdTasks[key]; !exists {
			t.Fatalf("task.created events omit %q: %#v", key, createdTasks)
		}
	}

	// Every Task the apply creates is the applier's own to run (ADR 0014),
	// so nobody is told they were assigned anything: a plan never hands work
	// to another member, and an Inbox item nobody needs is noise.
	var assignedItems int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::integer
		FROM inbox_items
		WHERE company_id=$1 AND kind='task_assigned'`,
		fixture.CompanyA,
	).Scan(&assignedItems); err != nil {
		t.Fatalf("read task_assigned Inbox items: %v", err)
	}
	if assignedItems != 0 {
		t.Fatalf("task_assigned Inbox items = %d, want none from a Plan apply", assignedItems)
	}
	if createdItems := inboxEvents.named(inbox.EventItemCreated); len(createdItems) != 0 {
		t.Fatalf("published inbox.created events = %d, want none", len(createdItems))
	}
	if presented := presenter.presentations(); len(presented) != 0 {
		t.Fatalf("presented notifications = %#v, want none", presented)
	}

	baseTasks := make([]PlanBaseEntity, 0, len(createdTasks))
	for _, key := range []string{"buy_domain", "launch_review", "website"} {
		created := createdTasks[key]
		baseTasks = append(baseTasks, PlanBaseEntity{
			ID: created.ID.String(), Key: key, Revision: created.Revision,
			ConversationID: created.ConversationID.String(),
		})
	}
	desiredWithDependency := desired
	desiredWithDependency.Tasks = append(
		[]PlanTask(nil), desired.Tasks...,
	)
	for index := range desiredWithDependency.Tasks {
		if desiredWithDependency.Tasks[index].Key == "launch_review" {
			desiredWithDependency.Tasks[index].DependsOn = []PlanTaskDependency{{
				TaskKey: "website",
			}}
		}
	}
	result, err = service.ApplyCompanyPlan(
		ctx,
		auth.UserPrincipal{
			UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
		},
		PlanArtifact{
			ArtifactVersion: 1,
			CompanyID:       fixture.CompanyA.String(),
			CompanySlug:     slug,
			CreatedAt:       time.Now().UTC(),
			Base: PlanBase{
				Projects: []PlanBaseEntity{
					{ID: fixture.ProjectA.String(), Key: "work", Revision: 1},
				},
				Tasks:        baseTasks,
				Dependencies: []PlanBaseDependency{},
			},
			Desired: desiredWithDependency,
		},
	)
	if err != nil {
		t.Fatalf("ApplyCompanyPlan(add dependency) error = %v", err)
	}
	if countsOf(result) != (planCounts{Updated: 1}) {
		t.Fatalf("ApplyCompanyPlan(add dependency) = %+v, want one updated Task", result)
	}
	updatedEvents := taskEvents.named(taskdomain.EventUpdated)
	if len(updatedEvents) != 1 {
		t.Fatalf("published task.updated events = %d, want 1", len(updatedEvents))
	}
	updatedTask := payloadOf[taskdomain.Response](t, updatedEvents[0])
	if updatedTask.Key != "launch_review" || updatedTask.BlockedByCount != 1 ||
		updatedTask.DependenciesResolved ||
		updatedTask.Revision != createdTasks["launch_review"].Revision+1 {
		t.Fatalf("dependency task.updated payload = %#v", updatedTask)
	}

	var executedByAgentID uuid.UUID
	var milestoneType string
	if err := pool.QueryRow(ctx, `
		SELECT executed_by_agent_id,milestone_type
		FROM tasks WHERE company_id=$1 AND key='website'
	`, fixture.CompanyA).Scan(
		&executedByAgentID,
		&milestoneType,
	); err != nil {
		t.Fatalf("read direct Task routing: %v", err)
	}
	if executedByAgentID != engineeringID || milestoneType != "product" {
		t.Fatalf(
			"direct Task routing = executed by %s, milestone %q",
			executedByAgentID,
			milestoneType,
		)
	}
	// Every applied Task is the applier's to run; the plan's Agent is who
	// does the work (ADR 0014).
	rows, err := pool.Query(ctx, `
		SELECT key,assignee_user_id,executed_by_agent_id
		FROM tasks WHERE company_id=$1 ORDER BY key
	`, fixture.CompanyA)
	if err != nil {
		t.Fatalf("read execution assignments: %v", err)
	}
	defer rows.Close()
	wantAssignments := map[string]struct {
		userID  *uuid.UUID
		agentID *uuid.UUID
	}{
		"buy_domain":    {userID: &fixture.UserA},
		"launch_review": {userID: &fixture.UserA, agentID: &engineeringID},
		"website":       {userID: &fixture.UserA, agentID: &engineeringID},
	}
	for rows.Next() {
		var key string
		var userID, agentID *uuid.UUID
		if err := rows.Scan(&key, &userID, &agentID); err != nil {
			t.Fatalf("scan execution assignment: %v", err)
		}
		want, exists := wantAssignments[key]
		if !exists || !sameOptionalUUID(userID, want.userID) ||
			!sameOptionalUUID(agentID, want.agentID) {
			t.Fatalf(
				"Task %q assignment = user %v agent %v, want %+v",
				key,
				userID,
				agentID,
				want,
			)
		}
		delete(wantAssignments, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate execution assignments: %v", err)
	}
	if len(wantAssignments) != 0 {
		t.Fatalf("missing execution assignments: %+v", wantAssignments)
	}
}

func sameOptionalUUID(left, right *uuid.UUID) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
