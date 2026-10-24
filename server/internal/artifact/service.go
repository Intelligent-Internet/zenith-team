// Package artifact owns the identity, access and discovery of ongoing work.
// It is separate from canvas placement, Page content and Run delivery domains:
// those domains consume its authority without owning the work's lifecycle.
package artifact

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

var (
	ErrNotFound  = errors.New("artifact is unavailable")
	ErrForbidden = errors.New("artifact access cannot be changed")
	ErrInvalid   = errors.New("invalid artifact access change")
	ErrConflict  = errors.New("artifact access changed")
)

type ArtifactService interface {
	Content(context.Context, auth.UserPrincipal, ContentInput) (Content, error)
	ContentAttachment(context.Context, auth.UserPrincipal, ContentAttachmentInput) (uuid.UUID, error)
	Access(context.Context, auth.UserPrincipal, uuid.UUID) (Access, error)
	ChangeAccess(context.Context, auth.UserPrincipal, AccessChange) (Access, error)
	Pages(context.Context, auth.UserPrincipal) ([]Page, error)
}

var _ ArtifactService = (*DefaultArtifactService)(nil)

type DefaultArtifactService struct {
	store *db.Store
	bus   *events.Bus
}

func NewArtifactService(store *db.Store, bus *events.Bus) *DefaultArtifactService {
	return &DefaultArtifactService{store: store, bus: bus}
}

