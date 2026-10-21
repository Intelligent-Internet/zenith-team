package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/calendar"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	"github.com/Intelligent-Internet/zenith-team/server/internal/mailbox"
	"github.com/Intelligent-Internet/zenith-team/server/internal/realtime"
	"github.com/Intelligent-Internet/zenith-team/server/internal/schedule"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// recordingBroadcaster records which room each frame was addressed to, so a
// test reads the routing decision without a socket.
type recordingBroadcaster struct {
	// mu guards the slices: a deferred delivery records from its own goroutine.
	mu           sync.Mutex
	sent         []string
	frames       []realtime.BroadcastEvent
	disconnected []string
	goodbyes     []realtime.BroadcastEvent
	batches      []int
}

func (r *recordingBroadcaster) BroadcastToCompany(companyID string, event realtime.BroadcastEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, "company/"+companyID)
	r.frames = append(r.frames, event)
	return nil
}

func (r *recordingBroadcaster) BroadcastToUser(userID string, event realtime.BroadcastEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, "user/"+userID)
	r.frames = append(r.frames, event)
	return nil
}

func (r *recordingBroadcaster) BroadcastToCompanyUser(
	companyID,
	userID string,
	event realtime.BroadcastEvent,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, "company_user/"+companyID+"/"+userID)
	r.frames = append(r.frames, event)
	return nil
}

func (r *recordingBroadcaster) BroadcastToCompanyOrUser(
	companyID,
	userID string,
	event realtime.BroadcastEvent,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, "company_or_user/"+companyID+"/"+userID)
	r.frames = append(r.frames, event)
	return nil
}

// BroadcastToCompanyUsers records one company_user room per user, like the
// single-user method, and the batch it came in, so a test sees both who got
// the frame and that the router kept to the 50-user bound.
func (r *recordingBroadcaster) BroadcastToCompanyUsers(
	_ context.Context,
	companyID string,
	userIDs []string,
	event realtime.BroadcastEvent,
) ([]realtime.RelayBatchResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches = append(r.batches, len(userIDs))
	results := make([]realtime.RelayBatchResult, len(userIDs))
	for index, userID := range userIDs {
		r.sent = append(r.sent, "company_user/"+companyID+"/"+userID)
		r.frames = append(r.frames, event)
		results[index].EventID = realtime.NewEventID()
	}
	return results, nil
}

