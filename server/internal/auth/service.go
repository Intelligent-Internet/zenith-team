// Package auth implements user and machine authentication.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

const maxClientMetadataBytes = 4 << 10

type ServiceOptions struct {
	Store              *db.Store
	Bus                *events.Bus
	Passwords          PasswordManager
	Tokens             *TokenManager
	SessionInvalidator SessionInvalidator
	EmailVerification  *EmailVerificationOptions
	PasswordReset      *PasswordResetOptions
}

// AuthService owns user credentials and sessions: signup, login, token
// refresh and authentication, plus the account-facing profile, email
// verification, and password reset flows.
type AuthService interface {
	Signup(context.Context, SignupInput) (SignupResult, error)
	Login(context.Context, LoginInput) (LoginResult, error)
	Refresh(context.Context, RefreshInput) (RefreshResult, error)
	Authenticate(context.Context, string, TokenKind) (UserPrincipal, error)
	MintDaemonSession(context.Context, UserPrincipal) (Result, error)
	Logout(context.Context, string) (SessionMutationResult, error)
	CurrentUser(context.Context, uuid.UUID) (User, error)
	UpdateProfile(context.Context, uuid.UUID, UpdateProfileInput) (User, error)
	VerifyEmail(context.Context, VerifyEmailInput) (Result, error)
	ResendEmailVerification(context.Context, ResendEmailVerificationInput) (PendingEmailVerification, error)
	EmailVerificationStatus(context.Context, EmailVerificationStatusInput) (EmailVerificationStatusResult, error)
	RequestPasswordReset(context.Context, PasswordResetRequestInput) error
	ResetPassword(context.Context, PasswordResetInput) error
}

var _ AuthService = (*DefaultAuthService)(nil)

type DefaultAuthService struct {
	store             *db.Store
	bus               *events.Bus
	passwords         PasswordManager
	tokens            *TokenManager
	invalidator       SessionInvalidator
	dummyHash         string
	emailVerification *emailVerificationManager
	passwordReset     *passwordResetManager
}

type PasswordManager interface {
	Hash(password string) (string, error)
	Verify(encodedHash, password string) bool
}

func NewAuthService(options ServiceOptions) (*DefaultAuthService, error) {
	if options.Store == nil || options.Bus == nil || isNilDependency(options.Passwords) ||
		isNilDependency(options.Tokens) || isNilDependency(options.SessionInvalidator) {
		return nil, errors.New(
			"auth store, event bus, password hasher, token manager, and session invalidator are required",
		)
	}
	dummyHash, err := options.Passwords.Hash("zenith-team-dummy-password-never-used")
	if err != nil {
		return nil, fmt.Errorf("construct password timing defense: %w", err)
	}
	verification, err := newEmailVerificationManager(options.EmailVerification, options.Store)
	if err != nil {
		return nil, err
	}
	passwordReset, err := newPasswordResetManager(options.PasswordReset, options.Store)
	if err != nil {
		return nil, err
	}
	return &DefaultAuthService{
		store:             options.Store,
		bus:               options.Bus,
		passwords:         options.Passwords,
		tokens:            options.Tokens,
		invalidator:       options.SessionInvalidator,
		dummyHash:         dummyHash,
		emailVerification: verification,
		passwordReset:     passwordReset,
	}, nil
}

