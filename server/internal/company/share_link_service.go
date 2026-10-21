package company

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

var (
	ErrShareLinkMembershipRemoved    = errors.New("removed membership requires an owner or admin invitation")
	ErrShareLinkVerificationRequired = errors.New("verified email is required to join")
	ErrShareLinkUnavailable          = errors.New("share links are not configured")
)

type ShareLinkResponse struct {
	URL       *string          `json:"url"`
	Role      auth.CompanyRole `json:"role"`
	ExpiresAt *time.Time       `json:"expiresAt"`
	Revision  int64            `json:"revision"`
	Available bool             `json:"available"`
}

func (s *DefaultInvitationService) shareLinkResponse(row generated.CompanyShareLink) (ShareLinkResponse, error) {
	result := ShareLinkResponse{
		Role:      auth.CompanyRole(row.Role),
		ExpiresAt: util.TimestampToTime(row.ExpiresAt),
		Revision:  row.Version,
		Available: s.shareCipher != nil,
	}
	if row.RevokedAt.Valid || !row.ExpiresAt.Time.After(time.Now()) || s.shareCipher == nil {
		return result, nil
	}
	n := s.shareCipher.NonceSize()
	if len(row.EncryptedToken) < n {
		return result, errors.New("share link ciphertext is incomplete")
	}
	token, err := s.shareCipher.Open(
		nil,
		row.EncryptedToken[:n],
		row.EncryptedToken[n:],
		[]byte(row.CompanyID.String()),
	)
	if err != nil {
		return result, fmt.Errorf("decrypt share link: %w", err)
	}
	url := s.origin + "/invite/" + string(token)
	result.URL = &url
	return result, nil
}

// GetShareLink reports Member as the role before any link exists, the role a
// first CreateShareLink without a role would grant.
func (s *DefaultInvitationService) GetShareLink(ctx context.Context, p auth.UserPrincipal) (ShareLinkResponse, error) {
	result := ShareLinkResponse{Role: auth.CompanyRoleMember, Available: s.shareCipher != nil}
	err := s.inviterTx(ctx, p, func(q *generated.Queries, _ generated.Company) error {
		row, err := q.GetCompanyShareLink(ctx, p.CompanyID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		result, err = s.shareLinkResponse(row)
		return err
	})
	return result, err
}

// CreateShareLink is idempotent while a live link exists; opening two owner
// dialogs or retrying a copy operation never invalidates an already shared URL.
// An empty role means Member on create and "keep the current role" on reset.
func (s *DefaultInvitationService) CreateShareLink(
	ctx context.Context,
	p auth.UserPrincipal,
	role auth.CompanyRole,
) (ShareLinkResponse, error) {
	if role == "" {
		role = auth.CompanyRoleMember
	}
	if !invitableRole(role) {
		return ShareLinkResponse{}, ErrInvalidInput
	}
	return s.writeShareLink(ctx, p, shareLinkChange{role: role})
}

func (s *DefaultInvitationService) ResetShareLink(
	ctx context.Context,
	p auth.UserPrincipal,
	revision int64,
	role auth.CompanyRole,
) (ShareLinkResponse, error) {
	if revision < 1 || (role != "" && !invitableRole(role)) {
		return ShareLinkResponse{}, ErrInvalidInput
	}
	return s.writeShareLink(ctx, p, shareLinkChange{revision: revision, role: role})
}

func (s *DefaultInvitationService) DisableShareLink(
	ctx context.Context,
	p auth.UserPrincipal,
	revision int64,
) (ShareLinkResponse, error) {
	if revision < 1 {
		return ShareLinkResponse{}, ErrInvalidInput
	}
	return s.writeShareLink(ctx, p, shareLinkChange{revision: revision, disable: true})
}

type shareLinkChange struct {
	revision int64
	role     auth.CompanyRole
	disable  bool
}

func (s *DefaultInvitationService) writeShareLink(
	ctx context.Context,
	p auth.UserPrincipal,
	change shareLinkChange,
) (ShareLinkResponse, error) {
	if s.shareCipher == nil {
		return ShareLinkResponse{}, ErrShareLinkUnavailable
	}
	var result ShareLinkResponse
	var pending []events.Event
	err := s.inviterTx(ctx, p, func(q *generated.Queries, _ generated.Company) error {
		pending = nil
		existing, err := q.GetCompanyShareLink(ctx, p.CompanyID)
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if change.revision > 0 && (!exists || existing.Version != change.revision) {
			return ErrInvitationConflict
		}
		if change.role == "" {
			change.role = auth.CompanyRoleMember
			if exists {
				change.role = auth.CompanyRole(existing.Role)
			}
		}
		if change.revision == 0 && exists && !existing.RevokedAt.Valid &&
			existing.ExpiresAt.Time.After(time.Now()) {
			result, err = s.shareLinkResponse(existing)
			return err
		}
		var row generated.CompanyShareLink
		action := "created"
		if change.disable {
			row, err = q.RevokeCompanyShareLink(
				ctx,
				generated.RevokeCompanyShareLinkParams{
					CompanyID: p.CompanyID,
					Version:   change.revision,
				},
			)
			action = "revoked"
		} else {
			raw := make([]byte, 32)
			if _, err = rand.Read(raw); err != nil {
				return err
			}
			hash := sha256.Sum256(raw)
			token := base64.RawURLEncoding.EncodeToString(raw)
			nonce := make([]byte, s.shareCipher.NonceSize())
			if _, err = rand.Read(nonce); err != nil {
				return err
			}
			encrypted := s.shareCipher.Seal(nonce, nonce, []byte(token), []byte(p.CompanyID.String()))
			row, err = q.UpsertCompanyShareLink(ctx, generated.UpsertCompanyShareLinkParams{
				CompanyID:       p.CompanyID,
				TokenHash:       hash[:],
				EncryptedToken:  encrypted,
				CreatedByUserID: p.UserID,
				ExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().UTC().Add(7 * 24 * time.Hour),
					Valid: true,
				},
				Role: string(change.role),
			})
			if exists {
				action = "reset"
			}
		}
		if err != nil {
			return err
		}
		result, err = s.shareLinkResponse(row)
		if err != nil {
			return err
		}
		if err = stageShareLinkChanged(
			ctx,
			q,
			&pending,
			events.UserActor(p.UserID.String()),
			result,
			p.CompanyID,
		); err != nil {
			return err
		}
		return recordShareLinkInTx(
			ctx,
			q,
			&pending,
			shareLinkEvidence{row: row, actor: p.UserID, action: action},
		)
	})
	if err != nil {
		return ShareLinkResponse{}, err
	}
	s.publish(ctx, pending)
	return result, nil
}

