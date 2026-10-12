package canvas

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

func (s *DefaultBoardService) Access(ctx context.Context, p auth.UserPrincipal, id uuid.UUID) (BoardAccess, error) {
	result := BoardAccess{Grants: []BoardGrant{}}
	err := s.store.WithinTx(ctx, boardRead, func(q *generated.Queries) error {
		b, viewer, err := boardScopeLock(ctx, q, p, BoardScope{BoardID: id}, false)
		if err != nil {
			return err
		}
		owner, err := q.BoardOwnerProfile(
			ctx,
			generated.BoardOwnerProfileParams{CompanyID: p.CompanyID, ID: id},
		)
		if err != nil {
			return err
		}
		result.Owner = protocol.ActorRef{
			ID:          b.CreatedBy,
			Kind:        protocol.ActorKindUser,
			DisplayName: owner.Name,
			AvatarURL:   util.TextToPtr(owner.AvatarUrl),
		}
		result.Permission = "read"
		if viewer.CanEdit {
			result.Permission = "edit"
		}
		if viewer.CanShare {
			result.Permission = "manage"
		}
		result.Visibility, result.CompanyAccess, result.Revision = b.Visibility, b.CompanyAccess, b.Revision
		if viewer.CanShare {
			result.PublicToken = util.NullUUIDPtr(b.PublicToken)
		}
		rows, err := q.ListBoardAccess(
			ctx,
			generated.ListBoardAccessParams{CompanyID: p.CompanyID, BoardID: id},
		)
		if err != nil {
			return err
		}
		for _, row := range rows {
			result.Grants = append(
				result.Grants,
				BoardGrant{
					Actor: protocol.ActorRef{
						ID:          row.ActorID,
						Kind:        protocol.ActorKind(row.ActorType),
						DisplayName: row.Name,
						AvatarURL:   util.TextToPtr(row.AvatarUrl),
					},
					Access: row.Access,
				},
			)
		}
		return nil
	})
	return result, boardError(err)
}

// BoardAgentLimit is how many unarchived Agents a Board invites: they are its
// chat's Agents, and a channel holds at most 20.
const BoardAgentLimit = 20

