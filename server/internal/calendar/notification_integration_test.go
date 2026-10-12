//go:build integration

package calendar

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestCalendarChangesNotifyAffectedPeople(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO company_members(company_id,user_id,role,status) VALUES($1,$2,'member','active')`,
		f.CompanyA,
		f.UserB,
	); err != nil {
		t.Fatal(err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	s := newCalendarTestService(t, store)
	s.presenter = inbox.NewRealtimePresenter(s.bus)
	var nativeHints atomic.Int32
	if err := s.bus.Subscribe(inbox.EventNotificationIssued, func(ctx context.Context, event events.Event) error {
		hint, err := events.PayloadOf[inbox.ToastHint](event)
		if err != nil {
			return err
		}
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_items WHERE id=$1 AND recipient_user_id=$2 AND kind='calendar_update')`, hint.NotificationID, hint.RecipientUserID).
			Scan(&exists); err != nil {
			return err
		}
		if !exists || event.CompanyID != f.CompanyA {
			t.Error("native hint must follow the committed shared calendar Inbox item")
		}
		nativeHints.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a := auth.UserPrincipal{UserID: f.UserA, CompanyID: f.CompanyA}
	b := auth.UserPrincipal{UserID: f.UserB, CompanyID: f.CompanyA}
	assertCount := func(user uuid.UUID, want int) {
		t.Helper()
		var got int
		err := pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE recipient_user_id=$1 AND kind='calendar_update'`, user).
			Scan(&got)
		if err != nil || got != want {
			t.Fatalf("notices for %s = %d, %v; want %d", user, got, err, want)
		}
	}
	assertLatest := func(user uuid.UUID, title, body, link string) {
		t.Helper()
		var gotTitle, gotBody, gotLink string
		err := pool.QueryRow(ctx, `SELECT title,body,deep_link FROM inbox_items WHERE recipient_user_id=$1 AND kind='calendar_update' ORDER BY created_at DESC,id DESC LIMIT 1`, user).
			Scan(&gotTitle, &gotBody, &gotLink)
		if err != nil || gotTitle != title || !strings.Contains(gotBody, body) || gotLink != link {
			t.Fatalf(
				"notice = %q, %q, %q, %v; want %q containing %q, %q",
				gotTitle,
				gotBody,
				gotLink,
				err,
				title,
				body,
				link,
			)
		}
	}
	input := Input{
		Type:            "event",
		Title:           "Planning",
		Color:           "sage",
		StartLocal:      "2026-09-22T09:00",
		EndLocal:        "2026-09-22T10:00",
		TimeZone:        "Asia/Ho_Chi_Minh",
		Recurrence:      "daily",
		AttendeeUserIDs: []string{f.UserB.String()},
	}
	event, err := s.Create(ctx, a, input)
	if err != nil {
		t.Fatal(err)
	}
	link := "/calendar?eventId=" + event.ID + "&date=2026-09-22"
	assertCount(f.UserA, 0)
	assertCount(f.UserB, 1)
	assertLatest(
		f.UserB,
		calendarNoticeTitle(event.OrganizerName+" invited you to ", "Planning"),
		"Tue, 22 Sep 2026",
		link,
	)
	for i, status := range []string{"accepted", "tentative", "declined", "accepted"} {
		var wg sync.WaitGroup
		for range 3 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.Respond(ctx, b, event.ID, event.Revision, status); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		assertCount(f.UserA, i+1)
		verb := status
		if verb == "tentative" {
			verb = "may attend"
		}
		var guestName string
		if err := pool.QueryRow(ctx, `SELECT display_name FROM users WHERE id=$1`, f.UserB).
			Scan(&guestName); err != nil {
			t.Fatal(err)
		}
		assertLatest(
			f.UserA,
			calendarNoticeTitle(guestName+" "+verb+" ", "Planning"),
			"09:00–10:00 (UTC+7)",
			link,
		)
		if err := s.Respond(ctx, b, event.ID, event.Revision, status); err != nil {
			t.Fatal(err)
		}
		assertCount(f.UserA, i+1)
	}
	assertCount(f.UserB, 1)
	input.ExpectedRevision = event.Revision
	input.StartLocal = "2026-09-22T11:00"
	input.EndLocal = "2026-09-22T12:00"
	event, err = s.Update(ctx, a, event.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	assertCount(f.UserB, 2)
	assertLatest(f.UserB, "Updated: “Planning”", "Please respond again", link)
	// A stale edit rolls back both the event and notification.
	if _, err := s.Update(ctx, a, event.ID, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale edit = %v, want conflict", err)
	}
	assertCount(f.UserB, 2)
	// Saving unchanged details must not generate an update notice.
	input.ExpectedRevision = event.Revision
	event, err = s.Update(ctx, a, event.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	assertCount(f.UserB, 2)
	for range 2 {
		if err := s.Occurrence(
			ctx,
			a,
			event.ID,
			OccurrenceInput{
				ExpectedRevision: event.Revision,
				OccurrenceDate:   "2026-09-23",
				Action:           "cancel",
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	assertCount(f.UserB, 3)
	assertLatest(
		f.UserB,
		"Cancelled: “Planning”",
		"Wed, 23 Sep 2026 · This occurrence only.",
		"/calendar?date=2026-09-23",
	)
	input.ExpectedRevision = event.Revision
	input.AttendeeUserIDs = nil
	event, err = s.Update(ctx, a, event.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	assertCount(f.UserB, 4)
	assertLatest(f.UserB, "Invitation removed: “Planning”", "Tue, 22 Sep 2026", "/calendar?date=2026-09-22")
	input.ExpectedRevision = event.Revision
	input.AttendeeUserIDs = []string{f.UserB.String()}
	event, err = s.Update(ctx, a, event.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	assertCount(f.UserB, 5)
	if err := s.Delete(ctx, a, event.ID, event.Revision); err != nil {
		t.Fatal(err)
	}
	assertCount(f.UserB, 6)
	assertLatest(f.UserB, "Cancelled: “Planning”", "Tue, 22 Sep 2026", "/calendar?date=2026-09-22")
	assertCount(f.UserA, 4)
	// Membership revocation suppresses future notices as well as calendar access.
	event, err = s.Create(
		ctx,
		a,
		Input{
			Type:            "event",
			Title:           "Private",
			Color:           "sage",
			StartLocal:      "2026-09-22T09:00",
			EndLocal:        "2026-09-22T10:00",
			TimeZone:        "UTC",
			Recurrence:      "none",
			AttendeeUserIDs: []string{f.UserB.String()},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertCount(f.UserB, 7)
	if _, err := pool.Exec(
		ctx,
		`UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		f.CompanyA,
		f.UserB,
	); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, a, event.ID, event.Revision); err != nil {
		t.Fatal(err)
	}
	assertCount(f.UserB, 7)
	if nativeHints.Load() != 11 {
		t.Fatalf("native hints = %d, want 11", nativeHints.Load())
	}
}

