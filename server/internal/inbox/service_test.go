package inbox

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"boardly.dev/boardly/server/pkg/protocol"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type notificationRecorder struct {
	notifications []Notification
}

func (r *notificationRecorder) Present(notification Notification) {
	r.notifications = append(r.notifications, notification)
}

func validItemCreateInput() CreateInput {
	deepLink := "/app?taskId=1"
	actorID := uuid.Must(uuid.NewV7())
	return CreateInput{
		CompanyID: uuid.Must(uuid.NewV7()), RecipientUserID: uuid.Must(uuid.NewV7()),
		Type:   TypeTaskAssigned,
		Entity: Entity{Type: protocol.EntityTask, ID: uuid.Must(uuid.NewV7()).String()},
		Title:  "Task assigned",
		Body:   "You were assigned a task.", DeepLink: &deepLink,
		DedupeKey: "task:" + uuid.Must(uuid.NewV7()).String() + ":version:1:assigned",
		Actor:     Actor{Kind: "agent", ID: &actorID},
	}
}

func TestValidCreateInputFencesIdentityContentAndActor(t *testing.T) {
	t.Parallel()
	if !ValidCreateInput(validItemCreateInput()) {
		t.Fatal("ValidCreateInput(valid) = false")
	}
	for _, testCase := range []struct {
		name string
		edit func(*CreateInput)
	}{
		{"nil company", func(i *CreateInput) { i.CompanyID = uuid.Nil }},
		{"nil recipient", func(i *CreateInput) { i.RecipientUserID = uuid.Nil }},
		{"nil conversation", func(i *CreateInput) {
			id := uuid.Nil
			i.ConversationID = &id
		}},
		{"unknown type", func(i *CreateInput) { i.Type = "gossip" }},
		{"empty title", func(i *CreateInput) { i.Title = "" }},
		{"untrimmed title", func(i *CreateInput) { i.Title = " padded " }},
		{"empty body", func(i *CreateInput) { i.Body = "" }},
		{"oversized body", func(i *CreateInput) { i.Body = strings.Repeat("x", 2049) }},
		{"empty dedupe key", func(i *CreateInput) { i.DedupeKey = "" }},
		{"absolute deep link", func(i *CreateInput) {
			link := "https://example.test/app"
			i.DeepLink = &link
		}},
		{"unknown actor kind", func(i *CreateInput) { i.Actor = Actor{Kind: "poltergeist"} }},
		{"agent actor without id", func(i *CreateInput) { i.Actor = Actor{Kind: "agent"} }},
		{"system actor with id", func(i *CreateInput) {
			id := uuid.Must(uuid.NewV7())
			i.Actor = Actor{Kind: "system", ID: &id}
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			input := validItemCreateInput()
			testCase.edit(&input)
			if ValidCreateInput(input) {
				t.Fatalf("ValidCreateInput(%s) = true, want false", testCase.name)
			}
		})
	}
}

func TestHumanInputRequestTypesAreValid(t *testing.T) {
	t.Parallel()

	for _, itemType := range []Type{TypeApprovalRequest, TypeQuestionRequest} {
		input := validItemCreateInput()
		input.Type = itemType
		if !ValidCreateInput(input) {
			t.Errorf("ValidCreateInput(%q) = false", itemType)
		}
	}
}

// TestRuntimeOfflineIsAValidInboxKind pins the machine-level notification the
// stale runtime sweep raises: a system-authored item about a runtime, linking
// into the runtime detail. The kind is the only new vocabulary — the entity
// type, the actor and the deep link shape are the Inbox's existing ones.
func TestRuntimeOfflineIsAValidInboxKind(t *testing.T) {
	t.Parallel()

	if TypeRuntimeOffline != "runtime_offline" {
		t.Fatalf("TypeRuntimeOffline = %q, want runtime_offline", TypeRuntimeOffline)
	}
	if !validType(TypeRuntimeOffline) {
		t.Fatal("validType(TypeRuntimeOffline) = false")
	}
	runtimeID := uuid.Must(uuid.NewV7())
	deepLink := "/settings/agents/" + runtimeID.String()
	input := validItemCreateInput()
	input.Type = TypeRuntimeOffline
	input.ConversationID = nil
	input.Entity = Entity{Type: protocol.EntityRuntime, ID: runtimeID.String()}
	input.Title = "MacBook Pro went offline with 1 run in flight"
	input.DeepLink = &deepLink
	input.DedupeKey = "runtime:" + runtimeID.String() + ":offline:1757376000"
	input.Actor = Actor{Kind: "system"}
	if !ValidCreateInput(input) {
		t.Fatal("ValidCreateInput(runtime_offline) = false")
	}
}

