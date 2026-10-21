package activity

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
)

// message.updated only tells a browser to refetch a message's files. It must
// write no Activity row: record returns before it touches the database, so a
// nil store proves it (ADR 0017).
func TestMessageUpdatedHintWritesNoActivity(t *testing.T) {
	t.Parallel()

	err := record(context.Background(), nil, nil, events.Event{
		EntityType: "message", Name: "message.updated", CompanyID: uuid.New(),
		ConversationID: uuid.New(), Actor: events.UserActor(uuid.NewString()),
		Payload: map[string]string{"conversationId": uuid.NewString(), "messageId": uuid.NewString()},
	})
	if err != nil {
		t.Fatalf("record(message.updated) = %v, want skipped", err)
	}
}

// A message's evidence keeps how many files it carried, never their names:
// who may see a file changes when it joins or leaves a Project (ADR 0017).
func TestMessageEvidenceKeepsNoFileNames(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"id":"m1","content":"Plan","attachments":[` +
		`{"id":"a1","filename":"secret-roadmap.pdf"},{"id":"a2","filename":"budget.xlsx"}]}`)
	trimmed, err := withoutMessageFiles(payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(trimmed), "filename") || strings.Contains(string(trimmed), "secret-roadmap") {
		t.Fatalf("evidence still names a file: %s", trimmed)
	}
	var fields map[string]any
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["attachmentCount"] != float64(2) || fields["id"] != "m1" || fields["content"] != "Plan" {
		t.Fatalf("evidence = %s, want the message with attachmentCount 2", trimmed)
	}

	untouched, err := withoutMessageFiles([]byte(`{"id":"m2"}`))
	if err != nil || string(untouched) != `{"id":"m2"}` {
		t.Fatalf("evidence without files = %s, %v; want unchanged", untouched, err)
	}
}
