//go:build integration

package conversation

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/usage"
)

func TestHuddleAIClaimAndShutdown(t *testing.T) {
	f := newDiscussionFixture(t)
	// Old saved preferences cannot suppress the once-per-invitation greeting.
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE agents SET name='Helia', instructions='Act as a meeting secretary', settings=jsonb_set(settings,'{voice}','{"enabled":true,"voice":"marin","greet":false,"participation":"addressed"}') WHERE id=$1`,
		f.seed.AgentA,
	)
	budget, err := usage.NewHuddleBudgetService(f.store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = budget.Configure(
		f.ctx,
		f.a,
		usage.ConfigureHuddleBudgetInput{MonthlyLimitUSDTicks: 100000000000},
	); err != nil {
		t.Fatal(err)
	}
	huddles, err := NewHuddleService(
		HuddleServiceOptions{Store: f.store, Bus: f.bus, Media: &lifecycleHuddleMedia{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	ai, err := NewHuddleAIService(
		HuddleAIServiceOptions{Store: f.store, Enabled: true, TicksPerSecond: 100, ReasoningReserveTicks: 1000},
	)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	started, err := huddles.Start(f.ctx, f.a, direct.Conversation.ID, StartHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := huddles.Join(f.ctx, f.a, started.Huddle.ID, JoinHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = huddles.ConfirmConnected(f.ctx, f.a.CompanyID, joined.Connection.ID); err != nil {
		t.Fatal(err)
	}
	if err = ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	status, err := ai.Get(f.ctx, f.a, started.Huddle.ID)
	if err != nil || status.AttemptID != nil || !status.Disabled || status.State != "disabled" {
		t.Fatalf("human connection invited Helia: %+v error=%v", status, err)
	}
	var reservations int
	if err = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM huddle_ai_budget_reservations WHERE huddle_id=$1`, started.Huddle.ID).
		Scan(&reservations); err != nil ||
		reservations != 0 {
		t.Fatalf("uninvited budget reservations=%d error=%v", reservations, err)
	}
	if _, err = f.discussion.Send(
		f.ctx,
		f.a,
		started.Huddle.ChatConversationID,
		textSend("before Helia was invited"),
	); err != nil {
		t.Fatal(err)
	}
	invite := SetHuddleAIInput{
		Enabled:          true,
		AgentID:          f.seed.AgentA,
		ExpectedRevision: status.Revision,
		RequestKey:       uuid.New(),
	}
	invalid := invite
	invalid.AgentID = uuid.Nil
	if _, err = ai.SetEnabled(f.ctx, f.a, started.Huddle.ID, invalid); !errors.Is(err, ErrHuddleInvalid) {
		t.Fatalf("missing agent: %v", err)
	}
	invalid.AgentID = uuid.New()
	if _, err = ai.SetEnabled(f.ctx, f.a, started.Huddle.ID, invalid); !errors.Is(err, ErrHuddleUnavailable) {
		t.Fatalf("unknown agent: %v", err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE agents SET settings=jsonb_set(settings,'{voice}','{"enabled":true,"voice":"marin","greet":true,"participation":"addressed"}') WHERE id=$1`,
		f.seed.AgentB,
	)
	invalid.AgentID = f.seed.AgentB
	if _, err = ai.SetEnabled(f.ctx, f.a, started.Huddle.ID, invalid); !errors.Is(err, ErrHuddleUnavailable) {
		t.Fatalf("foreign Voice Agent: %v", err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE agents SET settings=jsonb_set(settings,'{voice,enabled}','false') WHERE id=$1`,
		f.seed.AgentA,
	)
	if _, err = ai.SetEnabled(f.ctx, f.a, started.Huddle.ID, invite); !errors.Is(err, ErrHuddleUnavailable) {
		t.Fatalf("disabled Voice Agent: %v", err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE agents SET settings=jsonb_set(settings,'{voice,enabled}','true'), archived_at=now() WHERE id=$1`,
		f.seed.AgentA,
	)
	if _, err = ai.SetEnabled(f.ctx, f.a, started.Huddle.ID, invite); !errors.Is(err, ErrHuddleUnavailable) {
		t.Fatalf("archived Voice Agent: %v", err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE agents SET archived_at=NULL WHERE id=$1`, f.seed.AgentA)
	if _, err = ai.SetEnabled(f.ctx, f.b, started.Huddle.ID, invite); !errors.Is(err, ErrHuddleForbidden) {
		t.Fatalf("non-moderator invited Helia: %v", err)
	}
	if _, err = ai.SetEnabled(f.ctx, f.a, started.Huddle.ID, invite); err != nil {
		t.Fatal(err)
	}
	if _, err = ai.SetEnabled(f.ctx, f.a, started.Huddle.ID, invite); err != nil {
		t.Fatalf("replayed invitation: %v", err)
	}
	invalid.AgentID = uuid.New()
	if _, err = ai.SetEnabled(f.ctx, f.a, started.Huddle.ID, invalid); !errors.Is(err, ErrHuddleRequestConflict) {
		t.Fatalf("changed replay identity: %v", err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE agents SET instructions='New instructions for next invitation' WHERE id=$1`,
		f.seed.AgentA,
	)
	if _, err = budget.Configure(
		f.ctx,
		f.a,
		usage.ConfigureHuddleBudgetInput{MonthlyLimitUSDTicks: 1099, ExpectedRevision: 1},
	); err != nil {
		t.Fatal(err)
	}
	if err = ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	blocked, err := ai.Get(f.ctx, f.a, started.Huddle.ID)
	if err != nil || blocked.State != "unavailable" || blocked.UnavailableReason != "budget" ||
		blocked.AttemptID != nil {
		t.Fatalf("insufficient start budget = %+v, error=%v", blocked, err)
	}
	if _, err = budget.Configure(
		f.ctx,
		f.a,
		usage.ConfigureHuddleBudgetInput{MonthlyLimitUSDTicks: 100000000000, ExpectedRevision: 2},
	); err != nil {
		t.Fatal(err)
	}
	if err = ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	status, err = ai.Get(f.ctx, f.a, started.Huddle.ID)
	if err != nil || status.AttemptID == nil {
		t.Fatalf("status=%+v error=%v", status, err)
	}
	claim, err := ai.Claim(f.ctx, *status.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Agent.ID != f.seed.AgentA || claim.Agent.Name != "Helia" ||
		claim.Agent.Instructions != "Act as a meeting secretary" {
		t.Fatalf("invitation snapshot = %+v", claim.Agent)
	}
	if _, err = ai.Claim(f.ctx, *status.AttemptID); !errors.Is(err, ErrHuddleUnavailable) {
		t.Fatalf("duplicate claim=%v", err)
	}
	if !claim.Greet || len(claim.HumanIdentities) != 1 {
		t.Fatalf("claim=%+v", claim)
	}
	if _, err = ai.Heartbeat(
		f.ctx,
		*status.AttemptID,
		"wrong",
		HuddleAIHeartbeat{},
	); !errors.Is(
		err,
		ErrHuddleForbidden,
	) {
		t.Fatalf("bad capability=%v", err)
	}

	if _, err = f.discussion.Send(
		f.ctx,
		f.a,
		started.Huddle.ChatConversationID,
		textSend("https://example.test/?exact=1&second=2"),
	); err != nil {
		t.Fatal(err)
	}
	items, err := ai.Chat(f.ctx, *status.AttemptID, claim.Capability)
	if err != nil || len(items) != 1 || items[0].Text != "https://example.test/?exact=1&second=2" ||
		items[0].AuthorID != f.a.UserID {
		t.Fatalf("chat=%+v %v", items, err)
	}
	ready, err := ai.Heartbeat(
		f.ctx,
		*status.AttemptID,
		claim.Capability,
		HuddleAIHeartbeat{State: "listening", ChatCursor: items[0].Seq},
	)
	if err != nil || !ready.Greet {
		t.Fatalf("ready=%+v %v", ready, err)
	}
	ready, err = ai.Heartbeat(
		f.ctx,
		*status.AttemptID,
		claim.Capability,
		HuddleAIHeartbeat{State: "listening", ChatCursor: items[0].Seq},
	)
	if err != nil || ready.Greet {
		t.Fatalf("repeated greeting=%+v %v", ready, err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE agents SET settings=jsonb_set(settings,'{voice,enabled}','false') WHERE id=$1`,
		f.seed.AgentA,
	)
	if _, err = ai.Heartbeat(
		f.ctx,
		*status.AttemptID,
		claim.Capability,
		HuddleAIHeartbeat{},
	); !errors.Is(
		err,
		ErrHuddleUnavailable,
	) {
		t.Fatalf("Voice revoked heartbeat = %v", err)
	}
	if _, err = ai.Chat(f.ctx, *status.AttemptID, claim.Capability); !errors.Is(err, ErrHuddleUnavailable) {
		t.Fatalf("Voice revoked chat = %v", err)
	}
	unavailable, err := ai.Get(f.ctx, f.a, started.Huddle.ID)
	if err != nil || unavailable.UnavailableReason != "agent_unavailable" {
		t.Fatalf("Voice revoked status = %+v, %v", unavailable, err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE agents SET settings=jsonb_set(settings,'{voice,enabled}','true') WHERE id=$1`,
		f.seed.AgentA,
	)
	if _, err = ai.SetEnabled(
		f.ctx,
		f.b,
		started.Huddle.ID,
		SetHuddleAIInput{Enabled: false, ExpectedRevision: status.Revision, RequestKey: uuid.New()},
	); !errors.Is(
		err,
		ErrHuddleForbidden,
	) {
		t.Fatalf("member moderated=%v", err)
	}
	report := HuddleAIUsage{ModelSessionID: "model-session", ModelSeconds: 10}
	if err = ai.Finish(f.ctx, *status.AttemptID, claim.Capability, report); err != nil {
		t.Fatal(err)
	}
	if err = ai.Finish(f.ctx, *status.AttemptID, claim.Capability, report); err != nil {
		t.Fatal(err)
	}
	var seconds float64
	if err = f.pool.QueryRow(f.ctx, `SELECT sum(model_seconds) FROM huddle_ai_usage_receipts WHERE huddle_id=$1`, started.Huddle.ID).
		Scan(&seconds); err != nil ||
		seconds != 10 {
		t.Fatalf("usage doubled=%v %v", seconds, err)
	}
	report.ModelSeconds = 9
	if err = ai.Finish(
		f.ctx,
		*status.AttemptID,
		claim.Capability,
		report,
	); !errors.Is(
		err,
		usage.ErrHuddleBudgetConflict,
	) {
		t.Fatalf("decreasing usage=%v", err)
	}
	report.ModelSeconds, report.Closed, report.StopReason = 10, true, "provider_error"
	if err = ai.Finish(f.ctx, *status.AttemptID, claim.Capability, report); err != nil {
		t.Fatal(err)
	}
	stopped, err := ai.Get(f.ctx, f.a, started.Huddle.ID)
	if err != nil || stopped.StopReason != "provider_error" {
		t.Fatalf("stop reason = %+v, %v", stopped, err)
	}

	if _, err = f.pool.Exec(
		f.ctx,
		`UPDATE companies SET deleting_at=now() WHERE id=$1`,
		f.a.CompanyID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = ai.Heartbeat(
		f.ctx,
		*status.AttemptID,
		claim.Capability,
		HuddleAIHeartbeat{},
	); !errors.Is(
		err,
		ErrHuddleUnavailable,
	) {
		t.Fatalf("deleting company heartbeat=%v", err)
	}
	if _, err = f.pool.Exec(f.ctx, `UPDATE companies SET deleting_at=NULL WHERE id=$1`, f.a.CompanyID); err != nil {
		t.Fatal(err)
	}
	request := SetHuddleAIInput{Enabled: false, ExpectedRevision: status.Revision, RequestKey: uuid.New()}
	disabled, err := ai.SetEnabled(f.ctx, f.a, started.Huddle.ID, request)
	if err != nil || !disabled.Disabled {
		t.Fatalf("disabled=%+v error=%v", disabled, err)
	}
	if _, err = ai.SetEnabled(f.ctx, f.a, started.Huddle.ID, request); err != nil {
		t.Fatalf("replay=%v", err)
	}
	if _, err = ai.Heartbeat(
		f.ctx,
		*status.AttemptID,
		claim.Capability,
		HuddleAIHeartbeat{},
	); !errors.Is(
		err,
		ErrHuddleUnavailable,
	) {
		t.Fatalf("disabled heartbeat=%v", err)
	}
	if _, err = ai.Chat(f.ctx, *status.AttemptID, claim.Capability); !errors.Is(err, ErrHuddleUnavailable) {
		t.Fatalf("revoked chat=%v", err)
	}
	if err = ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	disabled, err = ai.Get(f.ctx, f.a, started.Huddle.ID)
	if err != nil || disabled.State != "disabled" {
		t.Fatalf("recovered intentional shutdown: %+v %v", disabled, err)
	}
}

func TestHuddleAIConcurrentControllersAndExpiredClaim(t *testing.T) {
	f := newDiscussionFixture(t)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE agents SET settings=jsonb_set(settings,'{voice}','{"enabled":true,"voice":"marin","greet":true,"participation":"addressed"}') WHERE id=$1`,
		f.seed.AgentA,
	)
	budget, err := usage.NewHuddleBudgetService(f.store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = budget.Configure(
		f.ctx,
		f.a,
		usage.ConfigureHuddleBudgetInput{MonthlyLimitUSDTicks: 100000000000},
	); err != nil {
		t.Fatal(err)
	}
	service, err := NewHuddleService(
		HuddleServiceOptions{Store: f.store, Bus: f.bus, Media: &lifecycleHuddleMedia{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	ai, err := NewHuddleAIService(
		HuddleAIServiceOptions{Store: f.store, Enabled: true, TicksPerSecond: 100, ReasoningReserveTicks: 1000},
	)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.Start(f.ctx, f.a, direct.Conversation.ID, StartHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := service.Join(f.ctx, f.a, started.Huddle.ID, JoinHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if err = ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	before, err := ai.Get(f.ctx, f.a, started.Huddle.ID)
	if err != nil || before.AttemptID != nil {
		t.Fatalf("reserved join dispatched: %+v %v", before, err)
	}
	if _, err = service.ConfirmConnected(f.ctx, f.a.CompanyID, joined.Connection.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ai.SetEnabled(f.ctx, f.a, started.Huddle.ID, SetHuddleAIInput{
		Enabled: true, AgentID: f.seed.AgentA, ExpectedRevision: before.Revision, RequestKey: uuid.New(),
	}); err != nil {
		t.Fatal(err)
	}
	results := make(chan error)
	for range 2 {
		go func() { results <- ai.Reconcile(f.ctx) }()
	}
	for range 2 {
		if err = <-results; err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM huddle_ai_sessions WHERE huddle_id=$1`, started.Huddle.ID).
		Scan(&count); err != nil ||
		count != 1 {
		t.Fatalf("attempts=%d err=%v", count, err)
	}
	status, err := ai.Get(f.ctx, f.a, started.Huddle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(
		f.ctx,
		`UPDATE huddle_ai_sessions SET lease_expires_at=now()-interval '1 second' WHERE id=$1`,
		*status.AttemptID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = ai.Claim(f.ctx, *status.AttemptID); !errors.Is(err, ErrHuddleUnavailable) {
		t.Fatalf("expired claim=%v", err)
	}
	if err = ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM huddle_ai_sessions WHERE huddle_id=$1`, started.Huddle.ID).
		Scan(&count); err != nil ||
		count != 1 {
		t.Fatalf("replacement before fence=%d %v", count, err)
	}
}

func TestHuddleChatAdmissionAndHistory(t *testing.T) {
	f := newDiscussionFixture(t)
	service, err := NewHuddleService(
		HuddleServiceOptions{Store: f.store, Bus: f.bus, Media: &lifecycleHuddleMedia{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.Start(f.ctx, f.a, direct.Conversation.ID, StartHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := service.Join(f.ctx, f.a, started.Huddle.ID, JoinHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.discussion.Send(
		f.ctx,
		f.a,
		started.Huddle.ChatConversationID,
		textSend("before confirmation"),
	); err == nil {
		t.Fatal("reservation sent to chat")
	}
	if _, err = service.ConfirmConnected(f.ctx, f.a.CompanyID, joined.Connection.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.discussion.Send(
		f.ctx,
		f.a,
		started.Huddle.ChatConversationID,
		textSend("connected"),
	); err != nil {
		t.Fatal(err)
	}
	if _, err = f.discussion.ListMessages(
		f.ctx,
		f.b,
		started.Huddle.ChatConversationID,
		DiscussionPageOptions{},
	); !errors.Is(
		err,
		ErrDiscussionNotFound,
	) {
		t.Fatalf("nonparticipant history=%v", err)
	}
	if _, err = service.Leave(f.ctx, f.a, started.Huddle.ID, joined.Connection.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.discussion.Send(
		f.ctx,
		f.a,
		started.Huddle.ChatConversationID,
		textSend("after leave"),
	); !errors.Is(
		err,
		ErrDiscussionForbidden,
	) {
		t.Fatalf("send after leave=%v", err)
	}
	history, err := f.discussion.ListMessages(
		f.ctx,
		f.a,
		started.Huddle.ChatConversationID,
		DiscussionPageOptions{},
	)
	if err != nil || len(history.Items) != 1 {
		t.Fatalf("retained history=%+v %v", history, err)
	}
}

func TestHuddleStatusSignalsOnlyRealTransitions(t *testing.T) {
	f := newDiscussionFixture(t)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE agents SET settings=jsonb_set(settings,'{voice}','{"enabled":true,"voice":"marin","greet":true,"participation":"addressed"}') WHERE id=$1`,
		f.seed.AgentA,
	)
	budget, err := usage.NewHuddleBudgetService(f.store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = budget.Configure(
		f.ctx,
		f.a,
		usage.ConfigureHuddleBudgetInput{MonthlyLimitUSDTicks: 100000000000},
	); err != nil {
		t.Fatal(err)
	}
	huddles, err := NewHuddleService(
		HuddleServiceOptions{Store: f.store, Bus: f.bus, Media: &lifecycleHuddleMedia{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	ai, err := NewHuddleAIService(
		HuddleAIServiceOptions{
			Store:                 f.store,
			Bus:                   f.bus,
			Enabled:               true,
			TicksPerSecond:        100,
			ReasoningReserveTicks: 1000,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var signals []HuddleStatusChangedEvent
	if err := f.bus.Subscribe(TopicHuddle, func(_ context.Context, e events.Event) error {
		if e.Name != EventHuddleStatusChanged {
			return nil
		}
		status, err := events.PayloadOf[HuddleStatusChangedEvent](e)
		if err != nil {
			return err
		}
		signals = append(signals, status)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	expect := func(step string, want int) {
		t.Helper()
		if len(signals) != want {
			t.Fatalf("%s: status signals = %d, want %d", step, len(signals), want)
		}
		for _, signal := range signals {
			if len(signal.RecipientUserIDs) != 1 || signal.RecipientUserIDs[0] != f.a.UserID {
				t.Fatalf(
					"%s: recipients = %v, want only the connected member",
					step,
					signal.RecipientUserIDs,
				)
			}
		}
		signals = nil
	}

	direct, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	started, err := huddles.Start(f.ctx, f.a, direct.Conversation.ID, StartHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := huddles.Join(f.ctx, f.a, started.Huddle.ID, JoinHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = huddles.ConfirmConnected(f.ctx, f.a.CompanyID, joined.Connection.ID); err != nil {
		t.Fatal(err)
	}
	if err = ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	signals = nil
	if err = ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	expect("idle reconcile", 0)

	status, err := ai.Get(f.ctx, f.a, started.Huddle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ai.SetEnabled(
		f.ctx,
		f.a,
		started.Huddle.ID,
		SetHuddleAIInput{
			Enabled:          true,
			AgentID:          f.seed.AgentA,
			ExpectedRevision: status.Revision,
			RequestKey:       uuid.New(),
		},
	); err != nil {
		t.Fatal(err)
	}
	expect("invite", 1)
	if err = ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	expect("dispatch", 1)
	if err = ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	expect("unchanged reconcile", 0)

	if err = huddles.EndFromMedia(f.ctx, f.a.CompanyID, *started.Huddle.RoomName); err != nil {
		t.Fatal(err)
	}
	expect("call ended", 1)
}
