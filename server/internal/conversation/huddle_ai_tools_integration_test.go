//go:build integration

package conversation

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent/voice"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/usage"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestHuddleContextAuthority(t *testing.T) {
	f := newDiscussionFixture(t)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE agents SET settings=jsonb_set(settings,'{capabilities}','{"disabledBuiltIns":["planning"]}') WHERE id=$1`,
		f.seed.AgentA,
	)
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
	calls := 0
	ai, err := NewHuddleAIService(HuddleAIServiceOptions{
		Store:                 f.store,
		Enabled:               true,
		TicksPerSecond:        100,
		ReasoningReserveTicks: 1000,
		ReadContext: func(_ context.Context, _ *generated.Queries, companyID uuid.UUID, request voice.ContextRequest) (voice.ContextResult, error) {
			calls++
			if companyID != f.a.CompanyID {
				t.Fatalf("scope = %s", companyID)
			}
			// The reads are the delegate rule's: the service, never
			// the model, names the Authorizer (who switched context on),
			// and the reads use that person's access alone.
			if request.AuthorizerUserID != f.a.UserID {
				t.Fatalf("context read for %+v, want Authorizer %s", request, f.a.UserID)
			}
			return voice.ContextResult{
				Items: []voice.ContextItem{{ID: companyID, Title: "Shared context"}},
			}, nil
		},
	})
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
	invite := SetHuddleAIInput{Enabled: true, AgentID: f.seed.AgentA, ExpectedRevision: 1, RequestKey: uuid.New()}
	if _, err = ai.SetEnabled(f.ctx, f.a, started.Huddle.ID, invite); err != nil {
		t.Fatal(err)
	}
	if err = ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	status, err := ai.Get(f.ctx, f.a, started.Huddle.ID)
	if err != nil || status.AttemptID == nil {
		t.Fatalf("status = %+v, %v", status, err)
	}
	lease, err := ai.Claim(f.ctx, *status.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	in := voice.ContextRequest{Tool: "get_company_context", RequestID: "read-1"}
	if _, err = ai.ReadContext(
		f.ctx,
		*status.AttemptID,
		lease.Capability,
		in,
	); !errors.Is(
		err,
		ErrHuddleForbidden,
	) {
		t.Fatalf("default access = %v", err)
	}
	for _, tool := range []string{"get_work_status", "get_work_result"} {
		if _, err = ai.ReadContext(
			f.ctx,
			*status.AttemptID,
			lease.Capability,
			voice.ContextRequest{Tool: tool, RequestID: "work", ProposalID: uuid.New()},
		); !errors.Is(
			err,
			ErrHuddleForbidden,
		) {
			t.Fatalf("work without context grant = %v", err)
		}
	}
	changed := invite
	changed.AllowCompanyContext = true
	if _, err = ai.SetEnabled(f.ctx, f.a, started.Huddle.ID, changed); !errors.Is(err, ErrHuddleRequestConflict) {
		t.Fatalf("replay scope change = %v", err)
	}
	// Grant a new invitation, never silently elevate the already-running attempt.
	changed.RequestKey = uuid.New()
	changed.ExpectedRevision = status.Revision
	if _, err = ai.SetEnabled(f.ctx, f.a, started.Huddle.ID, changed); err != nil {
		t.Fatal(err)
	}
	if _, err = ai.ReadContext(
		f.ctx,
		*status.AttemptID,
		lease.Capability,
		in,
	); !errors.Is(
		err,
		ErrHuddleUnavailable,
	) {
		t.Fatalf("old attempt = %v", err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE huddle_ai_sessions SET state='finished',usage_final=true WHERE id=$1`,
		*status.AttemptID,
	)
	if err = ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	status, err = ai.Get(f.ctx, f.a, started.Huddle.ID)
	if err != nil || status.AttemptID == nil {
		t.Fatalf("new status = %+v, %v", status, err)
	}
	lease, err = ai.Claim(f.ctx, *status.AttemptID)
	if err != nil || !lease.AllowCompanyContext {
		t.Fatalf("new lease = %+v, %v", lease, err)
	}
	if _, err = ai.ReadContext(f.ctx, *status.AttemptID, "wrong", in); !errors.Is(err, ErrHuddleForbidden) {
		t.Fatalf("wrong token = %v", err)
	}
	result, err := ai.ReadContext(f.ctx, *status.AttemptID, lease.Capability, in)
	if err != nil || len(result.Items) != 1 || calls != 1 {
		t.Fatalf("read = %+v, %v, calls %d", result, err, calls)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE agents SET settings=jsonb_set(settings,'{capabilities}','{"disabledBuiltIns":[]}') WHERE id=$1`,
		f.seed.AgentA,
	)
	for range 4 {
		if _, err = ai.ReadContext(f.ctx, *status.AttemptID, lease.Capability, in); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = ai.ReadContext(
		f.ctx,
		*status.AttemptID,
		lease.Capability,
		in,
	); !errors.Is(
		err,
		ErrHuddleToolLimit,
	) {
		t.Fatalf("burst limit = %v", err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE huddle_ai_sessions SET last_tool_at=now()-interval '11 seconds' WHERE id=$1`,
		*status.AttemptID,
	)
	if _, err = ai.ReadContext(f.ctx, *status.AttemptID, lease.Capability, in); err != nil {
		t.Fatalf("window renewal = %v", err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE huddle_ai_sessions SET lease_expires_at=now()-interval '1 second' WHERE id=$1`,
		*status.AttemptID,
	)
	if _, err = ai.ReadContext(
		f.ctx,
		*status.AttemptID,
		lease.Capability,
		in,
	); !errors.Is(
		err,
		ErrHuddleUnavailable,
	) {
		t.Fatalf("expired lease = %v", err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE huddle_ai_sessions SET lease_expires_at=now()+interval '30 seconds' WHERE id=$1`,
		*status.AttemptID,
	)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		f.a.CompanyID,
		f.a.UserID,
	)
	if _, err = ai.ReadContext(
		f.ctx,
		*status.AttemptID,
		lease.Capability,
		in,
	); !errors.Is(
		err,
		ErrHuddleUnavailable,
	) {
		t.Fatalf("revoked grant = %v", err)
	}
	for _, tool := range []string{"get_work_status", "get_work_result"} {
		if _, err = ai.ReadContext(
			f.ctx,
			*status.AttemptID,
			lease.Capability,
			voice.ContextRequest{Tool: tool, RequestID: "work", ProposalID: uuid.New()},
		); !errors.Is(
			err,
			ErrHuddleUnavailable,
		) {
			t.Fatalf("work after revocation = %v", err)
		}
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE company_members SET status='active' WHERE company_id=$1 AND user_id=$2`,
		f.a.CompanyID,
		f.a.UserID,
	)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE agents SET settings=jsonb_set(settings,'{capabilities}','{"disabledBuiltIns":["planning"]}') WHERE id=$1`,
		f.seed.AgentA,
	)
	if _, err = ai.ReadContext(f.ctx, *status.AttemptID, lease.Capability, in); err != nil {
		t.Fatalf("planning disabled read = %v", err)
	}
	if _, err = ai.Heartbeat(f.ctx, *status.AttemptID, lease.Capability, HuddleAIHeartbeat{}); err != nil {
		t.Fatalf("planning disabled heartbeat = %v", err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		f.a.CompanyID,
		f.a.UserID,
	)
	if _, err = ai.ReadContext(
		f.ctx,
		*status.AttemptID,
		lease.Capability,
		in,
	); !errors.Is(
		err,
		ErrHuddleUnavailable,
	) {
		t.Fatalf("revoked membership = %v", err)
	}
	if _, err = ai.Heartbeat(
		f.ctx,
		*status.AttemptID,
		lease.Capability,
		HuddleAIHeartbeat{},
	); !errors.Is(
		err,
		ErrHuddleUnavailable,
	) {
		t.Fatalf("cached context session survived revoke = %v", err)
	}
	if calls != 7 {
		t.Fatalf("unauthorized reader calls = %d", calls)
	}
	if err = ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	var attemptState string
	if err = f.pool.QueryRow(f.ctx, `SELECT state FROM huddle_ai_sessions WHERE id=$1`, *status.AttemptID).
		Scan(&attemptState); err != nil ||
		attemptState != "stopping" {
		t.Fatalf("revoked reconciliation = %s, %v", attemptState, err)
	}
}
