package execution

import (
	"context"
	"errors"
	"time"

	artifactdomain "github.com/Intelligent-Internet/zenith-team/server/internal/artifact"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	dbgen "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type ArtifactVisibilityInput struct {
	Visibility       string `json:"visibility"       binding:"required,oneof=shared company public"`
	ExpectedRevision int64  `json:"expectedRevision" binding:"revision"`
}

type ArtifactConversationGrant struct {
	ConversationID uuid.UUID `json:"conversationId"`
	Title          string    `json:"title"`
	GrantedAt      time.Time `json:"grantedAt"`
}

type ArtifactConversationInput struct {
	ConversationID   uuid.UUID
	ExpectedRevision int64
}

func lockArtifactManager(
	ctx context.Context,
	q *dbgen.Queries,
	p auth.UserPrincipal,
	id uuid.UUID,
) (dbgen.LockOutputForAccessChangeRow, error) {
	row, err := q.LockOutputForAccessChange(
		ctx,
		dbgen.LockOutputForAccessChangeParams{CompanyID: p.CompanyID, OutputID: id},
	)
	if err != nil {
		return row, artifactReadError(err)
	}
	level, err := q.GetOutputAccessForManage(
		ctx,
		dbgen.GetOutputAccessForManageParams{CompanyID: p.CompanyID, OutputID: id, ActorUserID: p.UserID},
	)
	if err != nil {
		return row, err
	}
	if level == 0 {
		return row, ErrDeliverableNotFound
	}
	if level < 3 {
		return row, ErrDeliverableForbidden
	}
	return row, nil
}

func (s *DefaultDeliverableService) SetArtifactVisibility(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	input ArtifactVisibilityInput,
) (ArtifactAccess, error) {
	if id == uuid.Nil || input.ExpectedRevision < 1 {
		return ArtifactAccess{}, ErrDeliverableInvalidInput
	}
	switch input.Visibility {
	case "shared", "company", "public":
	default:
		return ArtifactAccess{}, ErrDeliverableInvalidInput
	}
	var pending events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *dbgen.Queries) error {
		_, active, err := lockActiveMember(ctx, q, p.CompanyID, p.UserID)
		if err != nil {
			return err
		}
		if !active {
			return ErrDeliverableNotFound
		}
		row, err := lockArtifactManager(ctx, q, p, id)
		if err != nil {
			return err
		}
		if row.AccessVersion != input.ExpectedRevision {
			return ErrDeliverableConflict
		}
		if _, err := q.SetOutputVisibility(
			ctx,
			dbgen.SetOutputVisibilityParams{
				CompanyID:  p.CompanyID,
				OutputID:   id,
				Visibility: input.Visibility,
			},
		); err != nil {
			return err
		}
		pending, err = artifactAccessEvent(ctx, q, p)
		return err
	})
	if err != nil {
		return ArtifactAccess{}, err
	}
	s.bus.Publish(ctx, artifactdomain.Topic, pending)
	return s.getArtifactAccess(ctx, p, id)
}

// ShareArtifactInTx participates in the caller's send transaction. The caller
// holds the Company/membership and target Conversation locks before this call.
func ShareArtifactInTx(
	ctx context.Context,
	q *dbgen.Queries,
	p auth.UserPrincipal,
	id, conversationID uuid.UUID,
) error {
	_, err := lockArtifactManager(ctx, q, p, id)
	if err != nil {
		return err
	}
	allowed, err := q.CanShareOutputToConversation(
		ctx,
		dbgen.CanShareOutputToConversationParams{
			CompanyID:      p.CompanyID,
			ConversationID: conversationID,
			ActorUserID:    p.UserID,
		},
	)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrDeliverableNotFound
	}
	if err := q.ShareOutputWithConversation(
		ctx,
		dbgen.ShareOutputWithConversationParams{
			CompanyID:       p.CompanyID,
			OutputID:        id,
			ConversationID:  conversationID,
			GrantedByUserID: p.UserID,
		},
	); err != nil {
		return err
	}
	if _, err := q.BumpOutputAccessVersion(
		ctx,
		dbgen.BumpOutputAccessVersionParams{CompanyID: p.CompanyID, OutputID: id},
	); err != nil {
		return err
	}
	return nil
}