func (s *DefaultBoardService) SetAccess(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	in BoardAccessInput,
) error {
	if in.ExpectedRevision < 1 || in.Actor.ID == uuid.Nil ||
		!slices.Contains([]string{"read", "edit", "manage", "none"}, in.Access) ||
		(in.Actor.Kind != protocol.ActorKindUser && in.Actor.Kind != protocol.ActorKindAgent) ||
		(in.Actor.Kind == protocol.ActorKindAgent && in.Access == "manage") {
		return ErrBoardInvalid
	}
	narrows := func(q *generated.Queries, _ generated.Board) (bool, error) {
		if in.Access == "none" {
			return true, nil
		}
		var current string
		var err error
		if in.Actor.Kind == protocol.ActorKindUser {
			current, err = q.GetBoardMemberGrant(
				ctx,
				generated.GetBoardMemberGrantParams{CompanyID: p.CompanyID, BoardID: id, UserID: in.Actor.ID},
			)
		} else {
			current, err = q.GetBoardAgentAccess(
				ctx,
				generated.GetBoardAgentAccessParams{CompanyID: p.CompanyID, BoardID: id, AgentID: in.Actor.ID},
			)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return boardAccessRank(in.Access) < boardAccessRank(current), err
	}
	return s.changeAccess(
		ctx,
		p,
		id,
		in.ExpectedRevision,
		narrows,
		func(q *generated.Queries, b generated.Board) error {
			if in.Actor.Kind == protocol.ActorKindUser {
				if in.Actor.ID == b.CreatedBy {
					return ErrBoardInvalid
				}
				if in.Access == "none" {
					return q.DeleteBoardMemberAccess(
						ctx,
						generated.DeleteBoardMemberAccessParams{
							CompanyID: p.CompanyID,
							BoardID:   id,
							UserID:    in.Actor.ID,
						},
					)
				}
				member, err := q.GetCompanyMemberForShare(
					ctx,
					generated.GetCompanyMemberForShareParams{
						CompanyID: p.CompanyID,
						UserID:    in.Actor.ID,
					},
				)
				if err != nil {
					return err
				}
				if member.Status != "active" {
					return ErrBoardNotFound
				}
				return q.SetBoardMemberAccess(
					ctx,
					generated.SetBoardMemberAccessParams{
						CompanyID: p.CompanyID,
						BoardID:   id,
						UserID:    in.Actor.ID,
						Access:    in.Access,
					},
				)
			}
			if in.Access == "none" {
				return q.DeleteBoardAgentAccess(
					ctx,
					generated.DeleteBoardAgentAccessParams{
						CompanyID: p.CompanyID,
						BoardID:   id,
						AgentID:   in.Actor.ID,
					},
				)
			}
			if _, err := q.GetAgentForCompany(
				ctx,
				generated.GetAgentForCompanyParams{CompanyID: p.CompanyID, AgentID: in.Actor.ID},
			); err != nil {
				return err
			}
			// The Agents invited to a Board are its chat's Agents.
			if _, err := q.GetBoardAgentAccess(
				ctx,
				generated.GetBoardAgentAccessParams{
					CompanyID: p.CompanyID,
					BoardID:   id,
					AgentID:   in.Actor.ID,
				},
			); errors.Is(err, pgx.ErrNoRows) {
				count, err := q.CountBoardAgentGrants(
					ctx,
					generated.CountBoardAgentGrantsParams{CompanyID: p.CompanyID, BoardID: id},
				)
				if err != nil {
					return err
				}
				if count >= BoardAgentLimit {
					return ErrBoardLimit
				}
			} else if err != nil {
				return err
			}
			return q.SetBoardAgentAccess(
				ctx,
				generated.SetBoardAgentAccessParams{
					CompanyID: p.CompanyID,
					BoardID:   id,
					AgentID:   in.Actor.ID,
					Access:    in.Access,
				},
			)
		},
	)
}

func (s *DefaultBoardService) SetVisibility(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	in BoardVisibilityInput,
) error {
	if in.ExpectedRevision < 1 || !slices.Contains([]string{"private", "company", "public"}, in.Visibility) ||
		(in.CompanyAccess != "read" && in.CompanyAccess != "edit") {
		return ErrBoardInvalid
	}
	narrows := func(_ *generated.Queries, b generated.Board) (bool, error) {
		reach := map[string]int{"private": 0, "company": 1, "public": 2}
		if reach[in.Visibility] != reach[b.Visibility] {
			return reach[in.Visibility] < reach[b.Visibility], nil
		}
		return boardAccessRank(in.CompanyAccess) <= boardAccessRank(b.CompanyAccess), nil
	}
	return s.changeAccess(ctx, p, id, in.ExpectedRevision, narrows, func(q *generated.Queries, _ generated.Board) error {
		return q.SetBoardVisibility(
			ctx,
			generated.SetBoardVisibilityParams{
				CompanyID:     p.CompanyID,
				ID:            id,
				Visibility:    in.Visibility,
				CompanyAccess: in.CompanyAccess,
			},
		)
	})
}

func boardAccessRank(access string) int {
	return map[string]int{"read": 1, "edit": 2, "manage": 3}[access]
}

// The Board lock serializes scene writes and sharing changes. Refetch hints
// reach readers before and after revocation; subsequent content reaches only
// the current audience and every HTTP call resolves access again. An archived
// Board accepts only changes that narrow who reaches it.
func (s *DefaultBoardService) changeAccess(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	revision int64,
	narrows func(*generated.Queries, generated.Board) (bool, error),
	apply func(*generated.Queries, generated.Board) error,
) error {
	var pending events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		b, viewer, err := boardScopeLock(ctx, q, p, BoardScope{BoardID: id}, true)
		if errors.Is(err, ErrBoardArchived) {
			var narrowing bool
			if narrowing, err = narrows(q, b); err == nil && !narrowing {
				err = ErrBoardArchived
			}
		}
		if err != nil {
			return err
		}
		if !viewer.CanShare {
			return ErrBoardForbidden
		}
		if b.Revision != revision {
			return ErrBoardConflict
		}
		before, err := q.ListBoardAudience(
			ctx,
			generated.ListBoardAudienceParams{
				CompanyID:  p.CompanyID,
				BoardID:    id,
				ActorRoles: auth.RolesWith(auth.PermissionWhiteboardsRead),
			},
		)
		if err != nil {
			return err
		}
		if err = apply(q, b); err != nil {
			return err
		}
		if err = q.AdvanceBoardAccessRevision(
			ctx,
			generated.AdvanceBoardAccessRevisionParams{CompanyID: p.CompanyID, ID: id},
		); err != nil {
			return err
		}
		seq, err := boardAdvance(ctx, q, p, id, boardChanges{})
		if err != nil {
			return err
		}
		pending, err = boardAccessEvent(ctx, q, p, id, seq, before)
		return err
	})
	if err == nil {
		s.publishEvent(ctx, pending)
	}
	return boardError(err)
}