// TestValidDeepLinkRejectsOffSiteTargets pins the one field of an Inbox item a
// client follows. A deep link is always same-origin and always path-absolute.
func TestValidDeepLinkRejectsOffSiteTargets(t *testing.T) {
	t.Parallel()
	if !validDeepLink(nil) {
		t.Fatal("validDeepLink(nil) = false; an item without a link is valid")
	}
	for _, accepted := range []string{"/app", "/app?taskId=1", "/inbox"} {
		value := accepted
		if !validDeepLink(&value) {
			t.Fatalf("validDeepLink(%q) = false", accepted)
		}
	}
	for _, rejected := range []string{
		"", "app", "//evil.test/app", "https://evil.test/app", `/app\evil`,
		" /app", "/app ", strings.Repeat("/a", 600),
	} {
		value := rejected
		if validDeepLink(&value) {
			t.Fatalf("validDeepLink(%q) = true, want false", rejected)
		}
	}
}

func TestMutationAlreadyAppliedReportsTheAlreadyAppliedCase(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	read := Item{ReadAt: &now}
	archived := Item{ArchivedAt: &now}
	fresh := Item{}
	for _, testCase := range []struct {
		name         string
		item         Item
		mutation     Mutation
		expectedNoop bool
	}{
		{"read a fresh item", fresh, MutationRead, false},
		{"read an already read item", read, MutationRead, true},
		{"unread a fresh item", fresh, MutationUnread, true},
		{"unread a read item", read, MutationUnread, false},
		{"archive a fresh item", fresh, MutationArchive, false},
		{"archive an archived item", archived, MutationArchive, true},
		{"restore a fresh item", fresh, MutationRestore, true},
		{"restore an archived item", archived, MutationRestore, false},
		{"unknown mutation", fresh, "shred", true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if noop := mutationAlreadyApplied(
				testCase.item,
				testCase.mutation,
			); noop != testCase.expectedNoop {
				t.Fatalf("mutationAlreadyApplied() = %t, want %t", noop, testCase.expectedNoop)
			}
		})
	}
}

// TestCountsVersionFromOrderingTokenUsesTheTrustedAggregate pins the derived
// counts contract: a present ordering token is rendered directly as unix
// milliseconds; the absent aggregate (an empty inbox, which the counts
// queries return as NULL) is the initial version 1.
func TestCountsVersionFromOrderingTokenUsesTheTrustedAggregate(t *testing.T) {
	t.Parallel()
	if got := countsVersionFromOrderingToken(pgtype.Timestamptz{}); got != 1 {
		t.Fatalf("countsVersionFromOrderingToken(null) = %d, want 1", got)
	}
	earlier := time.Date(2026, time.August, 9, 10, 0, 0, 0, time.UTC)
	later := earlier.Add(time.Millisecond)
	first := countsVersionFromOrderingToken(pgtype.Timestamptz{Time: earlier, Valid: true})
	second := countsVersionFromOrderingToken(pgtype.Timestamptz{Time: later, Valid: true})
	if first != earlier.UnixMilli() || second <= first {
		t.Fatalf("countsVersionFromOrderingToken order = %d then %d", first, second)
	}
}

func TestPresentCreatedCarriesTheDurableItemPresentation(t *testing.T) {
	t.Parallel()

	deepLink := "/app?conversationId=conversation-1&runId=42"
	itemID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	recipientID := uuid.Must(uuid.NewV7())
	recorder := &notificationRecorder{}
	result := CreateResult{
		Item: Item{
			ID:        itemID,
			CompanyID: &companyID,
			Type:      TypeRunCompleted,
			Title:     "Work completed",
			Body:      "An agent finished its work.",
			DeepLink:  &deepLink,
		},
		RecipientUserID: recipientID,
		Created:         true,
	}

	PresentCreated(recorder, result)

	want := []Notification{{
		NotificationID: itemID,
		CompanyID:      companyID,
		RecipientID:    recipientID,
		Type:           TypeRunCompleted,
		Title:          "Work completed",
		Body:           "An agent finished its work.",
		DeepLink:       &deepLink,
	}}
	if !reflect.DeepEqual(recorder.notifications, want) {
		t.Fatalf("PresentCreated() notifications = %+v, want %+v", recorder.notifications, want)
	}
}
