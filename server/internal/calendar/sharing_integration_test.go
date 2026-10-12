//go:build integration

package calendar

import (
	"context"
	"errors"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestSharedCalendarVisibilityAndResponses(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO company_members(company_id,user_id,role,status) VALUES($1,$2,'member','active')`, f.CompanyA, f.UserB); err != nil {
		t.Fatal(err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	s := newCalendarTestService(t, store)
	a := auth.UserPrincipal{UserID: f.UserA, CompanyID: f.CompanyA}
	b := auth.UserPrincipal{UserID: f.UserB, CompanyID: f.CompanyA}
	catalog, err := s.Catalog(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	var personal, company string
	for _, c := range catalog.Calendars {
		if c.Type == "personal" {
			personal = c.ID
		} else {
			company = c.ID
		}
	}
	if personal == "" || company == "" {
		t.Fatalf("calendars = %#v", catalog)
	}
	input := Input{CalendarID: personal, Type: "event", Title: "Planning", StartLocal: "2026-09-22T09:00", EndLocal: "2026-09-22T10:00", TimeZone: "Asia/Ho_Chi_Minh", Recurrence: "none", Color: "sage"}
	event, err := s.Create(ctx, a, input)
	if err != nil {
		t.Fatal(err)
	}
	from, _ := time.Parse(time.RFC3339, "2026-09-22T00:00:00Z")
	to := from.Add(24 * time.Hour)
	rows, err := s.List(ctx, b, from, to)
	if err != nil || len(rows) != 0 {
		t.Fatalf("private event leaked: %v, %v", rows, err)
	}
	input.ExpectedRevision = event.Revision
	input.AttendeeUserIDs = []string{f.UserB.String()}
	event, err = s.Update(ctx, a, event.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = s.List(ctx, auth.UserPrincipal{UserID: f.UserB}, from, to)
	if err != nil || len(rows) != 1 || rows[0].CanEdit || rows[0].ResponseStatus != "needs_action" {
		t.Fatalf("invitation = %v, %v", rows, err)
	}
	if _, err = s.Update(ctx, b, event.ID, input); !errors.Is(err, ErrNotFound) {
		t.Fatalf("guest edit = %v", err)
	}
	if err = s.Respond(ctx, b, event.ID, event.Revision, "accepted"); err != nil {
		t.Fatal(err)
	}
	rows, err = s.List(ctx, a, from, to)
	if err != nil || rows[0].Attendees[0].ResponseStatus != "accepted" {
		t.Fatalf("organizer state = %v, %v", rows, err)
	}
	input.ExpectedRevision = event.Revision
	input.Title = "Planning agenda"
	event, err = s.Update(ctx, a, event.ID, input)
	if err != nil || len(event.Attendees) != 1 || event.Attendees[0].ResponseStatus != "accepted" {
		t.Fatalf("title edit must preserve RSVP: %v, %v", event, err)
	}
	input.ExpectedRevision = event.Revision
	input.StartLocal = "2026-09-22T09:30"
	event, err = s.Update(ctx, a, event.ID, input)
	if err != nil || len(event.Attendees) != 1 || event.Attendees[0].ResponseStatus != "needs_action" {
		t.Fatalf("time change must reset RSVP: %v, %v", event, err)
	}
	input.ExpectedRevision = event.Revision
	input.AttendeeUserIDs = nil
	event, err = s.Update(ctx, a, event.ID, input)
	if err != nil || event.CompanyID != nil || len(event.Attendees) != 0 {
		t.Fatalf("removing guests must restore private event: %v, %v", event, err)
	}
	rows, err = s.List(ctx, b, from, to)
	if err != nil || len(rows) != 0 {
		t.Fatalf("removed guest retained event access: %v, %v", rows, err)
	}
	input.ExpectedRevision = event.Revision
	input.AttendeeUserIDs = []string{f.UserB.String()}
	event, err = s.Update(ctx, a, event.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	input.CalendarID = company
	input.AttendeeUserIDs = nil
	input.ExpectedRevision = 0
	shared, err := s.Create(ctx, a, input)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = s.List(ctx, b, from, to)
	if err != nil || len(rows) != 2 {
		t.Fatalf("company visibility = %v, %v", rows, err)
	}
	if err = s.Delete(ctx, b, shared.ID, shared.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member delete = %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE company_members SET role='admin' WHERE company_id=$1 AND user_id=$2`, f.CompanyA, f.UserB); err != nil {
		t.Fatal(err)
	}
	input.ExpectedRevision = shared.Revision
	if _, err = s.Update(ctx, b, shared.ID, input); err != nil {
		t.Fatalf("company admin edit = %v", err)
	}
	input.CalendarID = personal
	input.ExpectedRevision = event.Revision
	if _, err = s.Update(ctx, b, event.ID, input); !errors.Is(err, ErrNotFound) {
		t.Fatalf("company admin edited another member's personal event: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`, f.CompanyA, f.UserB); err != nil {
		t.Fatal(err)
	}
	rows, err = s.List(ctx, auth.UserPrincipal{UserID: f.UserB}, from, to)
	if err != nil || len(rows) != 0 {
		t.Fatalf("revoked invitation access = %v, %v", rows, err)
	}
	if err = s.Respond(ctx, b, event.ID, event.Revision, "declined"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked RSVP = %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`, f.CompanyA, f.UserA); err != nil {
		t.Fatal(err)
	}
	personalActor := auth.UserPrincipal{UserID: f.UserA}
	rows, err = s.List(ctx, personalActor, from, to)
	if err != nil || len(rows) != 1 || rows[0].CompanyID != nil || len(rows[0].Attendees) != 0 {
		t.Fatalf("departed organizer's personal event = %v, %v", rows, err)
	}
	input.CalendarID = personal
	input.ExpectedRevision = event.Revision
	input.AttendeeUserIDs = nil
	if _, err = s.Update(ctx, personalActor, event.ID, input); err != nil {
		t.Fatalf("personal calendar after leaving company: %v", err)
	}
}

func TestCompanyCalendarWritesDoNotInvertAccountDeletionLocks(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO company_members(company_id,user_id,role,status) VALUES($1,$2,'member','active')`, f.CompanyA, f.UserB); err != nil {
		t.Fatal(err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	s := newCalendarTestService(t, store)
	p := auth.UserPrincipal{UserID: f.UserA, CompanyID: f.CompanyA}
	catalog, err := s.Catalog(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	var companyCalendar string
	for _, calendar := range catalog.Calendars {
		if calendar.Type == "company" {
			companyCalendar = calendar.ID
		}
	}
	if companyCalendar == "" {
		t.Fatal("missing company calendar")
	}
	// Account deletion locks Users before Companies. A calendar mutation that
	// already holds its Company fence must not wait for either participant's User.
	deletion, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer deletion.Rollback(ctx)
	if _, err = deletion.Exec(ctx, `SELECT id FROM users WHERE id IN ($1,$2) ORDER BY id FOR NO KEY UPDATE`, f.UserA, f.UserB); err != nil {
		t.Fatal(err)
	}
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	event, err := s.Create(writeCtx, p, Input{
		CalendarID: companyCalendar, AttendeeUserIDs: []string{f.UserB.String()},
		Type: "reminder", Title: "Review together", Color: "sage",
		StartLocal: "2026-09-22T09:00", EndLocal: "2026-09-22T10:00",
		TimeZone: "Asia/Ho_Chi_Minh", Recurrence: "none",
	})
	if err != nil {
		t.Fatalf("calendar write behind account deletion User locks: %v", err)
	}
	if err = s.Occurrence(writeCtx, p, event.ID, OccurrenceInput{ExpectedRevision: event.Revision, OccurrenceDate: "2026-09-22", Action: "complete"}); err != nil {
		t.Fatalf("personal receipt behind account deletion User locks: %v", err)
	}
}
