package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type VerificationEmailSender interface {
	SendVerification(context.Context, string, string, string, string) error
}

// MaxVerificationAttempts is how many wrong codes a verification flow
// tolerates before it is spent.
//
// It is a constant, not a column and not a deployment knob. Stored per flow,
// a row could outlive the value it was written with and be judged by a number
// no longer in force; as a knob, it was a security parameter every operator
// had to have an opinion about and none did.
const MaxVerificationAttempts = 5

type EmailVerificationOptions struct {
	Sender         VerificationEmailSender
	Secret         []byte
	TTL            time.Duration
	ResendCooldown time.Duration
	Random         io.Reader
	Now            func() time.Time
}

type NewEmailVerification struct {
	UserID            *uuid.UUID
	Email             string
	PasswordHash      string
	DisplayName       string
	Client            ClientKind
	ClientMetadata    json.RawMessage
	CodeHash          []byte
	ExpiresAt         time.Time
	ResendAvailableAt time.Time
	Now               time.Time
}

type EmailVerification struct {
	ID                uuid.UUID
	UserID            *uuid.UUID
	Email             string
	PasswordHash      string
	DisplayName       string
	Client            ClientKind
	ClientMetadata    json.RawMessage
	CodeHash          []byte
	Generation        int
	FailedAttempts    int
	ExpiresAt         time.Time
	ResendAvailableAt time.Time
}

type EmailVerificationStartResult struct {
	Flow        EmailVerification
	CodeChanged bool
}

type EmailVerificationLookup struct {
	ID     uuid.UUID
	Email  string
	Client ClientKind
}

type EmailVerificationCodeUpdate struct {
	ID                uuid.UUID
	Email             string
	Client            ClientKind
	CodeHash          []byte
	ExpiresAt         time.Time
	ResendAvailableAt time.Time
	Now               time.Time
}

type EmailVerificationFailure struct {
	ID         uuid.UUID
	Email      string
	Client     ClientKind
	Generation int
	Now        time.Time
}

type EmailVerificationMatch struct {
	ID         uuid.UUID
	Email      string
	Client     ClientKind
	Generation int
	CodeHash   []byte
	Now        time.Time
}

type emailVerificationManager struct {
	store          *db.Store
	sender         VerificationEmailSender
	secret         []byte
	ttl            time.Duration
	resendCooldown time.Duration
	random         io.Reader
	now            func() time.Time
}

