package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// Publication is a separately authorized, immutable public copy of a capture.
// The token is returned only to members with publication authority.
type Publication struct {
	ID          *uuid.UUID `json:"id"`
	PublicToken *uuid.UUID `json:"publicToken"`
	CreatedAt   *time.Time `json:"createdAt"`
	Revoked     bool       `json:"revoked"`
	CanPublish  bool       `json:"canPublish"`
}

type PublicationInput struct {
	RequestKey            uuid.UUID `json:"requestKey"            binding:"required"`
	ConfirmPublicExposure bool      `json:"confirmPublicExposure" binding:"required"`
}

func authorizePublication(ctx context.Context, q *generated.Queries, p auth.UserPrincipal, project uuid.UUID) error {
	if _, err := q.LockCompanyForMembershipRead(ctx, p.CompanyID); err != nil {
		return lookup(err)
	}
	member, err := q.GetCompanyMemberForShare(
		ctx,
		generated.GetCompanyMemberForShareParams{CompanyID: p.CompanyID, UserID: p.UserID},
	)
	if err != nil {
		return lookup(err)
	}
	if member.Status != "active" {
		return ErrNotFound
	}
	if !auth.CompanyRole(member.Role).Can(auth.PermissionResultsPublish) {
		return ErrForbidden
	}
	if _, err := q.LockProjectForAccessRead(
		ctx,
		generated.LockProjectForAccessReadParams{CompanyID: p.CompanyID, ProjectID: project},
	); err != nil {
		return lookup(err)
	}
	_, err = authorize(ctx, q, p, project, false)
	return err
}

func (s *DefaultPresentationService) Publication(
	ctx context.Context,
	p auth.UserPrincipal,
	project, id uuid.UUID,
) (Publication, error) {
	result := Publication{}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := authorizePublication(ctx, q, p, project); err != nil {
			return err
		}
		if _, err := q.GetProjectPresentation(
			ctx,
			generated.GetProjectPresentationParams{CompanyID: p.CompanyID, ProjectID: project, ID: id},
		); err != nil {
			return lookup(err)
		}
		active, err := q.PresentationPublicationProjectActive(
			ctx,
			generated.PresentationPublicationProjectActiveParams{CompanyID: p.CompanyID, ID: project},
		)
		if err != nil {
			return lookup(err)
		}
		result.CanPublish = active
		row, err := q.GetPresentationPublication(
			ctx,
			generated.GetPresentationPublicationParams{
				CompanyID:      p.CompanyID,
				ProjectID:      project,
				PresentationID: id,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		result.ID = &row.ID
		result.PublicToken = &row.PublicToken
		result.CreatedAt = &row.CreatedAt.Time
		return nil
	})
	return result, mapWrite(err)
}

func (s *DefaultPresentationService) PublishPresentation(
	ctx context.Context,
	p auth.UserPrincipal,
	project, id uuid.UUID,
	in PublicationInput,
) (Publication, error) {
	result := Publication{}
	if in.RequestKey == uuid.Nil || !in.ConfirmPublicExposure {
		return result, ErrInvalid
	}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := authorizePublication(ctx, q, p, project); err != nil {
			return err
		}
		row, err := q.LockPresentationForPublication(
			ctx,
			generated.LockPresentationForPublicationParams{
				CompanyID: p.CompanyID,
				ProjectID: project,
				ID:        id,
			},
		)
		if err != nil {
			return lookup(err)
		}
		// Replays retain their original outcome, including revocation. A new URL
		// requires a fresh explicitly confirmed request.
		receipt, err := q.GetPresentationPublicationReceipt(
			ctx,
			generated.GetPresentationPublicationReceiptParams{
				CompanyID:       p.CompanyID,
				ProjectID:       project,
				PresentationID:  id,
				CreatedByUserID: p.UserID,
				RequestKey:      in.RequestKey,
			},
		)
		if err == nil {
			result = Publication{
				ID:         &receipt.ID,
				CreatedAt:  &receipt.CreatedAt.Time,
				Revoked:    receipt.RevokedAt.Valid,
				CanPublish: true,
			}
			if !result.Revoked {
				result.PublicToken = &receipt.PublicToken
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		active, err := q.PresentationPublicationProjectActive(
			ctx,
			generated.PresentationPublicationProjectActiveParams{CompanyID: p.CompanyID, ID: project},
		)
		if err != nil {
			return lookup(err)
		}
		if !active {
			return ErrForbidden
		}
		var evidence presentationUIEvidence
		if err := json.Unmarshal(row.IntelligentEvidence, &evidence); err != nil {
			return err
		}
		for _, dependency := range evidence.Projects {
			if _, err := q.LockProjectForAccessRead(
				ctx,
				generated.LockProjectForAccessReadParams{CompanyID: p.CompanyID, ProjectID: dependency},
			); err != nil {
				return lookup(err)
			}
		}
		if err := authorizePresentationExport(ctx, q, p, row); err != nil {
			return err
		}
		_, err = q.GetPresentationPublication(
			ctx,
			generated.GetPresentationPublicationParams{
				CompanyID:      p.CompanyID,
				ProjectID:      project,
				PresentationID: id,
			},
		)
		if err == nil {
			return ErrConflict
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		html, err := RenderPresentationHTML(row.ExportMarkdown)
		if err != nil {
			return err
		}
		if len(html) > 64<<20 {
			return ErrInvalid
		}
		published, err := q.InsertPresentationPublication(
			ctx,
			generated.InsertPresentationPublicationParams{
				CompanyID:       p.CompanyID,
				ProjectID:       project,
				PresentationID:  id,
				Html:            string(html),
				CreatedByUserID: p.UserID,
				RequestKey:      in.RequestKey,
			},
		)
		if err != nil {
			return err
		}
		result = Publication{
			ID:          &published.ID,
			PublicToken: &published.PublicToken,
			CreatedAt:   &published.CreatedAt.Time,
			CanPublish:  true,
		}
		return nil
	})
	return result, mapWrite(err)
}

func (s *DefaultPresentationService) RevokePublication(
	ctx context.Context,
	p auth.UserPrincipal,
	project, id, publication uuid.UUID,
) error {
	return mapWrite(s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := authorizePublication(ctx, q, p, project); err != nil {
			return err
		}
		if _, err := q.LockPresentationForPublication(
			ctx,
			generated.LockPresentationForPublicationParams{
				CompanyID: p.CompanyID,
				ProjectID: project,
				ID:        id,
			},
		); err != nil {
			return lookup(err)
		}
		// Exact publication identity makes a delayed revoke harmless to a newer URL.
		_, err := q.RevokePresentationPublication(
			ctx,
			generated.RevokePresentationPublicationParams{
				CompanyID:       p.CompanyID,
				ProjectID:       project,
				PresentationID:  id,
				ID:              publication,
				RevokedByUserID: uuid.NullUUID{UUID: p.UserID, Valid: true},
			},
		)
		return err
	}))
}

func (s *DefaultPresentationService) PublicPresentationHTML(ctx context.Context, token uuid.UUID) (string, error) {
	html, err := s.store.Queries().GetPublicPresentationHTML(ctx, token)
	return html, lookup(err)
}
