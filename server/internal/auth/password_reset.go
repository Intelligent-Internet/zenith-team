package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

const passwordResetTokenBytes = 32

type PasswordResetEmailSender interface {
	SendPasswordReset(context.Context, string, string, string, string) error
}

type PasswordResetOptions struct {
	Sender         PasswordResetEmailSender
	WebAppOrigin   string
	TTL            time.Duration
	ResendCooldown time.Duration
	Random         io.Reader
	Now            func() time.Time
}

type PasswordResetRequestInput struct{ Email string }

type PasswordResetInput struct {
	Token       string
	NewPassword string
}

type NewPasswordReset struct {
	UserID            uuid.UUID
	Email             string
	DisplayName       string
	TokenHash         []byte
	ExpiresAt         time.Time
	ResendAvailableAt time.Time
	Now               time.Time
}

type PasswordResetFlow struct {
	ID                uuid.UUID
	UserID            uuid.UUID
	Email             string
	DisplayName       string
	TokenHash         []byte
	Generation        int
	ExpiresAt         time.Time
	ResendAvailableAt time.Time
}

type PasswordResetStartResult struct {
	Flow         PasswordResetFlow
	TokenChanged bool
}

type PasswordResetConsumption struct {
	TokenHash    []byte
	PasswordHash string
	Now          time.Time
}

type PasswordResetConsumptionResult struct {
	UserID      uuid.UUID
	Revocations []SessionRevocation
}

type passwordResetManager struct {
	store          *db.Store
	sender         PasswordResetEmailSender
	webAppOrigin   string
	ttl            time.Duration
	resendCooldown time.Duration
	random         io.Reader
	now            func() time.Time
}

