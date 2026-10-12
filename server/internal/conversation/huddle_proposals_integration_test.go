//go:build integration

package conversation

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"boardly.dev/boardly/server/internal/agent/voice"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"boardly.dev/boardly/server/internal/usage"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
)

func TestHuddleProposalLifecycle(t *testing.T) {
	f := newDiscussionFixture(t)
	var tasksBefore, runsBefore int
	if err := f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM tasks WHERE company_id=$1),(SELECT count(*) FROM runs WHERE company_id=$1)`, f.a.CompanyID).Scan(&tasksBefore, &runsBefore); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE agents SET settings=jsonb_set(settings,'{voice}','{"enabled":true,"voice":"marin","greet":false,"participation":"addressed"}') WHERE id=$1`, f.seed.AgentA)
	budget, _ := usage.NewHuddleBudgetService(f.store)
	if _, err := budget.Configure(f.ctx, f.a, usage.ConfigureHuddleBudgetInput{MonthlyLimitUSDTicks: 100000000000}); err != nil {
		t.Fatal(err)
	}
	huddles, _ := NewHuddleService(HuddleServiceOptions{Store: f.store, Bus: f.bus, Media: &lifecycleHuddleMedia{}})
	ai, _ := NewHuddleAIService(HuddleAIServiceOptions{Store: f.store, Enabled: true, TicksPerSecond: 100, ReasoningReserveTicks: 1000,
		ReadContext: func(context.Context, *generated.Queries, uuid.UUID, voice.ContextRequest) (voice.ContextResult, error) {
			return voice.ContextResult{}, nil
		},
	})
	direct, err := f.conversations.CreateHumanDirect(f.ctx, f.a, CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}})
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
	if _, err = ai.UpdateWorkProposal(f.ctx, f.a, started.Huddle.ID, UpdateHuddleProposalInput{ProposalID: uuid.New(), Action: "reject", ExpectedRevision: 1}); !errors.Is(err, ErrHuddleNotFound) {
		t.Fatalf("proposal before any invitation = %v", err)
	}
	if _, err = ai.SetEnabled(f.ctx, f.a, started.Huddle.ID, SetHuddleAIInput{Enabled: true, AllowCompanyContext: true, AgentID: f.seed.AgentA, ExpectedRevision: 1, RequestKey: uuid.New()}); err != nil {
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
	var projectID uuid.UUID
	if err = f.pool.QueryRow(f.ctx, `SELECT id FROM projects WHERE company_id=$1 LIMIT 1`, f.a.CompanyID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	in := HuddleProposalInput{RequestKey: "draft-1", ProjectID: projectID, Title: "Fix login", Brief: "Fix the login bug and verify it"}
	if _, err = ai.ProposeWork(f.ctx, *status.AttemptID, "wrong", in); !errors.Is(err, ErrHuddleForbidden) {
		t.Fatalf("wrong capability = %v", err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE huddle_ai_states SET allow_company_context=false WHERE huddle_id=$1`, started.Huddle.ID)
	if _, err = ai.ProposeWork(f.ctx, *status.AttemptID, lease.Capability, in); !errors.Is(err, ErrHuddleForbidden) {
		t.Fatalf("context opt-out = %v", err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE huddle_ai_states SET allow_company_context=true WHERE huddle_id=$1`, started.Huddle.ID)
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE huddle_ai_sessions SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, *status.AttemptID)
	if _, err = ai.ProposeWork(f.ctx, *status.AttemptID, lease.Capability, in); !errors.Is(err, ErrHuddleUnavailable) {
		t.Fatalf("expired lease = %v", err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE huddle_ai_sessions SET lease_expires_at=now()+interval '30 seconds' WHERE id=$1`, *status.AttemptID)
	bad := in
	bad.ProjectID = f.seed.ProjectB
	if _, err = ai.ProposeWork(f.ctx, *status.AttemptID, lease.Capability, bad); !errors.Is(err, ErrHuddleNotFound) {
		t.Fatalf("foreign project = %v", err)
	}
	bad = in
	bad.TaskID = uuid.NullUUID{UUID: f.seed.TaskB, Valid: true}
	if _, err = ai.ProposeWork(f.ctx, *status.AttemptID, lease.Capability, bad); !errors.Is(err, ErrHuddleNotFound) {
		t.Fatalf("foreign task = %v", err)
	}
	proposal, err := ai.ProposeWork(f.ctx, *status.AttemptID, lease.Capability, in)
	if err != nil || proposal.AgentID != f.seed.AgentA || proposal.State != "pending" {
		t.Fatalf("proposal = %+v, %v", proposal, err)
	}
	again, err := ai.ProposeWork(f.ctx, *status.AttemptID, lease.Capability, in)
	if err != nil || again.ID != proposal.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	bad = in
	bad.Title = "Different request"
	if _, err = ai.ProposeWork(f.ctx, *status.AttemptID, lease.Capability, bad); !errors.Is(err, ErrHuddleProposalConflict) {
		t.Fatalf("changed replay = %v", err)
	}
	edit := UpdateHuddleProposalInput{ProposalID: proposal.ID, ExpectedRevision: 1, Action: "edit", Title: "Fix login now", Brief: in.Brief}
	if _, err = ai.UpdateWorkProposal(f.ctx, f.b, started.Huddle.ID, edit); !errors.Is(err, ErrHuddleForbidden) {
		t.Fatalf("non-moderator edit = %v", err)
	}
	changed, err := ai.UpdateWorkProposal(f.ctx, f.a, started.Huddle.ID, edit)
	if err != nil || changed.Revision != 2 || changed.Title != edit.Title {
		t.Fatalf("edit = %+v, %v", changed, err)
	}
	if _, err = ai.UpdateWorkProposal(f.ctx, f.a, started.Huddle.ID, edit); !errors.Is(err, ErrHuddleProposalConflict) {
		t.Fatalf("stale edit = %v", err)
	}
	// A tool retry must not overwrite the human's edits.
	again, err = ai.ProposeWork(f.ctx, *status.AttemptID, lease.Capability, in)
	if err != nil || again.Title != edit.Title {
		t.Fatalf("replay after edit = %+v, %v", again, err)
	}
	edit.ExpectedRevision, edit.Action, edit.Title, edit.Brief = 2, "reject", "", ""
	if _, err = ai.UpdateWorkProposal(f.ctx, f.a, started.Huddle.ID, edit); err != nil {
		t.Fatal(err)
	}
	fresh, _ := NewHuddleAIService(HuddleAIServiceOptions{Store: f.store, Enabled: true, TicksPerSecond: 100, ReasoningReserveTicks: 1000})
	status, err = fresh.Get(f.ctx, f.a, started.Huddle.ID)
	if err != nil || len(status.Proposals) != 1 || status.Proposals[0].State != "rejected" {
		t.Fatalf("persisted = %+v, %v", status.Proposals, err)
	}
	in.RequestKey = "draft-2"
	in.TaskID = uuid.NullUUID{UUID: f.seed.TaskA, Valid: true}
	proposal, err = ai.ProposeWork(f.ctx, *status.AttemptID, lease.Capability, in)
	if err != nil || proposal.TaskID == nil || *proposal.TaskID != f.seed.TaskA || proposal.TaskKey == "" {
		t.Fatalf("existing task proposal = %+v, %v", proposal, err)
	}
	for i := 3; i <= 20; i++ {
		in.RequestKey = fmt.Sprintf("draft-%d", i)
		if _, err = ai.ProposeWork(f.ctx, *status.AttemptID, lease.Capability, in); err != nil {
			t.Fatalf("proposal %d = %v", i, err)
		}
	}
	// Capacity must reject new drafts, not an exact replay of an existing draft.
	if _, err = ai.ProposeWork(f.ctx, *status.AttemptID, lease.Capability, in); err != nil {
		t.Fatalf("replay at capacity = %v", err)
	}
	in.RequestKey = "draft-21"
	if _, err = ai.ProposeWork(f.ctx, *status.AttemptID, lease.Capability, in); !errors.Is(err, ErrHuddleProposalLimit) {
		t.Fatalf("proposal cap = %v", err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE huddle_work_proposals SET expires_at=now()-interval '1 second' WHERE id=$1`, proposal.ID)
	edit.ProposalID, edit.ExpectedRevision = proposal.ID, 1
	if _, err = ai.UpdateWorkProposal(f.ctx, f.a, started.Huddle.ID, edit); !errors.Is(err, ErrHuddleProposalConflict) {
		t.Fatalf("edit expired draft = %v", err)
	}
	if _, err = ai.SetEnabled(f.ctx, f.a, started.Huddle.ID, SetHuddleAIInput{Enabled: false, ExpectedRevision: status.Revision, RequestKey: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	status, err = ai.Get(f.ctx, f.a, started.Huddle.ID)
	if err != nil || len(status.Proposals) != 20 || status.Proposals[0].State != "pending" || !status.Proposals[0].CanEdit {
		t.Fatalf("removed Agent = %+v, %v", status.Proposals, err)
	}
	edit.ProposalID, edit.ExpectedRevision = proposal.ID, 1
	if _, err = ai.UpdateWorkProposal(f.ctx, f.a, started.Huddle.ID, edit); !errors.Is(err, ErrHuddleProposalConflict) {
		t.Fatalf("edit removed invitation = %v", err)
	}
	var tasksAfter, runsAfter int
	if err = f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM tasks WHERE company_id=$1),(SELECT count(*) FROM runs WHERE company_id=$1)`, f.a.CompanyID).Scan(&tasksAfter, &runsAfter); err != nil || tasksAfter != tasksBefore || runsAfter != runsBefore {
		t.Fatalf("proposal created work = tasks %d -> %d, runs %d -> %d, %v", tasksBefore, tasksAfter, runsBefore, runsAfter, err)
	}
}
