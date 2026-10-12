package company

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

var (
	ErrInvitationNotFound        = errors.New("invitation not found")
	ErrInvitationAccountMismatch = errors.New("invitation requires the verified invited account")
	ErrInvitationInactive        = errors.New("invitation is no longer active")
	ErrInvitationConflict        = errors.New("invitation revision changed")
	ErrInvitationRateLimited     = errors.New("invitation rate limit exceeded")
	ErrInvitationUnavailable     = errors.New("invitation email is not configured")
)

type InvitationResponse struct {
	ID                 uuid.UUID        `json:"id"`
	Email              string           `json:"email"`
	Role               auth.CompanyRole `json:"role"`
	Status             string           `json:"status"`
	DeliveryStatus     string           `json:"deliveryStatus"`
	ExpiresAt          time.Time        `json:"expiresAt"`
	CreatedAt          time.Time        `json:"createdAt"`
	RenewalRequestedAt *time.Time       `json:"renewalRequestedAt"`
	Revision           int64            `json:"revision"`
}
type InvitationResult struct {
	Email      string              `json:"email"`
	Outcome    string              `json:"outcome"`
	Invitation *InvitationResponse `json:"invitation"`
}
type InvitationPreview struct {
	Type        string           `json:"type"`
	CompanyID   uuid.UUID        `json:"companyId"`
	CompanyName string           `json:"companyName"`
	InviterName string           `json:"inviterName"`
	MaskedEmail string           `json:"maskedEmail"`
	Role        auth.CompanyRole `json:"role"`
	State       string           `json:"state"`
	ExpiresAt   time.Time        `json:"expiresAt"`
}

// InvitationCreateInput names the addresses to invite and the role they
// join with. Nobody is invited as owner: ownership only moves through the
// role-change and transfer routes.
type InvitationCreateInput struct {
	Emails []string
	Role   auth.CompanyRole
}
type InvitationAcceptance struct {
	Company Response `json:"company"`
	Outcome string   `json:"outcome"`
}
type InvitationMutation struct {
	ID               uuid.UUID
	ExpectedRevision int64
}
type InvitationEmail struct {
	Email       string
	CompanyName string
	InviterName string
	URL         string
	ExpiresAt   time.Time
}
type InvitationEmailSender interface {
	SendInvitation(context.Context, InvitationEmail, string) error
}
type InvitationOptions struct {
	Store                  *db.Store
	Bus                    *events.Bus
	Sender                 InvitationEmailSender
	WebAppOrigin           string
	EncryptionKey          []byte
	ShareLinkEncryptionKey []byte
}
type InvitationService interface {
	GetShareLink(context.Context, auth.UserPrincipal) (ShareLinkResponse, error)
	CreateShareLink(context.Context, auth.UserPrincipal, auth.CompanyRole) (ShareLinkResponse, error)
	ResetShareLink(context.Context, auth.UserPrincipal, int64, auth.CompanyRole) (ShareLinkResponse, error)
	DisableShareLink(context.Context, auth.UserPrincipal, int64) (ShareLinkResponse, error)
	Create(context.Context, auth.UserPrincipal, InvitationCreateInput) ([]InvitationResult, error)
	List(context.Context, auth.UserPrincipal) ([]InvitationResponse, error)
	Preview(context.Context, string) (InvitationPreview, error)
	Inspect(context.Context, auth.UserPrincipal, string) (InvitationPreview, error)
	Accept(context.Context, auth.UserPrincipal, string) (InvitationAcceptance, error)
	Resend(context.Context, auth.UserPrincipal, InvitationMutation) (InvitationResponse, error)
	Cancel(context.Context, auth.UserPrincipal, InvitationMutation) (InvitationResponse, error)
	RequestRenewal(context.Context, string) error
	Deliver(context.Context) error
}

var _ InvitationService = (*DefaultInvitationService)(nil)

type DefaultInvitationService struct {
	store       *db.Store
	bus         *events.Bus
	sender      InvitationEmailSender
	origin      string
	cipher      cipher.AEAD
	shareCipher cipher.AEAD
}

