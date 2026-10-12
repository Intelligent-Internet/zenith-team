//go:build integration

package activity

import (
	"context"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestCalendarUsesHalfOpenUTCBoundsAndDenseLocalDays(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("db.MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	startAt := time.Date(2026, time.March, 1, 5, 0, 0, 0, time.UTC)
	endAt := time.Date(2026, time.April, 1, 4, 0, 0, 0, time.UTC)
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("time.LoadLocation() error = %v", err)
	}
	timestamps := []time.Time{
		startAt.Add(-time.Nanosecond),
		startAt,
		time.Date(2026, time.March, 8, 6, 59, 0, 0, time.UTC),
		time.Date(2026, time.March, 8, 7, 1, 0, 0, time.UTC),
		endAt.Add(-time.Minute),
		endAt,
	}
	for _, createdAt := range timestamps {
		dbtest.MustExec(
			t, ctx, pool, `
			INSERT INTO activity_events (
				company_id, actor_type, action, entity_type, entity_id,
				task_id, visibility, details, created_at, project_id
			) VALUES ($1, 'system', 'task.updated', 'task', $2, $3, 'company', '{}'::jsonb, $4, $5)`,
			fixture.CompanyA,
			fixture.TaskA.String(),
			fixture.TaskA,
			createdAt,
			fixture.ProjectA,
		)
	}

	calendar, err := NewFeed(generated.New(pool)).Calendar(ctx, fixture.CompanyA, CalendarOptions{
		UserID: fixture.UserA,
		Month:  "2026-03", TimeZone: "America/New_York", Location: location,
		StartAt: startAt, EndAt: endAt,
	})
	if err != nil {
		t.Fatalf("Calendar() error = %v", err)
	}
	if calendar.TotalCount != 4 || len(calendar.Days) != 31 {
		t.Fatalf("Calendar() total/days = %d/%d, want 4/31", calendar.TotalCount, len(calendar.Days))
	}
	if calendar.Days["2026-03-01"] != 1 || calendar.Days["2026-03-08"] != 2 ||
		calendar.Days["2026-03-31"] != 1 || calendar.Days["2026-03-15"] != 0 {
		t.Fatalf("Calendar().Days = %+v", calendar.Days)
	}
	if !calendar.StartAt.Equal(startAt) || !calendar.EndAt.Equal(endAt) {
		t.Fatalf("Calendar() bounds = %s/%s, want %s/%s", calendar.StartAt, calendar.EndAt, startAt, endAt)
	}
}