func routedBus(t *testing.T) (*events.Bus, *recordingBroadcaster) {
	t.Helper()
	bus, err := events.NewBus(events.BusOptions{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	recorder := &recordingBroadcaster{}
	if err := registerRealtimeRouting(bus, recorder, whiteboardHintRouting{}); err != nil {
		t.Fatalf("registerRealtimeRouting() error = %v", err)
	}
	return bus, recorder
}

func publish(t *testing.T, bus *events.Bus, companyID uuid.UUID, topic, name string, payload any) {
	t.Helper()
	bus.Publish(context.Background(), topic, events.Event{
		EntityType: topic,
		Name:       name,
		CompanyID:  companyID,
		CreatedAt:  time.Now().UTC(),
		Actor:      events.SystemActor(),
		Payload:    payload,
	})
}

func TestHuddleInvitationChangesRouteOnlyToInvitee(t *testing.T) {
	t.Parallel()
	bus, recorder := routedBus(t)
	companyID, inviteeID := uuid.New(), uuid.New()
	for _, name := range []string{conversation.EventHuddleInvitationCreated, conversation.EventHuddleInvitationChanged} {
		publish(t, bus, companyID, conversation.TopicHuddle, name, conversation.HuddleInvitationEvent{
			Invitation:      conversation.HuddleInvitation{ID: uuid.New(), InviteeUserID: inviteeID},
			RecipientUserID: inviteeID,
		})
	}
	want := "company_user/" + companyID.String() + "/" + inviteeID.String()
	if len(recorder.sent) != 2 {
		t.Fatalf("routes = %v, want two invitee routes", recorder.sent)
	}
	for _, got := range recorder.sent {
		if got != want {
			t.Errorf("route = %q, want %q", got, want)
		}
	}
}

func TestHuddleRosterChangesRouteOnlyToCallMembers(t *testing.T) {
	t.Parallel()
	bus, recorder := routedBus(t)
	companyID, first, second := uuid.New(), uuid.New(), uuid.New()
	publish(
		t,
		bus,
		companyID,
		conversation.TopicHuddle,
		conversation.EventHuddleRosterChanged,
		conversation.HuddleRosterChangedEvent{
			HuddleID:         uuid.New(),
			RecipientUserIDs: []uuid.UUID{first, second},
		},
	)
	want := []string{
		"company_user/" + companyID.String() + "/" + first.String(),
		"company_user/" + companyID.String() + "/" + second.String(),
	}
	if !slices.Equal(recorder.sent, want) {
		t.Fatalf("routes = %v, want %v", recorder.sent, want)
	}
}

func TestHuddleStatusChangesRouteOnlyToCallMembers(t *testing.T) {
	t.Parallel()
	bus, recorder := routedBus(t)
	companyID, member := uuid.New(), uuid.New()
	publish(
		t,
		bus,
		companyID,
		conversation.TopicHuddle,
		conversation.EventHuddleStatusChanged,
		conversation.HuddleStatusChangedEvent{
			HuddleID:         uuid.New(),
			RecipientUserIDs: []uuid.UUID{member},
		},
	)
	want := []string{"company_user/" + companyID.String() + "/" + member.String()}
	if !slices.Equal(recorder.sent, want) {
		t.Fatalf("routes = %v, want %v", recorder.sent, want)
	}
}

func TestSharedAuthTopicRoutesKnownEventsWithoutHandlerErrors(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	tests := []struct {
		name    string
		event   string
		payload any
	}{
		{
			name:  "session revoked",
			event: auth.EventSessionRevoked,
			payload: auth.RevokedSession{
				SessionID: uuid.New(), UserID: userID,
			},
		},
		{
			name:  "company access changed",
			event: company.EventAuthCompanyAccessChanged,
			payload: company.AccessChangedReceipt{
				CompanyID: uuid.New(), UserID: userID,
			},
		},
		{
			name:  "company access revoked",
			event: company.EventAuthCompanyAccessRevoked,
			payload: company.AccessRevokedReceipt{
				CompanyID: uuid.New(), UserID: userID,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			bus, err := events.NewBus(events.BusOptions{
				Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
			})
			if err != nil {
				t.Fatalf("NewBus() error = %v", err)
			}
			recorder := &recordingBroadcaster{}
			if err := registerRealtimeRouting(bus, recorder, whiteboardHintRouting{}); err != nil {
				t.Fatalf("registerRealtimeRouting() error = %v", err)
			}

			publish(t, bus, uuid.Nil, auth.Topic, test.event, test.payload)

			want := "user/" + userID.String()
			if len(recorder.sent) != 1 || recorder.sent[0] != want {
				t.Fatalf("rooms = %v, want [%s]", recorder.sent, want)
			}
			if test.event == company.EventAuthCompanyAccessRevoked {
				receipt, ok := test.payload.(company.AccessRevokedReceipt)
				if !ok {
					t.Fatal("invalid revocation fixture")
				}
				if len(recorder.disconnected) != 1 ||
					recorder.disconnected[0] != receipt.CompanyID.String()+"/"+userID.String() {
					t.Fatalf("disconnected = %v", recorder.disconnected)
				}
				// The user frame and the goodbye are one fact: a socket in
				// both audiences dedupes on the id, so both must carry it.
				if recorder.frames[0].EventID == "" ||
					recorder.goodbyes[0].EventID != recorder.frames[0].EventID {
					t.Fatalf("revocation ids differ: user %q goodbye %q",
						recorder.frames[0].EventID, recorder.goodbyes[0].EventID)
				}
			}
			if logs.Len() != 0 {
				t.Fatalf("auth routing logged a handler failure: %s", logs.String())
			}
		})
	}
}

// A run on a task conversation reaches the readers of the chat's Project
// (here a Company-visible one, so the Company room); a direct one belongs to
// the member who opened it. Kind decides, not the task id — a plan apply that
// deletes the task nulls taskId while the conversation keeps kind 'task'.
func TestRunRoutingFollowsTheConversationKind(t *testing.T) {
	t.Parallel()
	bus, recorder := routedBus(t)
	companyID, member := uuid.New(), uuid.New()

	publish(t, bus, companyID, execution.Topic, execution.EventRunCompleted, execution.RunSnapshot{
		CompanyID: companyID,
		ConversationRoute: execution.ConversationRoute{
			ConversationKind: "task", ConversationCreatedBy: &member,
			ProjectFrame: auth.ProjectFrame{Audience: &auth.ProjectAudience{CompanyWide: true}},
		},
	})
	publish(t, bus, companyID, execution.Topic, execution.EventRunCompleted, execution.RunSnapshot{
		CompanyID: companyID,
		ConversationRoute: execution.ConversationRoute{
			ConversationKind: string(conversation.KindDirect), ConversationCreatedBy: &member,
		},
	})

	want := []string{
		"company/" + companyID.String(),
		"company_user/" + companyID.String() + "/" + member.String(),
	}
	if len(recorder.sent) != 2 || recorder.sent[0] != want[0] || recorder.sent[1] != want[1] {
		t.Fatalf("rooms = %v, want %v", recorder.sent, want)
	}
}

func TestCapabilitiesRoutingIsPrivateAndSkipsDaemonWakeups(t *testing.T) {
	bus, recorder := routedBus(t)
	company, user, runtimeID := uuid.New(), uuid.New(), uuid.New()
	publish(t, bus, company, agent.TopicRuntime, agent.EventRuntimePendingWork, agent.PendingWork{})
	publish(t, bus, company, execution.TopicRuntime, execution.EventRuntimeWorkAvailable, execution.RuntimeWork{})
	publish(t, bus, company, agent.TopicRuntime, agent.EventRuntimeCapabilitiesChanged,
		agent.CapabilitiesChanged{UserID: user, RuntimeID: runtimeID, Catalog: "connector"})
	if len(recorder.sent) != 1 || recorder.sent[0] != "company_user/"+company.String()+"/"+user.String() {
		t.Fatalf("capability recipients = %v", recorder.sent)
	}
	raw, err := json.Marshal(recorder.frames[0])
	if err != nil || bytes.Contains(raw, []byte(user.String())) ||
		!bytes.Contains(raw, []byte(runtimeID.String())) {
		t.Fatalf("capability frame = %s, %v", raw, err)
	}
}

func TestConnectorPolicyHintReachesTheCompanyWithoutRuntimeDetails(t *testing.T) {
	t.Parallel()
	bus, recorder := routedBus(t)
	companyID := uuid.New()
	publish(t, bus, companyID, "connector", "connector.policy_changed", struct{}{})
	if len(recorder.sent) != 1 || recorder.sent[0] != "company/"+companyID.String() {
		t.Fatalf("policy hint recipients = %v, want the Company", recorder.sent)
	}
	raw, err := json.Marshal(recorder.frames[0].Payload)
	if err != nil || string(raw) != "{}" {
		t.Fatalf("policy hint payload = %s, %v, want {}", raw, err)
	}
}

func TestScheduleCalendarAndWikiHintsRespectTheirReadScopes(t *testing.T) {
	company, user := uuid.New(), uuid.New()
	cases := []struct {
		topic, name, room string
		payload           any
	}{
		{
			schedule.Topic,
			schedule.EventChanged,
			"company_user/" + company.String() + "/" + user.String(),
			schedule.Changed{ID: uuid.New(), CreatorUserID: user},
		},
		{calendar.Topic, calendar.EventChanged, "user/" + user.String(), calendar.Changed{UserID: user}},
		{
			calendar.Topic,
			calendar.EventChanged,
			"company/" + company.String(),
			calendar.Changed{CompanyWide: true},
		},
		// A Task Run's evidence is work inside a Project (ADR 0017): a
		// Company-visible Project's readers are everyone, a restricted one's
		// are only the people its producer named.
		{
			wiki.TopicWiki,
			wiki.EventRunReadsChanged,
			"company/" + company.String(),
			wiki.RunReadsChanged{
				ProjectFrame:     auth.ProjectFrame{Audience: &auth.ProjectAudience{CompanyWide: true}},
				RunID:            uuid.New(),
				ConversationKind: "task",
			},
		},
		{
			wiki.TopicWiki,
			wiki.EventRunReadsChanged,
			"company_user/" + company.String() + "/" + user.String(),
			wiki.RunReadsChanged{
				ProjectFrame: auth.ProjectFrame{
					Audience: &auth.ProjectAudience{UserIDs: []uuid.UUID{user}},
				},
				RunID:            uuid.New(),
				ConversationKind: "task",
			},
		},
		{
			wiki.TopicWiki,
			wiki.EventRunReadsChanged,
			"company_user/" + company.String() + "/" + user.String(),
			wiki.RunReadsChanged{
				RunID:                 uuid.New(),
				ConversationKind:      "direct",
				ConversationCreatedBy: &user,
			},
		},
	}
	for _, c := range cases {
		bus, recorder := routedBus(t)
		publish(t, bus, company, c.topic, c.name, c.payload)
		if len(recorder.sent) != 1 || recorder.sent[0] != c.room {
			t.Fatalf("%s audience = %v, want %s", c.name, recorder.sent, c.room)
		}
		raw, err := json.Marshal(recorder.frames[0].Payload)
		if err != nil || bytes.Contains(raw, []byte(user.String())) {
			t.Fatalf("routing fields exposed: %s, %v", raw, err)
		}
	}
}

// Inbox events reach the recipient across their company connections.
func TestInboxRoutingReachesOnlyItsRecipient(t *testing.T) {
	t.Parallel()
	for _, name := range []string{inbox.EventItemCreated, inbox.EventItemUpdated, inbox.EventNotificationIssued} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bus, recorder := routedBus(t)
			companyID, recipient := uuid.New(), uuid.New()
			topic := inbox.Topic
			var payload any = inbox.ItemWithCounts{Item: inbox.Item{CompanyID: &companyID, RecipientUserID: recipient}}
			if name == inbox.EventNotificationIssued {
				topic = inbox.TopicNotification
				payload = inbox.ToastHint{RecipientUserID: recipient}
			}
			publish(t, bus, companyID, topic, name, payload)
			want := "user/" + recipient.String()
			if len(recorder.sent) != 1 || recorder.sent[0] != want {
				t.Fatalf("rooms = %v, want [%s]", recorder.sent, want)
			}
			if recorder.frames[0].CompanyID == nil || *recorder.frames[0].CompanyID != companyID.String() {
				t.Fatal("notification lost its origin company")
			}
		})
	}
}