type shareLinkEvidence struct {
	row    generated.CompanyShareLink
	actor  uuid.UUID
	action string
}

func recordShareLinkInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	input shareLinkEvidence,
) error {
	return activity.RecordEvidenceInTx(ctx, q, pending, activity.EvidenceInput{
		CompanyID: input.row.CompanyID,
		EntityID:  input.row.ID,
		Action:    "invitation.link_" + input.action,
		Actor: events.UserActor(
			input.actor.String(),
		),
		RecipientUserID: input.row.CreatedByUserID,
		Payload:         map[string]any{"shareLinkId": input.row.ID, "revision": input.row.Version},
	})
}

func (s *DefaultInvitationService) findShareLink(
	ctx context.Context,
	token string,
) (generated.FindCompanyShareLinkRow, error) {
	hash, err := invitationHash(token)
	if err != nil {
		return generated.FindCompanyShareLinkRow{}, err
	}
	row, err := s.store.Queries().FindCompanyShareLink(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrInvitationNotFound
	}
	return row, err
}

func (s *DefaultInvitationService) previewShareLink(
	ctx context.Context,
	token string,
	p *auth.UserPrincipal,
) (InvitationPreview, error) {
	found, err := s.findShareLink(ctx, token)
	if err != nil {
		return InvitationPreview{}, err
	}
	row := found.CompanyShareLink
	result := InvitationPreview{
		Type:        "link",
		CompanyID:   row.CompanyID,
		CompanyName: found.CompanyName,
		Role:        auth.CompanyRole(row.Role),
		State:       "pending",
		ExpiresAt:   row.ExpiresAt.Time,
	}
	if row.RevokedAt.Valid {
		result.State = "revoked"
	} else if !row.ExpiresAt.Time.After(time.Now()) {
		result.State = "expired"
	}
	if p == nil || result.State != "pending" {
		return result, nil
	}
	member, err := s.store.Queries().
		GetCompanyMemberForShare(ctx, generated.GetCompanyMemberForShareParams{CompanyID: row.CompanyID, UserID: p.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if member.Status == "active" {
		result.State = "already-member"
	} else if member.RevokedByUserID.UUID != p.UserID {
		result.State = "removed"
	}
	return result, nil
}

func (s *DefaultInvitationService) acceptShareLink(
	ctx context.Context,
	p auth.UserPrincipal,
	token string,
) (InvitationAcceptance, error) {
	found, err := s.findShareLink(ctx, token)
	if err != nil {
		return InvitationAcceptance{}, err
	}
	var result InvitationAcceptance
	var pending []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = nil
		user, err := q.LockEnabledUserForMembershipMutation(ctx, p.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnauthenticated
		}
		if err != nil {
			return err
		}
		if !user.EmailVerifiedAt.Valid {
			return ErrShareLinkVerificationRequired
		}
		if _, err = q.LockCompanyForMembershipMutation(
			ctx,
			found.CompanyShareLink.CompanyID,
		); errors.Is(
			err,
			pgx.ErrNoRows,
		) {
			return ErrInvitationNotFound
		}
		if err != nil {
			return err
		}
		row, err := q.GetCompanyShareLink(ctx, found.CompanyShareLink.CompanyID)
		if err != nil {
			return err
		}
		if !bytes.Equal(row.TokenHash, found.CompanyShareLink.TokenHash) {
			return ErrInvitationInactive
		}
		if row.RevokedAt.Valid || !row.ExpiresAt.Time.After(time.Now()) {
			return ErrInvitationInactive
		}
		member, err := q.GetCompanyMemberForUpdate(
			ctx,
			generated.GetCompanyMemberForUpdateParams{CompanyID: row.CompanyID, UserID: p.UserID},
		)
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		result.Outcome = "alreadyMember"
		if !exists || member.Status != "active" {
			outcome := MembershipGranted
			if exists {
				if member.RevokedByUserID.UUID != p.UserID {
					return ErrShareLinkMembershipRemoved
				}
				member, err = q.ReactivateCompanyMember(
					ctx,
					generated.ReactivateCompanyMemberParams{
						CompanyID:       row.CompanyID,
						UserID:          p.UserID,
						Role:            row.Role,
						ActorUserID:     p.UserID,
						ExpectedVersion: member.Version,
					},
				)
				outcome = MembershipReactivated
			} else {
				member, err = q.InsertCompanyMember(
					ctx,
					generated.InsertCompanyMemberParams{
						CompanyID: row.CompanyID,
						UserID:    p.UserID,
						Role:      row.Role,
					},
				)
			}
			if err != nil {
				return err
			}
			if err = conversation.JoinGeneralChannelInTx(
				ctx,
				q,
				&pending,
				conversation.GeneralChannelMembership{CompanyID: row.CompanyID, UserID: p.UserID},
			); err != nil {
				return err
			}
			active := activeMemberResponse(
				memberResponse(member),
				user.Email,
				user.DisplayName,
				util.TextToPtr(user.AvatarUrl),
				false,
			)
			if err = stageMembershipEvents(
				&pending,
				p.UserID,
				memberResponse(member),
				active,
				outcome,
			); err != nil {
				return err
			}
			pending = append(
				pending,
				events.Event{
					EntityType: Topic,
					Name:       EventCompanyMembershipChanged,
					CompanyID:  row.CompanyID,
					CreatedAt:  time.Now().UTC(),
					Actor:      events.UserActor(p.UserID.String()),
					Payload:    Membership{UserID: p.UserID.String()},
				},
			)
			if err = recordShareLinkInTx(
				ctx,
				q,
				&pending,
				shareLinkEvidence{row: row, actor: p.UserID, action: "joined"},
			); err != nil {
				return err
			}
			result.Outcome = "joined"
		}
		view, err := companyViewRow(ctx, q, row.CompanyID, p.UserID)
		if err != nil {
			return err
		}
		result.Company = companyDTOFromGetRow(view)
		return nil
	})
	if err != nil {
		return InvitationAcceptance{}, err
	}
	s.publish(ctx, pending)
	return result, nil
}
