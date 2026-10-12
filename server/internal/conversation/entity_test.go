package conversation

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/pkg/protocol"
)

func TestConversationDTOJSONIsStrictlyDiscriminated(t *testing.T) {
	t.Parallel()

	taskConversation := validTaskConversation()
	encodedTask, err := json.Marshal(taskConversation)
	if err != nil {
		t.Fatalf("marshal task Conversation: %v", err)
	}
	if strings.Contains(string(encodedTask), "agentId") ||
		strings.Contains(string(encodedTask), "createdByUserId") {
		t.Fatalf("task Conversation JSON leaked direct fields: %s", encodedTask)
	}

	direct := validDirectConversation()
	encodedDirect, err := json.Marshal(direct)
	if err != nil {
		t.Fatalf("marshal direct Conversation: %v", err)
	}
	if !strings.Contains(string(encodedDirect), "agentId") ||
		!strings.Contains(string(encodedDirect), "createdByUserId") {
		t.Fatalf("direct Conversation JSON omitted provenance: %s", encodedDirect)
	}
}

func TestNormalizeDirectAndMessageInputs(t *testing.T) {
	t.Parallel()

	title := "\u00a0 Release review \u202f"
	direct, err := normalizeCreateDirectInput(CreateDirectInput{
		AgentID: uuid.Must(uuid.NewV7()), Title: &title,
	})
	if err != nil || direct.Title == nil || *direct.Title != "Release review" {
		t.Fatalf("normalizeCreateDirectInput() = %+v, %v", direct, err)
	}

	requestKey := uuid.Must(uuid.NewV7())
	message, err := normalizeSendInput(SendInput{
		RequestKey:    requestKey,
		Content:       "\n Please inspect this. \t",
		AttachmentIDs: []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())},
	})
	if err != nil || message.Content != "Please inspect this." {
		t.Fatalf("normalizeSendInput() = %+v, %v", message, err)
	}
	for _, invalid := range []SendInput{
		{RequestKey: uuid.Nil, Content: "Message"},
		{RequestKey: uuid.Must(uuid.NewV7()), Content: "   "},
		{RequestKey: uuid.Must(uuid.NewV7()), Content: "Message", AttachmentIDs: []uuid.UUID{message.AttachmentIDs[0], message.AttachmentIDs[0]}},
	} {
		if _, err := normalizeSendInput(invalid); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("normalizeSendInput(%+v) error = %v, want %v", invalid, err, ErrInvalidInput)
		}
	}

	tooManyAttachments := SendInput{RequestKey: uuid.Must(uuid.NewV7()), Content: "Message"}
	for range 21 {
		tooManyAttachments.AttachmentIDs = append(tooManyAttachments.AttachmentIDs, uuid.Must(uuid.NewV7()))
	}
	if _, err := normalizeSendInput(tooManyAttachments); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("normalizeSendInput(21 Attachments) error = %v, want %v", err, ErrInvalidInput)
	}
}

func TestDirectTitleFromMessage(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ content, want string }{
		{"Plan\n our\t next steps.", "Plan our next steps."},
		{"  Xin chào\u00a0bạn  ", "Xin chào bạn"},
		{strings.Repeat("界", 130), strings.Repeat("界", 120)},
	} {
		if got := directTitleFromMessage(test.content); got != test.want {
			t.Errorf("direct title = %q, want %q", got, test.want)
		}
	}
}

func TestNormalizeRenameDirectInput(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		input RenameDirectInput
		want  string
		err   error
	}{
		{name: "trim contract whitespace", input: RenameDirectInput{Title: "\ufeff\u00a0 Release review \u202f", ExpectedRevision: 1}, want: "Release review"},
		{name: "maximum Unicode title", input: RenameDirectInput{Title: strings.Repeat("界", 120), ExpectedRevision: 1}, want: strings.Repeat("界", 120)},
		{name: "empty title", input: RenameDirectInput{ExpectedRevision: 1}, err: ErrInvalidInput},
		{name: "blank title", input: RenameDirectInput{Title: "\n\ufeff\u2000", ExpectedRevision: 1}, err: ErrInvalidInput},
		{name: "too many Unicode runes", input: RenameDirectInput{Title: strings.Repeat("界", 121), ExpectedRevision: 1}, err: ErrInvalidInput},
		{name: "invalid UTF-8", input: RenameDirectInput{Title: string([]byte{0xff}), ExpectedRevision: 1}, err: ErrInvalidInput},
		{name: "zero revision", input: RenameDirectInput{Title: "Release review"}, err: ErrInvalidInput},
		{name: "unsafe revision", input: RenameDirectInput{Title: "Release review", ExpectedRevision: protocol.MaxJSONSafeInteger + 1}, err: ErrInvalidInput},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeRenameDirectInput(test.input)
			if !errors.Is(err, test.err) || err == nil && (got.Title != test.want || got.ExpectedRevision != test.input.ExpectedRevision) {
				t.Fatalf("normalizeRenameDirectInput() = %+v, %v, want title %q, error %v", got, err, test.want, test.err)
			}
		})
	}
}

func TestScheduleConversationTitleCanRepresentMaximumScheduleName(t *testing.T) {
	t.Parallel()

	if !validScheduleConversationTitle(strings.Repeat("a", 240)) {
		t.Fatal("validScheduleConversationTitle(240 runes) = false")
	}
	if validScheduleConversationTitle(strings.Repeat("a", 241)) {
		t.Fatal("validScheduleConversationTitle(241 runes) = true")
	}
}

