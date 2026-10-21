//go:build integration

package calendar

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestRemindersCreateDurableInboxWithoutAnOpenClient(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	s := newCalendarTestService(t, store)
	p := auth.UserPrincipal{UserID: f.UserA}
	lead := int32(10)
	event, err := s.Create(
		ctx,
		p,
		Input{
			Type:            "reminder",
			Title:           "Private review",
			Color:           "sage",
			StartLocal:      "2026-09-22T09:00",
			EndLocal:        "2026-09-22T10:00",
			TimeZone:        "Asia/Ho_Chi_Minh",
			Recurrence:      "daily",
			ReminderMinutes: &lead,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var nativeHints atomic.Int32
	if err := bus.Subscribe(inbox.EventNotificationIssued, func(ctx context.Context, event events.Event) error {
		hint, err := events.PayloadOf[inbox.ToastHint](event)
		if err != nil {
			return err
		}
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_items WHERE id=$1 AND recipient_user_id=$2)`, hint.NotificationID, f.UserA).
			Scan(&exists); err != nil {
			return err
		}
		if !exists || event.CompanyID != uuid.Nil {
			t.Error("native hint must follow the committed personal Inbox item")
		}
		nativeHints.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r := NewReminderService(store, bus, inbox.NewRealtimePresenter(bus))
	before := time.Date(2026, 9, 22, 1, 49, 0, 0, time.UTC)
	if err := r.Deliver(ctx, before); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE recipient_user_id=$1`, f.UserA).
		Scan(&count); err != nil ||
		count != 0 {
		t.Fatalf("early notices = %d, %v", count, err)
	}
	now := before.Add(time.Minute)
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.Deliver(ctx, now); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var companyID *uuid.UUID
	var title, path string
	if err := pool.QueryRow(ctx, `SELECT company_id,title,deep_link FROM inbox_items WHERE recipient_user_id=$1`, f.UserA).
		Scan(&companyID, &title, &path); err != nil {
		t.Fatal(err)
	}
	if companyID != nil || title != "Reminder: “Private review”" ||
		path != "/calendar?eventId="+event.ID+"&date=2026-09-22" {
		t.Fatalf("personal notice = %v, %q, %q", companyID, title, path)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE recipient_user_id=$1`, f.UserA).
		Scan(&count); err != nil ||
		count != 1 {
		t.Fatalf("deduplicated notices = %d, %v", count, err)
	}
	if err := s.Occurrence(
		ctx,
		p,
		event.ID,
		OccurrenceInput{ExpectedRevision: event.Revision, OccurrenceDate: "2026-09-23", Action: "complete"},
	); err != nil {
		t.Fatal(err)
	}
	if err := r.Deliver(ctx, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := r.Deliver(ctx, now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE recipient_user_id=$1`, f.UserA).
		Scan(&count); err != nil ||
		count != 2 {
		t.Fatalf("completed occurrence suppressed = %d, %v", count, err)
	}
	if nativeHints.Load() != 2 {
		t.Fatalf("native hints=%d", nativeHints.Load())
	}
	service, err := inbox.NewService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.List(ctx, p, inbox.ListOptions{})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("account inbox = %d, %v", len(page.Items), err)
	}
	other, err := service.List(ctx, auth.UserPrincipal{UserID: f.UserB}, inbox.ListOptions{})
	if err != nil || len(other.Items) != 0 {
		t.Fatalf("private reminder leaked = %v, %v", other, err)
	}
}

func TestSharedRemindersRespectRSVPRevocationAndCancellation(t *testing.T) {
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
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s := newCalendarTestService(t, store)
	r := NewReminderService(store, bus, nil)
	a := auth.UserPrincipal{UserID: f.UserA, CompanyID: f.CompanyA}
	b := auth.UserPrincipal{UserID: f.UserB, CompanyID: f.CompanyA}
	catalog, err := s.Catalog(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	var companyCalendar string
	for _, c := range catalog.Calendars {
		if c.Type == "company" {
			companyCalendar = c.ID
		}
	}
	input := Input{
		CalendarID: companyCalendar,
		Type:       "reminder",
		Title:      "Team review",
		Color:      "sky",
		StartLocal: "2026-09-22T09:00",
		EndLocal:   "2026-09-22T10:00",
		TimeZone:   "UTC",
		Recurrence: "daily",
	}
	event, err := s.Create(ctx, a, input)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	if err := r.Deliver(ctx, now); err != nil {
		t.Fatal(err)
	}
	assertCount := func(userID uuid.UUID, want int) {
		t.Helper()
		var got int
		err := pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE recipient_user_id=$1 AND kind='calendar_reminder'`, userID).
			Scan(&got)
		if err != nil || got != want {
			t.Fatalf("user %s notices=%d want=%d error=%v", userID, got, want, err)
		}
	}
	assertCount(f.UserA, 1)
	assertCount(f.UserB, 0) // Company visibility alone is not an invitation.
	input.ExpectedRevision = event.Revision
	input.AttendeeUserIDs = []string{f.UserB.String()}
	event, err = s.Update(ctx, a, event.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Respond(ctx, b, event.ID, event.Revision, "declined"); err != nil {
		t.Fatal(err)
	}
	if err := r.Deliver(ctx, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	assertCount(f.UserB, 0)
	if err := s.Respond(ctx, b, event.ID, event.Revision, "accepted"); err != nil {
		t.Fatal(err)
	}
	if err := r.Deliver(ctx, now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	assertCount(f.UserB, 1)
	if err := s.Occurrence(
		ctx,
		a,
		event.ID,
		OccurrenceInput{ExpectedRevision: event.Revision, OccurrenceDate: "2026-09-25", Action: "cancel"},
	); err != nil {
		t.Fatal(err)
	}
	if err := r.Deliver(ctx, now.Add(72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	assertCount(f.UserB, 1)
	if _, err := pool.Exec(
		ctx,
		`UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		f.CompanyA,
		f.UserB,
	); err != nil {
		t.Fatal(err)
	}
	if err := r.Deliver(ctx, now.Add(96*time.Hour)); err != nil {
		t.Fatal(err)
	}
	assertCount(f.UserB, 1)
}
