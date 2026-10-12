package execution

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "boardly.dev/boardly/server/pkg/db/generated"
)

// TestRunSnapshotKeepsFailureTextAndRoutingFactsOffTheWire pins the two
// reasons a field lives on the snapshot without reaching a browser: raw
// provider failure text, which only an authorized REST reader may see, and
// the conversation facts the realtime bridge routes on.
func TestRunSnapshotKeepsFailureTextAndRoutingFactsOffTheWire(t *testing.T) {
	t.Parallel()
	creator := uuid.New()
	snapshot := RunSnapshot{
		ID:        uuid.MustParse("00000000-0000-4000-8000-000000000007"),
		CompanyID: uuid.New(),
		Status:    "completed",
		ConversationRoute: ConversationRoute{
			ConversationKind:      "direct",
			ConversationCreatedBy: &creator,
		},
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	for _, forbidden := range []string{"conversationKind", "conversationCreatedBy", creator.String(), "error"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("run snapshot on the wire contains %q: %s", forbidden, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"status":"completed"`) {
		t.Fatalf("run snapshot lost its status: %s", encoded)
	}
}

// TestRunViewAddsTheFailureTextTheRESTReaderIsEntitledTo pins the other half:
// the REST read model is the snapshot plus the error, so the two can never
// drift apart field by field.
func TestRunViewAddsTheFailureTextTheRESTReaderIsEntitledTo(t *testing.T) {
	t.Parallel()
	failure := "provider refused the request"
	view := RunView{
		RunSnapshot: RunSnapshot{
			ID: uuid.MustParse("00000000-0000-4000-8000-000000000007"), Status: "failed",
			ConversationRoute: ConversationRoute{ConversationKind: "task"},
		},
		Error: &failure,
	}

	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("unmarshal view: %v", err)
	}
	if got := string(fields["error"]); got != `"provider refused the request"` {
		t.Fatalf("REST error = %s, want the failure text", got)
	}
	if _, leaked := fields["conversationKind"]; leaked {
		t.Fatalf("REST view leaked a routing fact: %s", encoded)
	}
	if got := string(fields["status"]); got != `"failed"` {
		t.Fatalf("REST status = %s, want failed", got)
	}
}

// TestRunSnapshotCarriesTheReadableFailureButNotTheRawText pins the split a
// failed run rides the wire with: the provider-aware sentence and the bounded
// detail are for every reader, the raw failure text stays on the REST view.
func TestRunSnapshotCarriesTheReadableFailureButNotTheRawText(t *testing.T) {
	t.Parallel()
	row := db.ListRunProjectionsRow{
		ID:              uuid.MustParse("00000000-0000-4000-8000-000000000007"),
		RuntimeProvider: "codex",
		Status:          "failed",
		Attempt:         1,
		FailureReason:   pgtype.Text{String: "agent_error.provider_quota_limit", Valid: true},
		Error: pgtype.Text{
			String: "You've hit your usage limit. Visit https://chatgpt.com/codex/settings/usage " +
				"to purchase more credits or try again at Sep 22nd, 2026 11:27 AM.",
			Valid: true,
		},
		Source: []byte("null"),
	}
	view := mapRunView(row, uuid.Nil)
	if view.Provider != "codex" {
		t.Fatalf("provider = %q, want codex", view.Provider)
	}
	if view.FailureMessage == nil || !strings.HasPrefix(*view.FailureMessage, "Codex ") {
		t.Fatalf("failureMessage = %v, want the Codex sentence", view.FailureMessage)
	}
	if view.FailureDetail == nil || !strings.Contains(*view.FailureDetail, "Sep 22nd") {
		t.Fatalf("failureDetail = %v, want the provider's own words", view.FailureDetail)
	}
	// A quota failure gets no retry, so the pair must read as final.
	if view.MaxAttempts != view.Attempt {
		t.Fatalf("attempt %d of %d for a failure that gets no retry, want them equal",
			view.Attempt, view.MaxAttempts)
	}

	encoded, err := json.Marshal(view.RunSnapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	for _, key := range []string{"provider", "failureMessage", "failureDetail"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("snapshot on the wire lacks %q: %s", key, encoded)
		}
	}
	if _, leaked := fields["error"]; leaked {
		t.Fatalf("snapshot on the wire carries the raw error field: %s", encoded)
	}

	healthy := mapRunView(db.ListRunProjectionsRow{Status: "completed", Source: []byte("null")}, uuid.Nil)
	if healthy.FailureMessage != nil || healthy.FailureDetail != nil {
		t.Fatalf("a run that has not failed must not carry failure copy: %+v", healthy.RunSnapshot)
	}
}

// TestRunSnapshotNamesTheRuntimeOnlyToItsRegistrant pins the viewer rule on
// the wire's runtimeId (ADR 0014): the registrant reads it, another member
// and a Company-wide frame read null, and the in-process id is never
// serialized.
func TestRunSnapshotNamesTheRuntimeOnlyToItsRegistrant(t *testing.T) {
	t.Parallel()
	registrant, member := uuid.New(), uuid.New()
	row := db.ListRunProjectionsRow{
		ID: uuid.New(), RuntimeID: uuid.New(), Status: "running", Source: []byte("null"),
		RuntimeOwnerUserID: uuid.NullUUID{UUID: registrant, Valid: true},
	}
	for _, test := range []struct {
		name   string
		viewer uuid.UUID
		named  bool
	}{
		{name: "registrant", viewer: registrant, named: true},
		{name: "another member", viewer: member},
		{name: "lifecycle frame", viewer: uuid.Nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			view := mapRunView(row, test.viewer)
			if view.RuntimeID != row.RuntimeID {
				t.Fatalf("in-process runtime = %s, want %s", view.RuntimeID, row.RuntimeID)
			}
			encoded, err := json.Marshal(view)
			if err != nil {
				t.Fatalf("marshal view: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatalf("unmarshal view: %v", err)
			}
			want := "null"
			if test.named {
				want = `"` + row.RuntimeID.String() + `"`
			}
			if got := string(fields["runtimeId"]); got != want {
				t.Fatalf("runtimeId = %s, want %s", got, want)
			}
			if !test.named && strings.Contains(string(encoded), row.RuntimeID.String()) {
				t.Fatalf("the wire names another member's runtime: %s", encoded)
			}
		})
	}
	unowned := row
	unowned.RuntimeOwnerUserID = uuid.NullUUID{}
	if view := mapRunView(unowned, registrant); view.ViewerRuntimeID != nil {
		t.Fatalf("a runtime with no registrant is named to %s", registrant)
	}
}
