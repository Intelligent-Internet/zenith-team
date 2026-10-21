//go:build integration

package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reearth/ygo/crdt"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// A lock stops movement, not membership: undoing a frame deletion puts a
// locked child back in its frame without moving it.
func TestBoardLockedObjectsFollowFrameUndoWithoutMoving(t *testing.T) {
	t.Parallel()
	s, _, p, _ := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, s, p)
	scope := BoardScope{BoardID: board.ID}
	frame := BoardCommand{
		Type: "create_item", DraftID: "frame", ItemType: "frame",
		Geometry: &BoardGeometry{Width: 1000, Height: 500}, Properties: &BoardProperties{Title: "Ideas"},
	}
	child := stickyCommand("note", "Pinned")
	child.ParentRef = "frame"
	created, err := s.Mutate(
		ctx,
		p,
		scope,
		BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{frame, child}},
	)
	if err != nil {
		t.Fatal(err)
	}
	note, parent := created.IDs["note"], created.IDs["frame"]
	if _, err = s.Mutate(ctx, p, scope, BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{
		{
			Type:             "content",
			ID:               &note,
			ExpectedRevision: 1,
			Properties:       &BoardProperties{Color: "#fef08a", Locked: true},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	pinned := BoardGeometry{X: 10, Y: 20, Width: 220, Height: 150, ParentFrameID: &parent}
	moved := pinned
	moved.X = 400
	if _, err = s.Mutate(ctx, p, scope, BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{
		{Type: "geometry", ID: &note, ExpectedRevision: 1, Geometry: &moved},
	}}); !errors.Is(err, ErrBoardLocked) {
		t.Fatalf("moving a locked object = %v, want ErrBoardLocked", err)
	}
	if _, err = s.Mutate(ctx, p, scope, BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{
		{Type: "delete_item", ID: &parent, ExpectedRevision: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	// Undo: restore the frame and reattach the locked child where it is.
	restored, err := s.Mutate(ctx, p, scope, BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{
		{Type: "restore_item", ID: &parent, ExpectedRevision: 2, ExpectedGeometryRevision: 2},
		{Type: "geometry", ID: &note, ExpectedRevision: 2, Geometry: &pinned},
	}})
	if err != nil {
		t.Fatalf("undo frame deletion with a locked child = %v", err)
	}
	for _, item := range restored.Items {
		if item.ID == note && (item.ParentFrameID == nil || *item.ParentFrameID != parent || item.X != 10) {
			t.Fatalf("locked child after undo = %+v", item)
		}
	}
}

// Hydration answers what it can: an ID that no longer names a live item is
// reported, not a reason to fail every other item of the batch.
func TestBoardHydrateReportsWhatItCannotReturn(t *testing.T) {
	t.Parallel()
	s, _, p, _ := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, s, p)
	scope := BoardScope{BoardID: board.ID}
	created, err := s.Mutate(ctx, p, scope, BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{
		stickyCommand("a", "kept"), stickyCommand("b", "gone"),
		{Type: "create_edge", DraftID: "edge", SourceRef: "a", TargetRef: "b"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	a, b, edge := created.IDs["a"], created.IDs["b"], created.IDs["edge"]
	if _, err = s.Mutate(ctx, p, scope, BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{
		{Type: "delete_item", ID: &b, ExpectedRevision: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	unknown := uuid.New()
	hydrated, err := s.Hydrate(ctx, p, scope, BoardHydrateInput{ItemIDs: []uuid.UUID{a, b, edge, unknown}})
	if err != nil {
		t.Fatalf("hydrate with absent IDs = %v", err)
	}
	if len(hydrated.Items) != 1 || hydrated.Items[0].ID != a || hydrated.Items[0].Text != "kept" {
		t.Fatalf("hydrated items = %+v", hydrated.Items)
	}
	slices.SortFunc(hydrated.Missing, func(x, y uuid.UUID) int { return strings.Compare(x.String(), y.String()) })
	want := []uuid.UUID{b, edge, unknown}
	slices.SortFunc(want, func(x, y uuid.UUID) int { return strings.Compare(x.String(), y.String()) })
	if !slices.Equal(hydrated.Missing, want) {
		t.Fatalf("missing = %v, want %v", hydrated.Missing, want)
	}
}

// A command naming an object that no longer exists says so; it is not the
// Board becoming unavailable.
func TestBoardCommandsNameMissingObjects(t *testing.T) {
	t.Parallel()
	s, _, p, _ := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, s, p)
	scope := BoardScope{BoardID: board.ID}
	created, err := s.Mutate(ctx, p, scope, BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{
		stickyCommand("a", "kept"), stickyCommand("b", "gone"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	a, b := created.IDs["a"], created.IDs["b"]
	if _, err = s.Mutate(ctx, p, scope, BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{
		{Type: "delete_item", ID: &b, ExpectedRevision: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	unknown := uuid.New()
	geometry := &BoardGeometry{Width: 100, Height: 100}
	for name, command := range map[string]BoardCommand{
		"unknown item": {Type: "geometry", ID: &unknown, ExpectedRevision: 1, Geometry: geometry},
		"deleted item": {Type: "geometry", ID: &b, ExpectedRevision: 2, Geometry: geometry},
		"edge to a deleted item": {
			Type: "create_edge", DraftID: "edge", SourceRef: a.String(), TargetRef: b.String(),
		},
		"unknown edge": {Type: "update_edge", ID: &unknown, ExpectedRevision: 1, Label: "x"},
	} {
		if _, err := s.Mutate(
			ctx,
			p,
			scope,
			BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{command}},
		); !errors.Is(
			err,
			ErrBoardItemNotFound,
		) {
			t.Errorf("%s = %v, want ErrBoardItemNotFound", name, err)
		}
	}
	if _, err := s.Mutate(ctx, p, BoardScope{BoardID: uuid.New()}, BoardBatch{
		RequestKey: uuid.New(), Commands: []BoardCommand{stickyCommand("c", "x")},
	}); !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("unknown Board = %v, want ErrBoardNotFound", err)
	}
}

// A request key names one request. Reusing it for a different request is a
// client error, not a collaborator's change.
func TestBoardRequestKeyReuseIsRefused(t *testing.T) {
	t.Parallel()
	s, _, p, _ := boardFixture(t)
	ctx := t.Context()
	key := uuid.New()
	board := createTestBoard(t, s, p)
	scope := BoardScope{BoardID: board.ID}
	created, err := s.Mutate(
		ctx,
		p,
		scope,
		BoardBatch{RequestKey: key, Commands: []BoardCommand{stickyCommand("a", "one")}},
	)
	if err != nil || created.Replayed {
		t.Fatalf("first batch = %+v, %v", created, err)
	}
	if _, err = s.Mutate(
		ctx,
		p,
		scope,
		BoardBatch{RequestKey: key, Commands: []BoardCommand{stickyCommand("a", "two")}},
	); !errors.Is(
		err,
		ErrBoardRequestKeyReused,
	) {
		t.Fatalf("batch with a reused key = %v", err)
	}
	note := created.IDs["a"]
	update := func(text string) []byte {
		hydrated, err := s.Hydrate(ctx, p, scope, BoardHydrateInput{ItemIDs: []uuid.UUID{note}})
		if err != nil {
			t.Fatal(err)
		}
		doc := crdt.New()
		defer doc.Destroy()
		if err = doc.ApplyUpdate(hydrated.Items[0].State); err != nil {
			t.Fatal(err)
		}
		body := doc.GetXmlFragment("content").Children()[0].(*crdt.YXmlElement).Children()[0].(*crdt.YXmlText)
		doc.Transact(func(tx *crdt.Transaction) { body.Insert(tx, 0, text, nil) })
		return doc.EncodeStateAsUpdate()
	}
	textKey := uuid.New()
	if _, err = s.Text(
		ctx,
		p,
		scope,
		BoardTextInput{RequestKey: textKey, ItemID: note, LifecycleRevision: 1, Update: update("A ")},
	); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Text(
		ctx,
		p,
		scope,
		BoardTextInput{RequestKey: textKey, ItemID: note, LifecycleRevision: 1, Update: update("B ")},
	); !errors.Is(
		err,
		ErrBoardRequestKeyReused,
	) {
		t.Fatalf("text with a reused key = %v", err)
	}
}

// Note size limits are limits, not malformed requests.
func TestBoardNoteSizeLimitsReportTheLimit(t *testing.T) {
	t.Parallel()
	s, _, p, _ := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, s, p)
	scope := BoardScope{BoardID: board.ID}
	long := strings.Repeat("x", boardMaxNoteText+1)
	if _, err := s.Mutate(
		ctx,
		p,
		scope,
		BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{stickyCommand("a", long)}},
	); !errors.Is(
		err,
		ErrBoardLimit,
	) {
		t.Fatalf("oversized sticky = %v, want ErrBoardLimit", err)
	}
	created, err := s.Mutate(
		ctx,
		p,
		scope,
		BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{stickyCommand("a", "short")}},
	)
	if err != nil {
		t.Fatal(err)
	}
	note := created.IDs["a"]
	if _, err = s.Mutate(ctx, p, scope, BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{
		{Type: "text", ID: &note, ExpectedRevision: 1, ExpectedTextRevision: 1, Text: long},
	}}); !errors.Is(err, ErrBoardLimit) {
		t.Fatalf("oversized text command = %v, want ErrBoardLimit", err)
	}
	hydrated, err := s.Hydrate(ctx, p, scope, BoardHydrateInput{ItemIDs: []uuid.UUID{note}})
	if err != nil {
		t.Fatal(err)
	}
	doc := crdt.New()
	defer doc.Destroy()
	if err = doc.ApplyUpdate(hydrated.Items[0].State); err != nil {
		t.Fatal(err)
	}
	body := doc.GetXmlFragment("content").Children()[0].(*crdt.YXmlElement).Children()[0].(*crdt.YXmlText)
	doc.Transact(func(tx *crdt.Transaction) { body.Insert(tx, 0, strings.Repeat("y", boardMaxNoteText), nil) })
	if _, err = s.Text(ctx, p, scope, BoardTextInput{
		RequestKey: uuid.New(), ItemID: note, LifecycleRevision: 1, Update: doc.EncodeStateAsUpdate(),
	}); !errors.Is(err, ErrBoardLimit) {
		t.Fatalf("text past the note limit = %v, want ErrBoardLimit", err)
	}
	if _, err = s.Text(ctx, p, scope, BoardTextInput{
		RequestKey: uuid.New(), ItemID: note, LifecycleRevision: 1, Update: make([]byte, boardMaxTextUpdate+1),
	}); !errors.Is(err, ErrBoardLimit) {
		t.Fatalf("oversized text update = %v, want ErrBoardLimit", err)
	}
}

// The change feed covers recent minutes, not a fixed record count, so a busy
// note does not push a reconnecting client to a full snapshot; and a delta is
// bounded by the objects it names, not the commits that touched them.
func TestBoardChangeFeedKeepsRecentCommits(t *testing.T) {
	t.Parallel()
	s, pool, p, _ := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, s, p)
	scope := BoardScope{BoardID: board.ID}
	created, err := s.Mutate(
		ctx,
		p,
		scope,
		BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{stickyCommand("a", "busy")}},
	)
	if err != nil {
		t.Fatal(err)
	}
	note := created.IDs["a"]
	revision := int64(1)
	for i := range 1100 {
		moved, err := s.Mutate(ctx, p, scope, BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{{
			Type: "geometry", ID: &note, ExpectedRevision: revision,
			Geometry: &BoardGeometry{X: float64(i), Y: 0, Width: 220, Height: 150},
		}}})
		if err != nil {
			t.Fatal(err)
		}
		revision = moved.Items[0].GeometryRevision
	}
	delta, err := s.Snapshot(ctx, p, scope, created.Sequence)
	if err != nil || delta.Full || len(delta.Items) != 1 || delta.Items[0].X != 1099 {
		t.Fatalf("delta across 1,100 commits = full %t, %d items, %v", delta.Full, len(delta.Items), err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE board_changes SET created_at=now()-interval '11 minutes' WHERE board_id=$1`,
		board.ID,
	)
	if _, err = s.Mutate(
		ctx,
		p,
		scope,
		BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{stickyCommand("b", "next")}},
	); err != nil {
		t.Fatal(err)
	}
	var kept int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM board_changes WHERE board_id=$1`, board.ID).
		Scan(&kept); err != nil ||
		kept > boardChangeFeed+1 {
		t.Fatalf("feed after the window = %d rows, %v; want old commits compacted", kept, err)
	}
}

// Reads run in their own snapshot and never wait for a writer holding the
// Board lock.
func TestBoardReadsDoNotWaitForWriters(t *testing.T) {
	t.Parallel()
	s, pool, p, _ := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, s, p)
	scope := BoardScope{BoardID: board.ID}
	created, err := s.Mutate(
		ctx,
		p,
		scope,
		BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{stickyCommand("a", "read me")}},
	)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(ctx) }()
	if _, err = writer.Exec(ctx, `SELECT 1 FROM boards WHERE id=$1 FOR UPDATE`, board.ID); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if snapshot, err := s.Snapshot(bounded, p, scope, 0); err != nil || snapshot.Sequence != created.Sequence {
		t.Fatalf("snapshot under a writer = %+v, %v", snapshot, err)
	}
	if _, err := s.Hydrate(
		bounded,
		p,
		scope,
		BoardHydrateInput{ItemIDs: []uuid.UUID{created.IDs["a"]}},
	); err != nil {
		t.Fatalf("hydrate under a writer = %v", err)
	}
	if _, err := s.Context(bounded, p, scope, BoardContextInput{}); err != nil {
		t.Fatalf("context under a writer = %v", err)
	}
}

// A replayed request changed nothing, so it raises no change hint.
func TestBoardReplaysRaiseNoHint(t *testing.T) {
	t.Parallel()
	s, _, p, _ := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, s, p)
	var hints atomic.Int32
	if err := s.bus.Subscribe(TopicBoard, func(context.Context, events.Event) error {
		hints.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	batch := BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{stickyCommand("a", "once")}}
	if _, err := s.Mutate(ctx, p, BoardScope{BoardID: board.ID}, batch); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Mutate(ctx, p, BoardScope{BoardID: board.ID}, batch); err != nil {
		t.Fatal(err)
	}
	if got := hints.Load(); got != 1 {
		t.Fatalf("hints = %d, want one for the commit and none for its replay", got)
	}
}

// Notes full of markup escape to far more JSON than their raw size; the read
// trims to the real budget instead of failing.
func TestBoardContextFitsEscapedTextIntoItsBudget(t *testing.T) {
	t.Parallel()
	s, _, p, _ := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, s, p)
	markup := strings.Repeat(`<div className="card">{a && b ? "<b>x</b>" : "y"}</div>`+"\n", 220)[:12000]
	var commands []BoardCommand
	for i := range 4 {
		commands = append(commands, stickyCommand(fmt.Sprint("markup-", i), markup))
	}
	for i := range 20 {
		commands = append(commands, stickyCommand(fmt.Sprint("idea-", i), fmt.Sprint("Ý tưởng số ", i)))
	}
	if _, err := s.Mutate(
		ctx,
		p,
		BoardScope{BoardID: board.ID},
		BoardBatch{RequestKey: uuid.New(), Commands: commands},
	); err != nil {
		t.Fatal(err)
	}
	read, err := s.Context(
		ctx,
		p,
		BoardScope{BoardID: board.ID},
		BoardContextInput{MaxItems: 200, MaxBytes: 131072},
	)
	if err != nil {
		t.Fatalf("context with escaped notes = %v", err)
	}
	raw, err := json.Marshal(read)
	if err != nil || len(raw) > 131072 || len(read.Items) == 0 || !read.Truncated {
		t.Fatalf(
			"context = %d bytes, %d items, truncated %t, %v",
			len(raw),
			len(read.Items),
			read.Truncated,
			err,
		)
	}
}
