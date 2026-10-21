package daemon

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// runContextGolden is the document the server writes into runs.context. It
// lives in the execution package's testdata because that package produces it;
// this test reads it across the boundary on purpose.
//
// The two sides declare the shape independently — execution.runResourceContext
// and daemon.RunResourceContext are separate types so the daemon does not
// import the server — so nothing but a shared artifact can prove they still
// agree. The relative path is the link: move the golden and this fails loudly,
// which is the point.
const runContextGolden = "../execution/testdata/run_context.golden.json"

// A mismatch here is silent in production and expensive: decodeRunContext
// degrades a malformed document to the zero value, localDirectoryFor then finds
// nothing, the daemon prepares a throwaway worktree, and the agent edits files
// the user never meant it to touch. Nothing errors.
func TestLocalDirectoryForReadsTheServerDocument(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Clean(runContextGolden))
	if err != nil {
		t.Fatalf("read the run-context golden: %v", err)
	}
	document := decodeRunContext(Run{Context: raw}, slog.Default())

	if len(document.Resources) != 1 {
		t.Fatalf("decoded %d resources, want 1 — the server document did not parse", len(document.Resources))
	}

	t.Run("this machine resolves its own directory", func(t *testing.T) {
		resource := localDirectoryFor(document, "daemon-a")
		if resource == nil {
			t.Fatal("localDirectoryFor() = nil; the run would silently take the worktree branch")
		}
		if resource.LocalPath != "/Users/dana/code/acme" {
			t.Errorf("LocalPath = %q, want the bound directory", resource.LocalPath)
		}
		if resource.Label != "Acme" {
			t.Errorf("Label = %q, want %q", resource.Label, "Acme")
		}
	})

	t.Run("another machine's directory is not claimed", func(t *testing.T) {
		// Every daemon receives every daemon's rows and must keep only its
		// own; picking up a colleague's path would mean writing to a
		// directory that does not exist on this machine, or worse, one that
		// does.
		if resource := localDirectoryFor(document, "daemon-b"); resource != nil {
			t.Fatalf("localDirectoryFor(daemon-b) = %+v, want nil", resource)
		}
	})

	t.Run("a tagged Run never claims its person's directory", func(t *testing.T) {
		// A channel answer never draws on its person's local folders, so it
		// starts in its own scratch even on the machine the directory is
		// bound to.
		tagged := document
		tagged.ChannelInvocation = &RunChannelInvocation{ID: "0198aa00-0000-7000-8000-0000000000c1"}
		if resource := localDirectoryFor(tagged, "daemon-a"); resource != nil {
			t.Fatalf("localDirectoryFor(tagged) = %+v, want nil", resource)
		}
	})

	t.Run("the frozen agent brief decodes under the agreed keys", func(t *testing.T) {
		// The enqueue kernel freezes the agent's execution identity into
		// context.agent; the daemon decoded these keys long before anything
		// wrote them, which is exactly the drift this golden now pins.
		if document.Agent.Name != "Agent A" {
			t.Errorf("agent name = %q, want the frozen brief's", document.Agent.Name)
		}
		if document.Agent.Model != "test" {
			t.Errorf("agent model = %q, want the frozen brief's", document.Agent.Model)
		}
	})

	t.Run("a run with no context takes the worktree branch", func(t *testing.T) {
		empty := decodeRunContext(Run{}, slog.Default())
		if resource := localDirectoryFor(empty, "daemon-a"); resource != nil {
			t.Fatalf("localDirectoryFor(empty) = %+v, want nil", resource)
		}
	})
}

