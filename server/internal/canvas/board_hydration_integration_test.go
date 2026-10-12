//go:build integration

package canvas

import (
	"testing"

	"github.com/google/uuid"
	"github.com/reearth/ygo/crdt"
)

func TestBoardHydrationSendsNoteDeltasAgainstClientState(t *testing.T) {
	t.Parallel()
	boards, _, owner, member := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, boards, owner, member)
	scope := BoardScope{BoardID: board.ID}
	created, err := boards.Mutate(ctx, member, scope, BoardBatch{
		RequestKey: uuid.New(),
		Commands:   []BoardCommand{stickyCommand("note", "Ý tưởng")},
	})
	if err != nil {
		t.Fatal(err)
	}
	note := created.Items[0]
	full, err := boards.Hydrate(ctx, owner, scope, BoardHydrateInput{ItemIDs: []uuid.UUID{note.ID}})
	if err != nil || full.Items[0].Delta {
		t.Fatalf("first hydration = %+v, %v; want full state", full, err)
	}
	held := full.Items[0].State
	vector, err := crdt.EncodeStateVectorFromUpdate(held)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = boards.Mutate(ctx, member, scope, BoardBatch{
		RequestKey: uuid.New(),
		Commands: []BoardCommand{{
			Type: "text", ID: &note.ID, ExpectedRevision: note.LifecycleRevision,
			ExpectedTextRevision: full.Items[0].TextRevision, Text: "Ý tưởng mới",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	delta, err := boards.Hydrate(ctx, owner, scope, BoardHydrateInput{
		ItemIDs:      []uuid.UUID{note.ID},
		StateVectors: map[string][]byte{note.ID.String(): vector},
	})
	if err != nil || !delta.Items[0].Delta || delta.Items[0].Text != "Ý tưởng mới" {
		t.Fatalf("delta hydration = %+v, %v", delta, err)
	}
	// The delta brings the held state to the committed text.
	if _, text, err := mergeBoardNote(note.ID, held, delta.Items[0].State); err != nil || text != "Ý tưởng mới" {
		t.Fatalf("merged text = %q, %v", text, err)
	}
	if _, err := boards.Hydrate(ctx, owner, scope, BoardHydrateInput{
		ItemIDs:      []uuid.UUID{note.ID},
		StateVectors: map[string][]byte{note.ID.String(): []byte("not a vector")},
	}); err == nil {
		t.Fatal("invalid state vector accepted")
	}
}