type Grant struct {
	UserID      uuid.UUID `json:"userId"`
	DisplayName string    `json:"displayName"`
	Permission  string    `json:"permission"`
}
type ConversationGrant struct {
	ConversationID uuid.UUID `json:"conversationId"`
	Title          string    `json:"title"`
}
type Access struct {
	Type               string              `json:"type"`
	ID                 uuid.UUID           `json:"id"`
	Title              string              `json:"title"`
	Ownership          string              `json:"ownership"`
	ProjectID          *uuid.UUID          `json:"projectId,omitempty"`
	OwnerUserID        *uuid.UUID          `json:"ownerUserId,omitempty"`
	Visibility         string              `json:"visibility"`
	CanEdit            bool                `json:"canEdit"`
	CanManage          bool                `json:"canManage"`
	Revision           int64               `json:"revision"`
	ConversationGrants []ConversationGrant `json:"conversationGrants"`
	Grants             []Grant             `json:"grants"`
}
type GrantChange struct {
	UserID     uuid.UUID `json:"userId"`
	Permission *string   `json:"permission"`
}
type AccessChange struct {
	ArtifactID           uuid.UUID
	ExpectedRevision     int64
	Visibility           *string
	Grant                *GrantChange
	RevokeConversationID *uuid.UUID
}
type Page struct {
	ID          uuid.UUID  `json:"id"`
	Title       string     `json:"title"`
	ProjectID   *uuid.UUID `json:"projectId,omitempty"`
	OwnerUserID *uuid.UUID `json:"ownerUserId,omitempty"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

func (s *DefaultArtifactService) Access(ctx context.Context, p auth.UserPrincipal, id uuid.UUID) (Access, error) {
	var result Access
	err := s.store.WithinTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(q *generated.Queries) error {
		var err error
		result, err = readAccess(ctx, q, p, id)
		return err
	})
	return result, err
}

func readAccess(ctx context.Context, q *generated.Queries, p auth.UserPrincipal, id uuid.UUID) (Access, error) {
	authority, err := auth.ResolveArtifactAccess(ctx, q, p, id)
	if err != nil {
		return Access{}, err
	}
	if !authority.Read {
		return Access{}, ErrNotFound
	}
	row, err := q.GetArtifactIdentity(ctx, generated.GetArtifactIdentityParams{CompanyID: p.CompanyID, ID: id})
	if err != nil {
		return Access{}, err
	}
	result := Access{
		Type:               row.Type,
		ID:                 id,
		Title:              row.Title,
		Visibility:         row.Visibility,
		Revision:           row.AccessVersion,
		Ownership:          "person",
		CanManage:          authority.Manage,
		CanEdit:            authority.Edit,
		Grants:             []Grant{},
		ConversationGrants: []ConversationGrant{},
	}
	switch {
	case row.Type == "wiki":
		result.Ownership = "company"
		result.CanManage = false
	case row.ProjectID.Valid:
		result.Ownership = "project"
		result.ProjectID = &row.ProjectID.UUID
		result.CanManage = false
	default:
		result.OwnerUserID = &row.OwnerUserID.UUID
		grants, err := q.ListArtifactGrants(
			ctx,
			generated.ListArtifactGrantsParams{CompanyID: p.CompanyID, ArtifactID: id},
		)
		if err != nil {
			return Access{}, err
		}
		conversations, err := q.ListArtifactConversationGrants(
			ctx,
			generated.ListArtifactConversationGrantsParams{
				CompanyID: p.CompanyID, ArtifactID: id, ActorUserID: p.UserID,
			},
		)
		if err != nil {
			return Access{}, err
		}
		for _, conversation := range conversations {
			result.ConversationGrants = append(
				result.ConversationGrants,
				ConversationGrant{
					ConversationID: conversation.ConversationID,
					Title:          conversation.Title,
				},
			)
		}
		for _, grant := range grants {
			result.Grants = append(
				result.Grants,
				Grant{
					UserID:      grant.UserID,
					DisplayName: grant.DisplayName,
					Permission:  grant.Permission,
				},
			)
		}
	}
	return result, nil
}

func (s *DefaultArtifactService) ChangeAccess(
	ctx context.Context,
	p auth.UserPrincipal,
	in AccessChange,
) (Access, error) {
	if in.ExpectedRevision < 1 || in.Visibility == nil && in.Grant == nil && in.RevokeConversationID == nil ||
		in.RevokeConversationID != nil && *in.RevokeConversationID == uuid.Nil {
		return Access{}, ErrInvalid
	}
	if in.Visibility != nil && !slices.Contains([]string{"private", "company"}, *in.Visibility) {
		return Access{}, ErrInvalid
	}
	if in.Grant != nil &&
		(in.Grant.UserID == uuid.Nil || in.Grant.Permission != nil && !slices.Contains([]string{"view", "edit", "manage"}, *in.Grant.Permission)) {
		return Access{}, ErrInvalid
	}
	var result Access
	var pending events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		authority, err := auth.LockArtifactAccess(ctx, q, p, in.ArtifactID)
		if err != nil {
			return err
		}
		if !authority.Read {
			return ErrNotFound
		}
		if !authority.Manage {
			return ErrForbidden
		}
		row, err := q.GetArtifactIdentity(
			ctx,
			generated.GetArtifactIdentityParams{CompanyID: p.CompanyID, ID: in.ArtifactID},
		)
		if err != nil {
			return err
		}
		if row.ProjectID.Valid || row.Type == "wiki" {
			return ErrForbidden
		}
		if row.AccessVersion != in.ExpectedRevision {
			return ErrConflict
		}
		before, err := q.ListArtifactReaders(
			ctx,
			generated.ListArtifactReadersParams{CompanyID: p.CompanyID, ArtifactID: in.ArtifactID},
		)
		if err != nil {
			return err
		}
		if in.Grant != nil {
			grant := in.Grant
			if grant.UserID == p.UserID || grant.UserID == row.OwnerUserID.UUID {
				return ErrForbidden
			}
			if grant.Permission == nil {
				if _, err = q.DeleteArtifactGrant(
					ctx,
					generated.DeleteArtifactGrantParams{
						CompanyID:  p.CompanyID,
						ArtifactID: in.ArtifactID,
						UserID:     grant.UserID,
					},
				); err != nil {
					return err
				}
			} else {
				_, err = q.UpsertArtifactGrant(
					ctx,
					generated.UpsertArtifactGrantParams{
						CompanyID:   p.CompanyID,
						ArtifactID:  in.ArtifactID,
						UserID:      grant.UserID,
						Permission:  *grant.Permission,
						ActorUserID: p.UserID,
					},
				)
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrInvalid
				}
				if err != nil {
					return err
				}
			}
		}
		if in.RevokeConversationID != nil {
			if _, err = q.DeleteArtifactConversationGrant(
				ctx,
				generated.DeleteArtifactConversationGrantParams{
					CompanyID:      p.CompanyID,
					ArtifactID:     in.ArtifactID,
					ConversationID: *in.RevokeConversationID,
				},
			); err != nil {
				return err
			}
		}
		if in.Visibility != nil {
			if err = q.UpdateArtifactVisibility(
				ctx,
				generated.UpdateArtifactVisibilityParams{
					CompanyID:  p.CompanyID,
					ID:         in.ArtifactID,
					Visibility: *in.Visibility,
				},
			); err != nil {
				return err
			}
		}
		if _, err = q.AdvanceArtifactAccess(
			ctx,
			generated.AdvanceArtifactAccessParams{CompanyID: p.CompanyID, ID: in.ArtifactID},
		); err != nil {
			return err
		}
		after, err := q.ListArtifactReaders(
			ctx,
			generated.ListArtifactReadersParams{CompanyID: p.CompanyID, ArtifactID: in.ArtifactID},
		)
		if err != nil {
			return err
		}
		pending = events.Event{
			EntityType: Topic,
			Name:       EventAccessChanged,
			CompanyID:  p.CompanyID,
			Actor:      events.UserActor(p.UserID.String()),
			CreatedAt:  time.Now().UTC(),
			Payload:    AccessChanged{RecipientUserIDs: append(before, after...)},
		}
		result, err = readAccess(ctx, q, p, in.ArtifactID)
		return err
	})
	if err != nil {
		return Access{}, err
	}
	s.bus.Publish(ctx, Topic, pending)
	return result, nil
}

func (s *DefaultArtifactService) Pages(ctx context.Context, p auth.UserPrincipal) ([]Page, error) {
	rows, err := s.store.Queries().
		ListReadablePages(ctx, generated.ListReadablePagesParams{CompanyID: p.CompanyID, ActorUserID: p.UserID})
	if err != nil {
		return nil, err
	}
	result := make([]Page, 0, len(rows))
	for _, row := range rows {
		page := Page{ID: row.ID, Title: row.Title, UpdatedAt: row.UpdatedAt.Time}
		if row.ProjectID.Valid {
			page.ProjectID = &row.ProjectID.UUID
		}
		if row.OwnerUserID.Valid {
			page.OwnerUserID = &row.OwnerUserID.UUID
		}
		result = append(result, page)
	}
	return result, nil
}
