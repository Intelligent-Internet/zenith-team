//go:build integration

package conversation

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/agent/voice"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"boardly.dev/boardly/server/internal/usage"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// TestHuddleContextNeedsItsAuthorizerInTheCall pins D31 step B5: only someone
// in the call (connected to the Huddle and an active member of its chat) may
// switch Company context on, and the voice assistant reads it only while that
// person stays in the call.
func TestHuddleContextNeedsItsAuthorizerInTheCall(t *testing.T) {
	f := newDiscussionFixture(t)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE agents SET settings=jsonb_set(settings,'{voice}','{"enabled":true,"voice":"marin","greet":false,"participation":"addressed"}') WHERE id=$1`,
		f.seed.AgentA,
	)
	budget, err := usage.NewHuddleBudgetService(f.store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = budget.Configure(f.ctx, f.a, usage.ConfigureHuddleBudgetInput{
		MonthlyLimitUSDTicks: 100000000000,
	}); err != nil {
		t.Fatal(err)
	}
	huddles, err := NewHuddleService(HuddleServiceOptions{
		Store: f.store, Bus: f.bus, Media: &lifecycleHuddleMedia{},
	})
	if err != nil {
		t.Fatal(err)
	}
	var authorizers []uuid.UUID
	ai, err := NewHuddleAIService(HuddleAIServiceOptions{
		Store: f.store, Enabled: true, TicksPerSecond: 100, ReasoningReserveTicks: 1000,
		ReadContext: func(
			_ context.Context, _ *generated.Queries, _ uuid.UUID, request voice.ContextRequest,
		) (voice.ContextResult, error) {
			authorizers = append(authorizers, request.AuthorizerUserID)
			return voice.ContextResult{
				Items: []voice.ContextItem{{ID: uuid.New(), Title: "Shared context"}},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Mia (f.b, a member) starts the call from her direct message with Ada
	// (f.a, an admin). Ada sees the call from that chat but is not in it.
	direct, err := f.conversations.CreateHumanDirect(f.ctx, f.b, CreateHumanDirectInput{
		PeerUserIDs: []uuid.UUID{f.a.UserID},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := huddles.Start(f.ctx, f.b, direct.Conversation.ID, StartHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	huddleID := started.Huddle.ID
	join := func(p auth.UserPrincipal) uuid.UUID {
		t.Helper()
		joined, err := huddles.Join(f.ctx, p, huddleID, JoinHuddleRequest{RequestKey: uuid.New()})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = huddles.ConfirmConnected(f.ctx, p.CompanyID, joined.Connection.ID); err != nil {
			t.Fatal(err)
		}
		return joined.Connection.ID
	}
	get := func(p auth.UserPrincipal) HuddleAIStatus {
		t.Helper()
		status, err := ai.Get(f.ctx, p, huddleID)
		if err != nil {
			t.Fatal(err)
		}
		return status
	}
	invite := func(p auth.UserPrincipal, revision int64, share bool) (HuddleAIStatus, error) {
		return ai.SetEnabled(f.ctx, p, huddleID, SetHuddleAIInput{
			Enabled: true, AgentID: f.seed.AgentA, AllowCompanyContext: share,
			ExpectedRevision: revision, RequestKey: uuid.New(),
		})
	}
	claim := func() (uuid.UUID, HuddleAILease) {
		t.Helper()
		dbtest.MustExec(
			t,
			f.ctx,
			f.pool,
			`UPDATE huddle_ai_states SET retry_at=now()-interval '1 second' WHERE huddle_id=$1`,
			huddleID,
		)
		if err := ai.Reconcile(f.ctx); err != nil {
			t.Fatal(err)
		}
		status := get(f.b)
		if status.AttemptID == nil {
			t.Fatalf("no attempt after reconcile: %+v", status)
		}
		lease, err := ai.Claim(f.ctx, *status.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		return *status.AttemptID, lease
	}
	read := voice.ContextRequest{Tool: "get_company_context", RequestID: "read"}

	miaConnection := join(f.b)

	// An admin outside the call may still invite the assistant, but may not
	// share Company context: that would make them its Authorizer.
	if got := get(f.a); !got.CanModerate || got.CanShareContext {
		t.Fatalf(
			"admin outside the call: canModerate = %v, canShareContext = %v, want true, false",
			got.CanModerate,
			got.CanShareContext,
		)
	}
	if _, err = invite(f.a, 1, true); !errors.Is(err, ErrHuddleForbidden) {
		t.Fatalf("admin outside the call shared context: got %v, want %v", err, ErrHuddleForbidden)
	}
	if got := get(f.a); !got.Disabled || got.Revision != 1 || got.AllowCompanyContext {
		t.Fatalf("a refused share changed the assistant: %+v", got)
	}

	// Mia, who started the call and is in it, shares context.
	if got := get(f.b); !got.CanModerate || !got.CanShareContext {
		t.Fatalf(
			"starter in the call: canModerate = %v, canShareContext = %v, want true, true",
			got.CanModerate,
			got.CanShareContext,
		)
	}
	if _, err = invite(f.b, 1, true); err != nil {
		t.Fatal(err)
	}
	attempt, lease := claim()
	if _, err = ai.ReadContext(f.ctx, attempt, lease.Capability, read); err != nil {
		t.Fatalf("read while the Authorizer is in the call: %v", err)
	}
	if _, err = ai.AppendTranscript(f.ctx, attempt, lease.Capability, HuddleTranscriptBatch{
		Sequence: 1, Entries: []HuddleTranscriptEntry{{Speaker: "participants", Text: "before Mia left"}},
	}); err != nil {
		t.Fatal(err)
	}

	// Ada joins; then Mia leaves the call. Her chat membership stays (she
	// keeps the call's history), but her grant ends with her connection.
	join(f.a)
	if got := get(f.a); !got.CanShareContext {
		t.Fatalf("admin in the call cannot share context: %+v", got)
	}
	if _, err = huddles.Leave(f.ctx, f.b, huddleID, miaConnection); err != nil {
		t.Fatal(err)
	}
	var chatMember bool
	if err = f.pool.QueryRow(
		f.ctx,
		`SELECT EXISTS(SELECT 1 FROM conversation_members WHERE conversation_id=$1 AND user_id=$2 AND status='active')`,
		started.Huddle.ChatConversationID,
		f.b.UserID,
	).Scan(&chatMember); err != nil || !chatMember {
		t.Fatalf("Mia lost the call's history when she left: member = %v, %v", chatMember, err)
	}
	reads := len(authorizers)
	if _, err = ai.ReadContext(f.ctx, attempt, lease.Capability, read); !errors.Is(err, ErrHuddleUnavailable) {
		t.Fatalf("read after the Authorizer left: got %v, want %v", err, ErrHuddleUnavailable)
	}
	if _, err = ai.Heartbeat(
		f.ctx, attempt, lease.Capability, HuddleAIHeartbeat{},
	); !errors.Is(err, ErrHuddleUnavailable) {
		t.Fatalf("heartbeat after the Authorizer left: got %v, want %v", err, ErrHuddleUnavailable)
	}
	if len(authorizers) != reads {
		t.Fatalf("the reader ran after the Authorizer left: %d calls, want %d", len(authorizers), reads)
	}
	if got := get(f.a); got.State != "unavailable" || got.UnavailableReason != "context_sharer_left" ||
		!got.CanShareContext {
		t.Fatalf(
			"status after the Authorizer left = %+v, want unavailable, context_sharer_left, canShareContext",
			got,
		)
	}
	// The session loop ends capture: the attempt stops and the transcript
	// is sealed.
	if err = ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	var attemptState string
	var sealed bool
	if err = f.pool.QueryRow(
		f.ctx,
		`SELECT a.state,(SELECT bool_and(t.ended_at IS NOT NULL) FROM huddle_transcripts t WHERE t.huddle_id=a.huddle_id)
		FROM huddle_ai_sessions a WHERE a.id=$1`,
		attempt,
	).Scan(&attemptState, &sealed); err != nil || attemptState != "stopping" || !sealed {
		t.Fatalf(
			"capture after the Authorizer left: attempt %q, transcript sealed %v, %v; want stopping, true",
			attemptState,
			sealed,
			err,
		)
	}
	// Mia started the call, so she still moderates it, but outside the call
	// she cannot share context again.
	status := get(f.a)
	if _, err = invite(f.b, status.Revision, true); !errors.Is(err, ErrHuddleForbidden) {
		t.Fatalf("starter outside the call shared context: got %v, want %v", err, ErrHuddleForbidden)
	}

	// Ada, still in the call, shares context again and reads resume as hers.
	if _, err = invite(f.a, status.Revision, true); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE huddle_ai_sessions SET state='finished',usage_final=true WHERE id=$1`,
		attempt,
	)
	attempt, lease = claim()
	if _, err = ai.ReadContext(f.ctx, attempt, lease.Capability, read); err != nil {
		t.Fatalf("read after Ada shared context: %v", err)
	}
	if got := authorizers[len(authorizers)-1]; got != f.a.UserID {
		t.Fatalf("resumed read for %s, want Ada %s", got, f.a.UserID)
	}
	if authorizers[0] != f.b.UserID {
		t.Fatalf("first read for %s, want Mia %s", authorizers[0], f.b.UserID)
	}

	// Both halves count: connected but no longer a member of the call's chat
	// is not in the call either.
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE conversation_members SET status='revoked',revoked_at=now() WHERE conversation_id=$1 AND user_id=$2`,
		started.Huddle.ChatConversationID,
		f.a.UserID,
	)
	if _, err = ai.ReadContext(f.ctx, attempt, lease.Capability, read); !errors.Is(err, ErrHuddleUnavailable) {
		t.Fatalf("read after the Authorizer lost the call's chat: got %v, want %v", err, ErrHuddleUnavailable)
	}
	// The same holds for sharing: still connected, but without the chat row,
	// Ada may not share context again.
	status = get(f.a)
	if status.CanShareContext {
		t.Fatalf("admin connected without the call's chat may share context: %+v", status)
	}
	if _, err = invite(f.a, status.Revision, true); !errors.Is(err, ErrHuddleForbidden) {
		t.Fatalf(
			"admin connected without the call's chat shared context: got %v, want %v",
			err,
			ErrHuddleForbidden,
		)
	}

	// Switching the assistant on without context keeps the moderator rule
	// alone: the starter, outside the call, may still invite it.
	status = get(f.b)
	if _, err = invite(f.b, status.Revision, false); err != nil {
		t.Fatalf("starter outside the call invited without context: %v", err)
	}
}
