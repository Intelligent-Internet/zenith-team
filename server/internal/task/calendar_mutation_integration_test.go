//go:build integration

package task

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/schedule"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestCalendarClaimRecoveryRechecksSchedule(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, wantStatus string
	}{
		{"unchanged", "queued"},
		{"runtime offline", "queued"},
		{"edit", "cancelled"},
		{"pause", "cancelled"},
		{"template consent", "cancelled"},
		{"occurrence consent", "cancelled"},
		{"deleted definition", "cancelled"},
		{"concurrent edit", "cancelled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx, pool, f, tasks, p := startTaskHarness(t)
			calendar := NewCalendarService(tasks)
			expression, zone := "*/5 * * * *", "UTC"
			created, err := tasks.Create(ctx, p, CreateInput{
				ProjectID: f.ProjectA, MilestoneType: MilestoneTypeProduct,
				Title: "Recover scheduled claim", Status: StatusTodo,
				ExecutedBy: &ActorRef{Kind: ActorAgent, ID: f.AgentA},
				Schedule: &schedule.Trigger{
					Type: schedule.TriggerTypeCron, Expression: &expression, TimeZone: &zone,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			first := *created.Task.ScheduledAt
			if err := calendar.ProcessDue(ctx, first.Add(time.Second), 100); err != nil {
				t.Fatal(err)
			}
			// Complete the first occurrence so recovery exercises a child whose
			// Conversation is distinct from the template edited below.
			dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE task_id=$1`, created.Task.ID)
			second := first.Add(5 * time.Minute)
			if err := calendar.ProcessDue(ctx, second.Add(time.Second), 100); err != nil {
				t.Fatal(err)
			}
			var childID uuid.UUID
			if err := pool.QueryRow(ctx, `UPDATE tasks SET scheduled_at=now()
				WHERE task_schedule_id=$1 AND schedule_occurrence_at=$2 RETURNING id`,
				created.Task.ScheduleID, second).Scan(&childID); err != nil {
				t.Fatal(err)
			}
			bus := newTestBus(t)
			var wakeup execution.RuntimeWork
			if err := bus.Subscribe(execution.TopicRuntime, func(_ context.Context, event events.Event) error {
				wakeup = event.Payload.(execution.RuntimeWork)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			runs := execution.NewRunService(execution.RunServiceOptions{
				Queries: tasks.store.Queries(), Transactor: tasks.store, Bus: bus,
				RouteResolver: conversation.ResolveConversationRoute,
			})
			claimed, err := runs.ClaimRun(ctx, f.AgentA, f.RuntimeA)
			if err != nil || claimed == nil {
				t.Fatalf("claim = %+v, %v; want child Run", claimed, err)
			}
			if claimed.TaskID.UUID != childID || !claimed.TaskScheduled {
				t.Fatalf("claimed Task = %v, scheduled = %v; want %v, true",
					claimed.TaskID, claimed.TaskScheduled, childID)
			}
			next := second.Add(5 * time.Minute)
			switch test.name {
			case "edit", "pause":
				current, err := tasks.Get(ctx, p, created.Task.ID, nil)
				if err != nil {
					t.Fatal(err)
				}
				change := CalendarScheduleChange{ExpectedRevision: current.Revision, Paused: test.name == "pause"}
				if test.name == "edit" {
					next = second.Add(time.Hour)
					change.Trigger = &schedule.Trigger{Type: schedule.TriggerTypeOnce, RunAt: &next}
				}
				if _, err := calendar.UpdateSchedule(ctx, p, current.ID, change); err != nil {
					t.Fatal(err)
				}
			case "template consent":
				dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET assignment_status='pending' WHERE id=$1`, created.Task.ID)
			case "occurrence consent":
				dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET assignment_status='pending' WHERE id=$1`, childID)
			case "deleted definition":
				dbtest.MustExec(t, ctx, pool, `DELETE FROM task_schedules WHERE id=$1`, created.Task.ScheduleID)
			case "runtime offline":
				if _, err := tasks.store.Queries().SetRuntimeOffline(ctx, f.RuntimeA); err != nil {
					t.Fatal(err)
				}
			}
			type recoveryResult struct {
				run *generated.Run
				err error
			}
			var result recoveryResult
			if test.name == "concurrent edit" {
				holder, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = holder.Rollback(context.Background()) }()
				q := generated.New(holder)
				if _, err := q.LockCompanyForMembershipMutation(ctx, f.CompanyA); err != nil {
					t.Fatal(err)
				}
				children, err := q.ListUnstartedTaskOccurrences(ctx, generated.ListUnstartedTaskOccurrencesParams{
					ScheduleID:     uuid.NullUUID{UUID: *created.Task.ScheduleID, Valid: true},
					ExcludedTaskID: created.Task.ID, Generation: created.Task.ScheduleGeneration,
					IncludeScheduled: true,
				})
				if err != nil || len(children) != 0 {
					t.Fatalf("unstarted children = %v, %v; want no dispatched child", children, err)
				}
				if _, err := q.AdvanceTaskScheduleGeneration(ctx, generated.AdvanceTaskScheduleGenerationParams{
					ID: *created.Task.ScheduleID, CompanyID: f.CompanyA,
				}); err != nil {
					t.Fatal(err)
				}
				done := make(chan recoveryResult, 1)
				go func() {
					run, err := runs.RequeueRunAfterClaimFailure(ctx, *claimed)
					done <- recoveryResult{run: run, err: err}
				}()
				deadline := time.Now().Add(5 * time.Second)
				for {
					select {
					case got := <-done:
						t.Fatalf("recovery bypassed uncommitted Calendar edit: %+v, %v", got.run, got.err)
					default:
					}
					var waiting bool
					if err := pool.QueryRow(ctx, `SELECT EXISTS (
						SELECT 1 FROM pg_stat_activity WHERE datname=current_database()
						AND state='active' AND wait_event_type='Lock'
						AND query LIKE '%LockCompanyForMembershipRead%')`).Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("recovery did not wait for the Calendar edit")
					}
					time.Sleep(10 * time.Millisecond)
				}
				// Calendar mutations may still need the Run lock after obtaining
				// the Company fence; recovery must not invert that order.
				if _, err := holder.Exec(ctx, `SELECT id FROM runs WHERE id=$1 FOR UPDATE NOWAIT`, claimed.ID); err != nil {
					t.Fatalf("recovery locked Run before Company: %v", err)
				}
				if err := holder.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case result = <-done:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			} else {
				result.run, result.err = runs.RequeueRunAfterClaimFailure(ctx, *claimed)
			}
			if result.err != nil || result.run == nil {
				t.Fatalf("recovery = %+v, %v", result.run, result.err)
			}
			if result.run.Status != test.wantStatus {
				t.Fatalf("recovered status = %q, want %q", result.run.Status, test.wantStatus)
			}
			cancelled := test.wantStatus == "cancelled"
			if result.run.FinishedAt.Valid != cancelled || result.run.Error.Valid != cancelled ||
				cancelled && result.run.Error.String != execution.CancellationScheduleInvalidated {
				t.Fatalf("recovery metadata: finishedAt=%v error=%v", result.run.FinishedAt, result.run.Error)
			}
			if result.run.ClaimedAt.Valid || result.run.PrepareLeaseExpiresAt.Valid {
				t.Fatal("recovery retained the failed claim lease")
			}
			wantRunID := claimed.ID.String()
			if cancelled {
				wantRunID = ""
			}
			if wakeup.RuntimeID != f.RuntimeA.String() || wakeup.RunID != wantRunID {
				t.Fatalf("runtime wakeup = %+v, want runtime %s and Run %q", wakeup, f.RuntimeA, wantRunID)
			}
			wantAction := execution.EventRunQueued
			if cancelled {
				wantAction = execution.EventRunCancelled
			}
			var action string
			if err := pool.QueryRow(ctx, `SELECT action FROM activity_events
				WHERE entity_id=$1 ORDER BY created_at DESC, id DESC LIMIT 1`, claimed.ID).Scan(&action); err != nil {
				t.Fatal(err)
			}
			if action != wantAction {
				t.Fatalf("recovery history = %q, want %q", action, wantAction)
			}
			if _, err := runs.RequeueRunAfterClaimFailure(ctx, *claimed); err == nil {
				t.Fatal("stale recovery succeeded, want CAS failure")
			}
			if test.name == "pause" {
				current, err := tasks.Get(ctx, p, created.Task.ID, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := calendar.UpdateSchedule(ctx, p, current.ID, CalendarScheduleChange{
					ExpectedRevision: current.Revision,
				}); err != nil {
					t.Fatal(err)
				}
			}
			if test.name == "edit" || test.name == "pause" || test.name == "concurrent edit" {
				if err := calendar.ProcessDue(ctx, next.Add(time.Second), 100); err != nil {
					t.Fatal(err)
				}
				var queued int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs r JOIN tasks t ON t.id=r.task_id
					WHERE t.task_schedule_id=$1 AND r.status='queued'`, created.Task.ScheduleID).Scan(&queued); err != nil {
					t.Fatal(err)
				}
				if queued != 1 {
					t.Fatalf("queued Runs for the next occurrence = %d, want 1", queued)
				}
			}
		})
	}
}

