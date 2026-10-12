//go:build integration

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"boardly.dev/boardly/server/pkg/protocol"
)

// A Board's sharing never touches its chat, yet by the time a sharing change
// returns, the chat has followed it: someone the owner stops sharing with has
// left the chat, and the Agents the Board invites are the chat's Agents. A
// Board invites at most a channel's 20 Agents.
func TestBoardChatFollowsSharingChanges(t *testing.T) {
	ctx := context.Background()
	pool, _ := dbtest.StartPostgres(t)
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		f.CompanyA,
		f.UserB,
	)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	channels, err := conversation.NewBoardChannelService(
		conversation.BoardChannelServiceOptions{Store: store, Bus: bus},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerBoardChannelSync(bus, channels); err != nil {
		t.Fatal(err)
	}
	boards := canvas.NewBoardService(store, bus)
	owner := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	member := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: auth.CompanyRoleMember}
	var board uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO boards (company_id, title, created_by) VALUES ($1, 'Plan', $2) RETURNING id`,
		f.CompanyA, f.UserA).
		Scan(&board); err != nil {
		t.Fatal(err)
	}
	revision := func() int64 {
		t.Helper()
		var value int64
		if err := pool.QueryRow(ctx, `SELECT revision FROM boards WHERE id = $1`, board).
			Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	share := func(actor protocol.ActorRef, access string) error {
		return boards.SetAccess(ctx, owner, board, canvas.BoardAccessInput{
			ExpectedRevision: revision(), Actor: actor, Access: access,
		})
	}
	person := protocol.ActorRef{ID: f.UserB, Kind: protocol.ActorKindUser}
	if err := share(person, "read"); err != nil {
		t.Fatal(err)
	}
	opened, err := channels.Join(ctx, member, board)
	if err != nil {
		t.Fatal(err)
	}
	if err := share(protocol.ActorRef{ID: f.AgentA, Kind: protocol.ActorKindAgent}, "edit"); err != nil {
		t.Fatal(err)
	}
	var agents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM conversation_agents WHERE conversation_id = $1 AND status = 'active'`,
		*opened.ConversationID).
		Scan(&agents); err != nil ||
		agents != 1 {
		t.Fatalf("the chat's Agents after inviting one to the Board = %d, %v", agents, err)
	}

	if err := share(person, "none"); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM conversation_members WHERE conversation_id = $1 AND user_id = $2`,
		*opened.ConversationID, f.UserB).
		Scan(&status); err != nil ||
		status != "revoked" {
		t.Fatalf("B in the chat after the Board stopped being shared with them = %q, %v", status, err)
	}

	var helper uuid.UUID
	for i := range 19 {
		agent := dbtest.NewUUID(t)
		helper = agent
		dbtest.SeedAgent(t, ctx, pool, agent, f.CompanyA, "Helper "+uuid.NewString()[:8], "codex", &f.AgentA)
		if err := share(protocol.ActorRef{ID: agent, Kind: protocol.ActorKindAgent}, "read"); err != nil {
			t.Fatalf("inviting Agent %d: %v", i+2, err)
		}
	}
	extra := dbtest.NewUUID(t)
	dbtest.SeedAgent(t, ctx, pool, extra, f.CompanyA, "One too many", "codex", &f.AgentA)
	if err := share(
		protocol.ActorRef{ID: extra, Kind: protocol.ActorKindAgent},
		"read",
	); !errors.Is(
		err,
		canvas.ErrBoardLimit,
	) {
		t.Fatalf("inviting a 21st Agent = %v, want ErrBoardLimit", err)
	}

	// An archived Agent leaves the chat, so it holds no place in it.
	dbtest.MustExec(t, ctx, pool, `UPDATE agents SET archived_at = now() WHERE id = $1`, helper)
	if err := share(protocol.ActorRef{ID: extra, Kind: protocol.ActorKindAgent}, "read"); err != nil {
		t.Fatalf("inviting an Agent in an archived one's place = %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM conversation_agents WHERE conversation_id = $1 AND status = 'active'`,
		*opened.ConversationID).
		Scan(&agents); err != nil ||
		agents != canvas.BoardAgentLimit {
		t.Fatalf("the chat's Agents after replacing an archived one = %d, %v", agents, err)
	}
}
