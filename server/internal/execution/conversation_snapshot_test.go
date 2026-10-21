package execution

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// runContextChatGoldenPath is the non-empty snapshot shape the daemon parses in
// TestRunContextChatGoldenDecodesAuthorsAndTrigger. Database-generated ids
// make the produced document non-deterministic, so this one is hand-authored
// and the struct literals below must encode to exactly its bytes.
const runContextChatGoldenPath = "testdata/run_context_chat.golden.json"

func snapshotRow(seq int64, content string) db.ListChatItemsForContextSnapshotRow {
	return db.ListChatItemsForContextSnapshotRow{
		ID:          uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("item-%d", seq))),
		Seq:         seq,
		RunID:       uuid.NullUUID{UUID: uuid.NameSpaceOID, Valid: true},
		AuthorKind:  "user",
		AuthorID:    uuid.NullUUID{UUID: uuid.NameSpaceURL, Valid: true},
		DisplayName: "Member",
		Content:     content,
		CreatedAt: pgtype.Timestamptz{
			Time:  time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC).Add(time.Duration(seq) * time.Minute),
			Valid: true,
		},
	}
}

// newestFirstRows mirrors the query's order: seq high..1, one row per seq.
func newestFirstRows(count int, content string) []db.ListChatItemsForContextSnapshotRow {
	rows := make([]db.ListChatItemsForContextSnapshotRow, 0, count)
	for seq := int64(count); seq >= 1; seq-- {
		rows = append(rows, snapshotRow(seq, content))
	}
	return rows
}

func decodeSnapshot(t *testing.T, raw json.RawMessage) runConversationSnapshot {
	t.Helper()
	var snapshot runConversationSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatalf("decode snapshot %s: %v", raw, err)
	}
	return snapshot
}

func TestBoundConversationSnapshotKeepsEmptyHistoryExplicit(t *testing.T) {
	t.Parallel()

	got, err := boundConversationSnapshot("direct", "conv-1", 0, nil, nil)
	if err != nil {
		t.Fatalf("boundConversationSnapshot() error = %v", err)
	}
	want := `{"schemaVersion":1,"kind":"direct","conversationId":"conv-1","throughSeq":0,"truncated":false,"items":[]}`
	if string(got) != want {
		t.Fatalf("empty snapshot = %s\nwant %s", got, want)
	}
}

func TestBoundConversationSnapshotDropsWholeOldestItemsUntilBothLimitsHold(t *testing.T) {
	t.Parallel()

	t.Run("one row past the item budget drops the oldest", func(t *testing.T) {
		raw, err := boundConversationSnapshot("task", "conv-1", 65, newestFirstRows(65, "short"), nil)
		if err != nil {
			t.Fatalf("boundConversationSnapshot() error = %v", err)
		}
		snapshot := decodeSnapshot(t, raw)
		if len(snapshot.Items) != 64 || !snapshot.Truncated {
			t.Fatalf(
				"items = %d, truncated = %v; want 64 items and truncated",
				len(snapshot.Items),
				snapshot.Truncated,
			)
		}
		if snapshot.Items[0].Seq != 2 || snapshot.Items[63].Seq != 65 {
			t.Fatalf(
				"items span seq %d..%d, want 2..65 ascending",
				snapshot.Items[0].Seq,
				snapshot.Items[63].Seq,
			)
		}
		for i := 1; i < len(snapshot.Items); i++ {
			if snapshot.Items[i].Seq != snapshot.Items[i-1].Seq+1 {
				t.Fatalf("items are not ascending and contiguous at index %d", i)
			}
		}
	})

	t.Run("the byte budget drops the oldest whole items", func(t *testing.T) {
		raw, err := boundConversationSnapshot(
			"task",
			"conv-1",
			10,
			newestFirstRows(10, strings.Repeat("x", 10<<10)),
			nil,
		)
		if err != nil {
			t.Fatalf("boundConversationSnapshot() error = %v", err)
		}
		if len(raw) > conversationSnapshotMaxBytes {
			t.Fatalf(
				"encoded snapshot is %d bytes, want at most %d",
				len(raw),
				conversationSnapshotMaxBytes,
			)
		}
		snapshot := decodeSnapshot(t, raw)
		if !snapshot.Truncated || len(snapshot.Items) == 0 {
			t.Fatalf(
				"truncated = %v with %d items; want a truncated, non-empty window",
				snapshot.Truncated,
				len(snapshot.Items),
			)
		}
		if snapshot.Items[len(snapshot.Items)-1].Seq != 10 {
			t.Fatalf(
				"newest surviving seq = %d, want 10: the budget must drop the oldest, not the newest",
				snapshot.Items[len(snapshot.Items)-1].Seq,
			)
		}
		for i := 1; i < len(snapshot.Items); i++ {
			if snapshot.Items[i].Seq != snapshot.Items[i-1].Seq+1 {
				t.Fatalf("surviving items are not contiguous at index %d", i)
			}
		}
	})
}

