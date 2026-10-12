package conversation

import (
	"context"
	"encoding/json"
	"fmt"

	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	db "boardly.dev/boardly/server/pkg/db/generated"
)

// InvokeDocumentAgentInTx reuses the ordinary direct-run queue inside the
// caller's comment transaction. No Task or independently committed prompt exists.
func InvokeDocumentAgentInTx(ctx context.Context, q *db.Queries, pending *[]events.Event, in canvas.DocumentAgentInput) error {
	conv, err := q.EnsureDocumentAgentConversation(ctx, db.EnsureDocumentAgentConversationParams{CompanyID: in.Principal.CompanyID, ThreadID: in.ThreadID, AgentID: in.AgentID, ActorUserID: in.Principal.UserID})
	if err != nil {
		return fmt.Errorf("open document agent conversation: %w", err)
	}
	if err = q.InitializeConversationModelSettings(ctx, db.InitializeConversationModelSettingsParams{ConversationID: conv.ID, AgentID: in.AgentID}); err != nil {
		return err
	}
	document, err := q.GetDocumentAgentContext(ctx, db.GetDocumentAgentContextParams{CompanyID: in.Principal.CompanyID, ConversationID: conv.ID})
	if err != nil {
		return err
	}
	prompt, snapshot, err := canvas.DocumentAgentPromptInTx(ctx, q, in, document)
	if err != nil {
		return err
	}

	if err = execution.AuthorizeAgentInvocationInTx(ctx, q, execution.RunTrigger{CompanyID: conv.CompanyID, AgentID: in.AgentID, AuthorizedByUserID: in.Principal.UserID}); err != nil {
		return err
	}
	resolved := resolvedConversationTarget{CompanyID: conv.CompanyID, ConversationID: conv.ID, ConversationKind: "direct", AgentID: &in.AgentID, ProjectID: &document.ProjectID.UUID}
	_, handled, err := queueMessageSendIfRunning(ctx, q, pending, in.Principal, MessageTarget{Kind: TargetConversation, ID: conv.ID}, SendInput{RequestKey: in.RequestKey, Content: prompt}, resolved)
	if err != nil || handled {
		return err
	}
	run, err := execution.EnqueueRunInTx(ctx, q, pending, execution.RunTrigger{CompanyID: conv.CompanyID, ConversationID: conv.ID, AgentID: in.AgentID, TriggerPrompt: prompt, DocumentWriting: snapshot != nil, Source: execution.RunSourceMessage, ActorUserID: in.Principal.UserID, AuthorizedByUserID: in.Principal.UserID})
	if err != nil {
		return err
	}
	if snapshot != nil {
		target, err := json.Marshal(snapshot.Target)
		if err != nil {
			return err
		}
		if err = q.SaveDocumentWritingRequest(ctx, db.SaveDocumentWritingRequestParams{
			CompanyID: conv.CompanyID, RunID: run.ID, PageID: document.PageID, RequestKey: in.RequestKey,
			Target: target, BeforeContent: snapshot.Content, Sequence: snapshot.Sequence,
		}); err != nil {
			return err
		}
	}
	_, err = insertRequestedUserMessage(ctx, q, run.ID, in.Principal.UserID, in.RequestKey, prompt, conv.ID, conv.CompanyID, &document.ProjectID.UUID)
	return err
}
