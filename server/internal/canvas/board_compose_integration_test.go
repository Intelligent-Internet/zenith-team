//go:build integration

package canvas

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// A Run composes through the same fences and receipts as its edits: a
// read-only invitation refuses it, a retry replays the very objects even
// after the scene changed, and a reused key with another intent is refused.
func TestBoardComposeAppliesOneReceiptedBatchWhereTheRequesterLooks(t *testing.T) {
	t.Parallel()
	boards, pool, owner, member := boardFixture(t)
	board := createTestBoard(t, boards, owner, member)
	ctx := t.Context()
	_, run := boardTestRun(t, pool, member)
	scope := BoardScope{BoardID: board.ID, RunID: run}
	focus := BoardFocus{Present: true, View: BoardVisualBounds{X: 2000, Y: 1000, Width: 1600, Height: 900}}
	input := BoardComposeInput{
		RequestKey: uuid.New(),
		Ops: []BoardComposeOp{
			{
				Type:  "diagram",
				Frame: "Flow",
				Nodes: []BoardComposeNode{
					{Key: "a", Text: "Start"},
					{Key: "b", Text: "Finish", Shape: "ellipse"},
				},
				Edges: []BoardComposeEdge{{From: "a", To: "b", Label: "then"}},
			},
		},
	}
	grantTestBoardAccess(t, boards, owner, board.ID, member.UserID, "view")
	if _, err := boards.Compose(ctx, member, scope, input, focus); !errors.Is(err, ErrBoardForbidden) {
		t.Fatalf("compose for a viewer = %v, want forbidden", err)
	}
	grantTestBoardAccess(t, boards, owner, board.ID, member.UserID, "edit")
	first, err := boards.Compose(ctx, member, scope, input, focus)
	if err != nil {
		t.Fatal(err)
	}
	if first.Bounds == nil || first.Placement != BoardPlacementTypeView || len(first.Mutation.IDs) != 4 {
		t.Fatalf("compose = %+v; want a frame, two nodes and a connector inside the view", first)
	}
	view := focus.View
	if b := first.Bounds; b.X < view.X || b.Y < view.Y || b.X+b.Width > view.X+view.Width {
		t.Fatalf("placed at %+v; want inside the requester's view %+v", b, view)
	}
	snapshot, err := boards.Snapshot(ctx, member, BoardScope{BoardID: board.ID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var nodes, edges int
	for _, item := range snapshot.Items {
		if item.ID == first.Mutation.IDs["a"] {
			nodes++
			if item.ParentFrameID == nil || *item.ParentFrameID != first.Mutation.IDs["b1.frame"] ||
				item.CreatedBy.Kind != protocol.ActorKindAgent || item.CreatedByRunID == nil || *item.CreatedByRunID != run {
				t.Fatalf("node a = %+v; want it in its frame, by the Agent in this Run", item)
			}
		}
	}
	for _, edge := range snapshot.Edges {
		if edge.SourceID == first.Mutation.IDs["a"] && edge.TargetID == first.Mutation.IDs["b"] &&
			edge.Label == "then" {
			edges++
		}
	}
	if nodes != 1 || edges != 1 {
		t.Fatalf("stored nodes = %d, edges = %d", nodes, edges)
	}
	// A collaborator fills the view; the retry still replays the same objects.
	if _, err := boards.Mutate(ctx, owner, BoardScope{BoardID: board.ID}, BoardBatch{
		RequestKey: uuid.New(), Commands: []BoardCommand{stickyCommand("later", "In the way")},
	}); err != nil {
		t.Fatal(err)
	}
	again, err := boards.Compose(ctx, member, scope, input, focus)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Mutation.Replayed || again.Mutation.IDs["a"] != first.Mutation.IDs["a"] || again.Bounds == nil {
		t.Fatalf("retry = %+v; want the first compose replayed", again)
	}
	other := input
	other.Ops = []BoardComposeOp{{Type: "text", Text: "Different"}}
	if _, err := boards.Compose(ctx, member, scope, other, focus); !errors.Is(err, ErrBoardRequestKeyReused) {
		t.Fatalf("reused key = %v, want ErrBoardRequestKeyReused", err)
	}
	// The next request of this Run continues beside its earlier work.
	next, err := boards.Compose(ctx, member, scope, BoardComposeInput{
		RequestKey: uuid.New(),
		Ops:        []BoardComposeOp{{Type: "notes", Items: []BoardComposeNote{{Text: "Next"}}}},
	}, focus)
	if err != nil {
		t.Fatal(err)
	}
	if next.Bounds.X < first.Bounds.X+first.Bounds.Width {
		t.Fatalf("next work at %+v; want it after this Run's earlier work %+v", next.Bounds, first.Bounds)
	}
}

// A tag in a chat works on the Board for the person who tagged: their access
// decides whether it may draw, and nobody else's proof carries that Run.
func TestBoardComposeFromATagInAChat(t *testing.T) {
	t.Parallel()
	boards, pool, owner, member := boardFixture(t)
	board := createTestBoard(t, boards, owner, member)
	ctx := t.Context()
	var agent, runtime uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM agents WHERE company_id = $1 LIMIT 1`, owner.CompanyID).
		Scan(&agent); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM runtimes WHERE company_id = $1 LIMIT 1`, owner.CompanyID).
		Scan(&runtime); err != nil {
		t.Fatal(err)
	}
	channel := dbtest.SeedDiscussionChannel(t, ctx, pool, owner.CompanyID, member.UserID, []uuid.UUID{agent})
	run := dbtest.SeedTaggedRun(t, ctx, pool, owner.CompanyID, channel, agent, runtime, member.UserID)
	scope := BoardScope{BoardID: board.ID, RunID: run}
	focus := BoardFocus{Present: true, View: BoardVisualBounds{X: -400, Y: 300, Width: 1200, Height: 800}}
	input := BoardComposeInput{
		RequestKey: uuid.New(),
		Ops:        []BoardComposeOp{{Type: "notes", Items: []BoardComposeNote{{Text: "From the chat"}}}},
	}
	grantTestBoardAccess(t, boards, owner, board.ID, member.UserID, "view")
	if _, err := boards.Compose(ctx, member, scope, input, focus); !errors.Is(err, ErrBoardForbidden) {
		t.Fatalf("a tag drawing for a viewer = %v, want forbidden", err)
	}
	grantTestBoardAccess(t, boards, owner, board.ID, member.UserID, "edit")
	if _, err := boards.Compose(ctx, owner, scope, input, focus); err == nil {
		t.Fatal("another member carried the tag's Run onto the Board")
	}
	result, err := boards.Compose(ctx, member, scope, input, focus)
	if err != nil {
		t.Fatal(err)
	}
	if result.Placement != BoardPlacementTypeView {
		t.Fatalf("a tag's notes landed %q at %+v, want where the tagger looks", result.Placement, result.Bounds)
	}
}

// A sequence diagram commits as one batch, self message included, and a
// retry replays it without drawing it twice.
func TestBoardComposeSequencePersistsAndReplays(t *testing.T) {
	t.Parallel()
	boards, pool, owner, member := boardFixture(t)
	board := createTestBoard(t, boards, owner, member)
	ctx := t.Context()
	_, run := boardTestRun(t, pool, member)
	grantTestBoardAccess(t, boards, owner, board.ID, member.UserID, "edit")
	scope := BoardScope{BoardID: board.ID, RunID: run}
	focus := BoardFocus{Present: true, View: BoardVisualBounds{Width: 2000, Height: 1400}}
	input := BoardComposeInput{RequestKey: uuid.New(), Ops: []BoardComposeOp{{
		Type:  "sequence",
		Nodes: []BoardComposeNode{{Key: "web", Text: "Web"}, {Key: "api", Text: "API"}},
		Edges: []BoardComposeEdge{
			{From: "web", To: "api", Label: "GET /orders"},
			{From: "api", To: "api", Label: "authorize()"},
			{From: "api", To: "web", Label: "200", Style: "reply"},
		},
		Fragments: []BoardComposeFragment{{Type: "opt", Label: "signed in", First: 2, Last: 3}},
	}}}
	first, err := boards.Compose(ctx, member, scope, input, focus)
	if err != nil {
		t.Fatal(err)
	}
	again, err := boards.Compose(ctx, member, scope, input, focus)
	if err != nil || again.Mutation.Sequence != first.Mutation.Sequence ||
		again.Mutation.IDs["o1.m2"] != first.Mutation.IDs["o1.m2"] {
		t.Fatalf("replay = %+v, %v; want the same objects", again.Mutation, err)
	}
	snapshot, err := boards.Snapshot(ctx, member, BoardScope{BoardID: board.ID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var messages, selfs int
	for _, edge := range snapshot.Edges {
		if edge.Route == BoardRouteTypeMessage && edge.AnchorY != nil {
			messages++
			if edge.SourceID == edge.TargetID {
				selfs++
			}
		}
	}
	var lifelines, fragments int
	for _, item := range snapshot.Items {
		switch item.Properties.Shape {
		case BoardShapeTypeLifeline:
			lifelines++
		case BoardShapeTypeFragment:
			fragments++
		}
	}
	if messages != 3 || selfs != 1 || lifelines != 2 || fragments != 1 {
		t.Fatalf(
			"committed %d messages (%d self), %d lifelines, %d fragments",
			messages,
			selfs,
			lifelines,
			fragments,
		)
	}
}