func TestBoundConversationSnapshotOmitsAnIndividuallyOversizeItem(t *testing.T) {
	t.Parallel()

	oversize := strings.Repeat("y", 70<<10)
	rows := []db.ListChatItemsForContextSnapshotRow{
		snapshotRow(3, oversize), snapshotRow(2, "kept two"), snapshotRow(1, "kept one"),
	}
	raw, err := boundConversationSnapshot("task", "conv-1", 3, rows, nil)
	if err != nil {
		t.Fatalf("boundConversationSnapshot() error = %v", err)
	}
	snapshot := decodeSnapshot(t, raw)
	if len(snapshot.Items) != 2 || snapshot.Items[0].Seq != 1 || snapshot.Items[1].Seq != 2 {
		t.Fatalf("items = %+v, want seq 1 and 2 kept with the oversize item left out", snapshot.Items)
	}
	if !snapshot.Truncated {
		t.Fatal("truncated = false, want true: an omitted item is omitted history")
	}
	if strings.Contains(string(raw), oversize[:512]) {
		t.Fatal(
			"a prefix of the oversize item leaked into the snapshot; it must be left out whole, never sliced",
		)
	}
}

func TestBoundConversationSnapshotMarksTruncatedOnlyWhenChatWasOmitted(t *testing.T) {
	t.Parallel()

	raw, err := boundConversationSnapshot("task", "conv-1", 64, newestFirstRows(64, "short"), nil)
	if err != nil {
		t.Fatalf("boundConversationSnapshot() error = %v", err)
	}
	snapshot := decodeSnapshot(t, raw)
	if len(snapshot.Items) != 64 || snapshot.Truncated {
		t.Fatalf(
			"items = %d, truncated = %v; want every item and no truncation",
			len(snapshot.Items),
			snapshot.Truncated,
		)
	}
}

func TestBoundConversationSnapshotUsesTheStreamWatermarkNotTheNewestChatSeq(t *testing.T) {
	t.Parallel()

	raw, err := boundConversationSnapshot("task", "conv-1", 12, newestFirstRows(10, "short"), nil)
	if err != nil {
		t.Fatalf("boundConversationSnapshot() error = %v", err)
	}
	snapshot := decodeSnapshot(t, raw)
	if snapshot.ThroughSeq != 12 {
		t.Fatalf(
			"throughSeq = %d, want 12: tool and reasoning rows count toward the watermark",
			snapshot.ThroughSeq,
		)
	}
	if snapshot.Items[len(snapshot.Items)-1].Seq != 10 {
		t.Fatalf("newest chat seq = %d, want 10", snapshot.Items[len(snapshot.Items)-1].Seq)
	}
}

func queuedRow(position int32, content string) db.ListQueuedRunQueueItemsForTaskRunRow {
	return db.ListQueuedRunQueueItemsForTaskRunRow{
		ConversationRunQueueItem: db.ConversationRunQueueItem{
			ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("queue-%d", position))),
			MessageID: uuid.NewSHA1(
				uuid.NameSpaceOID,
				[]byte(fmt.Sprintf("reserved-%d", position)),
			),
			InitiatedByUserID: uuid.NameSpaceDNS,
			Content:           content,
			Position:          position,
			CreatedAt: pgtype.Timestamptz{
				Time: time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC).
					Add(time.Duration(position) * time.Minute),
				Valid: true,
			},
		},
		DisplayName: "Colleague",
	}
}

// A Task Run freezes the colleagues' queued Messages after the transcript
// (ADR 0014): in queue order, at the seqs the caller's materialization will
// allocate past the watermark, under their reserved message ids, with no
// runId because the Run they belong to has no id yet. The watermark moves
// past them so the daemon's seq validation admits them.
func TestBoundConversationSnapshotAppendsQueuedTaskMessagesPastTheWatermark(t *testing.T) {
	t.Parallel()

	queued := []db.ListQueuedRunQueueItemsForTaskRunRow{queuedRow(1, "first queued"), queuedRow(2, "second queued")}
	raw, err := boundConversationSnapshot("task", "conv-1", 12, newestFirstRows(10, "short"), queued)
	if err != nil {
		t.Fatalf("boundConversationSnapshot() error = %v", err)
	}
	snapshot := decodeSnapshot(t, raw)
	if snapshot.ThroughSeq != 14 || len(snapshot.Items) != 12 || snapshot.Truncated {
		t.Fatalf("throughSeq = %d, items = %d, truncated = %v; want 14, 12 items, no truncation",
			snapshot.ThroughSeq, len(snapshot.Items), snapshot.Truncated)
	}
	first, second := snapshot.Items[10], snapshot.Items[11]
	if first.Seq != 13 || second.Seq != 14 || first.Content != "first queued" || second.Content != "second queued" {
		t.Fatalf("queued items = %+v / %+v, want seqs 13 and 14 in queue order", first, second)
	}
	if first.ID != queued[0].ConversationRunQueueItem.MessageID.String() || first.RunID != "" ||
		first.Author.Kind != "user" || first.Author.Name != "Colleague" ||
		first.Author.ID != uuid.NameSpaceDNS.String() {
		t.Fatalf(
			"queued item = %+v, want the reserved message id, an empty runId and the colleague as author",
			first,
		)
	}

	// The budgets still drop the OLDEST items first, so queued Messages
	// outlive the transcript they follow.
	raw, err = boundConversationSnapshot("task", "conv-1", 64, newestFirstRows(64, "short"), queued)
	if err != nil {
		t.Fatalf("boundConversationSnapshot(full) error = %v", err)
	}
	snapshot = decodeSnapshot(t, raw)
	if len(snapshot.Items) != 64 || !snapshot.Truncated || snapshot.Items[63].Seq != 66 ||
		snapshot.Items[0].Seq != 3 {
		t.Fatalf(
			"full window = %d items, truncated = %v, first seq %d, last seq %d; want 64 items 3..66",
			len(
				snapshot.Items,
			),
			snapshot.Truncated,
			snapshot.Items[0].Seq,
			snapshot.Items[len(snapshot.Items)-1].Seq,
		)
	}
}