func TestDeliverableRoutingInvalidatesOnlyArtifactRecipients(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		execution.EventDeliverableCreated, execution.EventDeliverableReviewed, execution.EventDeliverableSuperseded,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bus, recorder := routedBus(t)
			companyID, ownerID, viewerID := uuid.New(), uuid.New(), uuid.New()
			bus.Publish(context.Background(), execution.TopicDeliverable, events.Event{
				EntityType: execution.TopicDeliverable, Name: name, CompanyID: companyID,
				CreatedAt: time.Now().UTC(), Actor: events.UserActor(ownerID.String()),
				Payload: execution.DeliverableEvent{
					ID:               uuid.New(),
					RunID:            uuid.New(),
					Status:           execution.DeliverableStatusApproved,
					ItemCount:        2,
					RecipientUserIDs: []uuid.UUID{ownerID, viewerID, ownerID, uuid.Nil},
				},
			})
			want := []string{
				"company_user/" + companyID.String() + "/" + ownerID.String(),
				"company_user/" + companyID.String() + "/" + viewerID.String(),
			}
			if len(recorder.sent) != len(want) {
				t.Fatalf("rooms = %v, want %v", recorder.sent, want)
			}
			for index, room := range want {
				if recorder.sent[index] != room {
					t.Fatalf("room = %s, want %s", recorder.sent[index], room)
				}
				frame := recorder.frames[index]
				payload, err := json.Marshal(frame.Payload)
				if err != nil || string(payload) != "{}" || frame.ActorID != nil ||
					frame.ActorType != "system" {
					t.Fatalf(
						"private metadata reached browser: %+v, payload %s, error %v",
						frame,
						payload,
						err,
					)
				}
			}
			publish(t, bus, companyID, execution.TopicDeliverable, name, execution.DeliverableEvent{})
			if len(recorder.sent) != len(want) {
				t.Fatalf("unowned artifact broadcast to %v", recorder.sent)
			}
		})
	}
}

