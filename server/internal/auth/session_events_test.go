package auth

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
)

// A event event is opaque by design — holding one proves only that its
// payload matches its topic. So this reads the event where anything else
// reads it: at a subscriber, after publication.
func TestSessionRevocationReachesTheRevokedUserWithoutNamingThemOnTheWire(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("0197f590-70bd-78c3-825f-5f6e4df341af")
	sessionID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19")
	effectiveAt := time.Date(2026, time.July, 11, 12, 13, 14, 123456000, time.UTC)

	bus := authTestBus(t)
	received := recordTopic[RevokedSession](t, bus, Topic)

	built := newAuthSessionRevokedEvent(SessionRevocation{
		SessionID:   sessionID,
		UserID:      userID,
		Reason:      SessionRevocationRefreshRotated,
		EffectiveAt: effectiveAt,
	}, events.UserActor(userID.String()))
	bus.Publish(context.Background(), built.EntityType, built)

	published := received.snapshot()
	if len(published) != 1 {
		t.Fatalf("published events = %d, want 1", len(published))
	}
	event := published[0]
	if event.Name != "auth.session_revoked" {
		t.Fatalf("event name = %q, want auth.session_revoked", event.Name)
	}
	if event.Actor != events.UserActor(userID.String()) {
		t.Fatalf("event actor = %#v, want the revoking user", event.Actor)
	}

	// The bridge routes on this; the client never sees it.
	if event.Payload.UserID != userID {
		t.Fatalf("routing user = %v, want %v", event.Payload.UserID, userID)
	}
	encoded, err := json.Marshal(event.Payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload) != 2 || payload["sessionId"] != sessionID.String() ||
		payload["reason"] != string(SessionRevocationRefreshRotated) {
		t.Fatalf("payload on the wire = %#v, want exactly sessionId and reason", payload)
	}
}
