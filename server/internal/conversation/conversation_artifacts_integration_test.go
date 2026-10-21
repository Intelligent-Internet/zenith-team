//go:build integration

package conversation

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestChannelArtifactsIncludeInvocationsAndSharesWithCurrentAccess(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	service, err := execution.NewDeliverableService(execution.DeliverableServiceOptions{Store: f.store, Bus: f.bus})
	if err != nil {
		t.Fatal(err)
	}
	message := f.send(t, taggedSend(t, f.seed.AgentA))
	invocation := f.invocation(t, message.ID)
	run := f.run(t, invocation.ID)
	insertOutput := func(runID uuid.UUID, title string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := f.pool.QueryRow(f.ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,$3,'Summary',$3,$4, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`, f.a.CompanyID, runID, title, f.a.UserID).
			Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	invocationOutput := insertOutput(run.ID, "Channel result")
	unrelatedOutput := insertOutput(f.seed.RunA, "Task result")
	if detail, err := service.GetArtifact(
		f.ctx,
		f.a,
		invocationOutput,
	); err != nil || detail.Source == nil || detail.Source.Type != "channel" ||
		detail.Source.ConversationID != f.channelID {
		t.Fatalf("channel artifact source = %+v, %v", detail.Source, err)
	}
	list := func(principal auth.UserPrincipal) execution.ConversationArtifactPage {
		t.Helper()
		page, err := service.ListConversationArtifacts(
			f.ctx,
			principal,
			f.channelID,
			execution.ArtifactListOptions{Limit: 20},
		)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	page := list(f.a)
	if len(page.Items) != 1 || page.Items[0].Artifact.ID != invocationOutput {
		t.Fatalf("manager artifacts = %+v, want only invocation output", page)
	}
	if page := list(f.b); len(page.Items) != 0 {
		t.Fatalf("member artifacts = %+v, want no private output", page)
	}
	if _, err := service.ListConversationArtifacts(
		f.ctx,
		f.owner,
		f.channelID,
		execution.ArtifactListOptions{Limit: 20},
	); !errors.Is(
		err,
		execution.ErrArtifactNotFound,
	) {
		t.Fatalf("nonmember owner error = %v, want not found", err)
	}

	// Sharing through a thread creates both a grant and a message link. It must
	// produce one list entry, even if the same Artifact is shared twice.
	for range 2 {
		input := textSend("Shared result")
		input.ThreadRootID = &message.ID
		input.ArtifactIDs = []uuid.UUID{invocationOutput, unrelatedOutput}
		f.send(t, input)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE outputs SET created_at='2026-09-01'::timestamptz WHERE id=$1`,
		invocationOutput,
	)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE outputs SET created_at='2026-09-02'::timestamptz WHERE id=$1`,
		unrelatedOutput,
	)
	first, err := service.ListConversationArtifacts(
		f.ctx,
		f.b,
		f.channelID,
		execution.ArtifactListOptions{Limit: 1},
	)
	if err != nil || len(first.Items) != 1 || first.Items[0].Artifact.ID != unrelatedOutput || first.Next == nil {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	last, err := service.ListConversationArtifacts(
		f.ctx,
		f.b,
		f.channelID,
		execution.ArtifactListOptions{Limit: 1, Before: first.Next},
	)
	if err != nil || len(last.Items) != 1 || last.Items[0].Artifact.ID != invocationOutput || last.Next != nil ||
		last.Items[0].RuntimeID != nil {
		t.Fatalf("last page = %+v, %v; want one output without another member's runtime", last, err)
	}

	// A message link is discovery, never authorization.
	dbtest.MustExec(t, f.ctx, f.pool, `DELETE FROM output_conversation_grants WHERE output_id=$1`, invocationOutput)
	if page := list(f.b); len(page.Items) != 1 || page.Items[0].Artifact.ID != unrelatedOutput {
		t.Fatalf("after grant revocation = %+v, want shared task output only", page)
	}
	// Revoking the creator's channel membership removes the list too, even
	// though they still own the underlying outputs.
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE conversation_members SET status='revoked',revoked_at=now() WHERE conversation_id=$1 AND user_id=$2`,
		f.channelID,
		f.a.UserID,
	)
	if _, err := service.ListConversationArtifacts(
		f.ctx,
		f.a,
		f.channelID,
		execution.ArtifactListOptions{Limit: 20},
	); !errors.Is(
		err,
		execution.ErrArtifactNotFound,
	) {
		t.Fatalf("revoked creator error = %v, want not found", err)
	}
	if detail, err := service.GetArtifact(f.ctx, f.a, invocationOutput); err != nil || detail.Source != nil {
		t.Fatalf("revoked member source = %+v, %v, want absent", detail.Source, err)
	}
	otherCompany := f.b
	otherCompany.CompanyID = f.seed.CompanyB
	if _, err := service.ListConversationArtifacts(
		f.ctx,
		otherCompany,
		f.channelID,
		execution.ArtifactListOptions{Limit: 20},
	); !errors.Is(
		err,
		execution.ErrArtifactNotFound,
	) {
		t.Fatalf("cross-company error = %v, want not found", err)
	}
}