func TestArtifactReviewInboxDoesNotBroadcastPrivateMetadata(t *testing.T) {
	t.Parallel()
	bus, recorder := routedBus(t)
	companyID, recipient := uuid.New(), uuid.New()
	publish(t, bus, companyID, inbox.Topic, inbox.EventItemCreated,
		inbox.ItemWithCounts{Item: inbox.Item{
			CompanyID: &companyID, RecipientUserID: recipient,
			EntityType: protocol.EntityDeliverable, Title: "Private artifact",
		}})
	publish(t, bus, companyID, inbox.TopicNotification, inbox.EventNotificationIssued,
		inbox.ToastHint{
			RecipientUserID: recipient, EntityType: protocol.EntityDeliverable,
			Title: "Private artifact",
		})
	if len(recorder.sent) != 0 {
		t.Fatalf("Artifact review metadata broadcast to %v", recorder.sent)
	}
}

func TestArtifactChangeRoutingOnlyInvalidatesAuthorizedRecipients(t *testing.T) {
	bus, recorder := routedBus(t)
	company, owner := uuid.New(), uuid.New()
	publish(t, bus, company, execution.TopicArtifact, execution.EventArtifactChanged,
		execution.ArtifactChanged{RecipientUserIDs: []uuid.UUID{owner, owner, uuid.Nil}})
	if len(recorder.sent) != 1 || recorder.sent[0] != "company_user/"+company.String()+"/"+owner.String() {
		t.Fatalf("artifact recipients = %v", recorder.sent)
	}
	raw, err := json.Marshal(recorder.frames[0].Payload)
	if err != nil || string(raw) != "{}" || recorder.frames[0].ActorID != nil {
		t.Fatalf("artifact hint exposed metadata: %s, %v", raw, err)
	}
}

