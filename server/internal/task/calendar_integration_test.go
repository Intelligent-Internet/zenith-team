//go:build integration

package task

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/sync/errgroup"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/schedule"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestCalendarTaskWaitsForTimeAndFiresOnce(t *testing.T) {
	ctx, pool, fixture, tasks, principal := startTaskHarness(t)
	calendar := NewCalendarService(tasks)
	at := time.Now().UTC().Add(time.Hour).Truncate(time.Minute)
	created, err := tasks.Create(ctx, principal, CreateInput{
		ProjectID: fixture.ProjectA, MilestoneType: MilestoneTypeProduct,
		Title: "Scheduled work", Status: StatusTodo,
		ExecutedBy: &ActorRef{Kind: ActorAgent, ID: fixture.AgentA},
		Schedule:   &schedule.Trigger{Type: schedule.TriggerTypeOnce, RunAt: &at},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.RunID != nil || created.Task.ScheduledAt == nil || created.Task.AssignmentStatus != "accepted" {
		t.Fatalf("scheduled creation must wait: %+v", created)
	}
	if err := calendar.ProcessDue(ctx, time.Now().UTC(), 100); err != nil {
		t.Fatal(err)
	}
	var runs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE task_id=$1`, created.Task.ID).
		Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("started %d Runs before the scheduled time", runs)
	}
	for range 2 {
		if err := calendar.ProcessDue(ctx, at.Add(time.Second), 100); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE task_id=$1`, created.Task.ID).
		Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("got %d Runs after duplicate delivery, want one", runs)
	}
}

