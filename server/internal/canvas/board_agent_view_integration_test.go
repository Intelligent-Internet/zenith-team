//go:build integration

package canvas

import (
	"bytes"
	"errors"
	"image/png"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/pkg/protocol"
)

// A Run reads the area its member looks at in the compact form, full text
// of what it asks for by ref, and an image of exactly that area; the same
// invitation fence as every Board read applies.
func TestBoardAgentViewReadsTheRequestersAreaThroughTheRunFence(t *testing.T) {
	t.Parallel()
	boards, pool, owner, member := boardFixture(t)
	board := createTestBoard(t, boards, owner, member)
	ctx := t.Context()
	agent, run := boardTestRun(t, pool, member)
	scope := BoardScope{BoardID: board.ID, RunID: run}
	looked := BoardVisualBounds{X: 0, Y: 0, Width: 1000, Height: 600}
	focus := BoardFocus{Present: true, Name: "Member", View: looked}
	_, err := boards.AgentView(ctx, member, scope, BoardAgentViewInput{}, focus)
	if !errors.Is(err, ErrBoardForbidden) && !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("view without an invitation = %v, want refused", err)
	}
	if err := grantTestBoardAccess(
		t, boards, owner, board.ID, protocol.ActorRef{ID: agent, Kind: protocol.ActorKindAgent}, "read",
	); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("Onboarding step ", 40)
	near := stickyCommand("near", long)
	far := stickyCommand("far", "Far away")
	far.Geometry = &BoardGeometry{X: 9000, Y: 9000, Width: 220, Height: 150}
	ink := BoardCommand{
		Type: "create_item", DraftID: "ink", ItemType: "stroke",
		Geometry:   &BoardGeometry{X: 300, Y: 300, Width: 80, Height: 40},
		Points:     []BoardPoint{{X: 6, Y: 6}, {X: 74, Y: 34}},
		Properties: &BoardProperties{Color: "#4c6fff"},
	}
	mutation, err := boards.Mutate(ctx, owner, BoardScope{BoardID: board.ID}, BoardBatch{
		RequestKey: uuid.New(), Commands: []BoardCommand{near, far, ink},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := boards.AgentView(ctx, member, scope, BoardAgentViewInput{}, focus)
	if err != nil {
		t.Fatal(err)
	}
	nearRef := strings.ReplaceAll(mutation.IDs["near"].String(), "-", "")[:8]
	if view.Sequence != mutation.Sequence || view.Area == nil {
		t.Fatalf("view = %+v; want the committed sequence and the area read", view)
	}
	for _, want := range []string{`Board "Ideas", read only`, nearRef + " sticky 10,20 220x150", "color=blue"} {
		if !strings.Contains(view.Text, want) {
			t.Fatalf("view lacks %q:\n%s", want, view.Text)
		}
	}
	if strings.Contains(view.Text, "Far away") {
		t.Fatalf("view lists an object outside the area:\n%s", view.Text)
	}
	if strings.Contains(view.Text, long) {
		t.Fatal("the area reading lists whole note text; it should preview it")
	}
	detail, err := boards.AgentView(ctx, member, scope, BoardAgentViewInput{ItemRefs: []string{nearRef}}, focus)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail.Text, strings.TrimSpace(long)) || len(detail.Refs) != 1 ||
		detail.Refs[0].ID != mutation.IDs["near"] || detail.Refs[0].GeometryRevision < 1 {
		t.Fatalf("detail = %+v\n%s", detail, detail.Text)
	}
	store, _ := db.NewStore(pool)
	region, err := NewBoardMediaService(store, nil).RectVisual(ctx, member, scope, *view.Area, 1024)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(region.Data))
	if err != nil {
		t.Fatal(err)
	}
	if size := max(decoded.Bounds().Dx(), decoded.Bounds().Dy()); size > 1024 || len(region.ItemIDs) != 2 {
		t.Fatalf("area image is %d pixels showing %v; want both near objects within 1024", size, region.ItemIDs)
	}
}

// Every call a Run makes leaves its viewer (the Agent's name and the
// Board's audience) ready, so moving the Agent's cursor right after the call
// needs no transaction of its own.
func TestBoardRunCallsLeaveTheirViewerReady(t *testing.T) {
	t.Parallel()
	boards, pool, owner, member := boardFixture(t)
	board := createTestBoard(t, boards, owner, member)
	ctx := t.Context()
	agent, run := boardTestRun(t, pool, member)
	if err := grantTestBoardAccess(
		t, boards, owner, board.ID, protocol.ActorRef{ID: agent, Kind: protocol.ActorKindAgent}, "edit",
	); err != nil {
		t.Fatal(err)
	}
	if err := grantTestBoardAccess(
		t, boards, owner, board.ID, protocol.ActorRef{ID: member.UserID, Kind: protocol.ActorKindUser}, "edit",
	); err != nil {
		t.Fatal(err)
	}
	scope := BoardScope{BoardID: board.ID, RunID: run}
	key := boardViewerKey{company: member.CompanyID, user: member.UserID, board: board.ID, run: run}
	ready := func(call string) {
		t.Helper()
		viewer, ok := boards.viewers.get(key, time.Now())
		if !ok || viewer.Actor.ID != agent || viewer.Actor.DisplayName == "" || viewer.RunID == nil ||
			!slices.Contains(viewer.RecipientUserIDs, owner.UserID) {
			t.Fatalf(
				"after %s the viewer = %+v, %v; want the Agent's name and the audience",
				call,
				viewer,
				ok,
			)
		}
		boards.viewers.put(key, BoardViewer{}, time.Now().Add(-time.Hour))
	}
	if _, err := boards.AgentView(ctx, member, scope, BoardAgentViewInput{}, BoardFocus{}); err != nil {
		t.Fatal(err)
	}
	ready("a view")
	if _, err := boards.Context(ctx, member, scope, BoardContextInput{}); err != nil {
		t.Fatal(err)
	}
	ready("a context read")
	composed, err := boards.Compose(ctx, member, scope, BoardComposeInput{
		RequestKey: uuid.New(), Ops: []BoardComposeOp{{Type: "text", Text: "Hello"}},
	}, BoardFocus{})
	if err != nil {
		t.Fatal(err)
	}
	ready("a compose")
	if _, err := boards.Mutate(ctx, member, scope, BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{{
		Type:             "delete_item",
		ID:               &composed.Mutation.Items[0].ID,
		ExpectedRevision: composed.Mutation.Items[0].ContentRevision,
	}}}); err != nil {
		t.Fatal(err)
	}
	ready("an edit")
}