func (s *DefaultDeliverableService) RevokeArtifactConversation(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	input ArtifactConversationInput,
) (ArtifactAccess, error) {
	if id == uuid.Nil || input.ConversationID == uuid.Nil || input.ExpectedRevision < 1 {
		return ArtifactAccess{}, ErrDeliverableInvalidInput
	}
	var pending events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *dbgen.Queries) error {
		_, active, err := lockActiveMember(ctx, q, p.CompanyID, p.UserID)
		if err != nil {
			return err
		}
		if !active {
			return ErrDeliverableNotFound
		}
		row, err := lockArtifactManager(ctx, q, p, id)
		if err != nil {
			return err
		}
		if row.AccessVersion != input.ExpectedRevision {
			return ErrDeliverableConflict
		}
		removed, err := q.DeleteOutputConversationGrant(
			ctx,
			dbgen.DeleteOutputConversationGrantParams{
				CompanyID:      p.CompanyID,
				OutputID:       id,
				ConversationID: input.ConversationID,
			},
		)
		if err != nil {
			return err
		}
		if removed == 0 {
			return nil
		}

		if _, err := q.BumpOutputAccessVersion(
			ctx,
			dbgen.BumpOutputAccessVersionParams{CompanyID: p.CompanyID, OutputID: id},
		); err != nil {
			return err
		}
		pending, err = artifactAccessEvent(ctx, q, p)
		return err
	})
	if err != nil {
		return ArtifactAccess{}, err
	}
	if pending.Name != "" {
		s.bus.Publish(ctx, artifactdomain.Topic, pending)
	}
	return s.getArtifactAccess(ctx, p, id)
}

func artifactAccessEvent(ctx context.Context, q *dbgen.Queries, p auth.UserPrincipal) (events.Event, error) {
	recipients, err := q.ListArtifactCompanyRecipients(ctx, p.CompanyID)
	if err != nil {
		return events.Event{}, err
	}
	return events.Event{
		EntityType: artifactdomain.Topic,
		Name:       artifactdomain.EventAccessChanged,
		CompanyID:  p.CompanyID,
		CreatedAt:  time.Now().UTC(),
		Actor:      events.UserActor(p.UserID.String()),
		Payload:    artifactdomain.AccessChanged{RecipientUserIDs: recipients},
	}, nil
}

type PublicArtifact struct {
	Title   string               `json:"title"`
	Summary string               `json:"summary"`
	Type    ArtifactType         `json:"type"`
	Items   []PublicArtifactItem `json:"items"`
}

type PublicArtifactItem struct {
	Role        string `json:"role"`
	Ordinal     int    `json:"ordinal"`
	Label       string `json:"label"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
}

func (s *DefaultDeliverableService) GetPublicArtifact(ctx context.Context, token uuid.UUID) (PublicArtifact, error) {
	var result PublicArtifact
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *dbgen.Queries) error {
			output, err := q.GetPublicOutput(ctx, uuid.NullUUID{UUID: token, Valid: true})
			if err != nil {
				return artifactReadError(err)
			}
			rows, err := q.ListOutputItems(
				ctx,
				dbgen.ListOutputItemsParams{
					CompanyID: output.CompanyID,
					OutputIds: []uuid.UUID{output.ID},
				},
			)
			if err != nil {
				return err
			}
			result = PublicArtifact{
				Title:   output.Title,
				Summary: output.Summary,
				Type:    ArtifactType(output.Type),
				Items:   []PublicArtifactItem{},
			}
			for _, row := range rows {
				if (row.Role == "final" || (row.Role == "primary" && output.Type != "app")) &&
					row.ContentType != "application/vnd.zenith-studio.canvas+json" {
					result.Items = append(
						result.Items,
						PublicArtifactItem{
							Role:        row.Role,
							Ordinal:     int(row.Ordinal),
							Label:       row.Label,
							Filename:    row.Filename,
							ContentType: row.ContentType,
							SizeBytes:   row.SizeBytes,
						},
					)
				}
			}
			return nil
		},
	)
	return result, err
}

func (s *DefaultDeliverableService) ResolvePublicArtifactFile(
	ctx context.Context,
	token uuid.UUID,
	ordinal int,
) (uuid.UUID, uuid.UUID, error) {
	if token == uuid.Nil || ordinal < 0 || ordinal >= 32 {
		return uuid.Nil, uuid.Nil, ErrDeliverableNotFound
	}
	row, err := s.store.Queries().
		ResolvePublicOutputFile(ctx, dbgen.ResolvePublicOutputFileParams{PublicToken: uuid.NullUUID{UUID: token, Valid: true}, Ordinal: int32(ordinal)})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, uuid.Nil, ErrDeliverableNotFound
	}
	return row.CompanyID, row.AttachmentID, err
}
