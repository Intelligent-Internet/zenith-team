package agent

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// The notice's audience is the registrant alone (ADR 0014): a machine's
// name and liveness are theirs, and nobody else could restart it.
func TestRuntimeOfflineRecipientsAreTheRegistrantAlone(t *testing.T) {
	t.Parallel()
	first := uuid.MustParse("11111111-1111-4111-8111-111111111111")

	for _, testCase := range []struct {
		name  string
		owner uuid.NullUUID
		want  []uuid.UUID
	}{
		{
			name:  "the registrant",
			owner: uuid.NullUUID{UUID: first, Valid: true},
			want:  []uuid.UUID{first},
		},
		{
			name:  "an unowned runtime notifies nobody",
			owner: uuid.NullUUID{},
			want:  []uuid.UUID{},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := runtimeOfflineRecipients(testCase.owner)
			if got == nil || len(got) != len(testCase.want) {
				t.Fatalf("runtimeOfflineRecipients() = %v, want %v", got, testCase.want)
			}
			for index, want := range testCase.want {
				if got[index] != want {
					t.Fatalf("runtimeOfflineRecipients() = %v, want %v", got, testCase.want)
				}
			}
		})
	}
}

func TestRuntimeOfflineTitleNamesTheMachineAndFitsTheInboxLimit(t *testing.T) {
	t.Parallel()
	longName := strings.Repeat("m", 300)

	for _, testCase := range []struct {
		name    string
		machine string
		want    string
	}{
		{
			name:    "names the computer",
			machine: "MacBook Pro",
			want:    "MacBook Pro went offline",
		},
		{
			name:    "preserves Unicode",
			machine: "Máy của Sam",
			want:    "Máy của Sam went offline",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := runtimeOfflineTitle(testCase.machine); got != testCase.want {
				t.Fatalf("runtimeOfflineTitle() = %q, want %q", got, testCase.want)
			}
		})
	}

	// The Inbox caps a title at 160 runes; the machine name is the only part
	// a person controls, so it is the part that gives way.
	title := runtimeOfflineTitle(longName)
	if utf8.RuneCountInString(title) > 160 {
		t.Fatalf("runtimeOfflineTitle(300 runes) = %d runes, want <= 160",
			utf8.RuneCountInString(title))
	}
	if !strings.HasSuffix(title, "went offline") {
		t.Fatalf("runtimeOfflineTitle(300 runes) = %q, want the fact kept", title)
	}
	if strings.TrimSpace(title) != title {
		t.Fatalf("runtimeOfflineTitle(300 runes) = %q, want it trimmed", title)
	}
}