func isNilDependency(dependency any) bool {
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

func (s *DefaultAuthService) Signup(ctx context.Context, input SignupInput) (SignupResult, error) {
	email, err := normalizeEmail(input.Email)
	if err != nil {
		return SignupResult{}, err
	}
	displayName := protocol.TrimContractWhitespace(input.DisplayName)
	if !utf8.ValidString(displayName) || displayName == "" || utf8.RuneCountInString(displayName) > 120 {
		return SignupResult{}, fmt.Errorf(
			"%w: display name must contain between 1 and 120 characters",
			ErrInvalidInput,
		)
	}
	if err := validatePassword(input.Password); err != nil {
		return SignupResult{}, err
	}
	metadata, err := encodeClientMetadata(input.Client, input.ClientMetadata)
	if err != nil {
		return SignupResult{}, err
	}
	passwordHash, err := s.passwords.Hash(input.Password)
	if err != nil {
		return SignupResult{}, fmt.Errorf("hash password: %w", err)
	}
	if s.emailVerification != nil {
		if _, err := findUserByEmail(ctx, s.store.Queries(), email); err == nil {
			return SignupResult{}, ErrEmailExists
		} else if !errors.Is(err, ErrNotFound) {
			return SignupResult{}, err
		}
		pending, err := s.emailVerification.start(ctx, NewEmailVerification{
			Email: email, PasswordHash: passwordHash, DisplayName: displayName,
			Client: input.Client, ClientMetadata: metadata,
		})
		if err != nil {
			return SignupResult{}, err
		}
		return SignupResult{Pending: &pending}, nil
	}
	authResult, err := s.createImmediateSignup(ctx, input.Client, email, passwordHash, displayName, metadata)
	if err != nil {
		return SignupResult{}, err
	}
	return SignupResult{Auth: &authResult}, nil
}

func (s *DefaultAuthService) createImmediateSignup(
	ctx context.Context,
	client ClientKind,
	email string,
	passwordHash string,
	displayName string,
	metadata json.RawMessage,
) (Result, error) {
	refresh, err := s.tokens.IssueRefresh()
	if err != nil {
		return Result{}, err
	}
	verifiedAt := time.Now().UTC()
	user, sessionID, err := createUserWithSession(ctx, s.store, NewUser{
		Email:           email,
		PasswordHash:    passwordHash,
		DisplayName:     displayName,
		EmailVerifiedAt: &verifiedAt,
	}, NewSession{
		TokenPrefix:    refresh.Prefix,
		TokenHash:      refresh.Hash,
		ExpiresAt:      refresh.ExpiresAt,
		ClientMetadata: metadata,
	})
	if err != nil {
		return Result{}, err
	}
	issued, err := issueSessionTokens(s.tokens, user.ID, sessionID, refresh)
	if err != nil {
		return Result{}, err
	}
	return Result{User: user, Client: client, Tokens: issued}, nil
}

func (s *DefaultAuthService) Login(ctx context.Context, input LoginInput) (LoginResult, error) {
	metadata, err := encodeClientMetadata(input.Client, input.ClientMetadata)
	if err != nil {
		return LoginResult{}, err
	}
	email, emailErr := normalizeEmail(input.Email)
	if emailErr != nil {
		_ = s.passwords.Verify(s.dummyHash, input.Password)
		return LoginResult{}, ErrInvalidCredentials
	}
	user, err := findUserByEmail(ctx, s.store.Queries(), email)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			_ = s.passwords.Verify(s.dummyHash, input.Password)
			return LoginResult{}, ErrInvalidCredentials
		}
		return LoginResult{}, err
	}
	passwordHash := user.PasswordHash
	if passwordHash == "" {
		passwordHash = s.dummyHash
	}
	passwordMatches := s.passwords.Verify(passwordHash, input.Password)
	if user.DisabledAt != nil || user.PasswordHash == "" || !passwordMatches {
		return LoginResult{}, ErrInvalidCredentials
	}
	if user.EmailVerifiedAt == nil && s.emailVerification != nil {
		userID := user.ID
		pending, err := s.emailVerification.start(ctx, NewEmailVerification{
			UserID: &userID, Email: user.Email, PasswordHash: user.PasswordHash,
			DisplayName: user.DisplayName, Client: input.Client, ClientMetadata: metadata,
		})
		if err != nil {
			return LoginResult{}, err
		}
		return LoginResult{Pending: &pending}, nil
	}
	refresh, err := s.tokens.IssueRefresh()
	if err != nil {
		return LoginResult{}, err
	}
	user, sessionID, err := createSessionWithUserProjection(ctx, s.store, NewSession{
		UserID:         user.ID,
		TokenPrefix:    refresh.Prefix,
		TokenHash:      refresh.Hash,
		ExpiresAt:      refresh.ExpiresAt,
		ClientMetadata: metadata,
	})
	if err != nil {
		if errors.Is(err, ErrAccountDisabled) {
			return LoginResult{}, ErrInvalidCredentials
		}
		return LoginResult{}, err
	}
	issued, err := issueSessionTokens(s.tokens, user.ID, sessionID, refresh)
	if err != nil {
		return LoginResult{}, err
	}
	authResult := Result{User: user, Client: input.Client, Tokens: issued}
	return LoginResult{Auth: &authResult}, nil
}

