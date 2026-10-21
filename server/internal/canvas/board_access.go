package canvas

import (
	"context"
	"slices"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// boardRunReader checks the same live Run fence for a read without taking the
// Task, Conversation and Run write locks a mutation needs.
func boardRunReader(ctx context.Context, q *generated.Queries, p auth.UserPrincipal, id uuid.UUID) (uuid.UUID, error) {
	run, err := q.GetBoardRunReader(ctx, generated.GetBoardRunReaderParams{CompanyID: p.CompanyID, ID: id})
	if err != nil {
		return uuid.Nil, boardError(err)
	}
	if run.ConversationKind == "schedule" || run.Status != "running" ||
		run.ExecutionAuthorizedByUserID != (uuid.NullUUID{UUID: p.UserID, Valid: true}) ||
		(run.TaskID.Valid && (run.AssigneeUserID != (uuid.NullUUID{UUID: p.UserID, Valid: true}) ||
			!run.TaskExecutionGeneration.Valid || run.TaskExecutionGeneration != run.ExecutionGeneration)) {
		return uuid.Nil, ErrBoardForbidden
	}
	return run.AgentID, nil
}

// boardRun fences a Run's Board write. Company/member, Task, Run, Board is the
// execution lock order; the Task and Run are held in share mode, which blocks
// a handoff, cancellation or completion until the write commits without
// blocking the Run's transcript or its channel's queue. A cancellation,
// handoff or invitation revocation fences retries too.
func boardRun(ctx context.Context, q *generated.Queries, p auth.UserPrincipal, id uuid.UUID) (uuid.UUID, error) {
	run, err := q.GetRunInCompany(ctx, generated.GetRunInCompanyParams{ID: id, CompanyID: p.CompanyID})
	if err != nil {
		return uuid.Nil, boardError(err)
	}
	var generation int64
	if run.TaskID.Valid {
		task, err := q.LockBoardRunTask(
			ctx,
			generated.LockBoardRunTaskParams{CompanyID: p.CompanyID, ID: run.TaskID.UUID},
		)
		if err != nil {
			return uuid.Nil, boardError(err)
		}
		if task.AssigneeUserID != p.UserID {
			return uuid.Nil, ErrBoardForbidden
		}
		generation = task.ExecutionGeneration
	}
	locked, err := q.LockBoardRun(ctx, generated.LockBoardRunParams{CompanyID: p.CompanyID, ID: id})
	if err != nil {
		return uuid.Nil, boardError(err)
	}
	if locked.ConversationKind == "schedule" || locked.Status != "running" ||
		!locked.ExecutionAuthorizedByUserID.Valid || locked.ExecutionAuthorizedByUserID.UUID != p.UserID ||
		(locked.TaskID.Valid && (!locked.TaskExecutionGeneration.Valid || locked.TaskExecutionGeneration.Int64 != generation)) {
		return uuid.Nil, ErrBoardForbidden
	}
	return locked.AgentID, nil
}

// boardRecord is a Board with the title of the Whiteboard Artifact it is.
type boardRecord struct {
	generated.Board
	Title string
}

// boardScopeLock authorizes a Board call against its Whiteboard Artifact:
// reading needs any access to the Artifact, editing an edit grant or
// ownership, sharing ownership or a manage grant. A Run acts with its
// authorizing member's access. A write takes the Company/member lifecycle
// locks, holds the Run, then the Artifact's row against a sharing change, then
// the Board; a read takes none.
func boardScopeLock(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	scope BoardScope,
	write bool,
) (boardRecord, BoardViewer, error) {
	viewer := BoardViewer{Actor: protocol.ActorRef{ID: p.UserID, Kind: protocol.ActorKindUser}}
	var err error
	if write {
		err = boardAuthorize(ctx, q, p)
	} else {
		err = boardAuthorizeRead(ctx, q, p)
	}
	if err != nil {
		return boardRecord{}, viewer, err
	}
	if scope.RunID != uuid.Nil {
		var agent uuid.UUID
		if write {
			agent, err = boardRun(ctx, q, p, scope.RunID)
		} else {
			agent, err = boardRunReader(ctx, q, p, scope.RunID)
		}
		if err != nil {
			return boardRecord{}, viewer, err
		}
		viewer.Actor.ID, viewer.Actor.Kind, viewer.RunID = agent, protocol.ActorKindAgent, &scope.RunID
	}
	var level int32
	if write {
		level, err = auth.OutputAccessInTx(ctx, q, p, scope.BoardID)
	} else {
		level, err = q.GetOutputAccessForManage(
			ctx,
			generated.GetOutputAccessForManageParams{
				CompanyID:   p.CompanyID,
				OutputID:    scope.BoardID,
				ActorUserID: p.UserID,
			},
		)
	}
	if err != nil {
		return boardRecord{}, viewer, err
	}
	if level == 0 {
		return boardRecord{}, viewer, ErrBoardNotFound
	}
	var b boardRecord
	if write {
		var row generated.LockBoardRow
		row, err = q.LockBoard(ctx, generated.LockBoardParams{CompanyID: p.CompanyID, ID: scope.BoardID})
		b = boardRecord{Board: row.Board, Title: row.Title}
	} else {
		var row generated.ReadBoardRow
		row, err = q.ReadBoard(ctx, generated.ReadBoardParams{CompanyID: p.CompanyID, ID: scope.BoardID})
		b = boardRecord{Board: row.Board, Title: row.Title}
	}
	if err != nil {
		return b, viewer, boardError(err)
	}
	viewer.CanEdit = level >= 2
	viewer.CanShare = scope.RunID == uuid.Nil && level >= 3
	if write && !viewer.CanEdit {
		return b, viewer, ErrBoardForbidden
	}
	return b, viewer, nil
}

func boardActor(user uuid.UUID, agent uuid.NullUUID, name string) protocol.ActorRef {
	actor := protocol.ActorRef{ID: user, Kind: protocol.ActorKindUser, DisplayName: name}
	if agent.Valid {
		actor.ID, actor.Kind = agent.UUID, protocol.ActorKindAgent
	}
	return actor
}

// boardEventActor names who changed the Board: a member, or an Agent acting in
// its proven Run for the member who authorized it.
func boardEventActor(p auth.UserPrincipal, viewer BoardViewer) events.Actor {
	if viewer.Actor.Kind == protocol.ActorKindAgent && viewer.RunID != nil {
		return events.RunActor(viewer.Actor.ID.String(), viewer.RunID.String(), p.UserID.String())
	}
	return events.UserActor(viewer.Actor.ID.String())
}

func boardStampChanges(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	board uuid.UUID,
	changes boardChanges,
	created map[string]uuid.UUID,
	viewer BoardViewer,
) error {
	createdIDs := make([]uuid.UUID, 0, len(created))
	for _, id := range created {
		createdIDs = append(createdIDs, id)
	}
	agent, run := boardAttribution(viewer)
	// A removed ID names either a deleted item or a removed connector.
	items := slices.Concat(changes.Items, changes.Removed)
	if err := q.StampBoardItemsAttribution(
		ctx,
		generated.StampBoardItemsAttributionParams{
			CompanyID:  p.CompanyID,
			BoardID:    board,
			Ids:        items,
			CreatedIds: createdIDs,
			UpdatedBy:  p.UserID,
			AgentID:    agent,
			RunID:      run,
		},
	); err != nil {
		return err
	}
	return q.StampBoardEdgesAttribution(
		ctx,
		generated.StampBoardEdgesAttributionParams{
			CompanyID:  p.CompanyID,
			BoardID:    board,
			Ids:        slices.Concat(changes.Edges, changes.Removed),
			CreatedIds: createdIDs,
			AgentID:    agent,
			RunID:      run,
		},
	)
}

// boardAttribution names the Agent and Run behind a write, or neither for a
// member's own.
func boardAttribution(viewer BoardViewer) (uuid.NullUUID, uuid.NullUUID) {
	if viewer.Actor.Kind != protocol.ActorKindAgent {
		return uuid.NullUUID{}, uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: viewer.Actor.ID, Valid: true}, nullable(viewer.RunID)
}
