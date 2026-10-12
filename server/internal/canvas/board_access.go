package canvas

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
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

func boardScopeLock(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	scope BoardScope,
	write bool,
) (generated.Board, BoardViewer, error) {
	viewer := BoardViewer{Actor: protocol.ActorRef{ID: p.UserID, Kind: protocol.ActorKindUser}}
	permission := auth.PermissionWhiteboardsRead
	if write {
		permission = auth.PermissionWhiteboardsEdit
	}
	authorize := boardAuthorizeRead
	if write {
		authorize = boardAuthorize
	}
	if err := authorize(ctx, q, p, permission); err != nil {
		return generated.Board{}, viewer, err
	}
	if scope.RunID != uuid.Nil {
		var agent uuid.UUID
		if write {
			var err error
			if agent, err = boardRun(ctx, q, p, scope.RunID); err != nil {
				return generated.Board{}, viewer, err
			}
		} else {
			var err error
			if agent, err = boardRunReader(ctx, q, p, scope.RunID); err != nil {
				return generated.Board{}, viewer, err
			}
		}
		viewer.Actor.ID, viewer.Actor.Kind, viewer.RunID = agent, protocol.ActorKindAgent, &scope.RunID
	}
	var b generated.Board
	var err error
	if write {
		b, err = q.LockBoard(ctx, generated.LockBoardParams{CompanyID: p.CompanyID, ID: scope.BoardID})
	} else {
		b, err = q.ReadBoard(ctx, generated.ReadBoardParams{CompanyID: p.CompanyID, ID: scope.BoardID})
	}
	if err != nil {
		return b, viewer, boardError(err)
	}
	access, err := q.GetBoardMemberAccess(
		ctx,
		generated.GetBoardMemberAccessParams{CompanyID: p.CompanyID, BoardID: scope.BoardID, UserID: p.UserID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, viewer, ErrBoardNotFound
	}
	if err != nil {
		return b, viewer, err
	}
	viewer.CanEdit = p.Can(auth.PermissionWhiteboardsEdit) && access != "read" && !b.ArchivedAt.Valid
	viewer.CanShare = scope.RunID == uuid.Nil && access == "manage" && p.Can(auth.PermissionWhiteboardsEdit)
	// The viewer is complete here, so a sharing change on an archived Board
	// can still narrow access.
	if write && b.ArchivedAt.Valid {
		return b, viewer, ErrBoardArchived
	}
	if write && !viewer.CanEdit {
		return b, viewer, ErrBoardForbidden
	}
	if viewer.RunID != nil {
		access, err := q.GetBoardAgentAccess(
			ctx,
			generated.GetBoardAgentAccessParams{
				CompanyID: p.CompanyID,
				BoardID:   scope.BoardID,
				AgentID:   viewer.Actor.ID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && write && access != "edit") {
			return b, viewer, ErrBoardForbidden
		}
		if err != nil {
			return b, viewer, err
		}
		viewer.CanEdit = viewer.CanEdit && access == "edit"
	}
	return b, viewer, nil
}

func (s *DefaultBoardService) ListForRun(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	offset int32,
) (BoardList, error) {
	result := BoardList{Boards: []Board{}}
	if offset < 0 {
		return result, ErrBoardInvalid
	}
	err := s.store.WithinTx(ctx, boardRead, func(q *generated.Queries) error {
		if err := boardAuthorizeRead(ctx, q, p, auth.PermissionWhiteboardsRead); err != nil {
			return err
		}
		agent, err := boardRunReader(ctx, q, p, id)
		if err != nil {
			return err
		}
		rows, err := q.ListBoardsForAgent(
			ctx,
			generated.ListBoardsForAgentParams{
				CompanyID:   p.CompanyID,
				ActorUserID: p.UserID,
				AgentID:     agent,
				RowOffset:   offset,
			},
		)
		if err != nil {
			return err
		}
		result.HasMore = len(rows) > boardListPage
		if result.HasMore {
			rows = rows[:boardListPage]
		}
		for _, row := range rows {
			b := boardDTO(row.Board, p)
			b.CanEdit = b.CanEdit && row.Access == "edit" && row.MemberAccess != "read"
			b.CanShare = false
			b.CanManage = false
			result.Boards = append(result.Boards, b)
		}
		return nil
	})
	return result, boardError(err)
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
