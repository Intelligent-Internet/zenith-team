//go:build integration

package canvas

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"math"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/db"
)

func TestBoardVisualsPageAcknowledgedInkWithoutPersistingImages(t *testing.T) {
	t.Parallel()
	boards, pool, owner, member := boardFixture(t)
	board := createTestBoard(t, boards, owner, member)
	ctx := t.Context()
	_, run := boardTestRun(t, pool, member)
	grantTestBoardAccess(t, boards, owner, board.ID, member.UserID, "view")
	var commands []BoardCommand
	for i := range 7 {
		commands = append(
			commands,
			BoardCommand{
				Type:       "create_item",
				DraftID:    fmt.Sprintf("ink-%d", i),
				ItemType:   "stroke",
				Geometry:   &BoardGeometry{X: float64(i) * 4000, Y: 20, Width: 80, Height: 40},
				Points:     []BoardPoint{{X: 6, Y: 6}, {X: 74, Y: 34}},
				Properties: &BoardProperties{Color: "#000000"},
			},
		)
	}
	mutation, err := boards.Mutate(
		ctx,
		owner,
		BoardScope{BoardID: board.ID},
		BoardBatch{RequestKey: uuid.New(), Commands: commands},
	)
	if err != nil {
		t.Fatal(err)
	}
	store, _ := db.NewStore(pool)
	service := NewBoardMediaService(store, nil)
	scope := BoardScope{BoardID: board.ID, RunID: run}
	input := BoardContextInput{ExpectedSequence: &mutation.Sequence}
	seen := map[uuid.UUID]bool{}
	for {
		got, err := service.Visuals(ctx, member, scope, input)
		if err != nil {
			t.Fatal(err)
		}
		if got.Sequence != mutation.Sequence || got.TotalRegions != 7 || len(got.Regions) > 4 {
			t.Fatalf("page: %+v", got)
		}
		for _, region := range got.Regions {
			if _, err := png.Decode(bytes.NewReader(region.Data)); err != nil {
				t.Fatal(err)
			}
			for _, id := range region.ItemIDs {
				seen[id] = true
			}
		}
		if !got.HasMore {
			if got.NextOffset != nil {
				t.Fatal("end page has continuation")
			}
			break
		}
		if got.NextOffset == nil || *got.NextOffset <= input.VisualOffset {
			t.Fatal("pagination did not advance")
		}
		input.VisualOffset = *got.NextOffset
	}
	if len(seen) != 7 {
		t.Fatalf("visible strokes = %d, want 7", len(seen))
	}
	input.VisualOffset = 0
	input.ItemIDs = []uuid.UUID{mutation.IDs["ink-6"]}
	selected, err := service.Visuals(ctx, member, scope, input)
	if err != nil || len(selected.Regions) != 1 || len(selected.Regions[0].ItemIDs) != 1 ||
		selected.Regions[0].ItemIDs[0] != input.ItemIDs[0] {
		t.Fatalf("selected preview = %+v, %v", selected, err)
	}
	var saved int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM board_files WHERE board_id=$1)+(SELECT count(*) FROM board_operations WHERE board_id=$1)`, board.ID).
		Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if saved != 1 {
		t.Fatalf("read persisted rows = %d, want original mutation only", saved)
	}
	_, err = boards.Mutate(
		ctx,
		owner,
		BoardScope{BoardID: board.ID},
		BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{stickyCommand("new", "new revision")}},
	)
	if err != nil {
		t.Fatal(err)
	}
	// A collaborator's edit between pages does not refuse the next page.
	later, err := service.Visuals(ctx, member, scope, input)
	if err != nil || !later.Changed || later.Sequence != mutation.Sequence+1 {
		t.Fatalf("page after a concurrent edit = %+v, %v; want the latest state marked changed", later, err)
	}
	grantTestBoardAccess(t, boards, owner, board.ID, member.UserID, "none")
	if _, err := service.Visuals(ctx, member, scope, input); !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("revoked access = %v", err)
	}
}

// A Whiteboard card's preview frames all of the Board 16:9, is not drawn
// again for the sequence a client holds, and follows the Artifact's access.
func TestBoardPreviewFramesTheWholeBoard(t *testing.T) {
	t.Parallel()
	boards, pool, owner, member := boardFixture(t)
	board := createTestBoard(t, boards, owner)
	ctx := t.Context()
	store, _ := db.NewStore(pool)
	service := NewBoardMediaService(store, nil)
	scope := BoardScope{BoardID: board.ID}
	empty, err := service.Preview(ctx, owner, scope, -1)
	if err != nil || empty.Image != nil || empty.NotModified {
		t.Fatalf("empty Board preview = %+v, %v; want no image", empty, err)
	}
	mutation, err := boards.Mutate(ctx, owner, scope, BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{
		stickyCommand("note", "Launch plan"),
		{
			Type:       "create_item",
			DraftID:    "ink",
			ItemType:   "stroke",
			Geometry:   &BoardGeometry{X: 2400, Y: 900, Width: 80, Height: 40},
			Points:     []BoardPoint{{X: 6, Y: 6}, {X: 74, Y: 34}},
			Properties: &BoardProperties{Color: "#000000"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.Preview(ctx, owner, scope, empty.Sequence)
	if err != nil || got.Image == nil || got.NotModified || got.Sequence != mutation.Sequence {
		t.Fatalf("preview = %+v, %v; want an image at sequence %d", got, err, mutation.Sequence)
	}
	frame := got.Image.Bounds
	if math.Abs(frame.Width*9-frame.Height*16) > 1e-6 || len(got.Image.ItemIDs) != 2 {
		t.Fatalf("preview frame %+v shows %d items; want 16:9 around both", frame, len(got.Image.ItemIDs))
	}
	image, err := png.Decode(bytes.NewReader(got.Image.Data))
	if err != nil || image.Bounds().Dx() > boardPreviewPixels {
		t.Fatalf("preview image %v, %v", image.Bounds(), err)
	}
	again, err := service.Preview(ctx, owner, scope, got.Sequence)
	if err != nil || !again.NotModified || again.Image != nil {
		t.Fatalf("unchanged Board preview = %+v, %v; want not modified", again, err)
	}
	if _, err := service.Preview(ctx, member, scope, -1); !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("preview without access = %v", err)
	}
}

// The first page shows the whole Board at low resolution; every page names its
// region plan, and a later page reports when that plan moved under the reader.
func TestBoardVisualsOverviewAndRegionPlan(t *testing.T) {
	t.Parallel()
	boards, pool, owner, _ := boardFixture(t)
	board := createTestBoard(t, boards, owner)
	ctx := t.Context()
	var commands []BoardCommand
	for i := range 6 {
		command := stickyCommand(fmt.Sprintf("note-%d", i), fmt.Sprintf("Cụm %d", i))
		command.Geometry = &BoardGeometry{X: float64(i) * 3000, Y: 0, Width: 220, Height: 150}
		commands = append(commands, command)
	}
	created, err := boards.Mutate(ctx, owner, BoardScope{BoardID: board.ID}, BoardBatch{RequestKey: uuid.New(), Commands: commands})
	if err != nil {
		t.Fatal(err)
	}
	store, _ := db.NewStore(pool)
	service := NewBoardMediaService(store, nil)
	scope := BoardScope{BoardID: board.ID}
	first, err := service.Visuals(ctx, owner, scope, BoardContextInput{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Overview == nil || len(first.Overview.Data) == 0 || first.Overview.PixelWidth > 1280 ||
		first.PlanHash == "" || first.TotalRegions != 6 || first.RegionsShifted {
		t.Fatalf("first page = overview %v, plan %q, %d regions, shifted %t",
			first.Overview != nil, first.PlanHash, first.TotalRegions, first.RegionsShifted)
	}
	next := BoardContextInput{VisualOffset: *first.NextOffset, ExpectedSequence: &first.Sequence, ExpectedPlan: first.PlanHash}
	same, err := service.Visuals(ctx, owner, scope, next)
	if err != nil || same.Overview != nil || same.RegionsShifted || same.PlanHash != first.PlanHash {
		t.Fatalf("unchanged continuation = %+v, %v", same, err)
	}
	moved := created.IDs["note-0"]
	if _, err = boards.Mutate(ctx, owner, scope, BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{{
		Type: "geometry", ID: &moved, ExpectedRevision: 1, Geometry: &BoardGeometry{X: 3100, Y: 0, Width: 220, Height: 150},
	}}}); err != nil {
		t.Fatal(err)
	}
	shifted, err := service.Visuals(ctx, owner, scope, next)
	if err != nil || !shifted.Changed || !shifted.RegionsShifted || shifted.PlanHash == first.PlanHash {
		t.Fatalf("continuation after clusters merged = %+v, %v", shifted, err)
	}
}