// TestRuntimeOfflineDisplayNamePrefersTheCustomName pins the same rule the
// roster renders: the name a person gave the machine wins over the daemon's.
func TestRuntimeOfflineDisplayNamePrefersTheCustomName(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name       string
		customName pgtype.Text
		want       string
	}{
		{name: "no custom name", customName: pgtype.Text{}, want: "codex (host)"},
		{
			name:       "a custom name",
			customName: pgtype.Text{String: "MacBook Pro", Valid: true},
			want:       "MacBook Pro",
		},
		{
			name:       "a blank custom name is no name",
			customName: pgtype.Text{String: "   ", Valid: true},
			want:       "codex (host)",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			row := generated.Runtime{Name: "codex (host)", CustomName: testCase.customName}
			if got := runtimeOfflineDisplayName(row); got != testCase.want {
				t.Fatalf("runtimeOfflineDisplayName() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestRuntimeOfflineDedupeKeyChangesPerOfflineEpisode is the reason the key
// carries a clock at all. Resolving an item archives it without deleting it,
// and the UNIQUE (company, recipient, dedupe_key) survives the archive — so a
// fixed key would notify a flapping machine exactly once, ever.
func TestRuntimeOfflineDedupeKeyChangesPerOfflineEpisode(t *testing.T) {
	t.Parallel()
	runtimeID := uuid.MustParse("44444444-4444-4444-8444-444444444444")
	sweptAt := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	first := pgtype.Timestamptz{Time: sweptAt.Add(-10 * time.Minute), Valid: true}
	second := pgtype.Timestamptz{Time: sweptAt.Add(-90 * time.Minute), Valid: true}

	firstKey := runtimeOfflineDedupeKey(runtimeID, first, sweptAt)
	secondKey := runtimeOfflineDedupeKey(runtimeID, second, sweptAt)
	if firstKey == secondKey {
		t.Fatalf("dedupe keys for two offline episodes are both %q", firstKey)
	}
	if want := "runtime:" + runtimeID.String() + ":offline:" +
		"1788954600"; firstKey != want {
		t.Fatalf("runtimeOfflineDedupeKey() = %q, want %q", firstKey, want)
	}
	if len(firstKey) > 256 {
		t.Fatalf("runtimeOfflineDedupeKey() is %d bytes, want <= 256", len(firstKey))
	}
	// A runtime that never beat has no episode clock of its own; the sweep's
	// own time is the episode.
	nullKey := runtimeOfflineDedupeKey(runtimeID, pgtype.Timestamptz{}, sweptAt)
	if want := "runtime:" + runtimeID.String() + ":offline:1788955200"; nullKey != want {
		t.Fatalf("runtimeOfflineDedupeKey(NULL last_seen_at) = %q, want %q", nullKey, want)
	}
}

func TestRuntimeOfflineCreateInputPassesInboxValidation(t *testing.T) {
	t.Parallel()
	runtimeID := uuid.MustParse("55555555-5555-4555-8555-555555555555")
	companyID := uuid.MustParse("66666666-6666-4666-8666-666666666666")
	recipient := uuid.MustParse("77777777-7777-4777-8777-777777777777")
	sweptAt := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	row := generated.Runtime{
		ID: runtimeID, CompanyID: companyID,
		Name: "codex (MacBook Pro)", Provider: "codex",
		LastSeenAt: pgtype.Timestamptz{Time: sweptAt.Add(-10 * time.Minute), Valid: true},
	}

	input := runtimeOfflineCreateInput(row, recipient, 2, sweptAt)
	if !inbox.ValidCreateInput(input) {
		t.Fatalf("ValidCreateInput(%+v) = false", input)
	}
	if input.Type != inbox.TypeRuntimeOffline {
		t.Fatalf("Type = %q, want runtime_offline", input.Type)
	}
	if input.CompanyID != companyID || input.RecipientUserID != recipient {
		t.Fatalf("addressed to %s/%s, want %s/%s",
			input.CompanyID, input.RecipientUserID, companyID, recipient)
	}
	if input.ConversationID != nil {
		t.Fatalf("ConversationID = %v, want nil; a machine is not a conversation",
			input.ConversationID)
	}
	wantEntity := inbox.Entity{Type: protocol.EntityRuntime, ID: runtimeID.String()}
	if input.Entity != wantEntity {
		t.Fatalf("Entity = %+v, want %+v", input.Entity, wantEntity)
	}
	if input.Actor != (inbox.Actor{Kind: "system"}) {
		t.Fatalf("Actor = %+v, want the system actor", input.Actor)
	}
	if input.DeepLink == nil || *input.DeepLink != "/settings/agents/"+runtimeID.String() {
		t.Fatalf("DeepLink = %v, want the runtime detail route", input.DeepLink)
	}
	if want := "codex (MacBook Pro) went offline"; input.Title != want {
		t.Fatalf("Title = %q, want %q", input.Title, want)
	}
	if want := "2 active runs were interrupted. Reconnect this computer and check the affected work."; input.Body != want {
		t.Fatalf("Body = %q, want %q", input.Body, want)
	}
	if input.DedupeKey != runtimeOfflineDedupeKey(runtimeID, row.LastSeenAt, sweptAt) {
		t.Fatalf("DedupeKey = %q, want the episode key", input.DedupeKey)
	}
}
