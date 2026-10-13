//go:build integration

package canvas

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reearth/ygo/crdt"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

// Receipts keep the acknowledgement (hash, draft mapping and sequence), not
// the changed objects; a retry answers with the objects' current state.
func TestBoardReceiptsKeepOnlyTheAcknowledgement(t *testing.T) {
	t.Parallel()
	s, pool, p, other := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, s, p, other)
	scope := BoardScope{BoardID: board.ID}
	create := BoardBatch{
		RequestKey: uuid.New(),
		Commands:   []BoardCommand{stickyCommand("a", "first"), stickyCommand("b", "second")},
	}
	created, err := s.Mutate(ctx, p, scope, create)
	if err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := pool.QueryRow(
		ctx,
		`SELECT ids FROM board_operations WHERE board_id=$1 AND request_key=$2`,
		board.ID,
		create.RequestKey,
	).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var ids map[string]uuid.UUID
	if err := json.Unmarshal(stored, &ids); err != nil || len(ids) != 2 || ids["a"] != created.IDs["a"] {
		t.Fatalf("stored ids = %s, %v", stored, err)
	}

	// A collaborator moves one note; retrying the creation reports the same
	// IDs and sequence. The moved note is left out: its current revision
	// carries the collaborator's change, which must not become an undo guard.
	a := created.Items[0]
	if a.ID != created.IDs["a"] {
		a = created.Items[1]
	}
	if created.Replayed {
		t.Fatalf("first commit is marked replayed: %+v", created)
	}
	moved := BoardGeometry{X: 500, Y: 40, Width: 220, Height: 150}
	move := BoardBatch{
		RequestKey: uuid.New(),
		Commands: []BoardCommand{{
			Type: "geometry", ID: &a.ID, ExpectedRevision: a.GeometryRevision, Geometry: &moved,
		}},
	}
	if _, err := s.Mutate(ctx, other, scope, move); err != nil {
		t.Fatal(err)
	}
	replay, err := s.Mutate(ctx, p, scope, create)
	if err != nil || !replay.Replayed || replay.Sequence != created.Sequence || replay.IDs["b"] != created.IDs["b"] ||
		replay.IDs["a"] != created.IDs["a"] || len(replay.Items) != 1 || replay.Items[0].ID != created.IDs["b"] {
		t.Fatalf("replay = %+v, %v; want only the untouched note", replay, err)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT ids FROM board_operations WHERE board_id=$1 AND request_key=$2`,
		board.ID,
		move.RequestKey,
	).Scan(&stored); err != nil || stored != nil {
		t.Fatalf("geometry receipt ids = %s, %v; want none", stored, err)
	}

	// Text receipts replay from the accepted note.
	hydrated, err := s.Hydrate(ctx, p, scope, BoardHydrateInput{ItemIDs: []uuid.UUID{a.ID}})
	if err != nil {
		t.Fatal(err)
	}
	doc := crdt.New()
	defer doc.Destroy()
	if err = doc.ApplyUpdate(hydrated.Items[0].State); err != nil {
		t.Fatal(err)
	}
	paragraph := doc.GetXmlFragment("content").Children()[0].(*crdt.YXmlElement)
	body := paragraph.Children()[0].(*crdt.YXmlText)
	doc.Transact(func(tx *crdt.Transaction) { body.Insert(tx, 0, "Edited ", nil) })
	text := BoardTextInput{
		RequestKey:        uuid.New(),
		ItemID:            a.ID,
		LifecycleRevision: a.LifecycleRevision,
		Update:            doc.EncodeStateAsUpdate(),
	}
	written, err := s.Text(ctx, p, scope, text)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Text(ctx, p, scope, text)
	if err != nil || again.ItemID != a.ID || again.TextRevision != written.TextRevision ||
		again.Text != written.Text {
		t.Fatalf("text replay = %+v, %v; want %+v", again, err, written)
	}

	// Past the retry horizon receipts leave; the Board's objects stay.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE board_operations SET created_at=now()-interval '31 days' WHERE board_id=$1`,
		board.ID,
	)
	sweep, err := s.Sweep(ctx, time.Now())
	if err != nil || sweep.Receipts != 3 {
		t.Fatalf("sweep = %+v, %v; want three operation receipts", sweep, err)
	}
	snapshot, err := s.Snapshot(ctx, p, scope, 0)
	if err != nil || len(snapshot.Items) != 2 {
		t.Fatalf("snapshot after sweep = %+v, %v", snapshot, err)
	}
}