func NewInvitationService(options InvitationOptions) (*DefaultInvitationService, error) {
	if options.Store == nil || options.Bus == nil {
		return nil, errors.New("invitation store and bus are required")
	}
	s := &DefaultInvitationService{
		store:  options.Store,
		bus:    options.Bus,
		sender: options.Sender,
		origin: strings.TrimRight(options.WebAppOrigin, "/"),
	}
	if options.Sender != nil || len(options.EncryptionKey) > 0 || len(options.ShareLinkEncryptionKey) > 0 {
		origin, err := url.Parse(s.origin)
		if err != nil || origin.Host == "" || origin.User != nil || origin.Path != "" ||
			origin.RawQuery != "" ||
			origin.Fragment != "" ||
			(origin.Scheme != "https" && origin.Scheme != "http") {
			return nil, errors.New("invitation web origin is invalid")
		}

	}
	var err error
	if options.Sender != nil || len(options.EncryptionKey) > 0 {
		s.cipher, err = invitationCipher(options.EncryptionKey)
		if err != nil {
			return nil, err
		}
	}
	if len(options.ShareLinkEncryptionKey) > 0 {
		s.shareCipher, err = invitationCipher(options.ShareLinkEncryptionKey)
		if err != nil {
			return nil, err
		}
	}

	return s, nil
}

func invitationCipher(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("invitation encryption key: %w", err)
	}
	return cipher.NewGCM(block)
}

// inviterTx locks the Company and admits the caller when their role holds
// members.invite — owners and admins alike.
func (s *DefaultInvitationService) inviterTx(
	ctx context.Context,
	p auth.UserPrincipal,
	fn func(*generated.Queries, generated.Company) error,
) error {
	return s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		c, err := q.LockCompanyForMembershipMutation(ctx, p.CompanyID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err = authorizeMembershipActor(
			ctx,
			q,
			p.CompanyID,
			p.UserID,
			auth.PermissionMembersInvite,
		); err != nil {
			return err
		}
		return fn(q, c)
	})
}

func (s *DefaultInvitationService) publish(ctx context.Context, pending []events.Event) {
	for _, batch := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, batch.Topic, batch.Events...)
	}
}

func invitationResponse(row generated.CompanyInvitation, delivery string) InvitationResponse {
	status := row.Status
	if status == "pending" && !row.ExpiresAt.Time.After(time.Now()) {
		status = "expired"
	}
	return InvitationResponse{
		ID:                 row.ID,
		Email:              row.Email,
		Role:               auth.CompanyRole(row.Role),
		Status:             status,
		DeliveryStatus:     delivery,
		ExpiresAt:          row.ExpiresAt.Time,
		CreatedAt:          row.CreatedAt.Time,
		RenewalRequestedAt: util.TimestampToTime(row.RenewalRequestedAt),
		Revision:           row.Version,
	}
}

// record captures one invitation change: the Activity row and the
// invitation.changed frame for everyone who may see the invitation surface.
// delivery is the token delivery status the Team page shows after the change.
func (s *DefaultInvitationService) record(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	row generated.CompanyInvitation,
	actor events.Actor,
	action string,
	delivery string,
) error {
	if err := stageInvitationChanged(
		ctx,
		q,
		pending,
		actor,
		invitationResponse(row, delivery),
		row.CompanyID,
	); err != nil {
		return err
	}
	return activity.RecordEvidenceInTx(
		ctx,
		q,
		pending,
		activity.EvidenceInput{
			CompanyID:       row.CompanyID,
			EntityID:        row.ID,
			Action:          "invitation." + action,
			Actor:           actor,
			RecipientUserID: row.InvitedByUserID,
			Payload:         map[string]any{"invitationId": row.ID, "status": row.Status},
		},
	)
}

func (s *DefaultInvitationService) enqueue(
	ctx context.Context,
	q *generated.Queries,
	row generated.CompanyInvitation,
	c generated.Company,
) error {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return err
	}
	hash := sha256.Sum256(tokenBytes)
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	nonce := make([]byte, s.cipher.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	encrypted := s.cipher.Seal(nonce, nonce, []byte(token), []byte(row.ID.String()))
	user, err := q.GetUserByID(ctx, row.InvitedByUserID)
	if err != nil {
		return err
	}
	return q.InsertCompanyInvitationToken(
		ctx,
		generated.InsertCompanyInvitationTokenParams{
			InvitationID:   row.ID,
			Generation:     row.Generation,
			TokenHash:      hash[:],
			EncryptedToken: encrypted,
			RecipientEmail: row.Email,
			CompanyName:    c.Name,
			InviterName:    user.DisplayName,
			ExpiresAt:      row.ExpiresAt,
		},
	)
}

