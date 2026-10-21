package company

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
)

func TestNewCompanyServiceNeedsNoAccessVersionAccelerator(t *testing.T) {
	t.Parallel()

	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewCompanyService(ServiceOptions{Store: &db.Store{}, Bus: bus})
	if err != nil {
		t.Fatalf("NewCompanyService() error = %v", err)
	}
	if service == nil {
		t.Fatal("NewCompanyService() returned nil")
	}
}

// membershipFact is what a subscriber to one membership frame learns: the
// event's name and actor, the payload, and the ids the realtime bridge routes
// on. Audience is not a property of an event any more, so a routing
// expectation is an expectation about the payload.
type membershipFact struct {
	name      string
	actor     events.Actor
	companyID uuid.UUID
	userID    uuid.UUID
	payload   any
}

type membershipFactLog struct {
	mu    sync.Mutex
	facts []membershipFact
}

func (l *membershipFactLog) add(fact membershipFact) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.facts = append(l.facts, fact)
}

func (l *membershipFactLog) snapshot() []membershipFact {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]membershipFact(nil), l.facts...)
}

// recordMembershipFacts subscribes to both topics stageMembershipEvents
// publishes on, so one bus records the pair in publication order whichever
// outcome produced it. Each subscription switches on the event name, exactly
// as a real consumer does.
func recordMembershipFacts(t *testing.T, bus *events.Bus) *membershipFactLog {
	t.Helper()
	log := &membershipFactLog{}
	subscribe(t, bus, TopicMember, func(_ context.Context, event events.Event) error {
		switch event.Name {
		case EventMemberAdded, EventMemberUpdated:
			payload, err := events.PayloadOf[ActiveMemberResponse](event)
			if err != nil {
				return err
			}
			log.add(membershipFact{
				name: event.Name, actor: event.Actor,
				companyID: payload.CompanyID, userID: payload.Actor.ID,
				payload: payload,
			})
		case EventMemberRemoved:
			payload, err := events.PayloadOf[MemberRemovalReceipt](event)
			if err != nil {
				return err
			}
			log.add(membershipFact{
				name: event.Name, actor: event.Actor,
				companyID: payload.CompanyID, userID: payload.UserID,
				payload: payload,
			})
		}
		return nil
	})
	subscribe(t, bus, TopicAuth, func(_ context.Context, event events.Event) error {
		switch event.Name {
		case EventAuthCompanyAccessChanged:
			payload, err := events.PayloadOf[AccessChangedReceipt](event)
			if err != nil {
				return err
			}
			log.add(membershipFact{
				name: event.Name, actor: event.Actor,
				companyID: payload.CompanyID, userID: payload.UserID,
				payload: payload,
			})
		case EventAuthCompanyAccessRevoked:
			payload, err := events.PayloadOf[AccessRevokedReceipt](event)
			if err != nil {
				return err
			}
			log.add(membershipFact{
				name: event.Name, actor: event.Actor,
				companyID: payload.CompanyID, userID: payload.UserID,
				payload: payload,
			})
		}
		return nil
	})
	return log
}

func subscribe(
	t *testing.T,
	bus *events.Bus,
	topic string,
	handler func(context.Context, events.Event) error,
) {
	t.Helper()
	if err := bus.Subscribe(topic, handler); err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
}

func TestStageMembershipEventsPairsTheCompanyFrameWithThePersonalOne(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Microsecond)
	companyID := uuid.Must(uuid.NewV7())
	memberID := uuid.Must(uuid.NewV7())
	actorID := uuid.Must(uuid.NewV7())
	member := MemberResponse{
		CompanyID: companyID,
		UserID:    memberID,
		Role:      auth.CompanyRoleMember,
		Status:    MembershipStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
		Revision:  3,
	}
	active := activeMemberResponse(member, "member@example.com", "Member", nil, false)

	tests := []struct {
		name               string
		outcome            MembershipOutcome
		wantCompanyName    string
		wantCompanyPayload any
		wantAccessName     string
		wantAccessPayload  any
	}{
		{
			name:               "grant",
			outcome:            MembershipGranted,
			wantCompanyName:    "member.added",
			wantCompanyPayload: active,
			wantAccessName:     "auth.company_access_changed",
			wantAccessPayload: AccessChangedReceipt{
				CompanyID:   companyID,
				Role:        auth.CompanyRoleMember,
				Permissions: auth.PermissionsOf(auth.CompanyRoleMember),
				Reason:      AccessReasonMembershipGranted,
				UserID:      memberID,
			},
		},
		{
			name:               "reactivation",
			outcome:            MembershipReactivated,
			wantCompanyName:    "member.added",
			wantCompanyPayload: active,
			wantAccessName:     "auth.company_access_changed",
			wantAccessPayload: AccessChangedReceipt{
				CompanyID:   companyID,
				Role:        auth.CompanyRoleMember,
				Permissions: auth.PermissionsOf(auth.CompanyRoleMember),
				Reason:      AccessReasonMembershipReactivated,
				UserID:      memberID,
			},
		},
		{
			name:               "role change",
			outcome:            MembershipRoleChanged,
			wantCompanyName:    "member.updated",
			wantCompanyPayload: active,
			wantAccessName:     "auth.company_access_changed",
			wantAccessPayload: AccessChangedReceipt{
				CompanyID:   companyID,
				Role:        auth.CompanyRoleMember,
				Permissions: auth.PermissionsOf(auth.CompanyRoleMember),
				Reason:      AccessReasonRoleChanged,
				UserID:      memberID,
			},
		},
		{
			name:               "removal",
			outcome:            MembershipRemoved,
			wantCompanyName:    "member.removed",
			wantCompanyPayload: MemberRemovalReceipt{UserID: memberID, Revision: 3, CompanyID: companyID},
			wantAccessName:     "auth.company_access_revoked",
			wantAccessPayload: AccessRevokedReceipt{
				CompanyID: companyID,
				Reason:    AccessReasonMembershipRemoved,
				UserID:    memberID,
			},
		},
		{
			name:               "leave",
			outcome:            MembershipLeft,
			wantCompanyName:    "member.removed",
			wantCompanyPayload: MemberRemovalReceipt{UserID: memberID, Revision: 3, CompanyID: companyID},
			wantAccessName:     "auth.company_access_revoked",
			wantAccessPayload: AccessRevokedReceipt{
				CompanyID: companyID,
				Reason:    AccessReasonMembershipLeft,
				UserID:    memberID,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			bus := companyTestBus(t)
			log := recordMembershipFacts(t, bus)

			var pending []events.Event
			if err := stageMembershipEvents(&pending, actorID, member, active, test.outcome); err != nil {
				t.Fatalf("stageMembershipEvents() error = %v", err)
			}
			if len(pending) != 2 {
				t.Fatalf("event events = %d, want 2", len(pending))
			}
			for _, group := range events.GroupByTopic(pending) {
				bus.Publish(context.Background(), group.Topic, group.Events...)
			}

			published := log.snapshot()
			if len(published) != 2 {
				t.Fatalf(
					"published events = %d, want the Company frame and its personal twin",
					len(published),
				)
			}

			companyFact := published[0]
			if companyFact.name != test.wantCompanyName ||
				companyFact.actor != events.UserActor(actorID.String()) ||
				companyFact.payload != test.wantCompanyPayload {
				t.Fatalf("company event = %#v", companyFact)
			}
			// The Company frame is delivered to the Company; a grant and a
			// reactivation also have to reach a member who may not be in that
			// room yet, which is why the member is named too.
			if companyFact.companyID != companyID || companyFact.userID != memberID {
				t.Fatalf("company routing = (%v, %v)", companyFact.companyID, companyFact.userID)
			}

			accessFact := published[1]
			if accessFact.name != test.wantAccessName ||
				accessFact.actor != events.UserActor(actorID.String()) ||
				!reflect.DeepEqual(accessFact.payload, test.wantAccessPayload) {
				t.Fatalf("access event = %#v", accessFact)
			}
			// The personal frame belongs to the affected user alone.
			if accessFact.userID != memberID {
				t.Fatalf("access routing user = %v, want %v", accessFact.userID, memberID)
			}
		})
	}
}