func TestCompanyCalendarAdminChangesNotifyOrganizer(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO company_members(company_id,user_id,role,status) VALUES($1,$2,'admin','active')`,
		f.CompanyA,
		f.UserB,
	); err != nil {
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
	input := Input{
		Type:       "event",
		Title:      "Company review",
		Color:      "sage",
		StartLocal: "2026-09-22",
		EndLocal:   "2026-09-23",
		AllDay:     true,
		TimeZone:   "UTC",
		Recurrence: "none",
	}
	for _, calendar := range catalog.Calendars {
		if calendar.Type == "company" {
			input.CalendarID = calendar.ID
		}
	}
	event, err := s.Create(ctx, a, input)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE kind='calendar_update'`).
		Scan(&count); err != nil ||
		count != 0 {
		t.Fatalf("company-wide broadcast = %d, %v; want 0", count, err)
	}
	input.ExpectedRevision = event.Revision
	input.Location = "Room 2"
	event, err = s.Update(ctx, b, event.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, b, event.ID, event.Revision); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE kind='calendar_update' AND recipient_user_id=$1`, f.UserA).
		Scan(&count); err != nil ||
		count != 2 {
		t.Fatalf("organizer notices = %d, %v; want 2", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE kind='calendar_update' AND recipient_user_id=$1`, f.UserB).
		Scan(&count); err != nil ||
		count != 0 {
		t.Fatalf("acting admin notices = %d, %v; want 0", count, err)
	}
}
