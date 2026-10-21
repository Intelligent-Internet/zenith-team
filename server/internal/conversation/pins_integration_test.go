//go:build integration

package conversation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestTaskConversationArchiveIsPrivateAndReversible(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `INSERT INTO company_members(company_id,user_id,role,status)
		VALUES($1,$2,'member','active')`, fixture.CompanyA, fixture.UserB)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service := newTestConversationService(t, store, bus)
	principal := auth.UserPrincipal{CompanyID: fixture.CompanyA, UserID: fixture.UserA, Role: auth.CompanyRoleOwner}
	other := auth.UserPrincipal{CompanyID: fixture.CompanyA, UserID: fixture.UserB, Role: auth.CompanyRoleMember}
	// Task chats follow their Project (ADR 0017): a member who does not read
	// Project A cannot see the chat at all, so give UserB read access.
	if _, err := service.FetchState(ctx, other, fixture.ConversationA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-reader state error = %v, want ErrNotFound", err)
	}
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, fixture.UserB, "project.read")
	var before int64
	if err := pool.QueryRow(ctx, `SELECT version FROM conversations WHERE id=$1`, fixture.ConversationA).
		Scan(&before); err != nil {
		t.Fatal(err)
	}
	yes, no := true, false
	input := UpdateUserStateInput{ExpectedRevision: 0, Pinned: &yes, Archived: &yes}
	saved, err := service.UpdateState(ctx, principal, fixture.ConversationA, input)
	if err != nil || saved.ArchivedAt == nil || saved.PinnedAt == nil || saved.Revision != 1 {
		t.Fatalf("archive = %+v, %v, want archived and pinned revision 1", saved, err)
	}
	replayed, err := service.UpdateState(ctx, principal, fixture.ConversationA, input)
	if err != nil || replayed.Revision != saved.Revision || !replayed.ArchivedAt.Equal(*saved.ArchivedAt) {
		t.Fatalf("replay = %+v, %v, want original archive receipt", replayed, err)
	}
	peer, err := service.FetchState(ctx, other, fixture.ConversationA)
	if err != nil || peer.ArchivedAt != nil || peer.PinnedAt != nil || peer.Revision != 0 {
		t.Fatalf("other member state = %+v, %v, want unchanged", peer, err)
	}
	pins, err := service.ListPins(ctx, principal)
	if err != nil || len(pins) != 0 {
		t.Fatalf("pins = %+v, %v, want archived chats excluded", pins, err)
	}
	states, err := service.ListStates(ctx, principal)
	if err != nil || len(states) != 1 || states[0].ConversationID != fixture.ConversationA ||
		states[0].ArchivedAt == nil {
		t.Fatalf("saved states = %+v, %v, want personal archive", states, err)
	}
	otherStates, err := service.ListStates(ctx, other)
	if err != nil || len(otherStates) != 0 {
		t.Fatalf("other states = %+v, %v, want empty", otherStates, err)
	}
	var after int64
	var sharedArchived bool
	err = pool.QueryRow(ctx, `SELECT version,archived_at IS NOT NULL FROM conversations WHERE id=$1`, fixture.ConversationA).
		Scan(&after, &sharedArchived)
	if err != nil || after != before || sharedArchived {
		t.Fatalf(
			"shared conversation = revision %d, archived %t, %v, want unchanged",
			after,
			sharedArchived,
			err,
		)
	}
	if _, err := service.UpdateState(ctx, principal, fixture.ConversationA,
		UpdateUserStateInput{ExpectedRevision: 0, Archived: &no}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale restore = %v, want conflict", err)
	}
	restored, err := service.UpdateState(ctx, principal, fixture.ConversationA,
		UpdateUserStateInput{ExpectedRevision: saved.Revision, Archived: &no})
	if err != nil || restored.ArchivedAt != nil || restored.PinnedAt == nil || restored.Revision != 2 {
		t.Fatalf("restore = %+v, %v, want restored pin at revision 2", restored, err)
	}
	pins, err = service.ListPins(ctx, principal)
	if err != nil || len(pins) != 1 || pins[0].ConversationID != fixture.ConversationA {
		t.Fatalf("restored pins = %+v, %v, want task chat", pins, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		fixture.CompanyA,
		fixture.UserA,
	)
	if _, err := service.UpdateState(
		ctx,
		principal,
		fixture.ConversationA,
		UpdateUserStateInput{
			ExpectedRevision: restored.Revision,
			Archived:         &yes,
		},
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked member archive = %v, want not found", err)
	}
	states, err = service.ListStates(ctx, principal)
	if err != nil || len(states) != 0 {
		t.Fatalf("revoked member states = %+v, %v, want empty", states, err)
	}
}

func TestDirectArchiveRestoresAlongsideNewDefaultChat(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service := newTestConversationService(t, store, bus)
	principal := auth.UserPrincipal{CompanyID: fixture.CompanyA, UserID: fixture.UserA, Role: auth.CompanyRoleOwner}
	state, err := service.FetchState(ctx, principal, fixture.DirectConversationA)
	if err != nil {
		t.Fatal(err)
	}
	yes, no := true, false
	saved, err := service.UpdateState(ctx, principal, fixture.DirectConversationA,
		UpdateUserStateInput{ExpectedRevision: state.Revision, Archived: &yes})
	if err != nil {
		t.Fatal(err)
	}
	active, err := service.ListDirect(ctx, principal, false)
	if err != nil || len(active.Conversations) != 0 {
		t.Fatalf("active chats = %+v, %v, want empty", active, err)
	}
	all, err := service.ListDirect(ctx, principal, true)
	if err != nil || len(all.Conversations) != 1 || all.Conversations[0].UserState.ArchivedAt == nil {
		t.Fatalf("all chats = %+v, %v, want archived chat", all, err)
	}
	states, err := service.ListStates(ctx, principal)
	if err != nil || len(states) != 1 || states[0].ConversationID != fixture.DirectConversationA {
		t.Fatalf("filing states = %+v, %v, want archived direct", states, err)
	}
	foreign := auth.UserPrincipal{CompanyID: fixture.CompanyB, UserID: fixture.UserA}
	states, err = service.ListStates(ctx, foreign)
	if err != nil || len(states) != 0 {
		t.Fatalf("foreign states = %+v, %v, want empty", states, err)
	}
	created, err := service.CreateDirect(ctx, principal, CreateDirectInput{AgentID: fixture.AgentA})
	if err != nil || !created.Created || created.Conversation.ID == fixture.DirectConversationA {
		t.Fatalf("new chat = %+v, %v, want independent chat", created, err)
	}
	restored, err := service.UpdateState(ctx, principal, fixture.DirectConversationA,
		UpdateUserStateInput{ExpectedRevision: saved.Revision, Archived: &no})
	if err != nil || restored.ArchivedAt != nil {
		t.Fatalf("restore = %+v, %v, want restored history", restored, err)
	}
	active, err = service.ListDirect(ctx, principal, false)
	if err != nil || len(active.Conversations) != 2 {
		t.Fatalf("active chats = %+v, %v, want both histories", active, err)
	}
}
