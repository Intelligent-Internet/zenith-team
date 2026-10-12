package realtime

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestEphemeralRelayCompanyIsolationAndClose(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	a := attachRealtimeTestClient(hub, "company-a", "member-a")
	b := attachRealtimeTestClient(hub, "company-b", "member-b")
	relay, err := NewEphemeralRelay(context.Background(), hub, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	company := "company-a"
	if err = relay.Publish(
		t.Context(),
		company,
		BroadcastEvent{
			Type:      "whiteboard.presence",
			CompanyID: &company,
			CreatedAt: time.Now(),
			ActorType: "user",
			Payload:   map[string]any{"boardId": "board-a"},
		},
	); err != nil {
		t.Fatal(err)
	}
	if got := receiveRealtimeFrame(t, a); got.Type != "whiteboard.presence" || got.EventID == "" {
		t.Fatalf("frame = %+v", got)
	}
	select {
	case frame := <-b.send:
		t.Fatalf("cross-company leak: %s", frame)
	default:
	}
	relay.Close()
	relay.Close()
}

func TestEphemeralRelaySelectedReaders(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	readerID := uuid.New()
	reader := attachRealtimeTestClient(hub, "company-a", readerID.String())
	excluded := attachRealtimeTestClient(hub, "company-a", uuid.NewString())
	otherCompany := attachRealtimeTestClient(hub, "company-b", readerID.String())
	relay, err := NewEphemeralRelay(t.Context(), hub, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	company := "company-a"
	event := BroadcastEvent{Type: "whiteboard.presence", CompanyID: &company, CreatedAt: time.Now()}
	if err = relay.PublishToCompanyUsers(t.Context(), company, []uuid.UUID{readerID}, event); err != nil {
		t.Fatal(err)
	}
	receiveRealtimeFrame(t, reader)
	for _, client := range []*Client{excluded, otherCompany} {
		select {
		case frame := <-client.send:
			t.Fatalf("private presence leaked: %s", frame)
		default:
		}
	}
	if err = relay.PublishToCompanyUsers(t.Context(), company, nil, event); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-reader.send:
		t.Fatalf("empty audience leaked: %s", frame)
	default:
	}
}
