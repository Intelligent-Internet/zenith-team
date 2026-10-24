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
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// DocumentAgentInput links a human request to its existing document thread.
// The Conversation domain owns enqueueing; Canvas retains the comment transaction.
type DocumentAgentInput struct {
	RevisionChangeID uuid.UUID
	RevisionIndexes  []int
	Principal        auth.UserPrincipal
	ThreadID         uuid.UUID
	AgentID          uuid.UUID
	RequestKey       uuid.UUID
	WritingTarget    *DocumentWritingTarget
	Body             string
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
	Invoke    DocumentAgentInvoker
	Principal auth.UserPrincipal
	ThreadID  uuid.UUID
	Command   Command
}

var errDocumentAgentConversationChanged = errors.New("document agent conversation appeared while locking page")

type documentAgentConversationLocks struct {
	params db.LockDocumentAgentConversationsParams
	ids    []uuid.UUID
}

func lockCommentAgentConversations(
	ctx context.Context,
	q *db.Queries,
	request commentAgentRequest,
) (documentAgentConversationLocks, error) {
	if request.Command.ThreadID == nil || len(request.Command.AgentIDs) == 0 {
		return documentAgentConversationLocks{}, nil
	}
	params := db.LockDocumentAgentConversationsParams{
		CompanyID: request.Principal.CompanyID, DocumentThreadID: nullable(request.Command.ThreadID),
		CreatedByUserID: nullable(&request.Principal.UserID), AgentIds: request.Command.AgentIDs,
	}
	ids, err := q.LockDocumentAgentConversations(ctx, params)
	return documentAgentConversationLocks{params: params, ids: ids}, err
}

// check runs after locking the Page. A conversation created while we waited
// for that lock was absent from the prelock set; acquiring it now would invert
// completion's Conversation -> Artifact order. Restart the transaction instead.
func (locks documentAgentConversationLocks) check(ctx context.Context, q *db.Queries) error {
	if !locks.params.DocumentThreadID.Valid {
		return nil
	}
	ids, err := q.ListDocumentAgentConversations(ctx, db.ListDocumentAgentConversationsParams{
		CompanyID: locks.params.CompanyID, DocumentThreadID: locks.params.DocumentThreadID,
		CreatedByUserID: locks.params.CreatedByUserID, AgentIds: locks.params.AgentIds,
	})
	if err != nil {
		return err
	}
	for _, id := range ids {
		if !slices.Contains(locks.ids, id) {
			return errDocumentAgentConversationChanged
		}
	}
	return nil
}

func (s *DefaultCanvasService) invokeCommentAgents(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	request commentAgentRequest,
) error {
	request.Invoke = s.invokeDocumentAgent
	return invokePageCommentAgents(ctx, q, pending, request)
}

func invokePageCommentAgents(
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
	if request.Invoke == nil {
		return ErrInvalid
	}
	thread, err := q.GetDocumentThreadForInvocation(
		ctx,
		db.GetDocumentThreadForInvocationParams{CompanyID: in.Principal.CompanyID, ID: in.ThreadID},
	)
	if err != nil {
		return lookup(err)
	}
	if !thread.PageID.Valid || thread.Status != "open" {
		return ErrInvalid
	}
	if _, err = authorizeArtifactContent(ctx, q, in.Principal, thread.PageID.UUID, true); err != nil {
		return err
	}
	if thread.ProjectID.Valid {
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
	}
	for _, id := range agents {
		in.AgentID = id
		if err = request.Invoke(ctx, q, pending, in); err != nil {
			return err
		}
	}
	return nil
}

type DocumentAgentCompletion struct {
	Authoring wiki.UIAuthoringPolicy
	Run       db.Run
	Body      string
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
	principal := auth.UserPrincipal{CompanyID: run.CompanyID, UserID: run.ExecutionAuthorizedByUserID.UUID}
	access, err := auth.LockArtifactAccess(ctx, q, principal, document.PageID)
	if err != nil {
		return err
	}
	if !access.Edit {
		return nil
	}
	notice, err := stageDocumentWritingInTx(ctx, q, in)
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
	event, err := (documentTarget{PageID: document.PageID}).changed(ctx, q, principal, 0, document.PageID)
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