func newPasswordResetManager(options *PasswordResetOptions, store *db.Store) (*passwordResetManager, error) {
	if options == nil {
		return nil, nil
	}
	if store == nil || isNilPasswordResetDependency(options.Sender) ||
		strings.TrimSpace(options.WebAppOrigin) == "" || options.TTL <= 0 || options.ResendCooldown <= 0 ||
		options.ResendCooldown >= options.TTL {
		return nil, errors.New("password reset store, sender, Web app origin, TTL, and cooldown are required")
	}
	parsedOrigin, err := url.Parse(options.WebAppOrigin)
	if err != nil || (parsedOrigin.Scheme != "http" && parsedOrigin.Scheme != "https") ||
		parsedOrigin.Host == "" || parsedOrigin.User != nil || parsedOrigin.Opaque != "" ||
		parsedOrigin.Path != "" || parsedOrigin.RawQuery != "" || parsedOrigin.Fragment != "" {
		return nil, errors.New("password reset Web app origin is invalid")
	}
	randomReader := options.Random
	if randomReader == nil {
		randomReader = rand.Reader
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &passwordResetManager{
		store: store, sender: options.Sender,
		webAppOrigin: strings.TrimRight(options.WebAppOrigin, "/"), ttl: options.TTL,
		resendCooldown: options.ResendCooldown, random: randomReader, now: now,
	}, nil
}

func isNilPasswordResetDependency(dependency any) bool {
	if dependency == nil {
		return true
	}
	value := reflect.ValueOf(dependency)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (s *DefaultAuthService) RequestPasswordReset(ctx context.Context, input PasswordResetRequestInput) error {
	manager := s.passwordReset
	if manager == nil {
		return ErrPasswordResetUnavailable
	}
	email, err := normalizeEmail(input.Email)
	if err != nil {
		return err
	}
	user, err := findUserByEmail(ctx, s.store.Queries(), email)
	if errors.Is(err, ErrNotFound) || (err == nil && user.DisabledAt != nil) {
		return nil
	}
	if err != nil {
		return err
	}
	tokenBytes := make([]byte, passwordResetTokenBytes)
	if _, err := io.ReadFull(manager.random, tokenBytes); err != nil {
		return fmt.Errorf("generate password reset token: %w", err)
	}
	now := manager.now().UTC()
	tokenHash := sha256.Sum256(tokenBytes)
	started, err := startPasswordReset(ctx, manager.store.Queries(), NewPasswordReset{
		UserID: user.ID, Email: user.Email, DisplayName: user.DisplayName,
		TokenHash: tokenHash[:], ExpiresAt: now.Add(manager.ttl),
		ResendAvailableAt: now.Add(manager.resendCooldown), Now: now,
	})
	if err != nil {
		return err
	}
	if !started.TokenChanged {
		return nil
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	resetURL := manager.webAppOrigin + "/login/reset?token=" + url.QueryEscape(token)
	idempotencyKey := fmt.Sprintf("password-reset/%s/%d", started.Flow.ID, started.Flow.Generation)
	if err := manager.sender.SendPasswordReset(
		ctx,
		user.Email,
		user.DisplayName,
		resetURL,
		idempotencyKey,
	); err != nil {
		_ = manager.store.Queries().MarkPasswordResetDeliveryFailed(
			context.WithoutCancel(ctx),
			generated.MarkPasswordResetDeliveryFailedParams{
				RetryAt:    pgtype.Timestamptz{Time: now, Valid: true},
				ID:         started.Flow.ID,
				Generation: int32(started.Flow.Generation),
			},
		)
	}
	return nil
}

func (s *DefaultAuthService) ResetPassword(ctx context.Context, input PasswordResetInput) error {
	manager := s.passwordReset
	if manager == nil {
		return ErrPasswordResetUnavailable
	}
	if err := validatePassword(input.NewPassword); err != nil {
		return err
	}
	tokenBytes, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimSpace(input.Token))
	if err != nil || len(tokenBytes) != passwordResetTokenBytes {
		return ErrPasswordResetInvalid
	}
	passwordHash, err := s.hashPassword(ctx, input.NewPassword)
	if err != nil {
		return fmt.Errorf("hash reset password: %w", err)
	}
	tokenHash := sha256.Sum256(tokenBytes)
	result, err := consumePasswordReset(ctx, manager.store, s.bus, PasswordResetConsumption{
		TokenHash: tokenHash[:], PasswordHash: passwordHash, Now: manager.now().UTC(),
	})
	if err != nil {
		return err
	}
	s.invalidateCommittedSessions(ctx, result.Revocations)
	return nil
}

func startPasswordReset(
	ctx context.Context,
	q *generated.Queries,
	pending NewPasswordReset,
) (PasswordResetStartResult, error) {
	stored, err := q.UpsertPasswordResetFlow(ctx, generated.UpsertPasswordResetFlowParams{
		UserID:            pending.UserID,
		TokenHash:         pending.TokenHash,
		ExpiresAt:         pgtype.Timestamptz{Time: pending.ExpiresAt, Valid: true},
		ResendAvailableAt: pgtype.Timestamptz{Time: pending.ResendAvailableAt, Valid: true},
		NowAt:             pgtype.Timestamptz{Time: pending.Now, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		stored, err = q.GetPasswordResetFlowByUser(ctx, pending.UserID)
	}
	if err != nil {
		return PasswordResetStartResult{}, fmt.Errorf("start password reset: %w", err)
	}
	flow := passwordResetFromDB(stored, pending.Email, pending.DisplayName)
	return PasswordResetStartResult{
		Flow: flow, TokenChanged: bytes.Equal(flow.TokenHash, pending.TokenHash),
	}, nil
}

func consumePasswordReset(
	ctx context.Context,
	store *db.Store,
	bus *events.Bus,
	input PasswordResetConsumption,
) (PasswordResetConsumptionResult, error) {
	var result PasswordResetConsumptionResult
	var pending []events.Event
	err := store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		pending = pending[:0]
		nowAt := pgtype.Timestamptz{Time: input.Now, Valid: true}
		flow, err := queries.ConsumePasswordResetFlow(ctx, generated.ConsumePasswordResetFlowParams{
			TokenHash: input.TokenHash, NowAt: nowAt,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPasswordResetInvalid
		}
		if err != nil {
			return err
		}
		if _, err := queries.SetUserPasswordFromReset(ctx, generated.SetUserPasswordFromResetParams{
			PasswordHash: input.PasswordHash, NowAt: nowAt, ID: flow.UserID,
		}); errors.Is(err, pgx.ErrNoRows) {
			return ErrPasswordResetInvalid
		} else if err != nil {
			return err
		}
		rows, err := queries.RevokeAllLiveAuthSessionsForPasswordReset(
			ctx,
			generated.RevokeAllLiveAuthSessionsForPasswordResetParams{
				UserID: flow.UserID, NowAt: nowAt,
			},
		)
		if err != nil {
			return err
		}
		revocations := make([]SessionRevocation, len(rows))
		for index, row := range rows {
			revocations[index] = sessionRevocationFromRow(
				row.ID,
				row.UserID,
				row.RevokedAt,
				row.RevocationReason,
			)
		}
		stageSessionRevocations(&pending, revocations, events.SystemActor())
		result = PasswordResetConsumptionResult{UserID: flow.UserID, Revocations: revocations}
		return nil
	})
	if errors.Is(err, ErrPasswordResetInvalid) {
		return PasswordResetConsumptionResult{}, ErrPasswordResetInvalid
	}
	if err != nil {
		return PasswordResetConsumptionResult{}, fmt.Errorf("consume password reset: %w", err)
	}
	for _, group := range events.GroupByTopic(pending) {
		bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

func passwordResetFromDB(flow generated.PasswordResetFlow, email, displayName string) PasswordResetFlow {
	return PasswordResetFlow{
		ID: flow.ID, UserID: flow.UserID, Email: email, DisplayName: displayName,
		TokenHash: append([]byte(nil), flow.TokenHash...), Generation: int(flow.Generation),
		ExpiresAt: flow.ExpiresAt.Time, ResendAvailableAt: flow.ResendAvailableAt.Time,
	}
}
