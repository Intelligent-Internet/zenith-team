//go:build integration

package conversation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
)

type notesTestDispatch struct{}

func (notesTestDispatch) RemoveHuddleNotes(context.Context, string, uuid.UUID) error { return nil }

func (notesTestDispatch) DispatchHuddleNotes(context.Context, string, uuid.UUID) error { return nil }

func notesFixture(t *testing.T) (discussionFixture, *DefaultHuddleNotesService, Huddle, uuid.UUID) {
	t.Helper()
	f := newDiscussionFixture(t)
	calls, err := NewHuddleService(HuddleServiceOptions{Store: f.store, Bus: f.bus, Media: &lifecycleHuddleMedia{}, NotesEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := f.conversations.CreateHumanDirect(f.ctx, f.a, CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}})
	if err != nil {
		t.Fatal(err)
	}
	started, err := calls.Start(f.ctx, f.a, direct.Conversation.ID, StartHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := calls.Join(f.ctx, f.a, started.Huddle.ID, JoinHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = calls.ConfirmConnected(f.ctx, f.a.CompanyID, joined.Connection.ID); err != nil {
		t.Fatal(err)
	}
	notes, err := NewHuddleNotesService(HuddleNotesServiceOptions{Store: f.store, Bus: f.bus, Enabled: true, Dispatcher: notesTestDispatch{}, Summarize: func(_ context.Context, in HuddleNotesRecapInput) (HuddleNotesRecap, error) {
		r := emptyNotesRecap()
		r.Summary = "Discussed the release."
		r.Actions = []HuddleNotesStatement{{Text: "Review the release", SourceIDs: []uuid.UUID{in.Segments[0].ID}}}
		return r, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := notes.Policy(f.ctx, f.a, nil)
	if err != nil || !policy.Enabled || policy.RetentionDays != 30 || policy.MonthlySeconds != 360000 {
		t.Fatalf("default notes policy = %+v, %v; want enabled, 30 days, 100 stream-hours", policy, err)
	}
	return f, notes, started.Huddle, joined.Connection.ID
}
func startNotes(t *testing.T, f discussionFixture, s *DefaultHuddleNotesService, h Huddle, revision int64) (uuid.UUID, HuddleNotesLease) {
	t.Helper()
	if _, err := s.SetEnabled(f.ctx, f.a, h.ID, SetHuddleNotesInput{Enabled: true, ExpectedRevision: revision, RequestKey: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	n, err := f.store.Queries().GetHuddleNotes(f.ctx, generated.GetHuddleNotesParams{CompanyID: f.a.CompanyID, HuddleID: h.ID})
	if err != nil {
		t.Fatal(err)
	}
	intervals, err := f.store.Queries().ListHuddleNotesIntervals(f.ctx, generated.ListHuddleNotesIntervalsParams{CompanyID: f.a.CompanyID, NotesID: n.ID})
	if err != nil {
		t.Fatal(err)
	}
	id := intervals[len(intervals)-1].ID
	lease, err := s.Claim(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return id, lease
}
func TestHuddleNotesIndependentAuthorityAndRetries(t *testing.T) {
	f, s, h, _ := notesFixture(t)
	if _, err := s.Get(f.ctx, f.b, h.ID); !errors.Is(err, ErrHuddleForbidden) {
		t.Fatalf("source-only access = %v", err)
	}
	id, lease := startNotes(t, f, s, h, 1)
	if _, err := s.Claim(f.ctx, id); !errors.Is(err, ErrHuddleNotesConflict) {
		t.Fatalf("duplicate claim = %v", err)
	}
	batch := HuddleNotesBatch{Sequence: 1, Speaker: lease.Identities[0], Text: "Review the release", EndMs: 1000, Ready: true}
	if _, err := s.Append(f.ctx, id, "wrong", batch); !errors.Is(err, ErrHuddleForbidden) {
		t.Fatalf("wrong capability = %v", err)
	}
	for range 2 {
		if _, err := s.Append(f.ctx, id, lease.Capability, batch); err != nil {
			t.Fatal(err)
		}
	}
	changed := batch
	changed.Text = "changed"
	if _, err := s.Append(f.ctx, id, lease.Capability, changed); !errors.Is(err, ErrHuddleNotesConflict) {
		t.Fatalf("changed replay = %v", err)
	}
	status, err := s.Get(f.ctx, f.a, h.ID)
	if err != nil || status.State != "capturing" || len(status.Segments) != 1 {
		t.Fatalf("status = %+v, %v", status, err)
	}
	stopped, err := s.SetEnabled(f.ctx, f.a, h.ID, SetHuddleNotesInput{Enabled: false, ExpectedRevision: status.Revision, RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != "stopping" {
		t.Fatalf("stop state = %s", stopped.State)
	}
	if _, err = s.Append(f.ctx, id, lease.Capability, HuddleNotesBatch{Finished: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Append(f.ctx, id, lease.Capability, HuddleNotesBatch{}); !errors.Is(err, ErrHuddleNotesUnavailable) {
		t.Fatalf("stopped append = %v", err)
	}
	stopped, err = s.Get(f.ctx, f.a, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	newID, newLease := startNotes(t, f, s, h, stopped.Revision)
	if _, err = s.Append(f.ctx, newID, newLease.Capability, HuddleNotesBatch{Ready: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetEnabled(f.ctx, f.a, h.ID, SetHuddleNotesInput{Enabled: false, ExpectedRevision: status.Revision, RequestKey: uuid.New()}); !errors.Is(err, ErrHuddleNotesConflict) {
		t.Fatalf("stale stop = %v", err)
	}
	var agents int
	if err = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM huddle_ai_sessions WHERE huddle_id=$1`, h.ID).Scan(&agents); err != nil || agents != 0 {
		t.Fatalf("voice attempts = %d, %v", agents, err)
	}
}
func TestHuddleNotesAvailabilityFollowsCompanyPolicy(t *testing.T) {
	f, s, h, _ := notesFixture(t)
	if status, err := s.Get(f.ctx, f.a, h.ID); err != nil || !status.Available {
		t.Fatalf("default status = %+v, %v; want available", status, err)
	}
	policy, err := s.Policy(f.ctx, f.a, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy.Enabled = false
	if _, err = s.Policy(f.ctx, f.a, &policy); err != nil {
		t.Fatal(err)
	}
	if status, err := s.Get(f.ctx, f.a, h.ID); err != nil || status.Available {
		t.Fatalf("disabled policy status = %+v, %v; want unavailable", status, err)
	}
}
func TestHuddleNotesConcurrentStartBudgetAndRevocation(t *testing.T) {
	f, s, h, _ := notesFixture(t)
	request := SetHuddleNotesInput{Enabled: true, ExpectedRevision: 1, RequestKey: uuid.New()}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() { defer wg.Done(); _, errs[i] = s.SetEnabled(f.ctx, f.a, h.ID, request) }()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	policy, err := s.Policy(f.ctx, f.a, nil)
	if err != nil || policy.ReservedSeconds != 32400 {
		t.Fatalf("reservation = %+v, %v", policy, err)
	}
	n, _ := f.store.Queries().GetHuddleNotes(f.ctx, generated.GetHuddleNotesParams{CompanyID: f.a.CompanyID, HuddleID: h.ID})
	intervals, _ := f.store.Queries().ListHuddleNotesIntervals(f.ctx, generated.ListHuddleNotesIntervalsParams{CompanyID: f.a.CompanyID, NotesID: n.ID})
	lease, err := s.Claim(f.ctx, intervals[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	policy.Enabled = false
	if _, err = s.Policy(f.ctx, f.a, &policy); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Append(f.ctx, intervals[0].ID, lease.Capability, HuddleNotesBatch{}); !errors.Is(err, ErrHuddleNotesUnavailable) {
		t.Fatalf("disabled policy append = %v", err)
	}
	if err = s.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	status, err := s.Get(f.ctx, f.a, h.ID)
	if err != nil || status.State != "interrupted" {
		t.Fatalf("disabled state = %+v, %v", status, err)
	}
}
func TestHuddleNotesRecapEditExpiry(t *testing.T) {
	f, s, h, _ := notesFixture(t)
	id, lease := startNotes(t, f, s, h, 1)
	if _, err := s.Append(f.ctx, id, lease.Capability, HuddleNotesBatch{Sequence: 1, Speaker: lease.Identities[0], Text: "Review the release", EndMs: 1000, Ready: true}); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE huddles SET state='ended',ended_at=now() WHERE id=$1`, h.ID)
	if err := s.GenerateRecaps(f.ctx); err != nil {
		t.Fatal(err)
	}
	status, err := s.Get(f.ctx, f.a, h.ID)
	if err != nil || status.Recap == nil {
		t.Fatalf("recap = %+v, %v", status, err)
	}
	revision := status.RecapRevision
	status.Recap.Summary = "Corrected summary"
	edited, err := s.EditRecap(f.ctx, f.a, EditHuddleNotesRecapInput{HuddleID: h.ID, ExpectedRevision: revision, Recap: *status.Recap})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.EditRecap(f.ctx, f.a, EditHuddleNotesRecapInput{HuddleID: h.ID, ExpectedRevision: revision, Recap: *status.Recap}); !errors.Is(err, ErrHuddleNotesConflict) {
		t.Fatalf("stale edit = %v", err)
	}
	if err = s.GenerateRecaps(f.ctx); err != nil {
		t.Fatal(err)
	}
	status, err = s.Get(f.ctx, f.a, h.ID)
	if err != nil || status.RecapRevision != edited.RecapRevision || status.Recap.Summary != "Corrected summary" {
		t.Fatalf("retry overwrote recap = %+v, %v", status, err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE huddle_notes SET expires_at=$2 WHERE huddle_id=$1`, h.ID, time.Now().Add(-time.Hour))
	if err = s.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	status, err = s.Get(f.ctx, f.a, h.ID)
	if err != nil || status.State != "expired" || len(status.Segments) != 0 || status.Recap != nil {
		t.Fatalf("expired = %+v, %v", status, err)
	}
	var revisions int
	if err = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM huddle_notes_recap_revisions`).Scan(&revisions); err != nil || revisions != 0 {
		t.Fatalf("retained content = %d, %v", revisions, err)
	}
}

func TestHuddleNotesChatCaptureBoundary(t *testing.T) {
	f, s, h, _ := notesFixture(t)
	if _, err := f.discussion.Send(f.ctx, f.a, h.ChatConversationID, textSend("before capture")); err != nil {
		t.Fatal(err)
	}
	id, lease := startNotes(t, f, s, h, 1)
	if _, err := s.Append(f.ctx, id, lease.Capability, HuddleNotesBatch{Ready: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.discussion.Send(f.ctx, f.a, h.ChatConversationID, textSend("captured chat")); err != nil {
		t.Fatal(err)
	}
	status, err := s.Get(f.ctx, f.a, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetEnabled(f.ctx, f.a, h.ID, SetHuddleNotesInput{ExpectedRevision: status.Revision, RequestKey: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.discussion.Send(f.ctx, f.a, h.ChatConversationID, textSend("after capture")); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE huddles SET state='ended',ended_at=now() WHERE id=$1`, h.ID)
	if err := s.GenerateRecaps(f.ctx); err != nil {
		t.Fatal(err)
	}
	status, err = s.Get(f.ctx, f.a, h.ID)
	if err != nil || len(status.Segments) != 1 || status.Segments[0].Speaker != "chat" || status.Segments[0].Text != "captured chat" {
		t.Fatalf("captured chat = %+v, %v", status.Segments, err)
	}
}

func TestHuddleNotesLeaseExpiryRejectsLateText(t *testing.T) {
	f, s, h, _ := notesFixture(t)
	id, lease := startNotes(t, f, s, h, 1)
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE huddle_notes_intervals SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, id)
	if _, err := s.Append(f.ctx, id, lease.Capability, HuddleNotesBatch{Sequence: 1, Speaker: lease.Identities[0], Text: "late", EndMs: 1000}); !errors.Is(err, ErrHuddleNotesUnavailable) {
		t.Fatalf("expired append = %v", err)
	}
	if err := s.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	status, err := s.Get(f.ctx, f.a, h.ID)
	if err != nil || status.State != "interrupted" || len(status.Segments) != 0 {
		t.Fatalf("expired lease = %+v, %v", status, err)
	}
}

func TestHuddleNotesDrainCutoffAndFinish(t *testing.T) {
	f, s, h, _ := notesFixture(t)
	id, lease := startNotes(t, f, s, h, 1)
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE huddle_notes_intervals SET started_at=now()-interval '2 seconds' WHERE id=$1`, id)
	before, err := s.Get(f.ctx, f.a, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := SetHuddleNotesInput{ExpectedRevision: before.Revision, RequestKey: uuid.New()}
	for range 2 {
		status, err := s.SetEnabled(f.ctx, f.a, h.ID, input)
		if err != nil || status.State != "stopping" {
			t.Fatalf("stop = %+v, %v", status, err)
		}
	}
	draining, err := s.Append(f.ctx, id, lease.Capability, HuddleNotesBatch{})
	if err != nil || draining.StopAfterMs == nil {
		t.Fatalf("drain lease = %+v, %v", draining, err)
	}
	if *draining.StopAfterMs < 2000 {
		t.Fatalf("cutoff = %d", *draining.StopAfterMs)
	}
	batch := HuddleNotesBatch{Sequence: 1, Speaker: lease.Identities[0], Text: "Final decision", StartMs: 500, EndMs: 1500}
	if _, err := s.Append(f.ctx, id, "wrong", batch); !errors.Is(err, ErrHuddleForbidden) {
		t.Fatalf("wrong capability = %v", err)
	}
	late := batch
	late.EndMs = *draining.StopAfterMs + 1
	if _, err := s.Append(f.ctx, id, lease.Capability, late); !errors.Is(err, ErrHuddleNotesConflict) {
		t.Fatalf("post-cutoff audio = %v", err)
	}
	for range 2 {
		if _, err := s.Append(f.ctx, id, lease.Capability, batch); err != nil {
			t.Fatal(err)
		}
	}
	during, err := s.Get(f.ctx, f.a, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetEnabled(f.ctx, f.a, h.ID, SetHuddleNotesInput{Enabled: true, ExpectedRevision: during.Revision, RequestKey: uuid.New()}); !errors.Is(err, ErrHuddleNotesConflict) {
		t.Fatalf("restart during drain = %v", err)
	}
	for range 2 {
		done, err := s.Append(f.ctx, id, lease.Capability, HuddleNotesBatch{Finished: true})
		if err != nil || !done.Finished {
			t.Fatalf("finish = %+v, %v", done, err)
		}
	}
	if _, err := s.Append(f.ctx, id, lease.Capability, batch); !errors.Is(err, ErrHuddleNotesUnavailable) {
		t.Fatalf("append after seal = %v", err)
	}
	status, err := s.Get(f.ctx, f.a, h.ID)
	if err != nil || status.State != "stopped" || len(status.Segments) != 1 {
		t.Fatalf("sealed notes = %+v, %v", status, err)
	}
	interval, err := f.store.Queries().GetHuddleNotesInterval(f.ctx, id)
	if err != nil || !interval.EndedAt.Time.Equal(interval.StopRequestedAt.Time) {
		t.Fatalf("capture cutoff moved: %+v, %v", interval, err)
	}
}

func TestHuddleNotesDrainFailsClosed(t *testing.T) {
	for _, cause := range []string{"timeout", "policy"} {
		t.Run(cause, func(t *testing.T) {
			f, s, h, _ := notesFixture(t)
			id, lease := startNotes(t, f, s, h, 1)
			status, err := s.Get(f.ctx, f.a, h.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.SetEnabled(f.ctx, f.a, h.ID, SetHuddleNotesInput{ExpectedRevision: status.Revision, RequestKey: uuid.New()}); err != nil {
				t.Fatal(err)
			}
			if cause == "timeout" {
				dbtest.MustExec(t, f.ctx, f.pool, `UPDATE huddle_notes_intervals SET drain_until=now()-interval '1 second' WHERE id=$1`, id)
			} else {
				policy, err := s.Policy(f.ctx, f.a, nil)
				if err != nil {
					t.Fatal(err)
				}
				policy.Enabled = false
				if _, err := s.Policy(f.ctx, f.a, &policy); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Append(f.ctx, id, lease.Capability, HuddleNotesBatch{Finished: true}); !errors.Is(err, ErrHuddleNotesUnavailable) {
				t.Fatalf("unauthorized drain = %v", err)
			}
			if err := s.Reconcile(f.ctx); err != nil {
				t.Fatal(err)
			}
			status, err = s.Get(f.ctx, f.a, h.ID)
			if err != nil || status.State != "interrupted" || status.StopReason != "finalization_incomplete" {
				t.Fatalf("failed drain = %+v, %v", status, err)
			}
		})
	}
}

func TestHuddleNotesCallEndPreservesIncompleteDrain(t *testing.T) {
	f, s, h, _ := notesFixture(t)
	_, _ = startNotes(t, f, s, h, 1)
	status, err := s.Get(f.ctx, f.a, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetEnabled(f.ctx, f.a, h.ID, SetHuddleNotesInput{ExpectedRevision: status.Revision, RequestKey: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE huddles SET state='ended',ended_at=now() WHERE id=$1`, h.ID)
	if err := s.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.GenerateRecaps(f.ctx); err != nil {
		t.Fatal(err)
	}
	status, err = s.Get(f.ctx, f.a, h.ID)
	if err != nil || status.State != "ended" || status.StopReason != "finalization_incomplete" {
		t.Fatalf("ended drain = %+v, %v", status, err)
	}
}