func TestMailboxRoutingReachesOnlyAuthorizedOwners(t *testing.T) {
	t.Parallel()
	bus, recorder := routedBus(t)
	companyID, creatorID := uuid.New(), uuid.New()
	taskGroupID, taskDraftID := uuid.New(), uuid.New()
	directGroupID, directDraftID := uuid.New(), uuid.New()

	taskDraft, err := mailbox.NewDraftChangedPayload(
		taskGroupID,
		&taskDraftID,
		mailbox.DraftWireStatusDraft,
		1,
		mailbox.DraftChangeReasonCreated,
		string(conversation.KindTask),
		nil,
	)
	if err != nil {
		t.Fatalf("NewDraftChangedPayload(task) error = %v", err)
	}
	// The producer intersects Project readers with Mailbox permission holders.
	taskDraft.Audience = &auth.ProjectAudience{UserIDs: []uuid.UUID{creatorID}}
	directDraft, err := mailbox.NewDraftChangedPayload(
		directGroupID,
		&directDraftID,
		mailbox.DraftWireStatusDraft,
		1,
		mailbox.DraftChangeReasonCreated,
		string(conversation.KindDirect),
		&creatorID,
	)
	if err != nil {
		t.Fatalf("NewDraftChangedPayload(direct) error = %v", err)
	}

	directDraft.Audience = &auth.ProjectAudience{UserIDs: []uuid.UUID{creatorID}}
	publish(t, bus, companyID, mailbox.Topic, mailbox.EventChanged, mailbox.ChangedPayload{
		Reason: mailbox.ChangeReasonConfigured, RecipientUserIDs: []uuid.UUID{creatorID},
	})
	publish(t, bus, companyID, mailbox.Topic, mailbox.EventDraftChanged, taskDraft)
	publish(t, bus, companyID, mailbox.Topic, mailbox.EventDraftChanged, directDraft)
	publish(t, bus, companyID, mailbox.Topic, "mailbox.internal_sync", struct{}{})
	// Missing permission audiences never fall back to the Company or creator.
	publish(
		t,
		bus,
		companyID,
		mailbox.Topic,
		mailbox.EventChanged,
		mailbox.ChangedPayload{Reason: mailbox.ChangeReasonConfigured},
	)
	directDraft.Audience = nil
	publish(t, bus, companyID, mailbox.Topic, mailbox.EventDraftChanged, directDraft)
	taskDraft.Audience = &auth.ProjectAudience{CompanyWide: true}
	publish(t, bus, companyID, mailbox.Topic, mailbox.EventDraftChanged, taskDraft)

	want := []string{
		"company_user/" + companyID.String() + "/" + creatorID.String(),
		"company_user/" + companyID.String() + "/" + creatorID.String(),
		"company_user/" + companyID.String() + "/" + creatorID.String(),
	}
	if len(recorder.sent) != len(want) {
		t.Fatalf("rooms = %v, want %v", recorder.sent, want)
	}
	for i := range want {
		if recorder.sent[i] != want[i] {
			t.Fatalf("rooms[%d] = %q, want %q", i, recorder.sent[i], want[i])
		}
	}

	encoded, err := json.Marshal(recorder.frames[2])
	if err != nil {
		t.Fatalf("marshal direct draft frame: %v", err)
	}
	if strings.Contains(string(encoded), "conversationKind") ||
		strings.Contains(string(encoded), creatorID.String()) {
		t.Fatalf("draft frame exposed internal routing facts: %s", encoded)
	}
}