// The chat golden is what internal/daemon decodes; these literals are the
// producer's types, so a tag or field-order change on either side fails here
// before it silently changes what the agent reads.
func TestConversationSnapshotAndTriggerEncodeTheGoldenTheDaemonParses(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Clean(runContextChatGoldenPath))
	if err != nil {
		t.Fatalf("read the chat golden: %v", err)
	}
	var golden map[string]json.RawMessage
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("decode the chat golden: %v", err)
	}
	at := func(hour, minute, second int) time.Time {
		return time.Date(2026, 9, 18, hour, minute, second, 0, time.UTC)
	}
	alice := runContextActor{ID: "0197f5b0-4000-7000-8000-000000000031", Kind: "user", Name: "Alice Nguyen"}
	ada := runContextActor{ID: "0197f5b0-4000-7000-8000-000000000032", Kind: "agent", Name: "Ada"}
	bob := runContextActor{ID: "0197f5b0-4000-7000-8000-000000000033", Kind: "user", Name: "Bob Tran"}
	snapshot, err := json.Marshal(runConversationSnapshot{
		SchemaVersion: 1, Kind: "task", ConversationID: "0197f5b0-4000-7000-8000-000000000010",
		ThroughSeq: 12, Truncated: true,
		Items: []runConversationSnapshotItem{
			{
				ID:        "0197f5b0-4000-7000-8000-000000000011",
				Seq:       10,
				RunID:     "0197f5b0-4000-7000-8000-000000000021",
				Author:    alice,
				CreatedAt: at(9, 10, 0),
				Content:   "Can we ship the customer import by Friday?",
			},
			{
				ID:        "0197f5b0-4000-7000-8000-000000000012",
				Seq:       11,
				RunID:     "0197f5b0-4000-7000-8000-000000000021",
				Author:    ada,
				CreatedAt: at(9, 11, 0),
				Content:   "I can, if the file format is fixed.\nWhich fields are required?",
			},
			{
				ID:        "0197f5b0-4000-7000-8000-000000000013",
				Seq:       12,
				RunID:     "0197f5b0-4000-7000-8000-000000000022",
				Author:    bob,
				CreatedAt: at(9, 12, 0),
				Content:   "Email and name. Phone is optional.",
			},
		},
	})
	if err != nil {
		t.Fatalf("encode snapshot literal: %v", err)
	}
	if string(snapshot) != string(golden["conversationSnapshot"]) {
		t.Fatalf(
			"snapshot drifted from the golden the daemon reads.\n got: %s\nwant: %s",
			snapshot,
			golden["conversationSnapshot"],
		)
	}
	trigger, err := json.Marshal(runTriggerBrief{Author: bob, CreatedAt: at(9, 12, 41)})
	if err != nil {
		t.Fatalf("encode trigger literal: %v", err)
	}
	if string(trigger) != string(golden["trigger"]) {
		t.Fatalf(
			"trigger drifted from the golden the daemon reads.\n got: %s\nwant: %s",
			trigger,
			golden["trigger"],
		)
	}
}

func TestEncodeRunContextRejectsMoreThanOneMiB(t *testing.T) {
	t.Parallel()

	oversize, err := json.Marshal(strings.Repeat("z", 1<<20))
	if err != nil {
		t.Fatalf("encode filler: %v", err)
	}
	if _, err := encodeRunContext(
		map[string]json.RawMessage{"prompt": oversize},
	); !errors.Is(
		err,
		ErrRunContextTooLarge,
	) {
		t.Fatalf("encodeRunContext(1 MiB + envelope) error = %v, want ErrRunContextTooLarge", err)
	}
	small, err := encodeRunContext(map[string]json.RawMessage{"prompt": []byte(`"hello"`)})
	if err != nil || string(small) != `{"prompt":"hello"}` {
		t.Fatalf("encodeRunContext(small) = %s, %v", small, err)
	}
}