func (s *DefaultInvitationService) Create(
	ctx context.Context,
	p auth.UserPrincipal,
	input InvitationCreateInput,
) ([]InvitationResult, error) {
	if s.sender == nil {
		return nil, ErrInvitationUnavailable
	}
	emails := input.Emails
	if len(emails) == 0 || len(emails) > 20 || !invitableRole(input.Role) {
		return nil, ErrInvalidInput
	}
	normalized := make([]string, 0, len(emails))
	seen := map[string]bool{}
	for _, raw := range emails {
		value := strings.ToLower(strings.TrimSpace(raw))
		address, err := mail.ParseAddress(value)
		if err != nil || address.Address != value || len(value) > 254 {
			return nil, ErrInvalidInput
		}
		if !seen[value] {
			normalized = append(normalized, value)
			seen[value] = true
		}
	}
	var results []InvitationResult
	var pending []events.Event
	err := s.inviterTx(ctx, p, func(q *generated.Queries, c generated.Company) error {
		results = nil
		pending = nil
		count, err := q.CountRecentCompanyInvitations(ctx, p.CompanyID)
		if err != nil {
			return err
		}
		if count+int64(len(normalized)) > 100 {
			return ErrInvitationRateLimited
		}
		for _, email := range normalized {
			member, err := q.InvitationEmailIsMember(
				ctx,
				generated.InvitationEmailIsMemberParams{CompanyID: p.CompanyID, Email: email},
			)
			if err != nil {
				return err
			}
			if member {
				results = append(results, InvitationResult{Email: email, Outcome: "alreadyMember"})
				continue
			}
			expired, err := q.ExpireCompanyInvitations(
				ctx,
				generated.ExpireCompanyInvitationsParams{CompanyID: p.CompanyID, Email: email},
			)
			if err != nil {
				return err
			}
			for _, stale := range expired {
				delivery, err := q.GetCompanyInvitationDeliveryStatus(
					ctx,
					generated.GetCompanyInvitationDeliveryStatusParams{
						InvitationID: stale.ID,
						Generation:   stale.Generation,
					},
				)
				if err != nil {
					return fmt.Errorf("read expired invitation delivery: %w", err)
				}
				if err = stageInvitationChanged(
					ctx,
					q,
					&pending,
					events.UserActor(p.UserID.String()),
					invitationResponse(stale, delivery),
					p.CompanyID,
				); err != nil {
					return err
				}
			}
			row, err := q.FindPendingCompanyInvitation(
				ctx,
				generated.FindPendingCompanyInvitationParams{CompanyID: p.CompanyID, Email: email},
			)
			if err == nil {
				response := invitationResponse(row, "queued")
				results = append(
					results,
					InvitationResult{
						Email:      email,
						Outcome:    "alreadyInvited",
						Invitation: &response,
					},
				)
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			row, err = q.CreateCompanyInvitation(
				ctx,
				generated.CreateCompanyInvitationParams{
					CompanyID:       p.CompanyID,
					Email:           email,
					InvitedByUserID: p.UserID,
					Role:            string(input.Role),
				},
			)
			if err != nil {
				return err
			}
			if err = s.enqueue(ctx, q, row, c); err != nil {
				return err
			}
			if err = s.record(
				ctx,
				q,
				&pending,
				row,
				events.UserActor(p.UserID.String()),
				"created",
				"queued",
			); err != nil {
				return err
			}
			response := invitationResponse(row, "queued")
			results = append(
				results,
				InvitationResult{Email: email, Outcome: "queued", Invitation: &response},
			)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.publish(ctx, pending)
	return results, nil
}

func (s *DefaultInvitationService) List(ctx context.Context, p auth.UserPrincipal) ([]InvitationResponse, error) {
	items := []InvitationResponse{}
	err := s.inviterTx(ctx, p, func(q *generated.Queries, _ generated.Company) error {
		items = []InvitationResponse{}
		rows, err := q.ListCompanyInvitations(ctx, p.CompanyID)
		if err != nil {
			return err
		}
		for _, r := range rows {
			items = append(
				items,
				invitationResponse(
					generated.CompanyInvitation{
						ID:                 r.ID,
						Email:              r.Email,
						Status:             r.Status,
						ExpiresAt:          r.ExpiresAt,
						CreatedAt:          r.CreatedAt,
						RenewalRequestedAt: r.RenewalRequestedAt,
						Version:            r.Version,
						Role:               r.Role,
					},
					r.DeliveryStatus,
				),
			)
		}
		return nil
	})
	return items, err
}

func invitationHash(token string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != 32 {
		return nil, ErrInvitationNotFound
	}
	hash := sha256.Sum256(raw)
	return hash[:], nil
}

func (s *DefaultInvitationService) find(
	ctx context.Context,
	token string,
) (generated.FindCompanyInvitationTokenRow, error) {
	hash, err := invitationHash(token)
	if err != nil {
		return generated.FindCompanyInvitationTokenRow{}, err
	}
	row, err := s.store.Queries().FindCompanyInvitationToken(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrInvitationNotFound
	}
	return row, err
}

// invitableRole is the role an invitation or share link may grant: admin or
// member, never owner.
func invitableRole(role auth.CompanyRole) bool {
	return role == auth.CompanyRoleAdmin || role == auth.CompanyRoleMember
}

func previewInvitation(row generated.FindCompanyInvitationTokenRow) InvitationPreview {
	i := row.CompanyInvitation
	state := i.Status
	if state == "pending" && !i.ExpiresAt.Time.After(time.Now()) {
		state = "expired"
	}
	if state == "pending" && row.TokenGeneration != i.Generation {
		state = "replaced"
	}
	local, domain, _ := strings.Cut(i.Email, "@")
	masked := "***@" + domain
	if len(local) > 0 {
		masked = string([]rune(local)[0]) + masked
	}
	return InvitationPreview{
		Type:        "email",
		CompanyID:   i.CompanyID,
		CompanyName: row.CompanyName,
		InviterName: row.InviterName,
		MaskedEmail: masked,
		Role:        auth.CompanyRole(i.Role),
		State:       state,
		ExpiresAt:   i.ExpiresAt.Time,
	}
}

func (s *DefaultInvitationService) Preview(ctx context.Context, token string) (InvitationPreview, error) {
	row, err := s.find(ctx, token)
	if errors.Is(err, ErrInvitationNotFound) {
		return s.previewShareLink(ctx, token, nil)
	}
	if err != nil {
		return InvitationPreview{}, err
	}
	return previewInvitation(row), nil
}

func (s *DefaultInvitationService) Inspect(
	ctx context.Context,
	p auth.UserPrincipal,
	token string,
) (InvitationPreview, error) {
	row, err := s.find(ctx, token)
	if errors.Is(err, ErrInvitationNotFound) {
		return s.previewShareLink(ctx, token, &p)
	}
	if err != nil {
		return InvitationPreview{}, err
	}
	result := previewInvitation(row)
	user, err := s.store.Queries().GetUserByID(ctx, p.UserID)
	if err != nil {
		return result, err
	}
	if !user.EmailVerifiedAt.Valid || !strings.EqualFold(user.Email, row.CompanyInvitation.Email) {
		result.State = "wrong-account"
		return result, nil
	}
	_, err = s.store.Queries().
		GetActiveCompanyMembership(ctx, generated.GetActiveCompanyMembershipParams{CompanyID: result.CompanyID, UserID: p.UserID})
	if err == nil {
		result.State = "already-member"
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	return result, nil
}

func (s *DefaultInvitationService) Accept(
	ctx context.Context,
	p auth.UserPrincipal,
	token string,
) (InvitationAcceptance, error) {
	found, err := s.find(ctx, token)
	if errors.Is(err, ErrInvitationNotFound) {
		return s.acceptShareLink(ctx, p, token)
	}
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
		if !user.EmailVerifiedAt.Valid || !strings.EqualFold(user.Email, found.CompanyInvitation.Email) {
			return ErrInvitationAccountMismatch
		}
		if _, err = q.LockCompanyForMembershipMutation(
			ctx,
			found.CompanyInvitation.CompanyID,
		); errors.Is(
			err,
			pgx.ErrNoRows,
		) {
			return ErrInvitationNotFound
		} else if err != nil {
			return err
		}
		row, err := q.GetCompanyInvitationForUpdate(
			ctx,
			generated.GetCompanyInvitationForUpdateParams{
				ID:        found.CompanyInvitation.ID,
				CompanyID: found.CompanyInvitation.CompanyID,
			},
		)
		if err != nil {
			return err
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
			if row.Status != "pending" || !row.ExpiresAt.Time.After(time.Now()) ||
				row.Generation != found.TokenGeneration {
				return ErrInvitationInactive
			}
			outcome := MembershipGranted
			if exists {
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
			result.Outcome = "joined"
		}
		if row.Status == "pending" {
			row, err = q.ResolveCompanyInvitation(
				ctx,
				generated.ResolveCompanyInvitationParams{
					ID:             row.ID,
					CompanyID:      row.CompanyID,
					Status:         "accepted",
					AcceptedUserID: uuid.NullUUID{UUID: p.UserID, Valid: true},
				},
			)
			if err != nil {
				return err
			}
			if err = q.CancelCompanyInvitationDelivery(ctx, row.ID); err != nil {
				return err
			}
			if err = s.record(
				ctx,
				q,
				&pending,
				row,
				events.UserActor(p.UserID.String()),
				"accepted",
				"cancelled",
			); err != nil {
				return err
			}
		}
		company, err := companyViewRow(ctx, q, row.CompanyID, p.UserID)
		if err != nil {
			return err
		}
		result.Company = companyDTOFromGetRow(company)
		return nil
	})
	if err != nil {
		return InvitationAcceptance{}, err
	}
	s.publish(ctx, pending)
	return result, nil
}

func (s *DefaultInvitationService) Resend(
	ctx context.Context,
	p auth.UserPrincipal,
	input InvitationMutation,
) (InvitationResponse, error) {
	return s.mutate(ctx, p, input, true)
}

func (s *DefaultInvitationService) Cancel(
	ctx context.Context,
	p auth.UserPrincipal,
	input InvitationMutation,
) (InvitationResponse, error) {
	return s.mutate(ctx, p, input, false)
}

func (s *DefaultInvitationService) mutate(
	ctx context.Context,
	p auth.UserPrincipal,
	input InvitationMutation,
	resend bool,
) (InvitationResponse, error) {
	if resend && s.sender == nil {
		return InvitationResponse{}, ErrInvitationUnavailable
	}
	var result InvitationResponse
	var pending []events.Event
	err := s.inviterTx(ctx, p, func(q *generated.Queries, c generated.Company) error {
		pending = nil
		row, err := q.GetCompanyInvitationForUpdate(
			ctx,
			generated.GetCompanyInvitationForUpdateParams{ID: input.ID, CompanyID: p.CompanyID},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvitationNotFound
		}
		if err != nil {
			return err
		}
		if row.Version != input.ExpectedRevision {
			return ErrInvitationConflict
		}
		if row.Status != "pending" && row.Status != "expired" {
			return ErrInvitationInactive
		}
		if resend && time.Since(row.UpdatedAt.Time) < time.Minute {
			return ErrInvitationRateLimited
		}
		if err = q.CancelCompanyInvitationDelivery(ctx, row.ID); err != nil {
			return err
		}
		action, delivery := "revoked", "cancelled"
		if resend {
			other, lookupErr := q.FindPendingCompanyInvitation(
				ctx,
				generated.FindPendingCompanyInvitationParams{CompanyID: p.CompanyID, Email: row.Email},
			)
			if lookupErr == nil && other.ID != row.ID {
				return ErrInvitationConflict
			}
			if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
				return lookupErr
			}
			row, err = q.ResendCompanyInvitation(
				ctx,
				generated.ResendCompanyInvitationParams{
					ID:              row.ID,
					CompanyID:       p.CompanyID,
					InvitedByUserID: p.UserID,
				},
			)
			if err != nil {
				return err
			}
			if err = s.enqueue(ctx, q, row, c); err != nil {
				return err
			}
			action, delivery = "resent", "queued"
		} else {
			row, err = q.ResolveCompanyInvitation(
				ctx,
				generated.ResolveCompanyInvitationParams{
					ID:        row.ID,
					CompanyID: p.CompanyID,
					Status:    "revoked",
				},
			)
			if err != nil {
				return err
			}
		}
		if err = s.record(
			ctx,
			q,
			&pending,
			row,
			events.UserActor(p.UserID.String()),
			action,
			delivery,
		); err != nil {
			return err
		}
		result = invitationResponse(row, delivery)
		return nil
	})
	if err != nil {
		return result, err
	}
	s.publish(ctx, pending)
	return result, nil
}

func (s *DefaultInvitationService) RequestRenewal(ctx context.Context, token string) error {
	found, err := s.find(ctx, token)
	if err != nil {
		return err
	}
	var pending []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = nil
		if _, err := q.LockCompanyForMembershipMutation(
			ctx,
			found.CompanyInvitation.CompanyID,
		); errors.Is(
			err,
			pgx.ErrNoRows,
		) {
			return ErrInvitationNotFound
		} else if err != nil {
			return err
		}
		row, err := q.GetCompanyInvitationForUpdate(
			ctx,
			generated.GetCompanyInvitationForUpdateParams{
				ID:        found.CompanyInvitation.ID,
				CompanyID: found.CompanyInvitation.CompanyID,
			},
		)
		if err != nil {
			return err
		}
		if row.Generation != found.TokenGeneration ||
			(row.Status != "expired" && (row.Status != "pending" || row.ExpiresAt.Time.After(time.Now()))) {
			return ErrInvitationInactive
		}
		row, err = q.RequestCompanyInvitationRenewal(ctx, row.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		owners, err := q.ListActiveCompanyMemberIDsByRoles(
			ctx,
			generated.ListActiveCompanyMemberIDsByRolesParams{
				CompanyID: row.CompanyID,
				Roles:     auth.RolesWith(auth.PermissionMembersInvite),
			},
		)
		if err != nil {
			return err
		}
		link := "/team"
		for _, owner := range owners {
			_, err = inbox.CreateInTx(
				ctx,
				q,
				&pending,
				inbox.CreateInput{
					CompanyID:       row.CompanyID,
					RecipientUserID: owner,
					Type:            inbox.TypeInvitationRenewal,
					Entity:          inbox.Entity{Type: "company", ID: row.CompanyID.String()},
					Title:           "Invitation renewal requested",
					Body:            row.Email + " requested a new invitation. Review it on the Team page.",
					DeepLink:        &link,
					DedupeKey: fmt.Sprintf(
						"invitation-renewal/%s/%s",
						row.ID,
						time.Now().UTC().Format("2006-01-02"),
					),
					Actor: inbox.Actor{Kind: "system"},
				},
			)
			if err != nil {
				return err
			}
		}
		return s.record(ctx, q, &pending, row, events.SystemActor(), "renewal_requested", found.DeliveryStatus)
	})
	if err != nil {
		return err
	}
	s.publish(ctx, pending)
	return nil
}

// Deliver performs a bounded pass. Claims survive process loss and retries
// keep the same frozen template values and provider idempotency key.
func (s *DefaultInvitationService) Deliver(ctx context.Context) error {
	if s.sender == nil {
		return nil
	}
	for range 20 {
		var job generated.CompanyInvitationToken
		err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			if err := q.ExpireCompanyInvitationDelivery(ctx); err != nil {
				return err
			}
			var err error
			job, err = q.ClaimCompanyInvitationDelivery(ctx)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		})
		if err != nil {
			return fmt.Errorf("claim invitation delivery: %w", err)
		}
		if job.InvitationID == uuid.Nil {
			return nil
		}
		var token []byte
		if len(job.EncryptedToken) < s.cipher.NonceSize() {
			err = errors.New("invitation delivery secret is missing")
		} else {
			token, err = s.cipher.Open(
				nil,
				job.EncryptedToken[:s.cipher.NonceSize()],
				job.EncryptedToken[s.cipher.NonceSize():],
				[]byte(job.InvitationID.String()),
			)
		}
		status := "failed"
		if err == nil {
			sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err = s.sender.SendInvitation(
				sendCtx,
				InvitationEmail{
					Email:       job.RecipientEmail,
					CompanyName: job.CompanyName,
					InviterName: job.InviterName,
					URL:         s.origin + "/invite/" + string(token),
					ExpiresAt:   job.ExpiresAt.Time,
				},
				fmt.Sprintf("invitation/%s/%d", job.InvitationID, job.Generation),
			)
			cancel()
			status = "sent"
			if err != nil {
				status = "queued"
				if job.Attempts >= 5 {
					status = "failed"
				}
			}
		}
		if err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			return q.FinishCompanyInvitationDelivery(
				ctx,
				generated.FinishCompanyInvitationDeliveryParams{
					InvitationID:   job.InvitationID,
					Generation:     job.Generation,
					Attempts:       job.Attempts,
					DeliveryStatus: status,
				},
			)
		}); err != nil {
			return fmt.Errorf("finish invitation delivery: %w", err)
		}
	}
	return nil
}
