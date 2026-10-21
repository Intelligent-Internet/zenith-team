//go:build integration

package db

import (
	"context"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestCalendarMigrationUpgradesMainAndRollsBack(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	toMain := func(runner *migrate.Migrate) error { return runner.Migrate(29) }
	toCalendar := func(runner *migrate.Migrate) error { return runner.Migrate(30) }
	if err := runMigration(ctx, pool, "migrate to main schema", toMain); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	var existingItem uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO inbox_items(company_id,recipient_user_id,kind,entity_type,entity_id,title,body,dedupe_key)
		VALUES($1,$2,'task_assigned','task',$3,'Existing assignment','Preserve this notification','existing-assignment') RETURNING id`, f.CompanyA, f.UserA, f.TaskA.String()).
		Scan(&existingItem); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		name       string
		direction  func(*migrate.Migrate) error
		wantTables int
	}{
		{"upgrade Calendar", toCalendar, 6},
		{"rollback Calendar", toMain, 0},
		{"reapply Calendar", toCalendar, 6},
	} {
		if err := runMigration(ctx, pool, step.name, step.direction); err != nil {
			t.Fatal(err)
		}
		var tables int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND (table_name='calendars' OR table_name LIKE 'calendar\_%')`).
			Scan(&tables); err != nil ||
			tables != step.wantTables {
			t.Fatalf("%s tables = %d, %v; want %d", step.name, tables, err, step.wantTables)
		}
		var preserved bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_items WHERE id=$1 AND company_id=$2 AND recipient_user_id=$3 AND body='Preserve this notification')`, existingItem, f.CompanyA, f.UserA).
			Scan(&preserved); err != nil ||
			!preserved {
			t.Fatalf("%s existing Inbox preserved = %v, %v", step.name, preserved, err)
		}
		if step.wantTables == 0 {
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE company_id IS NULL OR kind LIKE 'calendar\_%'`).
				Scan(&count); err != nil ||
				count != 0 {
				t.Fatalf("rollback Calendar notices = %d, %v; want 0", count, err)
			}
			continue
		}
		if _, err := pool.Exec(
			ctx,
			`INSERT INTO inbox_items(company_id,recipient_user_id,kind,entity_type,entity_id,title,body,dedupe_key)
			VALUES(NULL,$1,'calendar_reminder','calendar','migration-test','Reminder','Personal reminder','calendar-migration'),
			($2,$1,'calendar_update','calendar','migration-test','Invitation','Shared invitation','calendar-migration')`,
			f.UserA,
			f.CompanyA,
		); err != nil {
			t.Fatalf("%s notification kinds: %v", step.name, err)
		}
	}
}

func TestCustomCalendarRecurrenceMigrationPreservesExistingEvents(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := runMigration(
		ctx,
		pool,
		"before custom recurrence",
		func(m *migrate.Migrate) error { return m.Migrate(34) },
	); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `WITH personal AS (INSERT INTO calendars(type,user_id) VALUES('personal',$1) RETURNING id)
 INSERT INTO calendar_events(user_id,calendar_id,type,title,color,start_local,end_local,time_zone,recurrence)
 SELECT $1,id,'event','Existing weekly meeting','sage','2026-09-23 09:00','2026-09-23 10:00','UTC','weekly' FROM personal RETURNING id`, f.UserA).
		Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, version := range []uint{35, 34, 35} {
		if err := runMigration(
			ctx,
			pool,
			"custom recurrence migration",
			func(m *migrate.Migrate) error { return m.Migrate(version) },
		); err != nil {
			t.Fatal(err)
		}
		var preserved bool
		if err := pool.QueryRow(ctx, `SELECT title='Existing weekly meeting' AND recurrence='weekly' AND start_local='2026-09-23 09:00'::timestamp FROM calendar_events WHERE id=$1`, id).
			Scan(&preserved); err != nil ||
			!preserved {
			t.Fatalf("version %d preserved = %v, %v", version, preserved, err)
		}
		if version == 35 {
			var defaults bool
			if err := pool.QueryRow(ctx, `SELECT recurrence_interval=1 AND cardinality(recurrence_weekdays)=0 AND recurrence_monthly_mode='day_of_month' AND recurrence_count IS NULL FROM calendar_events WHERE id=$1`, id).
				Scan(&defaults); err != nil ||
				!defaults {
				t.Fatalf("default recurrence = %v, %v", defaults, err)
			}
		}
	}
}