// boardAccessEvent tells the audience before and after an access change.
func boardAccessEvent(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	id uuid.UUID,
	seq int64,
	before []uuid.UUID,
) (events.Event, error) {
	pending := boardChangeEvent(p, id, seq)
	if err := stampBoardAudience(ctx, q, &pending); err != nil {
		return pending, err
	}
	change, err := events.PayloadOf[BoardChanged](pending)
	if err != nil {
		return pending, err
	}
	change.Access = true
	change.RecipientUserIDs = append(change.RecipientUserIDs, before...)
	slices.SortFunc(change.RecipientUserIDs, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	change.RecipientUserIDs = slices.Compact(change.RecipientUserIDs)
	pending.Payload = change
	return pending, nil
}

// Claim gives a Company Whiteboard manager manage access to a Board no active
// member manages, so its sharing and lifecycle never outlive its owner.
func (s *DefaultBoardService) Claim(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	expectedRevision int64,
) (Board, error) {
	var result Board
	var pending events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := boardAuthorize(ctx, q, p, auth.PermissionWhiteboardsManage); err != nil {
			return err
		}
		if _, err := q.LockBoard(ctx, generated.LockBoardParams{CompanyID: p.CompanyID, ID: id}); err != nil {
			return err
		}
		before, err := q.ListBoardAudience(
			ctx,
			generated.ListBoardAudienceParams{
				CompanyID:  p.CompanyID,
				BoardID:    id,
				ActorRoles: auth.RolesWith(auth.PermissionWhiteboardsRead),
			},
		)
		if err != nil {
			return err
		}
		row, err := q.ClaimUnownedBoard(
			ctx,
			generated.ClaimUnownedBoardParams{CompanyID: p.CompanyID, ID: id, Revision: expectedRevision},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBoardConflict
		}
		if err != nil {
			return err
		}
		if err = q.SetBoardMemberAccess(
			ctx,
			generated.SetBoardMemberAccessParams{CompanyID: p.CompanyID, BoardID: id, UserID: p.UserID, Access: "manage"},
		); err != nil {
			return err
		}
		seq, err := boardAdvance(ctx, q, p, id, boardChanges{})
		if err != nil {
			return err
		}
		row.HeadSeq = seq
		result = boardDTO(row, p)
		result.CanShare = true
		pending, err = boardAccessEvent(ctx, q, p, id, seq, before)
		return err
	})
	if err == nil {
		s.publishEvent(ctx, pending)
	}
	return result, boardError(err)
}

// RevokeBoardGrantsInTx removes a departing member's Board grants inside the
// caller's revocation transaction, which already holds the Company lock, and
// queues an access hint for every Board that lost one.
func RevokeBoardGrantsInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	companyID, userID, actorUserID uuid.UUID,
) error {
	boards, err := q.DeleteMemberBoardGrants(
		ctx,
		generated.DeleteMemberBoardGrantsParams{CompanyID: companyID, UserID: userID},
	)
	if err != nil {
		return err
	}
	slices.SortFunc(boards, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	actor := auth.UserPrincipal{CompanyID: companyID, UserID: actorUserID}
	for _, board := range boards {
		if _, err = q.LockBoard(ctx, generated.LockBoardParams{CompanyID: companyID, ID: board}); err != nil {
			return err
		}
		if err = q.AdvanceBoardAccessRevision(
			ctx,
			generated.AdvanceBoardAccessRevisionParams{CompanyID: companyID, ID: board},
		); err != nil {
			return err
		}
		seq, err := boardAdvance(ctx, q, actor, board, boardChanges{})
		if err != nil {
			return err
		}
		event, err := boardAccessEvent(ctx, q, actor, board, seq, nil)
		if err != nil {
			return err
		}
		*pending = append(*pending, event)
	}
	return nil
}