// The bus carries internal facts on the same topics as browser ones. A wakeup
// fact must reach no browser at all.
func TestInternalFactsReachNoBrowser(t *testing.T) {
	t.Parallel()
	bus, recorder := routedBus(t)

	publish(t, bus, uuid.New(), execution.TopicRuntime, execution.EventRuntimeWorkAvailable,
		execution.RuntimeWork{RuntimeID: uuid.New().String()})

	if len(recorder.sent) != 0 {
		t.Fatalf("rooms = %v, want none — a wakeup fact is not a browser frame", recorder.sent)
	}
}

// A payload of the wrong shape must be reported, not skipped: the consumer
// returns the error and the bus logs it against the subscription site.
func TestAWrongShapeIsReportedRatherThanSkipped(t *testing.T) {
	t.Parallel()
	bus, recorder := routedBus(t)

	publish(t, bus, uuid.New(), inbox.Topic, inbox.EventItemCreated, struct{ Nonsense string }{"x"})

	if len(recorder.sent) != 0 {
		t.Fatalf("rooms = %v, want none for an unreadable payload", recorder.sent)
	}
}

// A Company-wide fact has no routing decision left: the envelope names the
// company and the frame goes to its room. A Task in a Company-visible Project
// is one. This publishes a payload whose own company field is empty, so a
// consumer that went back to reading the payload would address the nil
// company and fail here.
func TestACompanyWideFactRoutesOffTheEnvelope(t *testing.T) {
	t.Parallel()
	bus, recorder := routedBus(t)
	companyID := uuid.New()

	publish(t, bus, companyID, task.Topic, task.EventCreated, task.Response{
		ProjectFrame: auth.ProjectFrame{Audience: &auth.ProjectAudience{CompanyWide: true}},
	})

	want := "company/" + companyID.String()
	if len(recorder.sent) != 1 || recorder.sent[0] != want {
		t.Fatalf("rooms = %v, want [%s]", recorder.sent, want)
	}
}

// company.membership_changed rides the company topic but asks a user's
// daemons to resync; it belongs to no company and reaches no browser. Routing
// it like its topic-mates would address the nil company on every account
// deletion.
func TestTheMembershipWakeupReachesNoBrowser(t *testing.T) {
	t.Parallel()
	bus, recorder := routedBus(t)

	publish(t, bus, uuid.Nil, company.Topic, company.EventCompanyMembershipChanged,
		company.Membership{UserID: uuid.New().String()})

	if len(recorder.sent) != 0 {
		t.Fatalf("rooms = %v, want none — the wakeup is a daemon fact", recorder.sent)
	}
}

func TestActivityRoutingUsesFrozenVisibilityWithoutLeakingRoutingFields(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		private bool
	}{{"Task", false}, {"Direct", true}, {"Schedule", true}, {"Task pin", true}, {"deleted private subject", true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bus, recorder := routedBus(t)
			companyID, creatorID := uuid.New(), uuid.New()
			entry := activity.Entry{
				CompanyID: companyID,
				ID:        uuid.NewString(),
				Action:    "conversation.updated",
				Details:   json.RawMessage(`{"schemaVersion":1,"before":null,"after":{}}`),
			}
			want := "company/" + companyID.String()
			if tc.private {
				entry.VisibilityUserID = uuid.NullUUID{UUID: creatorID, Valid: true}
				want = "company_user/" + companyID.String() + "/" + creatorID.String()
			}
			publish(t, bus, companyID, activity.Topic, activity.EventCreated, entry)
			if len(recorder.sent) != 1 || recorder.sent[0] != want {
				t.Fatalf("Activity route = %v, want %s", recorder.sent, want)
			}
			encoded, err := json.Marshal(recorder.frames[0])
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "visibilityUserId") ||
				strings.Contains(string(encoded), creatorID.String()) {
				t.Fatalf("internal routing leaked: %s", encoded)
			}
		})
	}
}