func (s *DefaultAuthService) Refresh(ctx context.Context, input RefreshInput) (RefreshResult, error) {
	metadata, err := encodeClientMetadata(input.Client, input.ClientMetadata)
	if err != nil {
		return RefreshResult{}, err
	}
	if input.RotationID != nil && !util.ValidUUID(*input.RotationID) {
		return RefreshResult{}, fmt.Errorf("%w: rotation id is invalid", ErrInvalidInput)
	}
	refreshHash, err := s.tokens.LookupRefresh(input.RefreshToken)
	if err != nil {
		return RefreshResult{
			Mutation: SessionMutationResult{Outcome: SessionMutationInvalid},
		}, ErrInvalidSession
	}
	var refresh IssuedRefresh
	if input.RotationID == nil {
		refresh, err = s.tokens.IssueRefresh()
	} else {
		refresh, err = s.tokens.IssueRefreshForRotation(refreshHash, *input.RotationID)
	}
	if err != nil {
		return RefreshResult{}, err
	}
	rotation, err := rotateSession(ctx, s.store, s.bus, refreshHash, NewSession{
		TokenPrefix:    refresh.Prefix,
		TokenHash:      refresh.Hash,
		ExpiresAt:      refresh.ExpiresAt,
		RotationID:     input.RotationID,
		ClientMetadata: metadata,
	})
	if err != nil {
		return RefreshResult{}, err
	}
	s.invalidateCommittedSessions(ctx, rotation.Mutation.Revocations)
	result := RefreshResult{Mutation: rotation.Mutation}
	switch rotation.Mutation.Outcome {
	case SessionMutationInvalid:
		return result, ErrInvalidSession
	case SessionMutationAlreadyRotated:
		return result, ErrSessionAlreadyRotated
	case SessionMutationRecovered:
		if input.RotationID == nil {
			return result, errors.New("recovered auth rotation has no rotation id")
		}
		refresh, err = s.tokens.RestoreRefreshForRotation(
			refreshHash,
			*input.RotationID,
			rotation.RefreshExpiresAt,
		)
		if err != nil {
			return RefreshResult{}, err
		}
	case SessionMutationTokenReuse:
		return result, ErrRefreshTokenReused
	case SessionMutationRotated:
	default:
		return result, fmt.Errorf(
			"rotate auth session: unexpected mutation outcome %q",
			rotation.Mutation.Outcome,
		)
	}
	issued, err := issueSessionTokens(s.tokens, rotation.User.ID, rotation.SessionID, refresh)
	if err != nil {
		return result, err
	}
	authResult := Result{
		User:   rotation.User,
		Client: input.Client,
		Tokens: issued,
	}
	result.Auth = &authResult
	return result, nil
}

func issueSessionTokens(
	tokens *TokenManager,
	userID uuid.UUID,
	sessionID uuid.UUID,
	refresh IssuedRefresh,
) (IssuedTokens, error) {
	issuedAt := tokens.now().UTC()
	accessToken, accessExpiresAt, err := tokens.issueAccessAt(userID, sessionID, issuedAt)
	if err != nil {
		return IssuedTokens{}, err
	}
	return IssuedTokens{
		SessionID:          sessionID,
		IssuedAt:           issuedAt,
		AccessToken:        accessToken,
		RefreshToken:       refresh.Token,
		RefreshTokenHash:   refresh.Hash,
		RefreshTokenPrefix: refresh.Prefix,
		AccessExpiresAt:    accessExpiresAt,
		RefreshExpiresAt:   refresh.ExpiresAt,
	}, nil
}

func (s *DefaultAuthService) Authenticate(
	ctx context.Context,
	accessToken string,
	kind TokenKind,
) (UserPrincipal, error) {
	if kind != TokenKindSession && kind != TokenKindBearer {
		return UserPrincipal{}, ErrInvalidSession
	}
	claims, err := s.tokens.ValidateAccess(accessToken)
	if err != nil {
		return UserPrincipal{}, ErrInvalidSession
	}
	if !util.ValidUUID(claims.SessionID) {
		return UserPrincipal{}, ErrInvalidSession
	}
	row, err := s.store.Queries().GetActiveSessionUser(ctx, generated.GetActiveSessionUserParams{
		SessionID: claims.SessionID,
		UserID:    claims.UserID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return UserPrincipal{}, ErrInvalidSession
		}
		return UserPrincipal{}, fmt.Errorf("get active auth session: %w", err)
	}
	return UserPrincipal{
		UserID: row.User.ID, SessionID: claims.SessionID, AuthKind: kind, AccessExpiresAt: claims.ExpiresAt,
		Client: SessionClient(row.Client),
	}, nil
}