func newEmailVerificationManager(
	options *EmailVerificationOptions,
	store *db.Store,
) (*emailVerificationManager, error) {
	if options == nil {
		return nil, nil
	}
	if store == nil || isNilEmailVerificationDependency(options.Sender) ||
		len(options.Secret) < 32 || options.TTL <= 0 || options.ResendCooldown <= 0 {
		return nil, errors.New(
			"email verification store, sender, 32-byte secret, TTL, and cooldown are required",
		)
	}
	randomReader := options.Random
	if randomReader == nil {
		randomReader = rand.Reader
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &emailVerificationManager{
		store: store, sender: options.Sender, secret: append([]byte(nil), options.Secret...),
		ttl: options.TTL, resendCooldown: options.ResendCooldown,
		random: randomReader, now: now,
	}, nil
}

func isNilEmailVerificationDependency(dependency any) bool {
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

func (m *emailVerificationManager) start(
	ctx context.Context,
	pending NewEmailVerification,
) (PendingEmailVerification, error) {
	code, err := newEmailVerificationCode(m.random)
	if err != nil {
		return PendingEmailVerification{}, fmt.Errorf("generate email verification code: %w", err)
	}
	now := m.now().UTC()
	pending.CodeHash = hashEmailVerificationCode(m.secret, code)
	pending.ExpiresAt = now.Add(m.ttl)
	pending.ResendAvailableAt = now.Add(m.resendCooldown)
	pending.Now = now
	started, err := startEmailVerification(ctx, m.store.Queries(), pending)
	if err != nil {
		return PendingEmailVerification{}, err
	}
	if started.CodeChanged {
		if err := m.deliver(ctx, started.Flow, code, now); err != nil {
			return PendingEmailVerification{}, err
		}
	}
	return pendingEmailVerification(started.Flow), nil
}

func (m *emailVerificationManager) deliver(
	ctx context.Context,
	flow EmailVerification,
	code string,
	now time.Time,
) error {
	idempotencyKey := fmt.Sprintf("email-verification/%s/%d", flow.ID, flow.Generation)
	if err := m.sender.SendVerification(ctx, flow.Email, flow.DisplayName, code, idempotencyKey); err != nil {
		_ = m.store.Queries().MarkEmailVerificationDeliveryFailed(
			context.WithoutCancel(ctx),
			generated.MarkEmailVerificationDeliveryFailedParams{
				RetryAt:    pgtype.Timestamptz{Time: now, Valid: true},
				ID:         flow.ID,
				Generation: int32(flow.Generation),
			},
		)
		return ErrEmailDeliveryUnavailable
	}
	return nil
}

func (s *DefaultAuthService) VerifyEmail(ctx context.Context, input VerifyEmailInput) (Result, error) {
	manager := s.emailVerification
	if manager == nil {
		return Result{}, ErrVerificationNotFound
	}
	lookup, err := normalizeEmailVerificationLookup(input.Email, input.VerificationID, input.Client)
	if err != nil || !validEmailVerificationCode(input.Code) {
		return Result{}, ErrVerificationCodeInvalid
	}
	flow, err := getEmailVerification(ctx, manager.store.Queries(), lookup)
	if err != nil {
		return Result{}, err
	}
	now := manager.now().UTC()
	if !flow.ExpiresAt.After(now) {
		return Result{}, ErrVerificationCodeExpired
	}
	if flow.FailedAttempts >= MaxVerificationAttempts {
		return Result{}, ErrVerificationCodeLocked
	}
	providedHash := hashEmailVerificationCode(manager.secret, input.Code)
	if subtle.ConstantTimeCompare(flow.CodeHash, providedHash) != 1 {
		updated, err := recordEmailVerificationFailure(ctx, manager.store.Queries(), EmailVerificationFailure{
			ID: flow.ID, Email: flow.Email, Client: flow.Client, Generation: flow.Generation, Now: now,
		})
		if err != nil {
			return Result{}, err
		}
		if updated.FailedAttempts >= MaxVerificationAttempts {
			return Result{}, ErrVerificationCodeLocked
		}
		return Result{}, ErrVerificationCodeInvalid
	}

	refresh, err := s.tokens.IssueRefresh()
	if err != nil {
		return Result{}, err
	}
	user, sessionID, err := consumeEmailVerification(ctx, manager.store, EmailVerificationMatch{
		ID: flow.ID, Email: flow.Email, Client: flow.Client, Generation: flow.Generation,
		CodeHash: providedHash, Now: now,
	}, NewUser{
		Email: flow.Email, PasswordHash: flow.PasswordHash, DisplayName: flow.DisplayName,
		EmailVerifiedAt: &now,
	}, NewSession{
		TokenPrefix: refresh.Prefix, TokenHash: refresh.Hash, ExpiresAt: refresh.ExpiresAt,
		ClientMetadata: append(json.RawMessage(nil), flow.ClientMetadata...),
	})
	if err != nil {
		return Result{}, err
	}
	issued, err := issueSessionTokens(s.tokens, user.ID, sessionID, refresh)
	if err != nil {
		return Result{}, err
	}
	return Result{User: user, Client: flow.Client, Tokens: issued}, nil
}

func (s *DefaultAuthService) ResendEmailVerification(
	ctx context.Context,
	input ResendEmailVerificationInput,
) (PendingEmailVerification, error) {
	manager := s.emailVerification
	if manager == nil {
		return PendingEmailVerification{}, ErrVerificationNotFound
	}
	lookup, err := normalizeEmailVerificationLookup(input.Email, input.VerificationID, input.Client)
	if err != nil {
		return PendingEmailVerification{}, err
	}
	flow, err := getEmailVerification(ctx, manager.store.Queries(), lookup)
	if err != nil {
		return PendingEmailVerification{}, err
	}
	now := manager.now().UTC()
	if !flow.ExpiresAt.After(now) {
		return PendingEmailVerification{}, ErrVerificationCodeExpired
	}
	if flow.FailedAttempts >= MaxVerificationAttempts {
		return PendingEmailVerification{}, ErrVerificationCodeLocked
	}
	if flow.ResendAvailableAt.After(now) {
		return PendingEmailVerification{}, ErrVerificationResendTooSoon
	}
	code, err := newEmailVerificationCode(manager.random)
	if err != nil {
		return PendingEmailVerification{}, fmt.Errorf("generate email verification code: %w", err)
	}
	updated, err := updateEmailVerificationCode(ctx, manager.store.Queries(), EmailVerificationCodeUpdate{
		ID: flow.ID, Email: flow.Email, Client: flow.Client,
		CodeHash: hashEmailVerificationCode(manager.secret, code), ExpiresAt: now.Add(manager.ttl),
		ResendAvailableAt: now.Add(manager.resendCooldown), Now: now,
	})
	if err != nil {
		return PendingEmailVerification{}, err
	}
	if err := manager.deliver(ctx, updated, code, now); err != nil {
		return PendingEmailVerification{}, err
	}
	return pendingEmailVerification(updated), nil
}

func (s *DefaultAuthService) EmailVerificationStatus(
	ctx context.Context,
	input EmailVerificationStatusInput,
) (EmailVerificationStatusResult, error) {
	manager := s.emailVerification
	if manager == nil {
		return EmailVerificationStatusResult{Status: EmailVerificationNotFound}, nil
	}
	lookup, err := normalizeEmailVerificationLookup(input.Email, input.VerificationID, input.Client)
	if err != nil {
		return EmailVerificationStatusResult{Status: EmailVerificationNotFound}, nil
	}
	flow, err := getEmailVerification(ctx, manager.store.Queries(), lookup)
	if errors.Is(err, ErrVerificationNotFound) || errors.Is(err, ErrNotFound) {
		return EmailVerificationStatusResult{Status: EmailVerificationNotFound}, nil
	}
	if err != nil {
		return EmailVerificationStatusResult{}, err
	}
	if !flow.ExpiresAt.After(manager.now().UTC()) || flow.FailedAttempts >= MaxVerificationAttempts {
		return EmailVerificationStatusResult{Status: EmailVerificationExpired}, nil
	}
	pending := pendingEmailVerification(flow)
	return EmailVerificationStatusResult{Status: EmailVerificationPending, Pending: &pending}, nil
}

func normalizeEmailVerificationLookup(email string, id uuid.UUID, client ClientKind) (EmailVerificationLookup, error) {
	normalized, err := normalizeEmail(email)
	if err != nil || !util.ValidUUID(id) || (client != ClientWeb && client != ClientDesktop) {
		return EmailVerificationLookup{}, ErrInvalidInput
	}
	return EmailVerificationLookup{ID: id, Email: normalized, Client: client}, nil
}

func pendingEmailVerification(flow EmailVerification) PendingEmailVerification {
	return PendingEmailVerification{
		Email: flow.Email, VerificationID: flow.ID,
		ExpiresAt: flow.ExpiresAt, ResendAvailableAt: flow.ResendAvailableAt,
		AttemptsRemaining: MaxVerificationAttempts - flow.FailedAttempts,
		MaxAttempts:       MaxVerificationAttempts,
	}
}

func newEmailVerificationCode(random io.Reader) (string, error) {
	value, err := rand.Int(random, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", value.Int64()), nil
}

func validEmailVerificationCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for index := range code {
		if code[index] < '0' || code[index] > '9' {
			return false
		}
	}
	return true
}

func hashEmailVerificationCode(secret []byte, code string) []byte {
	hasher := hmac.New(sha256.New, secret)
	_, _ = hasher.Write([]byte(code))
	return hasher.Sum(nil)
}

func startEmailVerification(
	ctx context.Context,
	q *generated.Queries,
	pending NewEmailVerification,
) (EmailVerificationStartResult, error) {
	stored, err := q.UpsertEmailVerificationFlow(ctx, generated.UpsertEmailVerificationFlowParams{
		UserID:            util.NullUUIDFromPtr(pending.UserID),
		Email:             pending.Email,
		PasswordHash:      pending.PasswordHash,
		DisplayName:       pending.DisplayName,
		Client:            string(pending.Client),
		ClientMetadata:    pending.ClientMetadata,
		CodeHash:          pending.CodeHash,
		ExpiresAt:         pgtype.Timestamptz{Time: pending.ExpiresAt, Valid: true},
		ResendAvailableAt: pgtype.Timestamptz{Time: pending.ResendAvailableAt, Valid: true},
		NowAt:             pgtype.Timestamptz{Time: pending.Now, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		stored, err = q.GetEmailVerificationFlowByEmail(ctx, pending.Email)
	}
	if err != nil {
		return EmailVerificationStartResult{}, fmt.Errorf("start email verification: %w", err)
	}
	flow := emailVerificationFromDB(stored)
	return EmailVerificationStartResult{
		Flow: flow, CodeChanged: bytes.Equal(flow.CodeHash, pending.CodeHash),
	}, nil
}

func getEmailVerification(
	ctx context.Context,
	q *generated.Queries,
	lookup EmailVerificationLookup,
) (EmailVerification, error) {
	stored, err := q.GetEmailVerificationFlow(ctx, generated.GetEmailVerificationFlowParams{
		ID: lookup.ID, Email: lookup.Email, Client: string(lookup.Client),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return EmailVerification{}, ErrVerificationNotFound
	}
	if err != nil {
		return EmailVerification{}, fmt.Errorf("get email verification: %w", err)
	}
	return emailVerificationFromDB(stored), nil
}

func updateEmailVerificationCode(
	ctx context.Context,
	q *generated.Queries,
	update EmailVerificationCodeUpdate,
) (EmailVerification, error) {
	stored, err := q.UpdateEmailVerificationCode(ctx, generated.UpdateEmailVerificationCodeParams{
		CodeHash:          update.CodeHash,
		ExpiresAt:         pgtype.Timestamptz{Time: update.ExpiresAt, Valid: true},
		ResendAvailableAt: pgtype.Timestamptz{Time: update.ResendAvailableAt, Valid: true},
		NowAt:             pgtype.Timestamptz{Time: update.Now, Valid: true},
		ID:                update.ID,
		Email:             update.Email,
		Client:            string(update.Client),
		MaxAttempts:       MaxVerificationAttempts,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return EmailVerification{}, classifyEmailVerificationMutation(ctx, q, EmailVerificationLookup{
			ID: update.ID, Email: update.Email, Client: update.Client,
		}, update.Now, true)
	}
	if err != nil {
		return EmailVerification{}, fmt.Errorf("update email verification code: %w", err)
	}
	return emailVerificationFromDB(stored), nil
}

func recordEmailVerificationFailure(
	ctx context.Context,
	q *generated.Queries,
	failure EmailVerificationFailure,
) (EmailVerification, error) {
	stored, err := q.RecordEmailVerificationFailure(ctx, generated.RecordEmailVerificationFailureParams{
		NowAt: pgtype.Timestamptz{Time: failure.Now, Valid: true},
		ID:    failure.ID, Email: failure.Email,
		Client: string(failure.Client), Generation: int32(failure.Generation),
		MaxAttempts: MaxVerificationAttempts,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return EmailVerification{}, classifyEmailVerificationMutation(ctx, q, EmailVerificationLookup{
			ID: failure.ID, Email: failure.Email, Client: failure.Client,
		}, failure.Now, false)
	}
	if err != nil {
		return EmailVerification{}, fmt.Errorf("record email verification failure: %w", err)
	}
	return emailVerificationFromDB(stored), nil
}

func consumeEmailVerification(
	ctx context.Context,
	store *db.Store,
	match EmailVerificationMatch,
	newUser NewUser,
	newSession NewSession,
) (User, uuid.UUID, error) {
	var (
		created   User
		sessionID uuid.UUID
	)
	err := store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		flow, err := queries.ConsumeEmailVerificationFlow(ctx, generated.ConsumeEmailVerificationFlowParams{
			ID: match.ID, Email: match.Email, Client: string(match.Client),
			Generation: int32(match.Generation), CodeHash: match.CodeHash,
			NowAt:       pgtype.Timestamptz{Time: match.Now, Valid: true},
			MaxAttempts: MaxVerificationAttempts,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVerificationCodeInvalid
		}
		if err != nil {
			return err
		}
		newUser.Email = flow.Email
		newUser.PasswordHash = flow.PasswordHash
		newUser.DisplayName = flow.DisplayName
		verifiedAt := pgtype.Timestamptz{Time: match.Now, Valid: true}
		var userID uuid.UUID
		if flow.UserID.Valid {
			userID = flow.UserID.UUID
			_, err = queries.MarkUserEmailVerified(ctx, generated.MarkUserEmailVerifiedParams{
				ID: flow.UserID.UUID, Email: flow.Email, VerifiedAt: verifiedAt,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrAccountDisabled
			}
			if err != nil {
				return err
			}
		} else {
			user, createErr := queries.CreateUser(ctx, generated.CreateUserParams{
				Email:        newUser.Email,
				PasswordHash: pgtype.Text{String: newUser.PasswordHash, Valid: true},
				DisplayName:  newUser.DisplayName, EmailVerifiedAt: verifiedAt,
			})
			if createErr != nil {
				return createErr
			}
			userID = user.ID
		}
		newSession.UserID = userID
		newSession.ClientMetadata = append(newSession.ClientMetadata[:0], flow.ClientMetadata...)
		session, err := queries.CreateAuthSession(ctx, createAuthSessionParams(newSession))
		if err != nil {
			return err
		}
		sessionID = session.ID
		created, err = getUserByID(ctx, queries, userID)
		return err
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrVerificationCodeInvalid):
			return User{}, uuid.Nil, ErrVerificationCodeInvalid
		case errors.Is(err, ErrAccountDisabled):
			return User{}, uuid.Nil, ErrAccountDisabled
		case isUniqueViolation(err, "users_email_ci_key"):
			return User{}, uuid.Nil, ErrEmailExists
		default:
			return User{}, uuid.Nil, fmt.Errorf("consume email verification: %w", err)
		}
	}
	return created, sessionID, nil
}

func classifyEmailVerificationMutation(
	ctx context.Context,
	q *generated.Queries,
	lookup EmailVerificationLookup,
	now time.Time,
	resend bool,
) error {
	flow, err := getEmailVerification(ctx, q, lookup)
	if err != nil {
		return err
	}
	if !flow.ExpiresAt.After(now) {
		return ErrVerificationCodeExpired
	}
	if flow.FailedAttempts >= MaxVerificationAttempts {
		return ErrVerificationCodeLocked
	}
	if resend && flow.ResendAvailableAt.After(now) {
		return ErrVerificationResendTooSoon
	}
	return ErrVerificationCodeInvalid
}

func emailVerificationFromDB(flow generated.EmailVerificationFlow) EmailVerification {
	return EmailVerification{
		ID:     flow.ID,
		UserID: util.NullUUIDPtr(flow.UserID), Email: flow.Email, PasswordHash: flow.PasswordHash,
		DisplayName: flow.DisplayName, Client: ClientKind(flow.Client),
		ClientMetadata: append([]byte(nil), flow.ClientMetadata...),
		CodeHash:       append([]byte(nil), flow.CodeHash...), Generation: int(flow.Generation),
		FailedAttempts: int(flow.FailedAttempts),
		ExpiresAt:      flow.ExpiresAt.Time, ResendAvailableAt: flow.ResendAvailableAt.Time,
	}
}
