//go:build integration

package task

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type proposalTestMedia struct{}

func (proposalTestMedia) EnsureRoom(context.Context, conversation.HuddleRoom) error { return nil }
func (proposalTestMedia) DeleteRoom(context.Context, string) error                  { return nil }
func (proposalTestMedia) ReadyRooms(_ context.Context, names []string) (map[string]bool, error) {
	ready := make(map[string]bool, len(names))
	for _, name := range names {
		ready[name] = true
	}
	return ready, nil
}

func (proposalTestMedia) IssueParticipantToken(context.Context, string, string) (conversation.HuddleMediaToken, error) {
	return conversation.HuddleMediaToken{URL: "ws://localhost", Token: "test"}, nil
}

func TestSubmitHuddleProposalAtomicReceiptAndAuthority(t *testing.T) {
	ctx, pool, f, s, p := startTaskHarness(t)
	peer := seedTaskMember(t, ctx, pool, f, "proposal-peer@example.com")
	conversations, err := conversation.NewConversationService(
		conversation.ServiceOptions{
			Store:           s.store,
			Bus:             s.bus,
			TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := conversations.CreateHumanDirect(
		ctx,
		p,
		conversation.CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{peer}},
	)
	if err != nil {
		t.Fatal(err)
	}
	huddles, err := conversation.NewHuddleService(
		conversation.HuddleServiceOptions{Store: s.store, Bus: s.bus, Media: proposalTestMedia{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	started, err := huddles.Start(
		ctx,
		p,
		direct.Conversation.ID,
		conversation.StartHuddleRequest{RequestKey: uuid.New()},
	)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := huddles.Join(ctx, p, started.Huddle.ID, conversation.JoinHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = huddles.ConfirmConnected(ctx, p.CompanyID, joined.Connection.ID); err != nil {
		t.Fatal(err)
	}
	createDraft := func(target uuid.NullUUID) generated.HuddleWorkProposal {
		t.Helper()
		row, err := s.store.Queries().
			CreateHuddleProposal(ctx, generated.CreateHuddleProposalParams{CompanyID: p.CompanyID, HuddleID: started.Huddle.ID, InvitationRevision: 1, CreatedByAgentID: f.AgentA, AgentName: "Eli", ProjectID: uuid.NullUUID{UUID: f.ProjectA, Valid: true}, ProjectTitle: "Project", TaskID: target, RequestKey: uuid.NewString(), RequestHash: []byte("test"), Title: "Reviewed login work", Brief: "Fix login and verify", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	draft := createDraft(uuid.NullUUID{})
	input := SubmitHuddleProposalInput{ExpectedRevision: 1, Confirm: true, MilestoneType: MilestoneTypeProduct}
	colleague := auth.UserPrincipal{UserID: peer, CompanyID: p.CompanyID, Role: auth.CompanyRoleMember}
	foreign := auth.UserPrincipal{UserID: f.UserB, CompanyID: f.CompanyB, Role: auth.CompanyRoleOwner}
	if _, err = s.SubmitHuddleProposal(
		ctx,
		foreign,
		started.Huddle.ID,
		draft.ID,
		input,
	); !errors.Is(
		err,
		conversation.ErrHuddleNotFound,
	) {
		t.Fatalf("foreign Company = %v", err)
	}
	if _, err = s.SubmitHuddleProposal(
		ctx,
		colleague,
		started.Huddle.ID,
		draft.ID,
		input,
	); !errors.Is(
		err,
		conversation.ErrHuddleForbidden,
	) {
		t.Fatalf("nonmoderator = %v", err)
	}
	stale := input
	stale.ExpectedRevision = 2
	if _, err = s.SubmitHuddleProposal(
		ctx,
		p,
		started.Huddle.ID,
		draft.ID,
		stale,
	); !errors.Is(
		err,
		conversation.ErrHuddleProposalConflict,
	) {
		t.Fatalf("stale = %v", err)
	}
	var before int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE company_id=$1`, p.CompanyID).
		Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Confirmation must work without a personal runtime binding.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`DELETE FROM agent_runtime_bindings WHERE company_id=$1 AND agent_id=$2 AND user_id=$3`,
		p.CompanyID,
		f.AgentA,
		p.UserID,
	)
	var after int
	// Both requests review the same revision, as with a double click or HTTP retry.
	var submissions [2]conversation.HuddleProposal
	var submitErrors [2]error
	var submissionsDone sync.WaitGroup
	for i := range submissions {
		submissionsDone.Go(func() {
			submissions[i], submitErrors[i] = s.SubmitHuddleProposal(
				ctx,
				p,
				started.Huddle.ID,
				draft.ID,
				input,
			)
		})
	}
	submissionsDone.Wait()
	result, err := submissions[0], submitErrors[0]
	if err != nil || result.State != "submitted" || result.SubmittedTaskID == nil {
		t.Fatalf("submit = %+v,%v", result, err)
	}
	if submitErrors[1] != nil || submissions[1].SubmittedTaskID == nil ||
		*submissions[1].SubmittedTaskID != *result.SubmittedTaskID {
		t.Fatalf("concurrent receipt = %+v, %v; want %+v", submissions[1], submitErrors[1], result)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE company_id=$1`, p.CompanyID).
		Scan(&after); err != nil ||
		after != before+1 {
		t.Fatalf("concurrent Tasks = %d, want %d; %v", after, before+1, err)
	}
	ai, err := conversation.NewHuddleAIService(conversation.HuddleAIServiceOptions{Store: s.store})
	if err != nil {
		t.Fatal(err)
	}
	assertProgress := func(want string) {
		t.Helper()
		status, readErr := ai.Get(ctx, p, started.Huddle.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, proposal := range status.Proposals {
			if proposal.ID == draft.ID && proposal.Work != nil && proposal.Work.State == want {
				return
			}
		}
		t.Fatalf("progress = %+v, want %s", status.Proposals, want)
	}
	assertProgress("confirmed")
	dbtest.MustExec(t, ctx, pool, `UPDATE runtimes SET status='online' WHERE id=$1`, f.RuntimeA)
	assertProgress("confirmed")
	task, err := s.Get(ctx, p, *result.SubmittedTaskID, nil)
	if err != nil || task.Assignee.ID != p.UserID || task.ExecutedBy.ID != f.AgentA || task.Title != draft.Title ||
		task.Description != draft.Brief {
		t.Fatalf("Task = %+v,%v", task, err)
	}
	if task.Status != StatusBacklog {
		t.Fatalf("Task status = %s, want backlog", task.Status)
	}
	var runCount int
	replay, err := s.SubmitHuddleProposal(ctx, p, started.Huddle.ID, draft.ID, input)
	if err != nil || replay.SubmittedTaskID == nil || *replay.SubmittedTaskID != task.ID {
		t.Fatalf("replay = %+v,%v", replay, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE task_id=$1`, task.ID).
		Scan(&runCount); err != nil ||
		runCount != 0 {
		t.Fatalf("Runs = %d,%v", runCount, err)
	}
	changed := input
	changed.MilestoneType = MilestoneTypeLaunch
	if _, err = s.SubmitHuddleProposal(
		ctx,
		p,
		started.Huddle.ID,
		draft.ID,
		changed,
	); !errors.Is(
		err,
		conversation.ErrHuddleProposalConflict,
	) {
		t.Fatalf("changed replay = %v", err)
	}
	existing := createDraft(uuid.NullUUID{UUID: task.ID, Valid: true})
	existingInput := SubmitHuddleProposalInput{
		ExpectedRevision:     1,
		Confirm:              true,
		ExpectedTaskRevision: task.Revision,
	}
	if _, err = s.SubmitHuddleProposal(ctx, p, started.Huddle.ID, existing.ID, existingInput); err != nil {
		t.Fatalf("existing Task confirmation = %v", err)
	}
	expired := createDraft(uuid.NullUUID{})
	humanTask, err := s.Create(
		ctx,
		p,
		CreateInput{
			ProjectID:     f.ProjectA,
			MilestoneType: MilestoneTypeProduct,
			Title:         "Original title",
			Description:   "Original description",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE tasks SET executed_by_agent_id=$2 WHERE id=$1`,
		humanTask.Task.ID,
		f.AgentA,
	)
	existingReady := createDraft(uuid.NullUUID{UUID: humanTask.Task.ID, Valid: true})
	existingInput.ExpectedTaskRevision = humanTask.Task.Revision
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET assignee_user_id=$2 WHERE id=$1`, humanTask.Task.ID, peer)
	if _, err = s.SubmitHuddleProposal(
		ctx,
		p,
		started.Huddle.ID,
		existingReady.ID,
		existingInput,
	); !errors.Is(
		err,
		ErrAssigneeRequired,
	) {
		t.Fatalf("another assignee = %v", err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE tasks SET assignee_user_id=$2,executed_by_agent_id=NULL WHERE id=$1`,
		humanTask.Task.ID,
		p.UserID,
	)
	if _, err = s.SubmitHuddleProposal(
		ctx,
		p,
		started.Huddle.ID,
		existingReady.ID,
		existingInput,
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("another executor = %v", err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE tasks SET executed_by_agent_id=$2 WHERE id=$1`,
		humanTask.Task.ID,
		f.AgentA,
	)
	accepted, err := s.SubmitHuddleProposal(ctx, p, started.Huddle.ID, existingReady.ID, existingInput)
	if err != nil || accepted.State != "submitted" {
		t.Fatalf("existing Task submit = %+v,%v", accepted, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE task_id=$1`, humanTask.Task.ID).
		Scan(&runCount); err != nil ||
		runCount != 0 {
		t.Fatalf("existing Task runs = %d, %v", runCount, err)
	}
	preserved, err := s.Get(ctx, p, humanTask.Task.ID, nil)
	if err != nil || preserved.Title != humanTask.Task.Title ||
		preserved.Description != humanTask.Task.Description ||
		preserved.Status != humanTask.Task.Status ||
		preserved.Revision != humanTask.Task.Revision {
		t.Fatalf("Task overwritten = %+v,%v", preserved, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE huddle_work_proposals SET expires_at=now()-interval '1 second' WHERE id=$1`,
		expired.ID,
	)
	if _, err = s.SubmitHuddleProposal(
		ctx,
		p,
		started.Huddle.ID,
		expired.ID,
		input,
	); !errors.Is(
		err,
		conversation.ErrHuddleProposalConflict,
	) {
		t.Fatalf("expired = %v", err)
	}
	if _, err = huddles.Leave(ctx, p, started.Huddle.ID, joined.Connection.ID); err != nil {
		t.Fatal(err)
	}
	assertProgress("confirmed")
}