func TestCalendarManualStartRestoresTaskChat(t *testing.T) {
	for _, disposition := range []string{"scheduled", "cancelled", "skipped", "missed", "started", ""} {
		t.Run("disposition="+disposition, func(t *testing.T) {
			ctx, pool, f, tasks, p := startTaskHarness(t)
			messages, err := conversation.NewConversationService(conversation.ServiceOptions{
				Store: tasks.store, Bus: newTestBus(t),
				TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{
					Check:    CheckMessageRunInTx,
					Prepare:  PrepareMessageRunInTx,
					NextStep: RunNextStepInTx,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			var firedAt *time.Time
			if disposition == "started" {
				previous := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
				firedAt = &previous
			}
			// Seed the worker's possible dispositions; the explicit start below
			// must supersede any decision not to run this occurrence automatically.
			dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET assignment_status='accepted',
				schedule_disposition=$2, schedule_fired_at=$3 WHERE id=$1`, f.TaskA, disposition, firedAt)
			target := conversation.MessageTarget{Kind: conversation.TargetTask, ID: f.TaskA}
			if disposition != "" && disposition != "started" {
				_, err := messages.Send(ctx, p, target, conversation.SendInput{
					RequestKey: dbtest.NewUUID(t), Content: "Do not bypass the calendar.",
				})
				if !errors.Is(err, conversation.ErrConflict) {
					t.Fatalf("chat before explicit start error = %v, want conflict", err)
				}
			}
			current, err := tasks.Get(ctx, p, f.TaskA, nil)
			if err != nil {
				t.Fatal(err)
			}
			started, err := tasks.StartExecution(
				ctx,
				p,
				f.TaskA,
				StartExecutionInput{ExpectedRevision: current.Revision},
			)
			if err != nil || started.RunID == nil {
				t.Fatalf("manual start = %+v, %v; want a Run", started, err)
			}
			want := "started"
			if disposition == "" {
				want = ""
			}
			if started.Task.ScheduleDisposition != want {
				t.Errorf(
					"disposition after manual start = %q, want %q",
					started.Task.ScheduleDisposition,
					want,
				)
			}
			if disposition != "" && started.Task.ScheduleFiredAt == nil {
				t.Error("manual start did not record the occurrence's execution")
			}
			if firedAt != nil &&
				(started.Task.ScheduleFiredAt == nil || !started.Task.ScheduleFiredAt.Equal(*firedAt)) {
				t.Error("restarting changed the original firing receipt")
			}
			queued, err := messages.Send(ctx, p, target, conversation.SendInput{
				RequestKey: dbtest.NewUUID(t), Content: "Include this follow-up after the current Run.",
			})
			if err != nil || queued.Disposition != conversation.SendDispositionQueued ||
				queued.QueueItem == nil {
				t.Fatalf("chat during manual Run = %+v, %v; want a queued message", queued, err)
			}
			dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, *started.RunID)
			followup, err := messages.Send(ctx, p, target, conversation.SendInput{
				RequestKey: dbtest.NewUUID(t), Content: "Continue after the completed Run.",
			})
			if err != nil || followup.RunID == nil {
				t.Fatalf("chat after manual Run = %+v, %v; want a new Run", followup, err)
			}
		})
	}
}

func TestCalendarVirtualOccurrenceRequiresItsBriefRevision(t *testing.T) {
	ctx, pool, f, tasks, p := startTaskHarness(t)
	calendar := NewCalendarService(tasks)
	expression, zone := "0 9 * * *", "UTC"
	created, err := tasks.Create(ctx, p, CreateInput{
		ProjectID: f.ProjectA, MilestoneType: MilestoneTypeProduct, Status: StatusTodo,
		Title: "Original series", Description: "Original instructions",
		Schedule: &schedule.Trigger{Type: schedule.TriggerTypeCron, Expression: &expression, TimeZone: &zone},
	})
	if err != nil {
		t.Fatal(err)
	}
	at := created.Task.ScheduledAt.Add(24 * time.Hour)
	preview, err := calendar.ListOccurrences(ctx, p, at, at.Add(time.Minute))
	if err != nil || len(preview.Occurrences) != 1 || preview.Occurrences[0].Materialized {
		t.Fatalf("preview = %+v, %v; want one virtual occurrence", preview, err)
	}
	stale := preview.Occurrences[0]
	title, description := "Revised series", "Revised instructions"
	if _, err := tasks.Update(ctx, p, created.Task.ID, UpdateInput{
		ExpectedRevision: created.Task.Revision, Title: &title, Description: &description,
	}); err != nil {
		t.Fatal(err)
	}
	movedAt := at.Add(time.Hour)
	_, err = calendar.UpdateOccurrence(ctx, p, stale.Task.ID, CalendarOccurrenceChange{
		ExpectedRevision: stale.Task.Revision, OccurrenceAt: at, RunAt: &movedAt,
		Title: &stale.Task.Title, Description: &stale.Task.Description,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale occurrence edit error = %v, want conflict", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE task_schedule_id=$1`, created.Task.ScheduleID).
		Scan(&count); err != nil || count != 1 {
		t.Fatalf("Tasks after stale edit = %d, %v; want only the template", count, err)
	}
	fresh, err := calendar.ListOccurrences(ctx, p, at, at.Add(time.Minute))
	if err != nil || len(fresh.Occurrences) != 1 {
		t.Fatalf("fresh preview = %+v, %v", fresh, err)
	}
	latest := fresh.Occurrences[0]
	if latest.Task.Revision <= stale.Task.Revision || latest.Task.Title != title ||
		latest.Task.Description != description {
		t.Fatalf("fresh preview mixes revisions: %+v", latest.Task)
	}
	updated, err := calendar.UpdateOccurrence(ctx, p, latest.Task.ID, CalendarOccurrenceChange{
		ExpectedRevision: latest.Task.Revision, OccurrenceAt: at, RunAt: &movedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Task.Title != title || updated.Task.Description != description ||
		!updated.Task.ScheduledAt.Equal(movedAt) {
		t.Fatalf("time-only edit lost the current brief: %+v", updated.Task)
	}
}

func TestCalendarMutationCancelsWaitingFirstRunAndPreservesClaimedWork(t *testing.T) {
	for _, test := range []struct {
		name, status string
		pause        bool
	}{
		{"edit queued", "queued", false},
		{"pause queued", "queued", true},
		{"edit dispatched", "dispatched", false},
		{"pause dispatched", "dispatched", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, pool, f, tasks, p := startTaskHarness(t)
			calendar := NewCalendarService(tasks)
			at := time.Now().UTC().Add(time.Hour).Truncate(time.Minute)
			created, err := tasks.Create(ctx, p, CreateInput{
				ProjectID:     f.ProjectA,
				MilestoneType: MilestoneTypeProduct,
				Title:         "Waiting occurrence",
				Status:        StatusTodo,
				ExecutedBy:    &ActorRef{Kind: ActorAgent, ID: f.AgentA},
				Schedule:      &schedule.Trigger{Type: schedule.TriggerTypeOnce, RunAt: &at},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := calendar.ProcessDue(ctx, at.Add(time.Second), 100); err != nil {
				t.Fatal(err)
			}
			var originalRun uuid.UUID
			if err := pool.QueryRow(ctx, `SELECT id FROM runs WHERE task_id=$1`, created.Task.ID).
				Scan(&originalRun); err != nil {
				t.Fatal(err)
			}
			if test.status == "dispatched" {
				dbtest.MustExec(
					t,
					ctx,
					pool,
					`UPDATE runs SET status='dispatched', claimed_at=now() WHERE id=$1`,
					originalRun,
				)
			}
			current, err := tasks.Get(ctx, p, created.Task.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			revisedAt := at.Add(time.Hour)
			change := CalendarScheduleChange{ExpectedRevision: current.Revision, Paused: test.pause}
			if !test.pause {
				change.Trigger = &schedule.Trigger{Type: schedule.TriggerTypeOnce, RunAt: &revisedAt}
			}
			updated, err := calendar.UpdateSchedule(ctx, p, current.ID, change)
			if err != nil {
				t.Fatal(err)
			}
			var status string
			if err := pool.QueryRow(ctx, `SELECT status FROM runs WHERE id=$1`, originalRun).
				Scan(&status); err != nil {
				t.Fatal(err)
			}
			want := "cancelled"
			if test.status == "dispatched" {
				want = "dispatched"
			}
			if status != want {
				t.Fatalf("original Run status = %s, want %s", status, want)
			}
			if test.pause && test.status == "queued" {
				if _, err := calendar.UpdateSchedule(
					ctx,
					p,
					current.ID,
					CalendarScheduleChange{ExpectedRevision: updated.Task.Revision},
				); err != nil {
					t.Fatal(err)
				}
				if err := calendar.ProcessDue(ctx, at.Add(2*time.Second), 100); err != nil {
					t.Fatal(err)
				}
				var count int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE task_id=$1`, current.ID).
					Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 1 {
					t.Fatalf("resume replayed stopped occurrence: %d Runs, want 1", count)
				}
			}
			if !test.pause && test.status == "queued" {
				if err := calendar.ProcessDue(ctx, at.Add(2*time.Second), 100); err != nil {
					t.Fatal(err)
				}
				var waiting int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs r JOIN tasks t ON t.id=r.task_id
					WHERE t.task_schedule_id=$1 AND r.status='queued'`, created.Task.ScheduleID).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting != 0 {
					t.Fatalf("queued Runs before revised time = %d, want 0", waiting)
				}
				if err := calendar.ProcessDue(ctx, revisedAt.Add(time.Second), 100); err != nil {
					t.Fatal(err)
				}
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs r JOIN tasks t ON t.id=r.task_id
					WHERE t.task_schedule_id=$1 AND r.status='queued'`, created.Task.ScheduleID).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting != 1 {
					t.Fatalf("queued Runs at revised time = %d, want 1", waiting)
				}
			}
		})
	}
}

func TestCalendarQueuedBriefEditRebuildsExecutionPrompt(t *testing.T) {
	ctx, pool, f, tasks, p := startTaskHarness(t)
	calendar := NewCalendarService(tasks)
	at := time.Now().UTC().Add(time.Hour).Truncate(time.Minute)
	created, err := tasks.Create(ctx, p, CreateInput{
		ProjectID: f.ProjectA, MilestoneType: MilestoneTypeProduct,
		Title: "Old scheduled title", Description: "Old scheduled instructions", Status: StatusTodo,
		ExecutedBy: &ActorRef{Kind: ActorAgent, ID: f.AgentA},
		Schedule:   &schedule.Trigger{Type: schedule.TriggerTypeOnce, RunAt: &at},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := calendar.ProcessDue(ctx, at.Add(time.Second), 100); err != nil {
		t.Fatal(err)
	}
	var originalRun uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM runs WHERE task_id=$1`, created.Task.ID).
		Scan(&originalRun); err != nil {
		t.Fatal(err)
	}
	current, err := tasks.Get(ctx, p, created.Task.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	title, description := "New scheduled title", "New scheduled instructions"
	updated, err := tasks.Update(ctx, p, current.ID, UpdateInput{
		ExpectedRevision: current.Revision, Title: &title, Description: &description,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RunID != nil {
		t.Fatal("editing scheduled instructions started an immediate Run")
	}
	var originalStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM runs WHERE id=$1`, originalRun).
		Scan(&originalStatus); err != nil {
		t.Fatal(err)
	}
	if originalStatus != "cancelled" {
		t.Fatalf("stale prompt Run status = %s, want cancelled", originalStatus)
	}
	if err := calendar.ProcessDue(ctx, at.Add(2*time.Second), 100); err != nil {
		t.Fatal(err)
	}
	var prompt string
	if err := pool.QueryRow(ctx, `SELECT r.context->>'prompt' FROM runs r JOIN tasks t ON t.id=r.task_id
		WHERE t.task_schedule_id=$1 AND r.status='queued'`, created.Task.ScheduleID).Scan(&prompt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, title) || !strings.Contains(prompt, description) ||
		strings.Contains(prompt, "Old scheduled") {
		t.Fatalf("replacement Run kept stale brief: %q", prompt)
	}
}

func TestCalendarQueuedHandoffDoesNotBypassScheduleOrAcceptance(t *testing.T) {
	for _, toMember := range []bool{false, true} {
		name := "another agent on own computer"
		if toMember {
			name = "another member"
		}
		t.Run(name, func(t *testing.T) {
			ctx, pool, f, tasks, p := startTaskHarness(t)
			calendar := NewCalendarService(tasks)
			at := time.Now().UTC().Add(time.Hour).Truncate(time.Minute)
			created, err := tasks.Create(ctx, p, CreateInput{
				ProjectID:     f.ProjectA,
				MilestoneType: MilestoneTypeProduct,
				Title:         "Scheduled handoff",
				Status:        StatusTodo,
				ExecutedBy:    &ActorRef{Kind: ActorAgent, ID: f.AgentA},
				Schedule:      &schedule.Trigger{Type: schedule.TriggerTypeOnce, RunAt: &at},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := calendar.ProcessDue(ctx, at.Add(time.Second), 100); err != nil {
				t.Fatal(err)
			}
			current, err := tasks.Get(ctx, p, created.Task.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			change := UpdateInput{ExpectedRevision: current.Revision, ConfirmHandoff: true}
			var recipient auth.UserPrincipal
			if toMember {
				member := seedTaskMember(t, ctx, pool, f, "calendar-recipient@example.com")
				runtime := seedTaskRuntime(t, ctx, pool, f, "calendar-recipient", member)
				dbtest.SeedAgentBinding(t, ctx, pool, f.CompanyA, f.AgentA, member, runtime, "codex")
				change.Assignee = &ActorRef{Kind: ActorUser, ID: member}
				recipient = auth.UserPrincipal{
					CompanyID: f.CompanyA,
					UserID:    member,
					Role:      auth.CompanyRoleMember,
				}
			} else {
				agent := dbtest.NewUUID(t)
				dbtest.SeedBoundAgent(
					t,
					ctx,
					pool,
					agent,
					f.CompanyA,
					"Calendar replacement",
					"codex",
					&f.AgentA,
					f.UserA,
					f.RuntimeA,
				)
				change.ExecutedBy = &ActorRef{Kind: ActorAgent, ID: agent}
			}
			handed, err := tasks.Update(ctx, p, current.ID, change)
			if err != nil {
				t.Fatal(err)
			}
			if handed.RunID != nil {
				t.Fatal("scheduled handoff started an immediate Run")
			}
			if toMember {
				if handed.Task.AssignmentStatus != "pending" {
					t.Fatalf("assignment = %s, want pending", handed.Task.AssignmentStatus)
				}
				if err := calendar.ProcessDue(ctx, at.Add(2*time.Second), 100); err != nil {
					t.Fatal(err)
				}
				accepted, err := calendar.DecideAssignment(
					ctx,
					recipient,
					handed.Task.ID,
					handed.Task.Revision,
					true,
				)
				if err != nil {
					t.Fatal(err)
				}
				if accepted.RunID != nil {
					t.Fatal("acceptance started future scheduled work immediately")
				}
			}
			var live int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE task_id=$1
				AND status NOT IN ('completed','failed','cancelled')`, current.ID).Scan(&live); err != nil {
				t.Fatal(err)
			}
			if live != 0 {
				t.Fatalf("live Runs before scheduled delivery = %d, want 0", live)
			}
		})
	}
}

func TestCalendarUnchangedBriefKeepsRecipientConsent(t *testing.T) {
	ctx, pool, f, tasks, p := startTaskHarness(t)
	calendar := NewCalendarService(tasks)
	member := seedTaskMember(t, ctx, pool, f, "calendar-consent@example.com")
	recipient := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: member, Role: auth.CompanyRoleMember}
	at := time.Now().UTC().Add(time.Hour).Truncate(time.Minute)
	created, err := tasks.Create(ctx, p, CreateInput{
		ProjectID: f.ProjectA, MilestoneType: MilestoneTypeProduct,
		Title: "Reviewed calendar task", Description: "Reviewed instructions", Status: StatusTodo,
		Assignee: ActorRef{Kind: ActorUser, ID: member},
		Schedule: &schedule.Trigger{Type: schedule.TriggerTypeOnce, RunAt: &at},
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := calendar.DecideAssignment(ctx, recipient, created.Task.ID, created.Task.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	priority := PriorityHigh
	updated, err := tasks.Update(ctx, p, created.Task.ID, UpdateInput{
		ExpectedRevision: accepted.Task.Revision,
		Title:            &accepted.Task.Title,
		Description:      &accepted.Task.Description,
		Priority:         &priority,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Task.AssignmentStatus != "accepted" {
		t.Fatalf("assignment after priority-only change = %s, want accepted", updated.Task.AssignmentStatus)
	}
	if updated.Task.ScheduleGeneration != accepted.Task.ScheduleGeneration {
		t.Fatalf(
			"occurrence generation changed from %d to %d without changed instructions",
			accepted.Task.ScheduleGeneration,
			updated.Task.ScheduleGeneration,
		)
	}
}

func TestCalendarCancelFirstOccurrenceKeepsRecipientConsent(t *testing.T) {
	ctx, pool, f, tasks, creator := startTaskHarness(t)
	calendar := NewCalendarService(tasks)
	member := seedTaskMember(t, ctx, pool, f, "calendar-cancel-consent@example.com")
	recipient := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: member, Role: auth.CompanyRoleMember}
	expression, zone := "0 9 * * *", "UTC"
	created, err := tasks.Create(ctx, creator, CreateInput{
		ProjectID: f.ProjectA, MilestoneType: MilestoneTypeProduct,
		Title: "Accepted recurring work", Status: StatusTodo,
		Assignee: ActorRef{Kind: ActorUser, ID: member},
		Schedule: &schedule.Trigger{Type: schedule.TriggerTypeCron, Expression: &expression, TimeZone: &zone},
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := calendar.DecideAssignment(ctx, recipient, created.Task.ID, created.Task.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := calendar.UpdateOccurrence(ctx, creator, created.Task.ID, CalendarOccurrenceChange{
		ExpectedRevision: accepted.Task.Revision,
		OccurrenceAt:     *created.Task.ScheduleOccurrenceAt,
		Cancel:           true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Task.AssignmentStatus != "accepted" || cancelled.Task.ScheduleDisposition != "cancelled" {
		t.Errorf(
			"cancelled occurrence = %s / %s, want accepted / cancelled",
			cancelled.Task.AssignmentStatus,
			cancelled.Task.ScheduleDisposition,
		)
	}
	next := created.Task.ScheduledAt.Add(24 * time.Hour)
	if err := calendar.ProcessDue(ctx, next.Add(time.Second), 100); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tasks
		WHERE task_schedule_id=$1 AND schedule_occurrence_at=$2 AND assignment_status='accepted'`, created.Task.ScheduleID, next).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("next accepted occurrence count = %d, want 1", count)
	}
}
