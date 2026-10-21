//go:build integration

package conversation

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestDirectMessageArtifactReferencesReachRunAndReplay(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	var id uuid.UUID
	err := f.pool.QueryRow(f.ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,'Forest website','A nature website','direct-reference',$3, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`, f.a.CompanyID, f.seed.RunA, f.a.UserID).
		Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	input := SendInput{RequestKey: uuid.New(), Content: "Review this", ArtifactIDs: []uuid.UUID{id}}
	target := MessageTarget{Kind: TargetConversation, ID: f.seed.DirectConversationA}
	// The direct chat and Artifact belong to the seed user.
	principal := f.a
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE outputs SET owner_user_id=$1 WHERE id=$2`, principal.UserID, id)
	// The real message route authenticates the user without company middleware.
	principal.CompanyID = uuid.Nil
	result, err := f.conversations.Send(f.ctx, principal, target, input)
	if err != nil {
		t.Fatal(err)
	}
	wantContent := "Review this\n\nReferenced artifacts:\n- artifactId: " + id.String()
	if result.View.Message.Content != wantContent {
		t.Fatalf("message context = %+v", result)
	}
	var prompt string
	if err := f.pool.QueryRow(f.ctx, `SELECT context->>'prompt' FROM runs WHERE id=$1`, *result.RunID).
		Scan(&prompt); err != nil ||
		prompt != wantContent {
		t.Fatalf("frozen prompt = %q, %v", prompt, err)
	}
	again, err := f.conversations.Send(f.ctx, principal, target, input)
	if err != nil || again.View.Message.ID != result.View.Message.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	queuedInput := input
	queuedInput.RequestKey = uuid.New()
	queuedInput.Content = "Review this again"
	queued, err := f.conversations.Send(f.ctx, principal, target, queuedInput)
	if err != nil || queued.QueueItem == nil || !strings.Contains(queued.QueueItem.Content, id.String()) {
		t.Fatalf("queued references = %+v, %v", queued, err)
	}
	replay, err := f.conversations.Send(f.ctx, principal, target, queuedInput)
	if err != nil || replay.QueueItem == nil || replay.QueueItem.ID != queued.QueueItem.ID {
		t.Fatalf("queued replay = %+v, %v", replay, err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE outputs SET owner_user_id=$1 WHERE id=$2`, f.b.UserID, id)
	replayAfterRevoke, err := f.conversations.Send(f.ctx, principal, target, input)
	if err != nil || replayAfterRevoke.View.Message.ID != result.View.Message.ID {
		t.Fatalf("replay after revoke = %+v, %v", replayAfterRevoke, err)
	}
	queueAfterRevoke, err := f.conversations.Send(f.ctx, principal, target, queuedInput)
	if err != nil || queueAfterRevoke.QueueItem == nil || queueAfterRevoke.QueueItem.ID != queued.QueueItem.ID {
		t.Fatalf("queue replay after revoke = %+v, %v", queueAfterRevoke, err)
	}
	input.RequestKey = uuid.New()
	input.ArtifactIDs = []uuid.UUID{uuid.New()}
	if _, err := f.conversations.Send(f.ctx, principal, target, input); err == nil {
		t.Fatal("accepted inaccessible Artifact")
	}
}
