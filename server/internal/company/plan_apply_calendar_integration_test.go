//go:build integration

package company

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/schedule"
	"boardly.dev/boardly/server/internal/task"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestCompanyPlanCalendarHandoffKeepsUnclaimedOccurrence(t *testing.T) {
	for _, originalStatus := range []string{"queued", "dispatched", "running"} {
		t.Run(originalStatus, func(t *testing.T) {
			w := newPlanAccessWorld(t)
			tasks, err := task.NewTaskService(w.store, w.bus, nil)
			if err != nil {
				t.Fatal(err)
			}
			calendar := task.NewCalendarService(tasks)
			at := time.Now().UTC().Add(time.Hour).Truncate(time.Minute)
			created, err := tasks.Create(w.ctx, w.owner, task.CreateInput{
				ProjectID:     w.fixture.ProjectA,
				Key:           "calendar_handoff",
				MilestoneType: task.MilestoneTypeProduct,
				Title:         "Scheduled handoff",
				Status:        task.StatusTodo,
				ExecutedBy:    &task.ActorRef{Kind: task.ActorAgent, ID: w.fixture.AgentA},
				Schedule:      &schedule.Trigger{Type: schedule.TriggerTypeOnce, RunAt: &at},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := calendar.ProcessDue(w.ctx, at.Add(time.Second), 100); err != nil {
				t.Fatal(err)
			}
			var originalRun uuid.UUID
			if err := w.pool.QueryRow(w.ctx, `SELECT id FROM runs WHERE task_id=$1`, created.Task.ID).
				Scan(&originalRun); err != nil {
				t.Fatal(err)
			}
			if originalStatus != "queued" {
				dbtest.MustExec(
					t,
					w.ctx,
					w.pool,
					`UPDATE runs SET status=$2, claimed_at=now() WHERE id=$1`,
					originalRun,
					originalStatus,
				)
			}
			agent := dbtest.NewUUID(t)
			dbtest.SeedBoundAgent(
				t,
				w.ctx,
				w.pool,
				agent,
				w.fixture.CompanyA,
				"Calendar replacement",
				"codex",
				&w.fixture.AgentA,
				w.fixture.UserA,
				w.fixture.RuntimeA,
			)
			if _, err := w.apply(t, w.owner, func(document *PlanDocument) {
				for i := range document.Tasks {
					if document.Tasks[i].Key == created.Task.Key {
						document.Tasks[i].AgentID = agent.String()
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			current, err := tasks.Get(w.ctx, w.owner, created.Task.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if (current.ScheduleFiredAt == nil) != (originalStatus == "queued") {
				t.Fatalf(
					"fired timestamp after %s handoff = %v; only an unclaimed occurrence can be replaced",
					originalStatus,
					current.ScheduleFiredAt,
				)
			}
			var status string
			if err := w.pool.QueryRow(w.ctx, `SELECT status FROM runs WHERE id=$1`, originalRun).
				Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "cancelled" {
				t.Fatalf("handed-off Run status = %s, want cancelled", status)
			}
			var count int
			if err := w.pool.QueryRow(w.ctx, `SELECT count(*) FROM runs WHERE task_id=$1`, current.ID).
				Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("handoff started immediate work: %d Runs, want 1", count)
			}
			if err := calendar.ProcessDue(w.ctx, at.Add(2*time.Second), 100); err != nil {
				t.Fatal(err)
			}
			if err := w.pool.QueryRow(w.ctx, `SELECT count(*) FROM runs WHERE task_id=$1 AND agent_id=$2 AND status='queued'`, current.ID, agent).
				Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 0
			if originalStatus == "queued" {
				want = 1
			}
			if count != want {
				t.Fatalf("replacement Runs after %s handoff = %d, want %d", originalStatus, count, want)
			}
		})
	}
}

func TestCompanyPlanDeleteCalendarTemplateReconcilesRetainedOccurrences(t *testing.T) {
	for _, test := range []struct {
		name, status     string
		editOccurrence   bool
		deleteOccurrence bool
	}{
		{name: "queued", status: "queued"},
		{name: "deferred", status: "deferred"},
		{name: "dispatched", status: "dispatched"},
		{name: "running", status: "running"},
		{name: "completed", status: "completed"},
		{name: "queued and edited", status: "queued", editOccurrence: true},
		{name: "delete whole series", status: "queued", deleteOccurrence: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := newPlanAccessWorld(t)
			tasks, err := task.NewTaskService(w.store, w.bus, nil)
			if err != nil {
				t.Fatal(err)
			}
			calendar := task.NewCalendarService(tasks)
			expression, zone := "0 9 * * *", "UTC"
			created, err := tasks.Create(w.ctx, w.owner, task.CreateInput{
				ProjectID:     w.fixture.ProjectA,
				Key:           "calendar_delete",
				MilestoneType: task.MilestoneTypeProduct,
				Title:         "Recurring work",
				Status:        task.StatusTodo,
				ExecutedBy:    &task.ActorRef{Kind: task.ActorAgent, ID: w.fixture.AgentA},
				Schedule: &schedule.Trigger{
					Type:       schedule.TriggerTypeCron,
					Expression: &expression,
					TimeZone:   &zone,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			at := created.Task.ScheduledAt.Add(24 * time.Hour)
			moved := at.Add(time.Hour)
			child, err := calendar.UpdateOccurrence(
				w.ctx,
				w.owner,
				created.Task.ID,
				task.CalendarOccurrenceChange{
					ExpectedRevision: created.Task.Revision, OccurrenceAt: at, RunAt: &moved,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			futureAt := moved.Add(24 * time.Hour)
			future, err := calendar.UpdateOccurrence(
				w.ctx,
				w.owner,
				created.Task.ID,
				task.CalendarOccurrenceChange{
					ExpectedRevision: created.Task.Revision,
					OccurrenceAt:     at.Add(24 * time.Hour),
					RunAt:            &futureAt,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := calendar.ProcessDue(w.ctx, moved.Add(time.Second), 100); err != nil {
				t.Fatal(err)
			}
			var originalRun uuid.UUID
			if err := w.pool.QueryRow(w.ctx, `SELECT id FROM runs WHERE task_id=$1`, child.Task.ID).
				Scan(&originalRun); err != nil {
				t.Fatal(err)
			}
			if test.status != "queued" {
				dbtest.MustExec(
					t,
					w.ctx,
					w.pool,
					`UPDATE runs SET status=$2 WHERE id=$1`,
					originalRun,
					test.status,
				)
			}
			if test.status == "completed" {
				dbtest.MustExec(
					t,
					w.ctx,
					w.pool,
					`UPDATE tasks SET status='done', completed_at=now() WHERE id=$1`,
					child.Task.ID,
				)
			}
			if _, err := w.apply(t, w.owner, func(document *PlanDocument) {
				withoutTask(document, created.Task.Key)
				if test.deleteOccurrence {
					withoutTask(document, child.Task.Key)
					withoutTask(document, future.Task.Key)
				}
				if test.editOccurrence {
					for i := range document.Tasks {
						if document.Tasks[i].Key == child.Task.Key {
							document.Tasks[i].Priority = "high"
						}
					}
				}
			}); err != nil {
				t.Fatalf("delete template while retaining occurrence: %v", err)
			}
			var status, reason string
			if err := w.pool.QueryRow(w.ctx, `SELECT status, COALESCE(error, '') FROM runs WHERE id=$1`, originalRun).
				Scan(&status, &reason); err != nil {
				t.Fatal(err)
			}
			want := test.status
			if test.status == "queued" || test.status == "deferred" {
				want = "cancelled"
				wantReason := execution.CancellationScheduleCancelled
				if test.deleteOccurrence {
					wantReason = execution.RunReasonTaskDeleted
				}
				if reason != wantReason {
					t.Errorf("Run cancellation reason = %q, want %q", reason, wantReason)
				}
			}
			if status != want {
				t.Fatalf("retained occurrence Run status = %s, want %s", status, want)
			}
			retained, err := tasks.Get(w.ctx, w.owner, child.Task.ID, nil)
			if test.deleteOccurrence {
				if !errors.Is(err, task.ErrNotFound) {
					t.Fatalf("deleted occurrence read error = %v, want not found", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if retained.ScheduleID != nil {
				t.Fatal("retained occurrence still references deleted schedule")
			}
			if want == "cancelled" &&
				(retained.ScheduleDisposition != "cancelled" || retained.Status != task.StatusTodo) {
				t.Fatalf(
					"retained occurrence = %s / %s, want cancelled / todo",
					retained.ScheduleDisposition,
					retained.Status,
				)
			}
			if test.status == "completed" && retained.Status != task.StatusDone {
				t.Fatalf("completed Task status = %s, want done", retained.Status)
			}
			futureTask, err := tasks.Get(w.ctx, w.owner, future.Task.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if futureTask.ScheduleID != nil || futureTask.ScheduleDisposition != "cancelled" {
				t.Fatalf(
					"future occurrence disposition = %s, want detached and cancelled",
					futureTask.ScheduleDisposition,
				)
			}
		})
	}
}

func TestCompanyPlanCalendarAgentChangeRequiresRecipientAcceptance(t *testing.T) {
	w := newPlanAccessWorld(t)
	tasks, err := task.NewTaskService(w.store, w.bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	calendar := task.NewCalendarService(tasks)
	recipient := w.seedMember(t, "calendar-plan-recipient@example.com", auth.CompanyRoleMember)
	dbtest.GrantProject(t, w.ctx, w.pool, w.fixture.CompanyA, w.fixture.ProjectA, recipient.UserID)
	at := time.Now().UTC().Add(time.Hour).Truncate(time.Minute)
	created, err := tasks.Create(w.ctx, w.owner, task.CreateInput{
		ProjectID: w.fixture.ProjectA, Key: "calendar_consent", MilestoneType: task.MilestoneTypeProduct,
		Title: "Accepted scheduled task", Status: task.StatusTodo,
		Assignee:   task.ActorRef{Kind: task.ActorUser, ID: recipient.UserID},
		ExecutedBy: &task.ActorRef{Kind: task.ActorAgent, ID: w.fixture.AgentA},
		Schedule:   &schedule.Trigger{Type: schedule.TriggerTypeOnce, RunAt: &at},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := calendar.DecideAssignment(
		w.ctx,
		recipient,
		created.Task.ID,
		created.Task.Revision,
		true,
	); err != nil {
		t.Fatal(err)
	}
	agent := dbtest.NewUUID(t)
	dbtest.SeedBoundAgent(
		t,
		w.ctx,
		w.pool,
		agent,
		w.fixture.CompanyA,
		"Calendar replacement",
		"codex",
		&w.fixture.AgentA,
		w.fixture.UserA,
		w.fixture.RuntimeA,
	)
	if _, err := w.apply(t, w.owner, func(document *PlanDocument) {
		for i := range document.Tasks {
			if document.Tasks[i].Key == created.Task.Key {
				document.Tasks[i].AgentID = agent.String()
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	current, err := tasks.Get(w.ctx, w.owner, created.Task.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if current.AssignmentStatus != "pending" {
		t.Fatalf(
			"assignment after changing Agent before first Run = %s, want pending",
			current.AssignmentStatus,
		)
	}
	if err := calendar.ProcessDue(w.ctx, at.Add(time.Second), 100); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := w.pool.QueryRow(w.ctx, `SELECT count(*) FROM runs WHERE task_id=$1`, current.ID).
		Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("Runs without renewed consent = %d, want 0", count)
	}
}

func TestCompanyPlanCalendarPriorityKeepsQueuedOccurrence(t *testing.T) {
	w := newPlanAccessWorld(t)
	tasks, err := task.NewTaskService(w.store, w.bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	calendar := task.NewCalendarService(tasks)
	expression, zone := "0 9 * * *", "UTC"
	created, err := tasks.Create(w.ctx, w.owner, task.CreateInput{
		ProjectID: w.fixture.ProjectA, Key: "calendar_series", MilestoneType: task.MilestoneTypeProduct,
		Title: "Recurring work", Status: task.StatusTodo,
		ExecutedBy: &task.ActorRef{Kind: task.ActorAgent, ID: w.fixture.AgentA},
		Schedule:   &schedule.Trigger{Type: schedule.TriggerTypeCron, Expression: &expression, TimeZone: &zone},
	})
	if err != nil {
		t.Fatal(err)
	}
	at := created.Task.ScheduledAt.Add(24 * time.Hour)
	moved := at.Add(time.Hour)
	child, err := calendar.UpdateOccurrence(w.ctx, w.owner, created.Task.ID, task.CalendarOccurrenceChange{
		ExpectedRevision: created.Task.Revision, OccurrenceAt: at, RunAt: &moved,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := calendar.ProcessDue(w.ctx, moved.Add(time.Second), 100); err != nil {
		t.Fatal(err)
	}
	var originalRun uuid.UUID
	if err := w.pool.QueryRow(w.ctx, `SELECT id FROM runs WHERE task_id=$1`, child.Task.ID).
		Scan(&originalRun); err != nil {
		t.Fatal(err)
	}
	if _, err := w.apply(t, w.owner, func(document *PlanDocument) {
		for i := range document.Tasks {
			if document.Tasks[i].Key == child.Task.Key {
				document.Tasks[i].Priority = "high"
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := w.pool.QueryRow(w.ctx, `SELECT status FROM runs WHERE id=$1`, originalRun).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "queued" {
		t.Fatalf("priority-only Plan edit changed Run to %s, want queued", status)
	}
}
