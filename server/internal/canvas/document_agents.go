package canvas

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// DocumentAgentInput links a human request to its existing document thread.
// The Conversation domain owns enqueueing; Canvas retains the comment transaction.
type DocumentAgentInput struct {
	Principal     auth.UserPrincipal
	ThreadID      uuid.UUID
	AgentID       uuid.UUID
	RequestKey    uuid.UUID
	WritingTarget *DocumentWritingTarget
	Body          string
}

type DocumentAgentInvoker func(context.Context, *db.Queries, *[]events.Event, DocumentAgentInput) error

type Option func(*DefaultCanvasService)

func WithDocumentAgentInvoker(invoke DocumentAgentInvoker) Option {
	return func(s *DefaultCanvasService) { s.invokeDocumentAgent = invoke }
}

func validateCommentAgents(c Command) error {
	if c.WritingTarget != nil && (c.Type != "comment" || len(c.AgentIDs) != 1 || c.PageID == nil) {
		return ErrInvalid
	}
	if len(c.AgentIDs) == 0 {
		return nil
	}
	if (c.Type != "comment" && c.Type != "reply") || len(c.AgentIDs) > 5 {
		return ErrInvalid
	}
	for i, id := range c.AgentIDs {
		if id == uuid.Nil || slices.Contains(c.AgentIDs[:i], id) {
			return ErrInvalid
		}
	}
	return nil
}

type commentAgentRequest struct {
	Principal auth.UserPrincipal
	ThreadID  uuid.UUID
	Command   Command
}

func (s *DefaultCanvasService) invokeCommentAgents(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	request commentAgentRequest,
) error {
	agents := request.Command.AgentIDs
	in := DocumentAgentInput{
		Principal:     request.Principal,
		ThreadID:      request.ThreadID,
		RequestKey:    request.Command.RequestKey,
		Body:          request.Command.Body,
		WritingTarget: request.Command.WritingTarget,
	}
	if len(agents) == 0 {
		return nil
	}
	if s.invokeDocumentAgent == nil {
		return ErrInvalid
	}
	thread, err := q.GetDocumentThreadForInvocation(
		ctx,
		db.GetDocumentThreadForInvocationParams{CompanyID: in.Principal.CompanyID, ID: in.ThreadID},
	)
	if err != nil {
		return lookup(err)
	}
	if !thread.PageID.Valid || !thread.ProjectID.Valid || thread.Status != "open" {
		return ErrInvalid
	}
	if _, err = authorize(ctx, q, in.Principal, thread.ProjectID.UUID, true); err != nil {
		return err
	}
	if err = auth.RequireProjectPermissionHeld(
		ctx,
		q,
		in.Principal.CompanyID,
		thread.ProjectID.UUID,
		in.Principal.UserID,
		auth.ProjectPermissionTasksExecute,
	); err != nil {
		return err
	}
	for _, id := range agents {
		in.AgentID = id
		if err = s.invokeDocumentAgent(ctx, q, pending, in); err != nil {
			return err
		}
	}
	return nil
}

type DocumentAgentCompletion struct {
	Run  db.Run
	Body string
}

// CompleteDocumentAgentInTx publishes the reply into the original thread only
// while its authorizer still has access. A lost grant must not strand a run queue.
func CompleteDocumentAgentInTx(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	in DocumentAgentCompletion,
) error {
	run, body := in.Run, in.Body
	document, err := q.GetDocumentAgentContext(
		ctx,
		db.GetDocumentAgentContextParams{CompanyID: run.CompanyID, ConversationID: run.ConversationID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if run.Status != "completed" || body == "" {
		return nil
	}
	for _, permission := range []auth.ProjectPermission{auth.ProjectPermissionRead, auth.ProjectPermissionTasksWrite} {
		allowed, e := auth.HoldsProjectPermission(
			ctx,
			q,
			run.CompanyID,
			document.ProjectID.UUID,
			run.ExecutionAuthorizedByUserID.UUID,
			permission,
		)
		if e != nil {
			return e
		}
		if !allowed {
			return nil
		}
	}
	notice, err := stageDocumentWritingInTx(ctx, q, run, body)
	if err != nil {
		return err
	}
	if notice != "" {
		body += "\n\n" + notice
	}
	if err = q.InsertDocumentAgentReply(
		ctx,
		db.InsertDocumentAgentReplyParams{RunID: run.ID, Body: body},
	); err != nil {
		return fmt.Errorf("save document agent reply: %w", err)
	}
	event, err := changeEvent(
		ctx,
		q,
		auth.UserPrincipal{CompanyID: run.CompanyID, UserID: run.ExecutionAuthorizedByUserID.UUID},
		document.ProjectID.UUID,
		0,
		document.PageID,
	)
	if err != nil {
		return err
	}
	event.Actor = events.RunActor(
		run.AgentID.String(),
		run.ID.String(),
		run.ExecutionAuthorizedByUserID.UUID.String(),
	)
	*pending = append(*pending, *event)
	return nil
}

func checkDocumentAgentPage(ctx context.Context, q *db.Queries, p auth.UserPrincipal, run, page uuid.UUID) error {
	doc, err := q.GetDocumentAgentRun(
		ctx,
		db.GetDocumentAgentRunParams{CompanyID: p.CompanyID, RunID: run, ActorUserID: p.UserID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !doc.PageID.Valid || doc.PageID.UUID != page {
		return ErrForbidden
	}
	return nil
}