func TestStageMembershipEventsDoesNothingForAnUnchangedMembership(t *testing.T) {
	t.Parallel()

	var pending []events.Event
	err := stageMembershipEvents(
		&pending,
		uuid.Must(uuid.NewV7()),
		MemberResponse{CompanyID: uuid.Must(uuid.NewV7()), UserID: uuid.Must(uuid.NewV7())},
		ActiveMemberResponse{},
		MembershipUnchanged,
	)
	if err != nil || len(pending) != 0 {
		t.Fatalf("stageMembershipEvents() events = %d, error = %v", len(pending), err)
	}
}

func TestStageOnboardingEventsCarriesTheCanonicalCompanyProjection(t *testing.T) {
	t.Parallel()

	ownerID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Microsecond)
	member := MemberResponse{
		CompanyID: companyID,
		UserID:    ownerID,
		Role:      auth.CompanyRoleOwner,
		Status:    MembershipStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
		Revision:  1,
	}
	active := activeMemberResponse(member, "owner@example.com", "Owner", nil, false)
	company := Response{
		ID:        companyID,
		Name:      "Zenith Team",
		Role:      auth.CompanyRoleOwner,
		Revision:  1,
		CreatedAt: now,
		UpdatedAt: now,
	}

	bus := companyTestBus(t)
	received := recordTopic(t, bus, Topic)

	var pending []events.Event
	if err := stageOnboardingEvents(
		&pending,
		ownerID,
		member,
		active,
		company,
	); err != nil {
		t.Fatalf("stageOnboardingEvents() error = %v", err)
	}
	if len(pending) != 4 {
		t.Fatalf("event events = %d, want 4", len(pending))
	}
	for _, group := range events.GroupByTopic(pending) {
		bus.Publish(context.Background(), group.Topic, group.Events...)
	}

	published := received.snapshot()
	if len(published) != 2 {
		t.Fatalf("published Company events = %d, want creation and membership wakeup", len(published))
	}
	// The company room is empty at creation — nobody has connected under a
	// company that did not exist a moment ago — so the receipt names the
	// founder, whose own room it also has to reach.
	want := company
	want.FounderUserID = ownerID
	created := published[0]
	if created.Name != "company.created" || !reflect.DeepEqual(created.Payload, want) ||
		created.Actor != events.UserActor(ownerID.String()) {
		t.Fatalf("company.created = %#v", created)
	}
	if _, onTheWire := marshalledPayload(t, created.Payload)["founderUserId"]; onTheWire {
		t.Fatal("company.created published the founder routing fact on the wire")
	}
	wakeup := published[1]
	if wakeup.Name != EventCompanyMembershipChanged ||
		wakeup.Payload != (Membership{UserID: ownerID.String()}) ||
		wakeup.Actor != events.UserActor(ownerID.String()) {
		t.Fatalf("company.membership_changed = %#v", wakeup)
	}
	wakeupPayload := marshalledPayload(t, wakeup.Payload)
	if _, exists := wakeupPayload["userId"]; !exists {
		t.Fatalf("company.membership_changed payload = %v, want camelCase userId", wakeupPayload)
	}
	if _, exists := wakeupPayload["UserID"]; exists {
		t.Fatalf("company.membership_changed payload leaked Go field name: %v", wakeupPayload)
	}
}

func stringPointer(value string) *string {
	return &value
}
