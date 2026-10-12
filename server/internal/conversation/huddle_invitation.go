package conversation

import (
	"context"
	"errors"
	"time"

	"boardly.dev/boardly/server/internal/auth"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type HuddleInvitation struct {
	ID                 uuid.UUID               `json:"id"`
	HuddleID           uuid.UUID               `json:"huddleId"`
	InviterUserID      uuid.UUID               `json:"inviterUserId"`
	InviteeUserID      uuid.UUID               `json:"inviteeUserId"`
	Status             string                  `json:"status"`
	Revision           int64                   `json:"revision"`
	ExpiresAt          time.Time               `json:"expiresAt"`
	UpdatedAt          time.Time               `json:"updatedAt"`
	InviterName        string                  `json:"inviterName,omitempty"`
	SourceConversation *HuddleInvitationSource `json:"sourceConversation,omitempty"`
}

// HuddleInvitationSource is disclosed on recipient reads only while the
// recipient can read the source discussion; an invitation grants no such access.
type HuddleInvitationSource struct {
	ID    uuid.UUID      `json:"id"`
	Type  DiscussionType `json:"type"`
	Title string         `json:"title"`
}
type InviteHuddleRequest struct{ InviteeUserID, RequestKey uuid.UUID }
type RespondHuddleInvitationRequest struct {
	Accept           bool
	ExpectedRevision int64
}

func (s *DefaultHuddleService) ListPendingInvitations(ctx context.Context, principal auth.UserPrincipal) ([]HuddleInvitation, error) {
	rows, err := s.store.Queries().ListPendingHuddleInvitations(ctx, generated.ListPendingHuddleInvitationsParams{CompanyID: principal.CompanyID, InviteeUserID: principal.UserID})
	if err != nil {
		return nil, err
	}
	result := make([]HuddleInvitation, 0, len(rows))
	for _, row := range rows {
		result = append(result, invitationFromRow(row))
	}
	return result, hydrateHuddleInvitations(ctx, s.store.Queries(), principal, result)
}

func (s *DefaultHuddleService) ListInvitationHistory(ctx context.Context, principal auth.UserPrincipal) ([]HuddleInvitation, error) {
	rows, err := s.store.Queries().ListHuddleInvitationHistory(ctx, generated.ListHuddleInvitationHistoryParams{CompanyID: principal.CompanyID, InviteeUserID: principal.UserID})
	if err != nil {
		return nil, err
	}
	result := make([]HuddleInvitation, 0, len(rows))
	for _, row := range rows {
		result = append(result, invitationFromRow(row))
	}
	return result, hydrateHuddleInvitations(ctx, s.store.Queries(), principal, result)
}

func hydrateHuddleInvitations(ctx context.Context, q *generated.Queries, p auth.UserPrincipal, invitations []HuddleInvitation) error {
	if len(invitations) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(invitations))
	for _, invitation := range invitations {
		ids = append(ids, invitation.ID)
	}
	contexts, err := q.ListHuddleInvitationContexts(ctx, generated.ListHuddleInvitationContextsParams{
		CompanyID: p.CompanyID, InviteeUserID: p.UserID, InvitationIds: ids,
	})
	if err != nil {
		return err
	}
	args := discussionParameters(p)
	byID := make(map[uuid.UUID]generated.ListHuddleInvitationContextsRow, len(contexts))
	for _, row := range contexts {
		byID[row.ID] = row
		args.ConversationIds = append(args.ConversationIds, row.SourceConversationID)
	}
	// An empty ID filter means "list all" to the shared projection.
	if len(args.ConversationIds) == 0 {
		return nil
	}
	args.PageLimit = int32(len(args.ConversationIds))
	sources, err := q.ListDiscussionAccess(ctx, args)
	if err != nil {
		return err
	}
	bySource := make(map[uuid.UUID]HuddleInvitationSource, len(sources))
	for _, row := range sources {
		summary := discussionSummary(row, p.UserID)
		bySource[row.ID] = HuddleInvitationSource{ID: row.ID, Type: summary.Type, Title: summary.Title}
	}
	for i := range invitations {
		row := byID[invitations[i].ID]
		invitations[i].InviterName = row.InviterName
		if source, ok := bySource[row.SourceConversationID]; ok {
			invitations[i].SourceConversation = &source
		}
	}
	return nil
}

