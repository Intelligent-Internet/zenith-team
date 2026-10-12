package realtime

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/events"
)

const nilUUID = "00000000-0000-0000-0000-000000000000"

func testEvent() BroadcastEvent {
	return BroadcastEvent{
		Type:      "task.updated",
		CreatedAt: time.Date(2026, 8, 24, 8, 30, 0, 0, time.UTC),
		ActorType: string(events.ActorTypeSystem),
		Payload:   map[string]any{"taskId": "task-1"},
	}
}

func TestToCompanyReachesEveryConnectionInThatCompany(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	inside := attachRealtimeTestClient(hub, "company-1", "user-1")
	alsoInside := attachRealtimeTestClient(hub, "company-1", "user-2")
	outside := attachRealtimeTestClient(hub, "company-2", "user-3")
	broadcaster := NewEventBroadcaster(hub, nil)

	if err := broadcaster.BroadcastToCompany("company-1", testEvent()); err != nil {
		t.Fatalf("BroadcastBroadcastToCompany() error = %v", err)
	}

	receiveRealtimeBytes(t, inside)
	receiveRealtimeBytes(t, alsoInside)
	expectNoRealtimeBytes(t, outside)
}

func TestToCompanyUserReachesOnlyThatUsersConnectionsInThatCompany(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	target := attachRealtimeTestClient(hub, "company-1", "user-1")
	sameCompanyOtherUser := attachRealtimeTestClient(hub, "company-1", "user-2")
	broadcaster := NewEventBroadcaster(hub, nil)

	// A direct conversation is visible to one founder, not the company.
	if err := broadcaster.BroadcastToCompanyUser("company-1", "user-1", testEvent()); err != nil {
		t.Fatalf("BroadcastBroadcastToCompanyUser() error = %v", err)
	}

	receiveRealtimeBytes(t, target)
	expectNoRealtimeBytes(t, sameCompanyOtherUser)
}

func TestToCompanyOrUserReachesBothAndDeliversOnceToSomeoneInBoth(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	// The company room is empty at creation, so the founder must be reached
	// through their own identity room as well.
	member := attachRealtimeTestClient(hub, "company-1", "user-2")
	founder := attachRealtimeTestClient(hub, "company-1", "user-1")
	broadcaster := NewEventBroadcaster(hub, nil)

	if err := broadcaster.BroadcastToCompanyOrUser("company-1", "user-1", testEvent()); err != nil {
		t.Fatalf("BroadcastBroadcastToCompanyOrUser() error = %v", err)
	}

	receiveRealtimeBytes(t, member)
	receiveRealtimeBytes(t, founder)
	// Being in both halves must not deliver the frame twice.
	expectNoRealtimeBytes(t, founder)
}

func TestDeliveryRefusesIdentifiersThatWouldAddressNobody(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	broadcaster := NewEventBroadcaster(hub, nil)

	tests := []struct {
		name    string
		deliver func() error
	}{
		{"company empty", func() error { return broadcaster.BroadcastToCompany("", testEvent()) }},
		{"company nil uuid", func() error { return broadcaster.BroadcastToCompany(nilUUID, testEvent()) }},
		{"user empty", func() error { return broadcaster.BroadcastToUser("", testEvent()) }},
		{"user nil uuid", func() error { return broadcaster.BroadcastToUser(nilUUID, testEvent()) }},
		{
			"company_user empty user",
			func() error { return broadcaster.BroadcastToCompanyUser("company-1", "", testEvent()) },
		},
		{
			"company_user nil company",
			func() error { return broadcaster.BroadcastToCompanyUser(nilUUID, "user-1", testEvent()) },
		},
		{
			"company_or_user empty user",
			func() error { return broadcaster.BroadcastToCompanyOrUser("company-1", "", testEvent()) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// A zero-value routing field must fail loudly here rather than
			// address a room nobody occupies.
			if err := tt.deliver(); err == nil {
				t.Fatal("delivery error = nil, want an error naming the invalid identifier")
			}
		})
	}
}

func TestFrameCarriesTheEventsNameCompanyActorAndPayload(t *testing.T) {
	t.Parallel()
	type runSnapshot struct {
		RunID     uuid.UUID `json:"id"`
		CompanyID string    `json:"companyId"`
		// A routing fact the bridge reads but the wire must never see.
		ConversationCreatedBy string `json:"-"`
	}
	stamp := time.Date(2026, 8, 24, 8, 30, 0, 0, time.UTC)
	companyID := uuid.New()

	frame := NewBroadcastEvent(events.Event{
		Name:      "run.completed",
		CompanyID: companyID,
		CreatedAt: stamp,
		Actor:     events.UserActor("user-1"),
		Payload:   runSnapshot{RunID: uuid.MustParse("00000000-0000-4000-8000-000000000007"), CompanyID: "company-1", ConversationCreatedBy: "user-9"},
	})

	if frame.Type != "run.completed" {
		t.Fatalf("NewBroadcastEvent().Type = %q, want run.completed", frame.Type)
	}
	if !frame.CreatedAt.Equal(stamp) {
		t.Fatalf("NewBroadcastEvent().CreatedAt = %v, want %v", frame.CreatedAt, stamp)
	}
	if frame.ActorType != string(events.ActorTypeUser) {
		t.Fatalf("NewBroadcastEvent().ActorType = %q, want user", frame.ActorType)
	}
	if frame.ActorID == nil || *frame.ActorID != "user-1" {
		t.Fatalf("NewBroadcastEvent().ActorID = %v, want user-1", frame.ActorID)
	}
	// The tenant is on the frame, so a client watching more than one company
	// knows which one a fact belongs to without reading the payload.
	if frame.CompanyID == nil || *frame.CompanyID != companyID.String() {
		t.Fatalf("NewBroadcastEvent().CompanyID = %v, want %s", frame.CompanyID, companyID)
	}

	// The routing field drove nothing here, but it must not reach the wire.
	encoded, err := json.Marshal(frame.Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if got := string(encoded); got != `{"id":"00000000-0000-4000-8000-000000000007","companyId":"company-1"}` {
		t.Fatalf("payload on the wire = %s, want the routing field excluded", got)
	}
}

func TestSystemEventsCarryNoActorID(t *testing.T) {
	t.Parallel()
	frame := NewBroadcastEvent(events.Event{
		Name:      "run.completed",
		CreatedAt: time.Now(),
		Actor:     events.SystemActor(),
		Payload:   struct{}{},
	})
	// nil, so the wire field is JSON null rather than an ambiguous "".
	if frame.ActorID != nil {
		t.Fatalf("NewBroadcastEvent().ActorID = %v, want nil for a system actor", frame.ActorID)
	}
	// The same rule for the tenant: a fact that follows a person across every
	// company they belong to names none.
	if frame.CompanyID != nil {
		t.Fatalf("NewBroadcastEvent().CompanyID = %v, want nil for a personal fact", frame.CompanyID)
	}
}

// unroutable answers no rule at all.