func TestConversationListItemCarriesExactSummaryAndUserProjection(t *testing.T) {
	t.Parallel()

	item := validDirectListItem()
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal Conversation list item: %v", err)
	}
	for _, fragment := range []string{
		`"taskId":null`,
		`"queuedCount":1`,
		`"activeCount":1`,
		`"latest":{"runId":`,
		`"createdAt":"2026-07-13T01:02:03Z"`,
		`"failureReason":null`,
		`"attempt":1`,
		`"maxAttempts":3`,
		`"userState"`,
	} {
		if !strings.Contains(string(encoded), fragment) {
			t.Fatalf("Conversation list JSON %s omitted %s", encoded, fragment)
		}
	}
	if strings.Contains(string(encoded), `"active":`) {
		t.Fatalf("Conversation list JSON retained embedded active Runs: %s", encoded)
	}
}

func TestMessageDTOAndViewOmitTenantAndAttachmentFieldsFromCanonicalMessage(t *testing.T) {
	t.Parallel()

	message := validMessage()
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal Message: %v", err)
	}
	if strings.Contains(string(encoded), "companyId") || strings.Contains(string(encoded), "attachments") {
		t.Fatalf("canonical Message JSON contains HTTP-only fields: %s", encoded)
	}
	for _, fragment := range []string{
		`"name":"Schema A"`,
		`"avatarUrl":"https://cdn.example.com/schema-a.png"`,
	} {
		if !strings.Contains(string(encoded), fragment) {
			t.Fatalf("canonical Message JSON %s omitted renderable author field %s", encoded, fragment)
		}
	}
}

func validTaskConversation() Response {
	now := time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC)
	title := "Release task"
	return Response{
		ID: uuid.Must(uuid.NewV7()), CompanyID: uuid.Must(uuid.NewV7()), Kind: KindTask,
		Title: &title, CreatedAt: now, UpdatedAt: now, Revision: 1,
	}
}

func validDirectConversation() Response {
	value := validTaskConversation()
	value.Kind = KindDirect
	value.AgentID = uuidPointer(uuid.Must(uuid.NewV7()))
	value.CreatedByUserID = uuidPointer(uuid.Must(uuid.NewV7()))
	return value
}

func validDirectListItem() ListItem {
	conversation := validDirectConversation()
	now := conversation.UpdatedAt
	agentID := *conversation.AgentID
	return ListItem{
		Conversation: conversation,
		Summary: ListSummary{
			UpdatedAt: now,
			Runs: RunsSummary{
				QueuedCount: 1,
				ActiveCount: 1,
				Latest: &LatestRunSummary{
					RunID: uuid.MustParse("00000000-0000-4000-8000-000000004242"), AgentID: agentID, Status: RunStatusRunning,
					CreatedAt: now, Attempt: 1, MaxAttempts: 3,
				},
			},
		},
		UserState: UserState{
			ConversationID: conversation.ID, UserID: *conversation.CreatedByUserID,
			UpdatedAt: now, Revision: 1,
		},
	}
}

func validMessage() MessageResponse {
	return MessageResponse{
		ID: uuid.Must(uuid.NewV7()), ConversationID: uuid.Must(uuid.NewV7()), Seq: 1,
		Type: ItemTypeUserMessage, RunID: uuid.MustParse("00000000-0000-4000-8000-000000000017"),
		Author: ActorRef{
			Kind: ActorUser, ID: uuid.Must(uuid.NewV7()), DisplayName: "Schema A",
			AvatarURL: stringPointer("https://cdn.example.com/schema-a.png"),
		},
		Content:   "Please inspect this.",
		CreatedAt: time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC),
	}
}

func uuidPointer(value uuid.UUID) *uuid.UUID { return &value }

func stringPointer(value string) *string { return &value }

// TestLastMessageSummaryCarriesThePreviewForEveryAuthor pins the preview line
// every publisher of this wire type owes its readers. A run transition
// publishes the same summary the list read publishes, and clients cache-write
// it, so a producer that omits the preview blanks the row the list just
// rendered — which is exactly what the agent-reply frame used to do.
func TestLastMessageSummaryCarriesThePreviewForEveryAuthor(t *testing.T) {
	t.Parallel()

	createdAt := pgtype.Timestamptz{
		Time:  time.Date(2026, time.August, 22, 5, 33, 9, 0, time.UTC),
		Valid: true,
	}
	for _, test := range []struct {
		name       string
		itemType   string
		authorKind string
		content    string
		truncated  bool
	}{
		{"user message", "user_message", "user", "Reply with one word", false},
		{"agent text", "agent_text", "agent", "PINEAPPLE7", false},
		{"truncated body", "user_message", "user", "long body", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := newLastMessageSummary(
				14, test.content, test.truncated, createdAt, test.itemType, test.authorKind,
			)
			if got.Content != test.content || got.ContentTruncated != test.truncated {
				t.Fatalf("preview = %q truncated=%v, want %q/%v",
					got.Content, got.ContentTruncated, test.content, test.truncated)
			}
			if got.Seq != 14 || got.Type != ItemType(test.itemType) ||
				got.AuthorKind != ActorKind(test.authorKind) ||
				!got.CreatedAt.Equal(createdAt.Time) {
				t.Fatalf("summary = %#v", got)
			}
		})
	}
}
