package company

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// ProactiveMode is the persisted Company automation preference returned to
// active members.
type ProactiveMode struct {
	CompanyID       uuid.UUID  `json:"companyId"`
	Enabled         bool       `json:"enabled"`
	EnabledByUserID *uuid.UUID `json:"enabledByUserId"`
	ConversationID  *uuid.UUID `json:"conversationId"`
	Revision        int64      `json:"revision"`
	CanManage       bool       `json:"canManage"`
}

// UpdateProactiveModeInput changes the preference with optimistic concurrency.
type UpdateProactiveModeInput struct {
	Enabled          bool
	ExpectedRevision int64
}

// ProactiveModeService owns member reads and owner-authorized preference
// changes.
type ProactiveModeService interface {
	Get(context.Context, auth.UserPrincipal) (ProactiveMode, error)
	Update(context.Context, auth.UserPrincipal, UpdateProactiveModeInput) (ProactiveMode, error)
}

var _ ProactiveModeService = (*DefaultProactiveModeService)(nil)

type DefaultProactiveModeService struct {
	store *db.Store
}

func NewProactiveModeService(store *db.Store) (*DefaultProactiveModeService, error) {
	if store == nil {
		return nil, errors.New("proactive mode database store is required")
	}
	return &DefaultProactiveModeService{store: store}, nil
}

func (s *DefaultProactiveModeService) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
) (ProactiveMode, error) {
	if !util.ValidUUID(principal.UserID) {
		return ProactiveMode{}, ErrUnauthenticated
	}
	if !util.ValidUUID(principal.CompanyID) {
		return ProactiveMode{}, ErrNotFound
	}
	row, err := s.store.Queries().GetProactiveModeForMember(
		ctx,
		generated.GetProactiveModeForMemberParams{
			UserID:    principal.UserID,
			CompanyID: principal.CompanyID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProactiveMode{}, ErrNotFound
	}
	if err != nil {
		return ProactiveMode{}, fmt.Errorf("get proactive mode: %w", err)
	}
	return proactiveModeFromGetRow(row), nil
}

func (s *DefaultProactiveModeService) Update(
	ctx context.Context,
	principal auth.UserPrincipal,
	input UpdateProactiveModeInput,
) (ProactiveMode, error) {
	if !util.ValidUUID(principal.UserID) {
		return ProactiveMode{}, ErrUnauthenticated
	}
	if !util.ValidUUID(principal.CompanyID) {
		return ProactiveMode{}, ErrNotFound
	}
	if input.ExpectedRevision < 1 || input.ExpectedRevision > protocol.MaxJSONSafeInteger {
		return ProactiveMode{}, ErrProactiveModeInvalidInput
	}

	var result ProactiveMode
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		if _, err := tx.LockCompanyForMembershipMutation(
			ctx,
			principal.CompanyID,
		); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock Company for proactive mode mutation: %w", err)
		}
		if _, err := authorizeMembershipActor(
			ctx,
			tx,
			principal.CompanyID,
			principal.UserID,
			auth.PermissionCompanyProactiveModeManage,
		); err != nil {
			return err
		}

		var enabledByUserID uuid.NullUUID
		var conversationID uuid.NullUUID
		if input.Enabled {
			resolved, err := tx.ResolveProactiveModeConversation(
				ctx,
				generated.ResolveProactiveModeConversationParams{
					CompanyID: principal.CompanyID,
					UserID:    principal.UserID,
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrProactiveModeConversationRequired
			}
			if err != nil {
				return fmt.Errorf("resolve proactive mode conversation: %w", err)
			}
			enabledByUserID = uuid.NullUUID{UUID: principal.UserID, Valid: true}
			conversationID = uuid.NullUUID{UUID: resolved, Valid: true}
		}

		row, err := tx.UpdateProactiveMode(ctx, generated.UpdateProactiveModeParams{
			Enabled:         input.Enabled,
			EnabledByUserID: enabledByUserID,
			ConversationID:  conversationID,
			CompanyID:       principal.CompanyID,
			ExpectedVersion: input.ExpectedRevision,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProactiveModeConflict
		}
		if err != nil {
			return fmt.Errorf("update proactive mode: %w", err)
		}
		result = proactiveModeFromUpdateRow(row)
		result.CanManage = true
		return nil
	})
	if err != nil {
		return ProactiveMode{}, err
	}
	return result, nil
}

func proactiveModeFromGetRow(row generated.GetProactiveModeForMemberRow) ProactiveMode {
	return ProactiveMode{
		CompanyID:       row.CompanyID,
		Enabled:         row.ProactiveModeEnabled,
		EnabledByUserID: optionalUUID(row.ProactiveModeEnabledByUserID),
		ConversationID:  optionalUUID(row.ProactiveModeConversationID),
		Revision:        row.ProactiveModeVersion,
		CanManage:       auth.CompanyRole(row.MemberRole).Can(auth.PermissionCompanyProactiveModeManage),
	}
}

func proactiveModeFromUpdateRow(row generated.UpdateProactiveModeRow) ProactiveMode {
	return ProactiveMode{
		CompanyID:       row.CompanyID,
		Enabled:         row.ProactiveModeEnabled,
		EnabledByUserID: optionalUUID(row.ProactiveModeEnabledByUserID),
		ConversationID:  optionalUUID(row.ProactiveModeConversationID),
		Revision:        row.ProactiveModeVersion,
	}
}
