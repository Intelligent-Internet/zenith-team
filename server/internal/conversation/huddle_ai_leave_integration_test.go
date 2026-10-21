//go:build integration

package conversation

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/usage"
)

func TestHuddleAgentLeavesWithoutEndingHumanCall(t *testing.T) {
	f := newDiscussionFixture(t)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE agents SET settings=jsonb_set(settings,'{voice}','{"enabled":true,"voice":"marin","greet":false,"participation":"addressed"}') WHERE id=$1`,
		f.seed.AgentA,
	)
	budget, _ := usage.NewHuddleBudgetService(f.store)
	if _, err := budget.Configure(
		f.ctx,
		f.a,
		usage.ConfigureHuddleBudgetInput{MonthlyLimitUSDTicks: 100000000000},
	); err != nil {
		t.Fatal(err)
	}
	huddles, _ := NewHuddleService(HuddleServiceOptions{Store: f.store, Bus: f.bus, Media: &lifecycleHuddleMedia{}})
	ai, _ := NewHuddleAIService(
		HuddleAIServiceOptions{Store: f.store, Enabled: true, TicksPerSecond: 100, ReasoningReserveTicks: 1000},
	)
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
	invite := SetHuddleAIInput{Enabled: true, AgentID: f.seed.AgentA, ExpectedRevision: 1, RequestKey: uuid.New()}
	if _, err = ai.SetEnabled(f.ctx, f.a, started.Huddle.ID, invite); err != nil {
		t.Fatal(err)
	}
	// Make the initial dispatch due independently of host/database clock skew.
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE huddle_ai_states SET retry_at=now()-interval '1 second' WHERE huddle_id=$1`,
		started.Huddle.ID,
	)
	if err = ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	status, err := ai.Get(f.ctx, f.a, started.Huddle.ID)
	if err != nil || status.AttemptID == nil {
		t.Fatalf("invited status = %+v, %v", status, err)
	}
	attemptID := *status.AttemptID
	lease, err := ai.Claim(f.ctx, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ai.Leave(f.ctx, attemptID, "wrong"); !errors.Is(err, ErrHuddleForbidden) {
		t.Fatalf("wrong capability = %v", err)
	}
	batch := HuddleTranscriptBatch{
		Sequence: 1,
		Entries:  []HuddleTranscriptEntry{{Speaker: "participants", Text: "test transcript"}},
	}
	if _, err = ai.AppendTranscript(f.ctx, attemptID, "wrong", batch); !errors.Is(err, ErrHuddleForbidden) {
		t.Fatalf("unauthorized transcript = %v", err)
	}
	for range 2 {
		if _, err = ai.AppendTranscript(f.ctx, attemptID, lease.Capability, batch); err != nil {
			t.Fatal(err)
		}
	}
	changed := batch
	changed.Entries = []HuddleTranscriptEntry{{Speaker: "agent", Text: "changed replay"}}
	if _, err = ai.AppendTranscript(
		f.ctx,
		attemptID,
		lease.Capability,
		changed,
	); !errors.Is(
		err,
		ErrHuddleRequestConflict,
	) {
		t.Fatalf("changed transcript replay = %v", err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE huddle_ai_sessions SET lease_expires_at=now()-interval '1 second' WHERE id=$1`,
		attemptID,
	)
	if _, err = ai.Leave(f.ctx, attemptID, lease.Capability); !errors.Is(err, ErrHuddleUnavailable) {
		t.Fatalf("expired leave = %v", err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE huddle_ai_sessions SET lease_expires_at=now()+interval '30 seconds' WHERE id=$1`,
		attemptID,
	)
	result, err := ai.Leave(f.ctx, attemptID, lease.Capability)
	if err != nil || !result.Left {
		t.Fatalf("leave = %+v, %v", result, err)
	}
	if _, err = ai.AppendTranscript(
		f.ctx,
		attemptID,
		lease.Capability,
		HuddleTranscriptBatch{Sequence: 2},
	); !errors.Is(
		err,
		ErrHuddleUnavailable,
	) {
		t.Fatalf("append after leave = %v", err)
	}
	for range 2 {
		if err = ai.Reconcile(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	var transcripts int
	var transcript string
	if err = f.pool.QueryRow(f.ctx, `SELECT count(*),coalesce(max(body->>'markdown'),'') FROM conversation_items WHERE conversation_id=$1 AND metadata->>'action'='huddle_transcript'`, started.Huddle.ChatConversationID).
		Scan(&transcripts, &transcript); err != nil ||
		transcripts != 1 ||
		strings.Count(transcript, "test transcript") != 1 {
		t.Fatalf("saved transcript count=%d body=%q error=%v", transcripts, transcript, err)
	}
	if _, err = ai.Heartbeat(
		f.ctx,
		attemptID,
		lease.Capability,
		HuddleAIHeartbeat{},
	); !errors.Is(
		err,
		ErrHuddleUnavailable,
	) {
		t.Fatalf("heartbeat after leave = %v", err)
	}
	if err = ai.Finish(
		f.ctx,
		attemptID,
		lease.Capability,
		HuddleAIUsage{
			ModelSessionID: "leave-test",
			ModelSeconds:   1,
			Final:          true,
			Closed:         true,
			StopReason:     "agent_left",
		},
	); err != nil {
		t.Fatalf("final accounting after leave = %v", err)
	}
	status, err = ai.Get(f.ctx, f.a, started.Huddle.ID)
	if err != nil || !status.Disabled {
		t.Fatalf("left status = %+v, %v", status, err)
	}
	var callState string
	var connected int
	if err = f.pool.QueryRow(f.ctx, `SELECT h.state,(SELECT count(*) FROM huddle_connections c WHERE c.huddle_id=h.id AND c.status='connected') FROM huddles h WHERE h.id=$1`, started.Huddle.ID).
		Scan(&callState, &connected); err != nil ||
		callState != "open" ||
		connected != 1 {
		t.Fatalf("human call = %s, %d, %v", callState, connected, err)
	}
	// A lost response can be retried, including after another invitation.
	invite.ExpectedRevision, invite.RequestKey = status.Revision, uuid.New()
	if _, err = ai.SetEnabled(f.ctx, f.a, started.Huddle.ID, invite); err != nil {
		t.Fatal(err)
	}
	if result, err = ai.Leave(f.ctx, attemptID, lease.Capability); err != nil || !result.Left {
		t.Fatalf("old leave replay = %+v, %v", result, err)
	}
	status, err = ai.Get(f.ctx, f.a, started.Huddle.ID)
	if err != nil || status.Disabled {
		t.Fatalf("old attempt removed new invitation = %+v, %v", status, err)
	}
}