func TestCalendarRescheduleOverExistingException(t *testing.T) {
	ctx, _, f, tasks, p := startTaskHarness(t)
	calendar := NewCalendarService(tasks)
	expression, zone := "0 9 * * *", "UTC"
	created, err := tasks.Create(ctx, p, CreateInput{
		ProjectID: f.ProjectA, MilestoneType: MilestoneTypeProduct, Title: "Series", Status: StatusTodo,
		Schedule: &schedule.Trigger{Type: schedule.TriggerTypeCron, Expression: &expression, TimeZone: &zone},
	})
	if err != nil {
		t.Fatal(err)
	}
	at := created.Task.ScheduledAt.Add(24 * time.Hour)
	moved := at.Add(time.Hour)
	exception, err := calendar.UpdateOccurrence(ctx, p, created.Task.ID, CalendarOccurrenceChange{
		ExpectedRevision: created.Task.Revision, OccurrenceAt: at, RunAt: &moved,
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := calendar.UpdateSchedule(ctx, p, created.Task.ID, CalendarScheduleChange{
		ExpectedRevision: created.Task.Revision,
		Trigger:          &schedule.Trigger{Type: schedule.TriggerTypeOnce, RunAt: &at},
	})
	if err != nil {
		t.Fatalf("reschedule onto an old exception: %v", err)
	}
	if !updated.Task.ScheduledAt.Equal(at) {
		t.Fatalf("scheduled at = %v, want %v", updated.Task.ScheduledAt, at)
	}
	old, err := tasks.Get(ctx, p, exception.Task.ID, nil)
	if err != nil || old.ScheduleDisposition != "cancelled" {
		t.Fatalf("old exception = %+v, %v", old, err)
	}
}

func TestCalendarBriefEditRestoresInvalidatedFutureSlot(t *testing.T) {
	ctx, _, f, tasks, p := startTaskHarness(t)
	calendar := NewCalendarService(tasks)
	expression, zone := "0 9 * * *", "UTC"
	created, err := tasks.Create(ctx, p, CreateInput{
		ProjectID: f.ProjectA, MilestoneType: MilestoneTypeProduct, Title: "Before", Status: StatusTodo,
		Schedule: &schedule.Trigger{Type: schedule.TriggerTypeCron, Expression: &expression, TimeZone: &zone},
	})
	if err != nil {
		t.Fatal(err)
	}
	at := created.Task.ScheduledAt.Add(24 * time.Hour)
	moved := at.Add(time.Hour)
	if _, err := calendar.UpdateOccurrence(ctx, p, created.Task.ID, CalendarOccurrenceChange{
		ExpectedRevision: created.Task.Revision, OccurrenceAt: at, RunAt: &moved,
	}); err != nil {
		t.Fatal(err)
	}
	title := "After"
	if _, err := tasks.Update(
		ctx,
		p,
		created.Task.ID,
		UpdateInput{ExpectedRevision: created.Task.Revision, Title: &title},
	); err != nil {
		t.Fatal(err)
	}
	page, err := calendar.ListOccurrences(ctx, p, at, at.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var scheduled int
	for _, item := range page.Occurrences {
		if item.RunAt.Equal(at) && item.Task.ScheduleDisposition == "scheduled" && item.Task.Title == title {
			scheduled++
		}
	}
	if scheduled != 1 {
		t.Fatalf("replacement occurrences = %d, want 1: %+v", scheduled, page.Occurrences)
	}
}

func TestCalendarRangeExpansionIsBounded(t *testing.T) {
	ctx, _, fixture, tasks, principal := startTaskHarness(t)
	calendar := NewCalendarService(tasks)
	expression, zone := "*/5 * * * *", "UTC"
	created, err := tasks.Create(ctx, principal, CreateInput{
		ProjectID: fixture.ProjectA, MilestoneType: MilestoneTypeProduct,
		Title: "Frequent calendar work", Status: StatusTodo,
		Schedule: &schedule.Trigger{Type: schedule.TriggerTypeCron, Expression: &expression, TimeZone: &zone},
	})
	if err != nil {
		t.Fatal(err)
	}
	from := *created.Task.ScheduledAt
	page, err := calendar.ListOccurrences(ctx, principal, from, from.Add(93*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Occurrences) != 2000 || !page.Truncated {
		t.Fatalf("dense calendar: %d occurrences, truncated=%t", len(page.Occurrences), page.Truncated)
	}
	if _, err := calendar.ListOccurrences(
		ctx,
		principal,
		from,
		from.Add(94*24*time.Hour),
	); !errors.Is(
		err,
		ErrInvalidInput,
	) {
		t.Fatalf("unbounded range error=%v", err)
	}
}

func TestCalendarScheduleEditsRetainActivitySnapshots(t *testing.T) {
	ctx, pool, fixture, tasks, principal := startTaskHarness(t)
	calendar := NewCalendarService(tasks)
	at := time.Now().UTC().Add(time.Hour).Truncate(time.Minute)
	created, err := tasks.Create(ctx, principal, CreateInput{
		ProjectID: fixture.ProjectA, MilestoneType: MilestoneTypeProduct,
		Title: "Audited schedule", Status: StatusTodo,
		Schedule: &schedule.Trigger{Type: schedule.TriggerTypeOnce, RunAt: &at},
	})
	if err != nil {
		t.Fatal(err)
	}
	paused, err := calendar.UpdateSchedule(ctx, principal, created.Task.ID, CalendarScheduleChange{
		ExpectedRevision: created.Task.Revision, Paused: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var before, after bool
	if err := pool.QueryRow(ctx, `SELECT (details->'before'->'schedule'->>'paused')::boolean,
		(details->'after'->'schedule'->>'paused')::boolean FROM activity_events
		WHERE entity_type='task' AND entity_id=$1 AND revision=$2 AND schema_version=1`,
		created.Task.ID.String(), paused.Task.Revision).Scan(&before, &after); err != nil {
		t.Fatal(err)
	}
	if before || !after {
		t.Fatalf("pause history before=%t after=%t", before, after)
	}
}

func TestCalendarRejectsStaleVirtualOccurrenceAndReturnsCanonicalTasks(t *testing.T) {
	ctx, _, fixture, tasks, principal := startTaskHarness(t)
	calendar := NewCalendarService(tasks)
	expression, zone := "0 9 * * *", "UTC"
	created, err := tasks.Create(
		ctx,
		principal,
		CreateInput{
			ProjectID:     fixture.ProjectA,
			MilestoneType: MilestoneTypeProduct,
			Title:         "Daily",
			Status:        StatusTodo,
			Schedule: &schedule.Trigger{
				Type:       schedule.TriggerTypeCron,
				Expression: &expression,
				TimeZone:   &zone,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	next := created.Task.ScheduledAt.Add(24 * time.Hour)
	moved := next.Add(time.Hour)
	change := CalendarOccurrenceChange{ExpectedRevision: created.Task.Revision, OccurrenceAt: next, RunAt: &moved}
	if _, err := calendar.UpdateOccurrence(ctx, principal, created.Task.ID, change); err != nil {
		t.Fatal(err)
	}
	if _, err := calendar.UpdateOccurrence(ctx, principal, created.Task.ID, change); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale virtual edit: %v", err)
	}
	page, err := calendar.ListOccurrences(ctx, principal, *created.Task.ScheduledAt, next.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Occurrences {
		if item.Task.Access == nil {
			t.Fatalf("incomplete Calendar projection: %+v", item.Task)
		}
	}
}

func TestCalendarPauseAndForeignEditFenceExecution(t *testing.T) {
	ctx, _, fixture, tasks, principal := startTaskHarness(t)
	calendar := NewCalendarService(tasks)
	at := time.Now().UTC().Add(time.Hour).Truncate(time.Minute)
	created, err := tasks.Create(
		ctx,
		principal,
		CreateInput{
			ProjectID:     fixture.ProjectA,
			MilestoneType: MilestoneTypeProduct,
			Title:         "Pause",
			Status:        StatusTodo,
			Schedule:      &schedule.Trigger{Type: schedule.TriggerTypeOnce, RunAt: &at},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := calendar.UpdateSchedule(
		ctx,
		principal,
		created.Task.ID,
		CalendarScheduleChange{ExpectedRevision: created.Task.Revision, Paused: true},
	); err != nil {
		t.Fatal(err)
	}
	if err := calendar.ProcessDue(ctx, at.Add(time.Second), 100); err != nil {
		t.Fatal(err)
	}
	value, err := tasks.Get(ctx, principal, created.Task.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if value.ScheduleFiredAt != nil {
		t.Fatal("paused Task ran")
	}
	foreign := principal
	foreign.CompanyID = fixture.CompanyB
	if _, err := calendar.GetSchedule(ctx, foreign, created.Task.ID); err == nil {
		t.Fatal("cross-company schedule read succeeded")
	}
}

func TestCalendarAssignmentRequiresRecipientAcceptance(t *testing.T) {
	ctx, pool, fixture, tasks, principal := startTaskHarness(t)
	calendar := NewCalendarService(tasks)
	member := seedTaskMember(t, ctx, pool, fixture, "calendar-recipient@example.com")
	at := time.Now().UTC().Add(time.Hour).Truncate(time.Minute)
	created, err := tasks.Create(ctx, principal, CreateInput{
		ProjectID: fixture.ProjectA, MilestoneType: MilestoneTypeProduct,
		Title: "Review this assignment", Status: StatusTodo,
		Assignee: ActorRef{Kind: ActorUser, ID: member},
		Schedule: &schedule.Trigger{Type: schedule.TriggerTypeOnce, RunAt: &at},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Task.AssignmentStatus != "pending" {
		t.Fatalf("assignment = %s", created.Task.AssignmentStatus)
	}
	if _, err := calendar.DecideAssignment(
		ctx,
		principal,
		created.Task.ID,
		created.Task.Revision,
		true,
	); err == nil {
		t.Fatal("creator accepted on recipient's behalf")
	}
	recipient := auth.UserPrincipal{UserID: member, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember}
	accepted, err := calendar.DecideAssignment(ctx, recipient, created.Task.ID, created.Task.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Task.AssignmentStatus != "accepted" || accepted.RunID != nil {
		t.Fatalf("acceptance started future work: %+v", accepted)
	}
}

func TestCalendarRecurringMaterializesIndependentTasksAndSkipsOverlap(t *testing.T) {
	ctx, pool, fixture, tasks, principal := startTaskHarness(t)
	calendar := NewCalendarService(tasks)
	expression, zone := "*/5 * * * *", "UTC"
	trigger := schedule.Trigger{Type: schedule.TriggerTypeCron, Expression: &expression, TimeZone: &zone}
	created, err := tasks.Create(
		ctx,
		principal,
		CreateInput{
			ProjectID:     fixture.ProjectA,
			MilestoneType: MilestoneTypeProduct,
			Title:         "Recurring report",
			Status:        StatusTodo,
			ExecutedBy:    &ActorRef{Kind: ActorAgent, ID: fixture.AgentA},
			Schedule:      &trigger,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	first := *created.Task.ScheduledAt
	if err := calendar.ProcessDue(ctx, first.Add(time.Second), 100); err != nil {
		t.Fatal(err)
	}
	next := first.Add(5 * time.Minute)
	for range 2 {
		if err := calendar.ProcessDue(ctx, next.Add(time.Second), 100); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE task_schedule_id=$1`, created.Task.ScheduleID).
		Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("got %d Tasks, want two distinct occurrences", count)
	}
	var disposition string
	if err := pool.QueryRow(ctx, `SELECT schedule_disposition FROM tasks WHERE task_schedule_id=$1 AND schedule_occurrence_at=$2`, created.Task.ScheduleID, next).
		Scan(&disposition); err != nil {
		t.Fatal(err)
	}
	if disposition != "skipped" {
		t.Fatalf("overlap = %s, want skipped", disposition)
	}
}

func TestCalendarTaskChatCannotStartBeforeAcceptanceOrTime(t *testing.T) {
	ctx, _, fixture, tasks, principal := startTaskHarness(t)
	at := time.Now().UTC().Add(time.Hour).Truncate(time.Minute)
	created, err := tasks.Create(
		ctx,
		principal,
		CreateInput{
			ProjectID:     fixture.ProjectA,
			MilestoneType: MilestoneTypeProduct,
			Title:         "Wait for the calendar",
			Status:        StatusTodo,
			Schedule:      &schedule.Trigger{Type: schedule.TriggerTypeOnce, RunAt: &at},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	row, err := tasks.store.Queries().
		GetTaskForCompanyByIDForUpdate(ctx, generated.GetTaskForCompanyByIDForUpdateParams{CompanyID: fixture.CompanyA, TaskID: created.Task.ID})
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := CheckMessageRunInTx(ctx, tasks.store.Queries(), row)
	if err != nil || allowed {
		t.Fatalf("future Task chat admitted=%v error=%v", allowed, err)
	}
}

func TestCalendarOccurrenceMoveAndCancellationKeepSeries(t *testing.T) {
	ctx, pool, fixture, tasks, principal := startTaskHarness(t)
	calendar := NewCalendarService(tasks)
	expression, zone := "0 9 * * *", "Asia/Ho_Chi_Minh"
	created, err := tasks.Create(
		ctx,
		principal,
		CreateInput{
			ProjectID:     fixture.ProjectA,
			MilestoneType: MilestoneTypeProduct,
			Title:         "Daily report",
			Status:        StatusTodo,
			Schedule: &schedule.Trigger{
				Type:       schedule.TriggerTypeCron,
				Expression: &expression,
				TimeZone:   &zone,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	first := *created.Task.ScheduledAt
	next := first.Add(24 * time.Hour)
	movedAt := next.Add(time.Hour)
	moved, err := calendar.UpdateOccurrence(
		ctx,
		principal,
		created.Task.ID,
		CalendarOccurrenceChange{ExpectedRevision: created.Task.Revision, OccurrenceAt: next, RunAt: &movedAt},
	)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Task.ID == created.Task.ID || !moved.Task.ScheduledAt.Equal(movedAt) {
		t.Fatalf("move changed template: %+v", moved.Task)
	}
	page, err := calendar.ListOccurrences(ctx, principal, first.Add(-time.Minute), next.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	occurrences := 0
	for _, item := range page.Occurrences {
		if item.OccurrenceAt.Equal(next) {
			occurrences++
			if !item.RunAt.Equal(movedAt) {
				t.Fatal("Calendar did not show moved time")
			}
		}
	}
	if occurrences != 1 {
		t.Fatalf("moved occurrence appears %d times", occurrences)
	}
	_, err = calendar.UpdateOccurrence(
		ctx,
		principal,
		moved.Task.ID,
		CalendarOccurrenceChange{ExpectedRevision: moved.Task.Revision, OccurrenceAt: next, Cancel: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	var paused bool
	if err := pool.QueryRow(ctx, `SELECT paused FROM task_schedules WHERE id=$1`, created.Task.ScheduleID).
		Scan(&paused); err != nil {
		t.Fatal(err)
	}
	if paused {
		t.Fatal("cancelling one occurrence paused the series")
	}
}

func TestCalendarRunsOverdueWorkWhenRuntimeReturns(t *testing.T) {
	for _, recurring := range []bool{false, true} {
		name := "one time"
		if recurring {
			name = "recurring coalesces missed periods"
		}
		t.Run(name, func(t *testing.T) {
			ctx, pool, f, tasks, principal := startTaskHarness(t)
			calendar := NewCalendarService(tasks)
			at := time.Now().UTC().Add(time.Hour).Truncate(time.Minute)
			trigger := schedule.Trigger{Type: schedule.TriggerTypeOnce, RunAt: &at}
			if recurring {
				expression, zone := "*/5 * * * *", "UTC"
				trigger = schedule.Trigger{
					Type:       schedule.TriggerTypeCron,
					Expression: &expression,
					TimeZone:   &zone,
				}
			}
			created, err := tasks.Create(ctx, principal, CreateInput{
				ProjectID:     f.ProjectA,
				MilestoneType: MilestoneTypeProduct,
				Title:         "Resume overdue work",
				Status:        StatusTodo,
				ExecutedBy:    &ActorRef{Kind: ActorAgent, ID: f.AgentA},
				Schedule:      &trigger,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !recurring {
				movedAt := created.Task.ScheduledAt.Add(-5 * time.Minute)
				moved, err := calendar.UpdateOccurrence(
					ctx,
					principal,
					created.Task.ID,
					CalendarOccurrenceChange{
						ExpectedRevision: created.Task.Revision,
						OccurrenceAt:     *created.Task.ScheduleOccurrenceAt,
						RunAt:            &movedAt,
					},
				)
				if err != nil {
					t.Fatal(err)
				}
				created.Task = moved.Task
			}
			at = *created.Task.ScheduledAt
			dbtest.MustExec(t, ctx, pool, `UPDATE runtimes SET status='offline' WHERE id=$1`, f.RuntimeA)
			// A moved one-shot must not supersede itself when its original time
			// is also overdue. Fire and materialize jobs can arrive in either order.
			target, err := tasks.store.Queries().
				GetCalendarTaskTarget(ctx, generated.GetCalendarTaskTargetParams{CompanyID: f.CompanyA, ID: created.Task.ID})
			if err != nil {
				t.Fatal(err)
			}
			if err := calendar.fireDue(ctx, target, at.Add(48*time.Hour)); err != nil {
				t.Fatal(err)
			}
			for _, due := range []time.Time{at, at.Add(48 * time.Hour)} {
				if err := calendar.ProcessDue(ctx, due, 100); err != nil {
					t.Fatal(err)
				}
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs r JOIN tasks t ON t.id=r.task_id WHERE t.task_schedule_id=$1`, created.Task.ScheduleID).
				Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("offline Runs = %d, want 0", count)
			}
			dbtest.MustExec(
				t,
				ctx,
				pool,
				`UPDATE runtimes SET status='online', last_seen_at=now() WHERE id=$1`,
				f.RuntimeA,
			)
			for range 2 {
				if err := calendar.ProcessDue(ctx, at.Add(48*time.Hour), 100); err != nil {
					t.Fatal(err)
				}
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs r JOIN tasks t ON t.id=r.task_id WHERE t.task_schedule_id=$1`, created.Task.ScheduleID).
				Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("recovered Runs = %d, want 1", count)
			}
			var runAt time.Time
			if err := pool.QueryRow(ctx, `SELECT t.scheduled_at FROM runs r JOIN tasks t ON t.id=r.task_id WHERE t.task_schedule_id=$1`, created.Task.ScheduleID).
				Scan(&runAt); err != nil {
				t.Fatal(err)
			}
			want := at
			if recurring {
				want = at.Add(48 * time.Hour)
			}
			if !runAt.Equal(want) {
				t.Fatalf("recovered occurrence = %s, want %s", runAt, want)
			}
		})
	}
}

func TestCalendarConcurrentWorkersCreateOneRunAndClaimRespectsPause(t *testing.T) {
	ctx, pool, f, tasks, principal := startTaskHarness(t)
	calendar := NewCalendarService(tasks)
	at := time.Now().UTC().Add(time.Hour).Truncate(time.Minute)
	created, err := tasks.Create(
		ctx,
		principal,
		CreateInput{
			ProjectID:     f.ProjectA,
			MilestoneType: MilestoneTypeProduct,
			Title:         "Concurrent calendar work",
			Status:        StatusTodo,
			ExecutedBy:    &ActorRef{Kind: ActorAgent, ID: f.AgentA},
			Schedule:      &schedule.Trigger{Type: schedule.TriggerTypeOnce, RunAt: &at},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	group, concurrentCtx := errgroup.WithContext(ctx)
	for range 8 {
		group.Go(func() error {
			// Asynq retries the intentionally short Company lock timeout.
			// Exercise the same duplicate-delivery behavior under contention.
			var err error
			for range 16 {
				err = calendar.ProcessDue(concurrentCtx, at.Add(time.Second), 100)
				var databaseError *pgconn.PgError
				if !errors.As(err, &databaseError) || databaseError.Code != "55P03" {
					return err
				}
				select {
				case <-concurrentCtx.Done():
					return concurrentCtx.Err()
				case <-time.After(25 * time.Millisecond):
				}
			}
			return err
		})
	}
	if err := group.Wait(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE task_id=$1`, created.Task.ID).
		Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("concurrent workers created %d Runs", count)
	}
	q := tasks.store.Queries()
	sets := auth.ProjectPermissionHeldSets()
	assertClaim := func(want bool) {
		t.Helper()
		candidates, err := q.ListQueuedClaimCandidatesByRuntimes(
			ctx,
			generated.ListQueuedClaimCandidatesByRuntimesParams{
				RuntimeIds:                []uuid.UUID{f.RuntimeA},
				ExecutePermission:         string(auth.ProjectPermissionTasksExecute),
				ReadPermission:            string(auth.ProjectPermissionRead),
				CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		_, err = q.SelectClaimableRun(
			ctx,
			generated.SelectClaimableRunParams{
				AgentID:                   f.AgentA,
				RuntimeID:                 f.RuntimeA,
				TaskLaneOpen:              true,
				ExecutePermission:         string(auth.ProjectPermissionTasksExecute),
				ReadPermission:            string(auth.ProjectPermissionRead),
				CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
			},
		)
		if want && (err != nil || len(candidates) != 1) {
			t.Fatalf("expected claim: %v, candidates=%v", err, candidates)
		}
		if !want && (!errors.Is(err, pgx.ErrNoRows) || len(candidates) != 0) {
			t.Fatalf("unexpected claim: %v, candidates=%v", err, candidates)
		}
	}
	assertClaim(false)
	if _, err := pool.Exec(ctx, `UPDATE tasks SET scheduled_at=now() WHERE id=$1`, created.Task.ID); err != nil {
		t.Fatal(err)
	}
	assertClaim(true)
	// A computer may go away after enqueue too. The queued first attempt remains
	// claimable when it returns, even beyond the ordinary manual queue TTL.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE runs SET created_at=now()-interval '2 days' WHERE task_id=$1`,
		created.Task.ID,
	)
	expired, err := q.ListQueuedRunExpiryCandidates(
		ctx,
		generated.ListQueuedRunExpiryCandidatesParams{TtlSecs: 60, OnceWaitSecs: 604800, MaxPerTick: 100},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 0 {
		t.Fatalf("scheduled expiry candidates = %v, want none", expired)
	}
	assertClaim(true)
	value, err := tasks.Get(ctx, principal, created.Task.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := calendar.UpdateSchedule(
		ctx,
		principal,
		value.ID,
		CalendarScheduleChange{ExpectedRevision: value.Revision, Paused: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertClaim(false)
	if _, err := calendar.UpdateSchedule(
		ctx,
		principal,
		value.ID,
		CalendarScheduleChange{ExpectedRevision: paused.Task.Revision},
	); err != nil {
		t.Fatal(err)
	}
	// Resuming does not resurrect the first attempt explicitly cancelled by pause.
	assertClaim(false)
}

func TestCalendarFirstOccurrenceBriefDoesNotRewriteTheSeries(t *testing.T) {
	ctx, _, f, tasks, principal := startTaskHarness(t)
	calendar := NewCalendarService(tasks)
	expression, zone := "0 9 * * *", "UTC"
	created, err := tasks.Create(
		ctx,
		principal,
		CreateInput{
			ProjectID:     f.ProjectA,
			MilestoneType: MilestoneTypeProduct,
			Title:         "Series brief",
			Status:        StatusTodo,
			Schedule: &schedule.Trigger{
				Type:       schedule.TriggerTypeCron,
				Expression: &expression,
				TimeZone:   &zone,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	title := "Only this occurrence"
	if _, err := calendar.UpdateOccurrence(
		ctx,
		principal,
		created.Task.ID,
		CalendarOccurrenceChange{
			ExpectedRevision: created.Task.Revision,
			OccurrenceAt:     *created.Task.ScheduleOccurrenceAt,
			RunAt:            created.Task.ScheduledAt,
			Title:            &title,
		},
	); err != nil {
		t.Fatal(err)
	}
	page, err := calendar.ListOccurrences(
		ctx,
		principal,
		*created.Task.ScheduledAt,
		created.Task.ScheduledAt.Add(48*time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Occurrences) != 2 {
		t.Fatalf("got %d occurrences", len(page.Occurrences))
	}
	for _, item := range page.Occurrences {
		want := "Series brief"
		if item.Materialized {
			want = title
		}
		if item.Task.Title != want {
			t.Fatalf("title=%q want %q", item.Task.Title, want)
		}
	}
}

func TestCalendarOccurrencesInheritResultPolicy(t *testing.T) {
	for _, advancedEdit := range []bool{false, true} {
		name := "worker"
		if advancedEdit {
			name = "advance edit"
		}
		t.Run(name, func(t *testing.T) {
			ctx, pool, f, tasks, p := startTaskHarness(t)
			calendar := NewCalendarService(tasks)
			expression, zone := "0 9 * * *", "UTC"
			created, err := tasks.Create(ctx, p, CreateInput{
				ProjectID:     f.ProjectA,
				MilestoneType: MilestoneTypeProduct,
				Title:         "Policy series",
				Status:        StatusTodo,
				ResultPolicy:  &ResultPolicy{ProposalOnly: true, ReviewRequired: false},
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
			if advancedEdit {
				moved := at.Add(time.Hour)
				_, err = calendar.UpdateOccurrence(ctx, p, created.Task.ID, CalendarOccurrenceChange{
					ExpectedRevision: created.Task.Revision, OccurrenceAt: at, RunAt: &moved,
				})
			} else {
				err = calendar.ProcessDue(ctx, at, 100)
			}
			if err != nil {
				t.Fatal(err)
			}
			var proposalOnly, reviewRequired bool
			if err := pool.QueryRow(
				ctx,
				`SELECT result_proposal_only, result_review_required FROM tasks
				WHERE task_schedule_id=$1 AND schedule_occurrence_at=$2 AND id<>$3`,
				created.Task.ScheduleID,
				at,
				created.Task.ID,
			).Scan(&proposalOnly, &reviewRequired); err != nil {
				t.Fatal(err)
			}
			if !proposalOnly || reviewRequired {
				t.Fatalf(
					"occurrence policy = proposalOnly %t, reviewRequired %t; want true, false",
					proposalOnly,
					reviewRequired,
				)
			}
		})
	}
}
