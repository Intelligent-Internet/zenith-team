//go:build integration

package auth

import (
	"bytes"
	"slices"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/events"
)

// Every Project frame's audience is decided from the rows the transaction
// sees (ADR 0017): a Task chat or a Task reaches its Project's readers and
// never a projects.manage holder who cannot read it, a Company-visible
// Project the whole Company, a legacy Task chat with no Project every active
// member, and any other chat keeps its own route.
func TestStampProjectAudiencesFollowsProjectAccess(t *testing.T) {
	t.Parallel()
	w := newProjectAccessWorld(t)
	ctx := t.Context()

	var legacy uuid.UUID
	if err := w.pool.QueryRow(ctx, `INSERT INTO conversations (company_id, kind, title, created_at, updated_at)
		VALUES ($1, 'task', 'Legacy', '2020-01-01', '2020-01-01') RETURNING id`, w.f.CompanyA).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	w.replace(t, w.f.ProjectA, w.member, ProjectPermissionRead)
	readers := []uuid.UUID{w.f.UserA, w.member}
	slices.SortFunc(readers, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })

	event := func(conversationID, taskID uuid.UUID, payload testProjectFrame) events.Event {
		return events.Event{CompanyID: w.f.CompanyA, ConversationID: conversationID, TaskID: taskID, Payload: payload}
	}
	stamped := &ProjectAudience{UserIDs: []uuid.UUID{w.admin}}
	pending := []events.Event{
		event(w.f.ConversationA, uuid.Nil, testProjectFrame{scoped: true}),
		event(uuid.Nil, w.f.TaskA, testProjectFrame{scoped: true}),
		event(uuid.Nil, uuid.Nil, testProjectFrame{scoped: true, projectID: w.companyProject}),
		event(w.f.DirectConversationA, uuid.Nil, testProjectFrame{scoped: true}),
		event(legacy, uuid.Nil, testProjectFrame{scoped: true}),
		event(w.f.ConversationA, uuid.Nil, testProjectFrame{}),
		event(w.f.ConversationA, uuid.Nil, testProjectFrame{scoped: true, ProjectFrame: ProjectFrame{Audience: stamped}}),
		event(uuid.Nil, uuid.Nil, testProjectFrame{scoped: true, projectID: uuid.New()}),
	}
	if err := StampProjectAudiencesInTx(ctx, w.q, pending); err != nil {
		t.Fatalf("StampProjectAudiencesInTx() error = %v", err)
	}
	audience := func(index int) *ProjectAudience {
		return pending[index].Payload.(testProjectFrame).Audience
	}
	for index, name := range []string{"Task chat", "Task"} {
		got := audience(index)
		if got == nil || got.CompanyWide || !slices.Equal(got.UserIDs, readers) {
			t.Errorf("%s audience = %+v, want the readers %v and no bridge-only admin", name, got, readers)
		}
	}
	if got := audience(2); got == nil || !got.CompanyWide {
		t.Errorf("Company-visible Project audience = %+v, want the Company", got)
	}
	if got := audience(3); got != nil {
		t.Errorf("direct chat audience = %+v, want none: it keeps its creator route", got)
	}
	if got := audience(4); got == nil || !got.CompanyWide {
		t.Errorf("legacy Task chat audience = %+v, want every active member", got)
	}
	if got := audience(5); got != nil {
		t.Errorf("unscoped payload audience = %+v, want it untouched", got)
	}
	if got := audience(6); got != stamped {
		t.Errorf("stamped payload audience = %+v, want the one already decided", got)
	}
	if got := audience(7); got == nil || got.CompanyWide || len(got.UserIDs) != 0 {
		t.Errorf("missing Project audience = %+v, want nobody", got)
	}

	// Project lifecycle and access frames also reach the projects.manage
	// holders.
	managed, err := ProjectFrameAudienceInTx(ctx, w.q, w.f.CompanyA, w.f.ProjectA, true)
	if err != nil {
		t.Fatal(err)
	}
	want := append(slices.Clone(readers), w.admin)
	slices.SortFunc(want, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	if managed.CompanyWide || !slices.Equal(managed.UserIDs, want) {
		t.Errorf("lifecycle audience = %+v, want readers and projects.manage holders %v", managed, want)
	}
}
