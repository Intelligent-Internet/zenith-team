//go:build integration

package realtime

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestEphemeralRelayAcrossReplicas(t *testing.T) {
	client := isolatedDiscussionRedis(t)
	hubA, hubB := NewHub(), NewHub()
	company := NewEventID()
	reader := uuid.New()
	excluded := attachRealtimeTestClient(hubB, company, uuid.NewString())
	a := attachRealtimeTestClient(hubA, company, reader.String())
	b := attachRealtimeTestClient(hubB, company, reader.String())
	rA, err := NewEphemeralRelay(t.Context(), hubA, client)
	if err != nil {
		t.Fatal(err)
	}
	defer rA.Close()
	rB, err := NewEphemeralRelay(t.Context(), hubB, client)
	if err != nil {
		t.Fatal(err)
	}
	defer rB.Close()
	if err = rA.PublishToCompanyUsers(
		t.Context(),
		company,
		[]uuid.UUID{reader},
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
	first, second := receiveRealtimeFrame(t, a), receiveRealtimeFrame(t, b)
	if first.EventID != second.EventID {
		t.Fatalf("event IDs differ: %s, %s", first.EventID, second.EventID)
	}
	select {
	case frame := <-excluded.send:
		t.Fatalf("private presence leaked across replicas: %s", frame)
	default:
	}
	keys, err := client.Keys(t.Context(), "*").Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("ephemeral relay persisted keys: %v", keys)
	}
}

func TestCanvasPresenceBoundsSessionsInRedis(t *testing.T) {
	assertCanvasPresenceBounds(t, NewCanvasPresence(isolatedDiscussionRedis(t)))
}

func TestCanvasPresenceFollowsAgentRunsInRedis(t *testing.T) {
	assertCanvasPresenceFollowsRuns(t, NewCanvasPresence(isolatedDiscussionRedis(t)))
}

func TestCanvasPresenceKeepsAgentsOutOfThePersonalLimitInRedis(t *testing.T) {
	assertCanvasPresenceAgentsOutsidePersonalLimit(t, NewCanvasPresence(isolatedDiscussionRedis(t)))
}

func TestCanvasPresenceRemembersTheLastViewInRedis(t *testing.T) {
	assertCanvasPresenceRemembersLastView(t, NewCanvasPresence(isolatedDiscussionRedis(t)))
}