// The empty snapshot is what every fresh Conversation starts from. It must
// decode as an explicit empty list: an absent key would read as "the server
// sent no history", which is a different claim from "there was none".
func TestRunContextGoldenCarriesAnExplicitEmptySnapshot(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Clean(runContextGolden))
	if err != nil {
		t.Fatalf("read the run-context golden: %v", err)
	}
	document := decodeRunContext(Run{Context: raw}, slog.Default())
	if len(document.ConversationSnapshot) == 0 {
		t.Fatal("the golden carries no conversationSnapshot; the daemon would launch with no history at all")
	}
	var snapshot RunConversationSnapshot
	if err := json.Unmarshal(document.ConversationSnapshot, &snapshot); err != nil {
		t.Fatalf("decode the frozen snapshot %s: %v", document.ConversationSnapshot, err)
	}
	if snapshot.SchemaVersion != 1 || snapshot.Kind != "direct" || snapshot.ThroughSeq != 0 || snapshot.Truncated {
		t.Fatalf("snapshot = %+v, want schema 1, kind direct, watermark 0, not truncated", snapshot)
	}
	if snapshot.Items == nil || len(snapshot.Items) != 0 {
		t.Fatalf("items = %#v, want an explicit empty list", snapshot.Items)
	}
	if snapshot.ConversationID != "0197f5b0-4000-7000-8000-000000000007" {
		t.Fatalf("conversationId = %q, want the golden Conversation", snapshot.ConversationID)
	}
	if document.Trigger != nil {
		t.Fatalf("trigger = %+v, want none: the golden Run has no acting member", document.Trigger)
	}
}

// runContextChatGolden is the hand-authored non-empty shape: two members and
// the agent in one Task chat, older history left out, and a trigger naming
// the member the Run answers. The producer's literals encode to these exact
// bytes on the server side.
const runContextChatGolden = "../execution/testdata/run_context_chat.golden.json"

func TestRunContextChatGoldenDecodesAuthorsAndTrigger(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Clean(runContextChatGolden))
	if err != nil {
		t.Fatalf("read the chat golden: %v", err)
	}
	document := decodeRunContext(Run{Context: raw}, slog.Default())
	if document.Prompt != "Please start with the CSV parser." {
		t.Fatalf("prompt = %q, want the frozen trigger words", document.Prompt)
	}
	if document.Trigger == nil || document.Trigger.Author.Name != "Bob Tran" ||
		document.Trigger.Author.Kind != protocol.ActorKindUser ||
		document.Trigger.Author.ID != "0197f5b0-4000-7000-8000-000000000033" {
		t.Fatalf("trigger = %+v, want Bob Tran, a user", document.Trigger)
	}
	if document.Trigger.CreatedAt.IsZero() {
		t.Fatal("trigger createdAt is zero; the enqueue time did not decode")
	}
	var snapshot RunConversationSnapshot
	if err := json.Unmarshal(document.ConversationSnapshot, &snapshot); err != nil {
		t.Fatalf("decode the frozen snapshot: %v", err)
	}
	if snapshot.SchemaVersion != 1 || snapshot.Kind != "task" || snapshot.ThroughSeq != 12 || !snapshot.Truncated {
		t.Fatalf("snapshot = %+v, want schema 1, kind task, watermark 12, truncated", snapshot)
	}
	if len(snapshot.Items) != 3 {
		t.Fatalf("decoded %d items, want 3", len(snapshot.Items))
	}
	if snapshot.Items[0].Author.Name != "Alice Nguyen" || snapshot.Items[0].Author.Kind != protocol.ActorKindUser {
		t.Errorf("item 0 author = %+v, want Alice Nguyen, a user", snapshot.Items[0].Author)
	}
	if snapshot.Items[1].Author.Kind != protocol.ActorKindAgent ||
		snapshot.Items[1].Author.ID != "0197f5b0-4000-7000-8000-000000000032" {
		t.Errorf("item 1 author = %+v, want the agent", snapshot.Items[1].Author)
	}
	if snapshot.Items[2].Seq != 12 || snapshot.Items[2].Content != "Email and name. Phone is optional." {
		t.Errorf("item 2 = %+v, want seq 12 with its content intact", snapshot.Items[2])
	}
	for i := 1; i < len(snapshot.Items); i++ {
		if snapshot.Items[i].Seq <= snapshot.Items[i-1].Seq {
			t.Fatalf("items are not ascending at index %d", i)
		}
	}
}