func (r *recordingBroadcaster) DisconnectCompanyUser(companyID, userID string, event realtime.BroadcastEvent) error {
	r.disconnected = append(r.disconnected, companyID+"/"+userID)
	r.goodbyes = append(r.goodbyes, event)
	return nil
}

// A member's binding and the availability it yields are theirs alone
// (ADR 0014): both frames reach the binder's own sockets in the company and
// carry no routing field; the Agent lifecycle stays company-wide.
func TestAgentBindingAndStatusFramesReachOnlyTheBinder(t *testing.T) {
	t.Parallel()
	companyID, binder, agentID := uuid.New(), uuid.New(), uuid.New()
	for _, test := range []struct {
		name    string
		payload any
		wantKey string
	}{
		{
			name:    agent.EventAgentBindingChanged,
			payload: agent.BindingChange{AgentID: agentID, Binding: nil, CompanyID: companyID, UserID: binder},
			wantKey: `"binding":null`,
		},
		{
			name: agent.EventAgentStatusChanged,
			payload: agent.AvailabilityChange{
				Agents: []agent.Availability{{AgentID: agentID, RuntimeAvailable: false}}, CompanyID: companyID, UserID: binder,
			},
			wantKey: `"runtimeAvailable":false`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			bus, recorder := routedBus(t)
			publish(t, bus, companyID, agent.Topic, test.name, test.payload)
			want := "company_user/" + companyID.String() + "/" + binder.String()
			if len(recorder.sent) != 1 || recorder.sent[0] != want {
				t.Fatalf("rooms = %v, want [%s]", recorder.sent, want)
			}
			raw, err := json.Marshal(recorder.frames[0])
			if err != nil {
				t.Fatalf("encode frame: %v", err)
			}
			if !strings.Contains(string(raw), test.wantKey) ||
				strings.Contains(string(raw), binder.String()) {
				t.Fatalf("frame = %s, want %s and no binder id on the wire", raw, test.wantKey)
			}
		})
	}
	t.Run(agent.EventAgentUpdated, func(t *testing.T) {
		t.Parallel()
		bus, recorder := routedBus(t)
		publish(
			t,
			bus,
			companyID,
			agent.Topic,
			agent.EventAgentUpdated,
			agent.Response{ID: agentID, CompanyID: companyID},
		)
		if want := "company/" + companyID.String(); len(recorder.sent) != 1 || recorder.sent[0] != want {
			t.Fatalf("rooms = %v, want [%s]", recorder.sent, want)
		}
	})
}

// The roster is the registrant's: a frame that names a machine goes to
// them, and the sweeps that name none stay a company-wide refetch signal.
func TestDaemonRosterFramesFollowTheRegistrant(t *testing.T) {
	t.Parallel()
	companyID, registrant := uuid.New(), uuid.New()
	bus, recorder := routedBus(t)
	publish(t, bus, companyID, agent.TopicDaemon, agent.EventDaemonRosterChanged, agent.DaemonRoster{
		UserID: registrant,
		DaemonRegisterPayload: protocol.DaemonRegisterPayload{
			Action:    protocol.DaemonRosterActionRegister,
			CompanyID: companyID.String(),
			DaemonID:  "daemon-a",
		},
	})
	publish(t, bus, companyID, agent.TopicDaemon, agent.EventDaemonRosterChanged, agent.DaemonRoster{
		DaemonRegisterPayload: protocol.DaemonRegisterPayload{
			Action: protocol.DaemonRosterActionStaleSweep, CompanyID: companyID.String(),
		},
	})
	want := []string{
		"company_user/" + companyID.String() + "/" + registrant.String(),
		"company/" + companyID.String(),
	}
	if strings.Join(recorder.sent, ",") != strings.Join(want, ",") {
		t.Fatalf("rooms = %v, want %v", recorder.sent, want)
	}
}

// recorded returns what reached the broadcaster so far.
func (r *recordingBroadcaster) recorded() ([]string, []realtime.BroadcastEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.sent), slices.Clone(r.frames)
}