// A Company-visible Artifact made by a Task Run in a restricted Project is
// Company-visible only to that Project's readers (ADR 0017 decision 12). A
// channel message linking it is discovery, not a share: the channel's
// Artifacts panel lists it for a member only while they read the Project.
func TestChannelArtifactsKeepARestrictedProjectsArtifactToItsReaders(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	service, err := execution.NewDeliverableService(execution.DeliverableServiceOptions{Store: f.store, Bus: f.bus})
	if err != nil {
		t.Fatal(err)
	}
	var outputID uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id,visibility, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,'Project result','Summary','project-result',$3,'company', origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`,
		f.a.CompanyID, f.seed.RunA, f.a.UserID).
		Scan(&outputID); err != nil {
		t.Fatal(err)
	}
	input := textSend("Linked project result")
	input.ArtifactIDs = []uuid.UUID{outputID}
	f.send(t, input)
	// Without the chat grant the message link alone remains.
	dbtest.MustExec(t, f.ctx, f.pool, `DELETE FROM output_conversation_grants WHERE output_id=$1`, outputID)
	listed := func(principal auth.UserPrincipal) bool {
		t.Helper()
		page, err := service.ListConversationArtifacts(
			f.ctx,
			principal,
			f.channelID,
			execution.ArtifactListOptions{Limit: 20},
		)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if item.Artifact.ID == outputID {
				return true
			}
		}
		return false
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`DELETE FROM project_grants WHERE project_id=$1 AND user_id=$2`,
		f.seed.ProjectA,
		f.b.UserID,
	)
	if listed(f.b) {
		t.Fatal("a channel member who cannot read the Project sees its Company-visible Artifact")
	}
	dbtest.GrantProject(t, f.ctx, f.pool, f.a.CompanyID, f.seed.ProjectA, f.b.UserID, "project.read")
	if !listed(f.b) {
		t.Fatal("a channel member who reads the Project does not see its Company-visible Artifact")
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE projects SET visibility='company' WHERE id=$1`, f.seed.ProjectA)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`DELETE FROM project_grants WHERE project_id=$1 AND user_id=$2`,
		f.seed.ProjectA,
		f.b.UserID,
	)
	if !listed(f.b) {
		t.Fatal("a Company-visible Project's Artifact is hidden from a channel member")
	}
}

func TestChannelArtifactsExcludeDeletedLinksAndOtherChannels(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	service, err := execution.NewDeliverableService(execution.DeliverableServiceOptions{Store: f.store, Bus: f.bus})
	if err != nil {
		t.Fatal(err)
	}
	var outputID uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,'Linked result','Summary','linked-result',$3, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`, f.a.CompanyID, f.seed.RunA, f.a.UserID).
		Scan(&outputID); err != nil {
		t.Fatal(err)
	}
	input := textSend("Shared artifact")
	input.ArtifactIDs = []uuid.UUID{outputID}
	message := f.send(t, input)
	dbtest.MustExec(t, f.ctx, f.pool, `DELETE FROM output_conversation_grants WHERE output_id=$1`, outputID)
	page, err := service.ListConversationArtifacts(
		f.ctx,
		f.a,
		f.channelID,
		execution.ArtifactListOptions{Limit: 20},
	)
	if err != nil || len(page.Items) != 1 || page.Items[0].Artifact.ID != outputID {
		t.Fatalf("owner's linked artifact = %+v, %v", page, err)
	}
	if _, err := f.discussion.DeleteMessage(
		f.ctx,
		f.a,
		DiscussionMessageTarget{ConversationID: f.channelID, MessageID: message.ID},
		message.Revision,
	); err != nil {
		t.Fatal(err)
	}
	page, err = service.ListConversationArtifacts(f.ctx, f.a, f.channelID, execution.ArtifactListOptions{Limit: 20})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("deleted link artifacts = %+v, %v, want empty", page, err)
	}

	other, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{RequestKey: uuid.New(), Name: "Other channel"},
	)
	if err != nil {
		t.Fatal(err)
	}
	message = f.send(t, taggedSend(t, f.seed.AgentA))
	invocation := f.invocation(t, message.ID)
	run := f.run(t, invocation.ID)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,'Channel result','Summary','channel-result',$3, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2`,
		f.a.CompanyID,
		run.ID,
		f.a.UserID,
	)
	page, err = service.ListConversationArtifacts(
		f.ctx,
		f.a,
		other.Conversation.ID,
		execution.ArtifactListOptions{Limit: 20},
	)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("other channel artifacts = %+v, %v, want empty", page, err)
	}
}
