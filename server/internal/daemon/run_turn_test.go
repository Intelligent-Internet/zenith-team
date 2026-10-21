package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/runfailure"
)

// The goldens under testdata/run_turn are the specification of what the
// provider reads; there is no update flag on purpose. Trailing newlines are
// compared loosely because editors add and strip them.
func readTurnGolden(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "run_turn", name+".golden.txt"))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	return strings.TrimRight(string(raw), "\n")
}

func assertTurnGolden(t *testing.T, name, got string) {
	t.Helper()
	if want := readTurnGolden(t, name); strings.TrimRight(got, "\n") != want {
		t.Fatalf("%s drifted from its golden.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

const (
	turnConversationID = "0197f5b0-4000-7000-8000-000000000010"
	turnAgentID        = "0197f5b0-4000-7000-8000-000000000032"
	turnPrompt         = "Please start with the CSV parser."
)

func turnRun() Run {
	return Run{ConversationID: turnConversationID, AgentID: turnAgentID}
}

var (
	turnAlice = RunConversationAuthor{
		ID:   "0197f5b0-4000-7000-8000-000000000031",
		Kind: protocol.ActorKindUser,
		Name: "Alice Nguyen",
	}
	turnAda = RunConversationAuthor{ID: turnAgentID, Kind: protocol.ActorKindAgent, Name: "Ada"}
	turnBob = RunConversationAuthor{
		ID:   "0197f5b0-4000-7000-8000-000000000033",
		Kind: protocol.ActorKindUser,
		Name: "Bob Tran",
	}
)

func turnAt(minute int) time.Time {
	return time.Date(2026, 9, 18, 9, minute, 0, 0, time.UTC)
}

// turnSnapshot is the three-message Task chat every rendering test starts
// from: two members and the Run's own agent, watermark 12, nothing dropped.
func turnSnapshot() *RunConversationSnapshot {
	return &RunConversationSnapshot{
		SchemaVersion: 1, Kind: "task", ConversationID: turnConversationID, ThroughSeq: 12,
		Items: []RunConversationItem{
			{
				ID:        "0197f5b0-4000-7000-8000-000000000011",
				Seq:       10,
				RunID:     "0197f5b0-4000-7000-8000-000000000021",
				Author:    turnAlice,
				CreatedAt: turnAt(10),
				Content:   "Can we ship the customer import by Friday?",
			},
			{
				ID:        "0197f5b0-4000-7000-8000-000000000012",
				Seq:       11,
				RunID:     "0197f5b0-4000-7000-8000-000000000021",
				Author:    turnAda,
				CreatedAt: turnAt(11),
				Content:   "I can, if the file format is fixed.\nWhich fields are required?",
			},
			{
				ID:        "0197f5b0-4000-7000-8000-000000000013",
				Seq:       12,
				RunID:     "0197f5b0-4000-7000-8000-000000000022",
				Author:    turnBob,
				CreatedAt: turnAt(12),
				Content:   "Email and name. Phone is optional.",
			},
		},
	}
}

func turnDoc() RunContext {
	return RunContext{
		Prompt:           turnPrompt,
		Trigger:          &RunTriggerContext{Author: turnBob, CreatedAt: turnAt(13)},
		ExecutionProfile: RunExecutionProfileContext{Task: &RunTaskContext{ConversationID: turnConversationID}},
	}
}

func TestRenderTurnInputFreshSessionInlinesBoundedHistoryThenTriggerOnce(t *testing.T) {
	t.Parallel()

	turn := renderTurnInput(turnPrompt, turnRun(), turnDoc(), turnSnapshot(), turnContinuity{})
	assertTurnGolden(t, "fresh", turn.prompt)
	if turn.continuityNotice != "" {
		t.Fatalf("continuityNotice = %q, want none on a fresh session", turn.continuityNotice)
	}
	if strings.Count(turn.prompt, turnPrompt) != 1 || !strings.HasSuffix(turn.prompt, turnPrompt) {
		t.Fatalf("the trigger must appear exactly once and last:\n%s", turn.prompt)
	}
}

func TestRenderTurnInputFreshSessionNamesTruncationAndTheExactReadCommand(t *testing.T) {
	t.Parallel()

	snapshot := turnSnapshot()
	snapshot.Truncated = true
	turn := renderTurnInput(turnPrompt, turnRun(), turnDoc(), snapshot, turnContinuity{})
	assertTurnGolden(t, "fresh_truncated", turn.prompt)
	if !strings.Contains(turn.prompt, "--through-seq 9 --limit 100 --output json") {
		t.Fatalf("prompt does not name the exact scroll-back read:\n%s", turn.prompt)
	}
}

func TestRenderTurnInputFreshSessionWithEmptyHistoryIsExplicit(t *testing.T) {
	t.Parallel()

	snapshot := &RunConversationSnapshot{SchemaVersion: 1, Kind: "task", ConversationID: turnConversationID}
	turn := renderTurnInput(turnPrompt, turnRun(), turnDoc(), snapshot, turnContinuity{})
	assertTurnGolden(t, "fresh_empty", turn.prompt)
}

func TestRenderTurnInputResumeWithEmptyDeltaSendsOnlyTheNewMessage(t *testing.T) {
	t.Parallel()

	turn := renderTurnInput(turnPrompt, turnRun(), turnDoc(), turnSnapshot(), turnContinuity{
		resumeExpected: true, priorSessionThroughSeq: 12,
	})
	assertTurnGolden(t, "resume_prompt_no_delta", turn.prompt)
	if strings.Contains(turn.prompt, "[seq ") {
		t.Fatalf("a working resume must not replay history as new turns:\n%s", turn.prompt)
	}
	if !strings.HasPrefix(turn.continuityNotice, conversationContinuityNotice+"\n\n") ||
		!strings.HasSuffix(turn.continuityNotice, "\n\n") {
		t.Fatalf(
			"notice must start with the loss statement and end with a blank line:\n%q",
			turn.continuityNotice,
		)
	}
	for _, seq := range []string{"[seq 10]", "[seq 11]", "[seq 12]"} {
		if !strings.Contains(turn.continuityNotice, seq) {
			t.Fatalf("notice lacks %s, which the lost session had seen:\n%s", seq, turn.continuityNotice)
		}
	}
}

func TestRenderTurnInputResumeListsOnlyMessagesAfterTheWatermark(t *testing.T) {
	t.Parallel()

	turn := renderTurnInput(turnPrompt, turnRun(), turnDoc(), turnSnapshot(), turnContinuity{
		resumeExpected: true, priorSessionThroughSeq: 11,
	})
	assertTurnGolden(t, "resume_prompt_delta", turn.prompt)
	assertTurnGolden(t, "resume_notice", turn.continuityNotice)
	if strings.Contains(turn.prompt, "[seq 11]") || strings.Contains(turn.continuityNotice, "[seq 12]") {
		t.Fatal("an item appeared on the wrong side of the watermark")
	}
}

func TestRenderTurnInputResumeWithUnknownWatermarkRendersFullHistory(t *testing.T) {
	t.Parallel()

	turn := renderTurnInput(turnPrompt, turnRun(), turnDoc(), turnSnapshot(), turnContinuity{resumeExpected: true})
	assertTurnGolden(t, "fresh", turn.prompt)
	if turn.continuityNotice != conversationContinuityNotice+"\n\n" {
		t.Fatalf("notice = %q, want the bare loss statement", turn.continuityNotice)
	}
	for _, seq := range []string{"[seq 10]", "[seq 11]", "[seq 12]"} {
		if strings.Count(turn.continuityNotice+turn.prompt, seq) != 1 {
			t.Fatalf("%s appears more than once across notice and prompt", seq)
		}
	}
}

// The invariant that lets Codex assemble the fallback at thread/resume time
// and lets Claude's fresh retry render the same text: the lost-continuity
// prompt is exactly the resumed notice followed by the resumed prompt.
func TestRenderTurnInputContinuityLostEqualsNoticePlusResumedPrompt(t *testing.T) {
	t.Parallel()

	for _, watermark := range []int64{0, 11, 12} {
		t.Run(fmt.Sprintf("watermark %d", watermark), func(t *testing.T) {
			t.Parallel()
			resumed := renderTurnInput(turnPrompt, turnRun(), turnDoc(), turnSnapshot(), turnContinuity{
				resumeExpected: true, priorSessionThroughSeq: watermark,
			})
			lost := renderTurnInput(turnPrompt, turnRun(), turnDoc(), turnSnapshot(), turnContinuity{
				continuityLost: true, priorSessionThroughSeq: watermark,
			})
			if lost.continuityNotice != "" {
				t.Fatalf("lost continuity still carries a notice to prepend: %q", lost.continuityNotice)
			}
			if lost.prompt != resumed.continuityNotice+resumed.prompt {
				t.Fatalf(
					"lost prompt is not notice + resumed prompt.\n--- lost ---\n%s\n--- notice+prompt ---\n%s",
					lost.prompt,
					resumed.continuityNotice+resumed.prompt,
				)
			}
			for _, seq := range []string{"[seq 10]", "[seq 11]", "[seq 12]"} {
				if strings.Count(lost.prompt, seq) != 1 {
					t.Fatalf(
						"%s appears %d times in the fallback prompt, want once",
						seq,
						strings.Count(lost.prompt, seq),
					)
				}
			}
		})
	}
	lost := renderTurnInput(turnPrompt, turnRun(), turnDoc(), turnSnapshot(), turnContinuity{
		continuityLost: true, priorSessionThroughSeq: 11,
	})
	assertTurnGolden(t, "fallback", lost.prompt)
	unknown := renderTurnInput(
		turnPrompt,
		turnRun(),
		turnDoc(),
		turnSnapshot(),
		turnContinuity{continuityLost: true},
	)
	assertTurnGolden(t, "fallback_unknown_watermark", unknown.prompt)
	fresh := turnContinuity{resumeExpected: true, priorSessionThroughSeq: 11}.afterRefusedResume()
	if fresh.resumeExpected || !fresh.continuityLost || fresh.priorSessionThroughSeq != 11 {
		t.Fatalf("afterRefusedResume() = %+v, want the loss certain and the watermark kept", fresh)
	}
}

func TestRenderTurnInputResumeDeltaCutByTruncationNamesTheAfterSeqRead(t *testing.T) {
	t.Parallel()

	snapshot := turnSnapshot()
	snapshot.Truncated = true
	snapshot.Items = snapshot.Items[2:]
	turn := renderTurnInput(turnPrompt, turnRun(), turnDoc(), snapshot, turnContinuity{
		resumeExpected: true, priorSessionThroughSeq: 10,
	})
	if !strings.Contains(turn.prompt, "more than are shown here") ||
		!strings.Contains(turn.prompt, "--after-seq 10 --limit 100 --output json") ||
		!strings.Contains(turn.prompt, "[seq 12] Bob Tran (member)") {
		t.Fatalf("cut delta must say so and name the forward read:\n%s", turn.prompt)
	}
	if !strings.Contains(turn.continuityNotice, "--through-seq 10 --limit 100") {
		t.Fatalf("notice must point at history older than the watermark:\n%s", turn.continuityNotice)
	}
}

func TestRenderTurnInputLabelsAuthorsByKindAndSelf(t *testing.T) {
	t.Parallel()

	snapshot := turnSnapshot()
	other := RunConversationAuthor{
		ID:   "0197f5b0-4000-7000-8000-000000000099",
		Kind: protocol.ActorKindAgent,
		Name: "Eli",
	}
	snapshot.Items = append(snapshot.Items,
		RunConversationItem{ID: "x1", Seq: 13, Author: other, Content: "  \n"},
		RunConversationItem{ID: "x2", Seq: 14, Author: turnAlice, Content: "line one\r\nline two\n"},
	)
	snapshot.ThroughSeq = 14
	turn := renderTurnInput(turnPrompt, turnRun(), turnDoc(), snapshot, turnContinuity{})
	for _, want := range []string{
		"[seq 10] Alice Nguyen (member): Can we ship",
		"[seq 11] Ada (you):\n> I can, if the file format is fixed.\n> Which fields are required?",
		"[seq 13] Eli (agent): (no text)",
		"[seq 14] Alice Nguyen (member):\n> line one\n> line two\n",
	} {
		if !strings.Contains(turn.prompt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, turn.prompt)
		}
	}
}

func TestRenderTurnInputLeavesNonChatTriggersUnchanged(t *testing.T) {
	t.Parallel()

	agentTrigger := turnDoc()
	agentTrigger.Trigger = &RunTriggerContext{Author: turnAda}
	noTrigger := turnDoc()
	noTrigger.Trigger = nil
	assignment := RunContext{
		ExecutionProfile: RunExecutionProfileContext{Task: &RunTaskContext{Title: "Customer import"}},
	}
	for _, tc := range []struct {
		name     string
		doc      RunContext
		snapshot *RunConversationSnapshot
	}{
		{"no snapshot", turnDoc(), nil},
		{"no trigger", noTrigger, turnSnapshot()},
		{"agent-authored trigger", agentTrigger, turnSnapshot()},
		{"assignment prompt", assignment, turnSnapshot()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plain := renderTurnInput("the prompt", turnRun(), tc.doc, tc.snapshot, turnContinuity{})
			if plain.prompt != "the prompt" || plain.continuityNotice != "" {
				t.Fatalf("fresh = %+v, want the prompt untouched", plain)
			}
			resumed := renderTurnInput(
				"the prompt",
				turnRun(),
				tc.doc,
				tc.snapshot,
				turnContinuity{resumeExpected: true, priorSessionThroughSeq: 11},
			)
			if resumed.prompt != "the prompt" ||
				resumed.continuityNotice != conversationContinuityNotice+"\n\n" {
				t.Fatalf("resumed = %+v, want the prompt untouched and the bare notice", resumed)
			}
			lost := renderTurnInput(
				"the prompt",
				turnRun(),
				tc.doc,
				tc.snapshot,
				turnContinuity{continuityLost: true, priorSessionThroughSeq: 11},
			)
			if lost.prompt != conversationContinuityNotice+"\n\n"+"the prompt" ||
				lost.continuityNotice != "" {
				t.Fatalf("lost = %+v, want the notice before the prompt", lost)
			}
		})
	}
}

func TestTurnContinuityForReadsTheClaimedRun(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		run  Run
		want turnContinuity
	}{
		{"no prior session", Run{}, turnContinuity{}},
		{"resumable", Run{PriorSessionID: "s", PriorSessionThroughSeq: 7}, turnContinuity{resumeExpected: true, priorSessionThroughSeq: 7}},
		{"withheld or dropped", Run{PriorSessionThroughSeq: 7}, turnContinuity{continuityLost: true, priorSessionThroughSeq: 7}},
		{"fresh on purpose", Run{PriorSessionThroughSeq: 7, ForceFreshSession: true}, turnContinuity{priorSessionThroughSeq: 7}},
	} {
		if got := turnContinuityFor(tc.run); got != tc.want {
			t.Errorf("%s: turnContinuityFor() = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func snapshotDocument(t *testing.T, mutate func(snapshot map[string]any)) RunContext {
	t.Helper()
	raw, err := json.Marshal(turnSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	mutate(snapshot)
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	doc := turnDoc()
	doc.ConversationSnapshot = encoded
	return doc
}

func TestDecodeConversationSnapshotRejectsSchemaIdentityAndLimits(t *testing.T) {
	t.Parallel()

	oversizeItems := make([]map[string]any, 65)
	for i := range oversizeItems {
		oversizeItems[i] = map[string]any{
			"id":      fmt.Sprint(i),
			"seq":     i + 1,
			"runId":   "r",
			"author":  map[string]any{"id": "a", "kind": "user", "name": "A"},
			"content": "x",
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(snapshot map[string]any)
		rule   string
	}{
		{"schema version", func(s map[string]any) { s["schemaVersion"] = 2 }, "schema version"},
		{"kind", func(s map[string]any) { s["kind"] = "direct" }, "kind"},
		{"conversation", func(s map[string]any) { s["conversationId"] = "another" }, "conversation"},
		{"too many items", func(s map[string]any) { s["throughSeq"] = 100; s["items"] = oversizeItems }, "64-item"},
		{"seq above the watermark", func(s map[string]any) { s["throughSeq"] = 11 }, "outside"},
		{"not ascending", func(s map[string]any) {
			items := s["items"].([]any)
			items[0], items[1] = items[1], items[0]
		}, "ascending"},
		{"author kind", func(s map[string]any) {
			s["items"].([]any)[0].(map[string]any)["author"].(map[string]any)["kind"] = "system"
		}, "author kind"},
		{"empty author name", func(s map[string]any) {
			s["items"].([]any)[0].(map[string]any)["author"].(map[string]any)["name"] = "  "
		}, "author name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeConversationSnapshot(turnRun(), snapshotDocument(t, tc.mutate))
			if !errors.Is(err, errConversationSnapshotInvalid) {
				t.Fatalf("error = %v, want errConversationSnapshotInvalid", err)
			}
			if !strings.Contains(err.Error(), tc.rule) {
				t.Fatalf("error %q does not name the rule %q", err, tc.rule)
			}
		})
	}

	t.Run("over the byte budget", func(t *testing.T) {
		t.Parallel()
		doc := turnDoc()
		doc.ConversationSnapshot = json.RawMessage(
			`{"schemaVersion":1,"pad":"` + strings.Repeat("p", 64<<10) + `"}`,
		)
		if _, err := decodeConversationSnapshot(
			turnRun(),
			doc,
		); !errors.Is(
			err,
			errConversationSnapshotInvalid,
		) {
			t.Fatalf("error = %v, want errConversationSnapshotInvalid", err)
		}
	})
}

func TestDecodeConversationSnapshotAcceptsTheAgreedDocument(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Clean(runContextChatGolden))
	if err != nil {
		t.Fatalf("read the chat golden: %v", err)
	}
	doc := decodeRunContext(Run{Context: raw}, slog.Default())
	snapshot, err := decodeConversationSnapshot(Run{ConversationID: turnConversationID}, doc)
	if err != nil {
		t.Fatalf("decodeConversationSnapshot(golden) error = %v", err)
	}
	if snapshot == nil || len(snapshot.Items) != 3 || !snapshot.Truncated || snapshot.ThroughSeq != 12 {
		t.Fatalf("snapshot = %+v, want the golden's three items", snapshot)
	}

	if got, err := decodeConversationSnapshot(turnRun(), turnDoc()); got != nil || err != nil {
		t.Fatalf("absent snapshot = %+v, %v; want nil, nil", got, err)
	}
	tolerant := snapshotDocument(t, func(s map[string]any) { s["someFutureField"] = true })
	if _, err := decodeConversationSnapshot(turnRun(), tolerant); err != nil {
		t.Fatalf("an unknown field must be tolerated: %v", err)
	}
}

func TestWriteRunConversationSnapshotIsAtomicAndPrivate(t *testing.T) {
	t.Parallel()

	envRoot := t.TempDir()
	first := json.RawMessage(`{"schemaVersion":1,"items":[]}`)
	path, err := writeRunConversationSnapshot(envRoot, first)
	if err != nil {
		t.Fatalf("writeRunConversationSnapshot() error = %v", err)
	}
	if path != filepath.Join(envRoot, "context", "conversation.json") {
		t.Fatalf("path = %q, want context/conversation.json under the Run environment", path)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(first) {
		t.Fatalf("file = %q, %v; want the exact bytes", got, err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("context directory mode = %v, %v; want 0700", info.Mode(), err)
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("file mode = %v, %v; want 0600", info.Mode(), err)
		}
	}
	second := json.RawMessage(`{"schemaVersion":1,"items":[{"seq":1}]}`)
	if _, err := writeRunConversationSnapshot(envRoot, second); err != nil {
		t.Fatalf("second write error = %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(second) {
		t.Fatalf("second write left %q, want it replaced", got)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("temporary file %q left behind", entry.Name())
		}
	}
}

func TestRunFailureReasonNamesAnInvalidRunContext(t *testing.T) {
	t.Parallel()

	err := wrapRunPreparationError(
		runPreparationPhasePrepare,
		fmt.Errorf("decode: %w", errConversationSnapshotInvalid),
	)
	if got := runFailureReason(err); got != string(runfailure.ReasonRunContextInvalid) {
		t.Fatalf("runFailureReason() = %q, want %q", got, runfailure.ReasonRunContextInvalid)
	}
}

func TestTurnRendersArtifactIDsSeparatelyFromUploadedFiles(t *testing.T) {
	doc := RunContext{ChannelArtifactIDs: []string{"00000000-0000-4000-8000-000000000001"}}
	got := renderTurnInput("Review this", Run{}, doc, nil, turnContinuity{}).prompt
	want := "Review this\n\nReferenced artifacts:\n- artifactId: 00000000-0000-4000-8000-000000000001"
	if got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
	if strings.Contains(got, "Files supplied") {
		t.Fatal("artifact rendered as uploaded file")
	}
}