func (s *DefaultHuddleService) Invite(ctx context.Context, principal auth.UserPrincipal, huddleID uuid.UUID, request InviteHuddleRequest) (HuddleInvitation, error) {
	if request.InviteeUserID == uuid.Nil || request.RequestKey == uuid.Nil {
		return HuddleInvitation{}, ErrHuddleInvalid
	}
	var result HuddleInvitation
	created := false
	var expired *HuddleInvitation
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := q.GetOpenHuddleConnection(ctx, generated.GetOpenHuddleConnectionParams{CompanyID: principal.CompanyID, HuddleID: huddleID, UserID: principal.UserID}); err != nil {
			return ErrHuddleForbidden
		}
		if _, err := q.GetDiscussionEligibleUser(ctx, generated.GetDiscussionEligibleUserParams{CompanyID: principal.CompanyID, UserID: request.InviteeUserID}); err != nil {
			return ErrHuddleNotFound
		}
		row, err := q.GetHuddleInvitationByRequestKey(ctx, generated.GetHuddleInvitationByRequestKeyParams{CompanyID: principal.CompanyID, HuddleID: huddleID, InviteeUserID: request.InviteeUserID, RequestKey: request.RequestKey})
		if err == nil {
			result = invitationFromRow(row)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		row, err = q.GetPendingHuddleInvitation(ctx, generated.GetPendingHuddleInvitationParams{
			CompanyID: principal.CompanyID, HuddleID: huddleID, InviteeUserID: request.InviteeUserID,
		})
		if err == nil {
			if row.ExpiresAt.Time.After(time.Now().UTC()) {
				result = invitationFromRow(row)
				return nil
			}
			row, err = q.ExpirePendingHuddleInvitation(ctx, generated.ExpirePendingHuddleInvitationParams{
				CompanyID: principal.CompanyID, InvitationID: row.ID,
			})
			if err != nil {
				return err
			}
			invitation := invitationFromRow(row)
			expired = &invitation
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		row, err = q.InsertHuddleInvitation(ctx, generated.InsertHuddleInvitationParams{CompanyID: principal.CompanyID, HuddleID: huddleID, InviterUserID: principal.UserID, InviteeUserID: request.InviteeUserID, RequestKey: request.RequestKey, ExpiresAt: pgtype.Timestamptz{Time: time.Now().UTC().Add(5 * time.Minute), Valid: true}})
		if err != nil {
			return err
		}
		result = invitationFromRow(row)
		created = true
		return nil
	})
	if err == nil {
		if expired != nil {
			s.bus.Publish(ctx, TopicHuddle, huddleInvitationEvent(principal.CompanyID, EventHuddleInvitationChanged, *expired))
		}
		if created {
			s.bus.Publish(ctx, TopicHuddle, huddleInvitationEvent(principal.CompanyID, EventHuddleInvitationCreated, result))
		}
	}
	return result, err
}

func (s *DefaultHuddleService) RespondInvitation(ctx context.Context, principal auth.UserPrincipal, invitationID uuid.UUID, request RespondHuddleInvitationRequest) (HuddleInvitation, error) {
	var result HuddleInvitation
	changed := false
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		row, err := q.GetHuddleInvitationForUpdate(ctx, generated.GetHuddleInvitationForUpdateParams{CompanyID: principal.CompanyID, InvitationID: invitationID})
		if err != nil || row.InviteeUserID != principal.UserID {
			return ErrHuddleNotFound
		}
		if row.Status != "pending" {
			result = invitationFromRow(row)
			return nil
		}
		status := "declined"
		if request.Accept {
			status = "accepted"
		}
		row, err = q.UpdateHuddleInvitationStatus(ctx, generated.UpdateHuddleInvitationStatusParams{CompanyID: principal.CompanyID, InvitationID: invitationID, Status: status, Revision: request.ExpectedRevision})
		if err != nil {
			return ErrHuddleUnavailable
		}
		result = invitationFromRow(row)
		changed = true
		return nil
	})
	if err == nil && changed {
		s.bus.Publish(ctx, TopicHuddle, huddleInvitationEvent(principal.CompanyID, EventHuddleInvitationChanged, result))
	}
	return result, err
}

func invitationFromRow(row generated.HuddleInvitation) HuddleInvitation {
	return HuddleInvitation{ID: row.ID, HuddleID: row.HuddleID, InviterUserID: row.InviterUserID, InviteeUserID: row.InviteeUserID, Status: row.Status, Revision: row.Revision, ExpiresAt: row.ExpiresAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC()}
}