// MintDaemonSession starts a daemon session for the person signed in on
// principal's session (POST /auth/daemon-session). It is a new session
// of its own, not a rotation of the caller's, so signing the person out of
// the app leaves the daemon signed in until it is cleared too. It carries
// the desktop bearer transport, because the daemon renews it as the CLI
// renews auth.json. A daemon session cannot start another.
func (s *DefaultAuthService) MintDaemonSession(ctx context.Context, principal UserPrincipal) (Result, error) {
	if principal.DaemonSession() {
		return Result{}, ErrDaemonSessionCannotMint
	}
	metadata, err := encodeClientMetadata(ClientDesktop, nil)
	if err != nil {
		return Result{}, err
	}
	refresh, err := s.tokens.IssueRefresh()
	if err != nil {
		return Result{}, err
	}
	user, sessionID, err := createSessionWithUserProjection(ctx, s.store, NewSession{
		UserID:         principal.UserID,
		TokenPrefix:    refresh.Prefix,
		TokenHash:      refresh.Hash,
		ExpiresAt:      refresh.ExpiresAt,
		ClientMetadata: metadata,
		Client:         SessionClientDaemon,
	})
	if errors.Is(err, ErrAccountDisabled) {
		return Result{}, ErrInvalidSession
	}
	if err != nil {
		return Result{}, err
	}
	issued, err := issueSessionTokens(s.tokens, user.ID, sessionID, refresh)
	if err != nil {
		return Result{}, err
	}
	return Result{User: user, Client: ClientDesktop, Tokens: issued}, nil
}

func (s *DefaultAuthService) Logout(ctx context.Context, refreshToken string) (SessionMutationResult, error) {
	refreshHash, err := s.tokens.LookupRefresh(refreshToken)
	if err != nil {
		return SessionMutationResult{Outcome: SessionMutationNoChange}, nil
	}
	result, err := revokeSession(ctx, s.store, s.bus, refreshHash)
	if err != nil {
		return SessionMutationResult{}, fmt.Errorf("revoke auth session: %w", err)
	}
	s.invalidateCommittedSessions(ctx, result.Revocations)
	return result, nil
}

func (s *DefaultAuthService) invalidateCommittedSessions(ctx context.Context, revocations []SessionRevocation) {
	if len(revocations) == 0 {
		return
	}
	committed := append([]SessionRevocation(nil), revocations...)
	s.invalidator.InvalidateSessions(context.WithoutCancel(ctx), committed)
}

func (s *DefaultAuthService) CurrentUser(ctx context.Context, userID uuid.UUID) (User, error) {
	user, err := getUserByID(ctx, s.store.Queries(), userID)
	if err != nil {
		return User{}, err
	}
	if user.DisabledAt != nil {
		return User{}, ErrInvalidSession
	}
	return user, nil
}

func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(protocol.TrimContractWhitespace(raw))
	if email == "" || len(email) > 320 || !utf8.ValidString(email) {
		return "", fmt.Errorf("%w: email is invalid", ErrInvalidInput)
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return "", fmt.Errorf("%w: email is invalid", ErrInvalidInput)
	}
	return email, nil
}

func validatePassword(password string) error {
	characters := utf8.RuneCountInString(password)
	if !utf8.ValidString(password) || characters < 8 || characters > 128 {
		return fmt.Errorf("%w: password must contain between 8 and 128 characters", ErrInvalidInput)
	}
	return nil
}

func encodeClientMetadata(client ClientKind, details map[string]any) (json.RawMessage, error) {
	if client != ClientWeb && client != ClientDesktop {
		return nil, fmt.Errorf("%w: client must be web or desktop", ErrInvalidInput)
	}
	metadata := map[string]any{"client": client}
	if len(details) > 0 {
		metadata["details"] = details
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: client metadata is invalid", ErrInvalidInput)
	}
	if len(encoded) > maxClientMetadataBytes {
		return nil, fmt.Errorf("%w: client metadata is too large", ErrInvalidInput)
	}
	return encoded, nil
}
