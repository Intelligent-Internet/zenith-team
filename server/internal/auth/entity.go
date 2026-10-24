package auth

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

type ClientKind string

const (
	ClientWeb     ClientKind = "web"
	ClientDesktop ClientKind = "desktop"
)

// AuthResultSession and AuthResultPendingVerification are the values of the
// `result` discriminator every signup/login-shaped response carries. The two
// bodies share one status code per operation, so clients branch on this
// field, never on HTTP status. Deliberately untyped strings: each response
// shape carries exactly one of them as a constant.
const (
	AuthResultSession             = "session"
	AuthResultPendingVerification = "pending_verification"
)

type User struct {
	ID               uuid.UUID
	Email            string
	PasswordHash     string
	DisplayName      string
	AvatarURL        *string
	Biography        string
	PhoneCountryISO2 *string
	PhoneNumber      *string
	DisabledAt       *time.Time
	DeletedAt        *time.Time
	EmailVerifiedAt  *time.Time
	LinkedProviders  []string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Version          int64
}

// ProjectUser maps a trusted stored User into its public response.
func ProjectUser(user User) UserResponse {
	return UserResponse{
		ID:               user.ID,
		Email:            user.Email,
		EmailVerifiedAt:  protocol.FormatOptionalWireTime(user.EmailVerifiedAt),
		HasPassword:      user.PasswordHash != "",
		LinkedProviders:  append([]string{}, user.LinkedProviders...),
		DisplayName:      user.DisplayName,
		AvatarURL:        user.AvatarURL,
		Biography:        user.Biography,
		PhoneCountryISO2: user.PhoneCountryISO2,
		PhoneNumber:      user.PhoneNumber,
		Revision:         user.Version,
		CreatedAt:        protocol.FormatWireTime(user.CreatedAt),
		UpdatedAt:        protocol.FormatWireTime(user.UpdatedAt),
	}
}

type NewUser struct {
	Email           string
	PasswordHash    string
	DisplayName     string
	EmailVerifiedAt *time.Time
}

type NewSession struct {
	UserID               uuid.UUID
	TokenPrefix          string
	TokenHash            []byte
	ExpiresAt            time.Time
	RotatedFromSessionID *uuid.UUID
	RotationID           *uuid.UUID
	ClientMetadata       json.RawMessage
	// Client is who holds the new session; empty stores a person's.
	Client SessionClient
}

type SessionRevocation struct {
	SessionID   uuid.UUID
	UserID      uuid.UUID
	Reason      SessionRevocationReason
	EffectiveAt time.Time
}

type SessionMutationOutcome string

const (
	SessionMutationRotated        SessionMutationOutcome = "rotated"
	SessionMutationLoggedOut      SessionMutationOutcome = "logged_out"
	SessionMutationNoChange       SessionMutationOutcome = "no_change"
	SessionMutationInvalid        SessionMutationOutcome = "invalid"
	SessionMutationAlreadyRotated SessionMutationOutcome = "already_rotated"
	SessionMutationRecovered      SessionMutationOutcome = "recovered"
	SessionMutationTokenReuse     SessionMutationOutcome = "refresh_token_reuse"
)

type SessionMutationResult struct {
	Outcome     SessionMutationOutcome
	Revocations []SessionRevocation
}

// SessionInvalidator applies committed session revocations to live connection
// indexes. Implementations own bounded cross-node delivery and must not turn a
// committed auth mutation into a request failure.
type SessionInvalidator interface {
	InvalidateSessions(context.Context, []SessionRevocation)
}

type SessionInvalidatorFunc func(context.Context, []SessionRevocation)

func (f SessionInvalidatorFunc) InvalidateSessions(ctx context.Context, revocations []SessionRevocation) {
	f(ctx, revocations)
}

type RotateSessionResult struct {
	User             User
	SessionID        uuid.UUID
	RefreshExpiresAt time.Time
	Mutation         SessionMutationResult
	// refusal names why an invalid or reused token was refused, for
	// RefusalReason.
	refusal string
}

type SignupInput struct {
	Email          string
	Password       string
	DisplayName    string
	Client         ClientKind
	ClientMetadata map[string]any
}

type SignupResult struct {
	Auth    *Result
	Pending *PendingEmailVerification
}

type LoginResult struct {
	Auth    *Result
	Pending *PendingEmailVerification
}

type PendingEmailVerification struct {
	Email             string
	VerificationID    uuid.UUID
	ExpiresAt         time.Time
	ResendAvailableAt time.Time
	AttemptsRemaining int
	MaxAttempts       int
}

type VerifyEmailInput struct {
	Email          string
	VerificationID uuid.UUID
	Code           string
	Client         ClientKind
}

type ResendEmailVerificationInput struct {
	Email          string
	VerificationID uuid.UUID
	Client         ClientKind
}

type EmailVerificationStatusInput struct {
	Email          string
	VerificationID uuid.UUID
	Client         ClientKind
}

type EmailVerificationStatus string

const (
	EmailVerificationPending  EmailVerificationStatus = "pending_verification"
	EmailVerificationExpired  EmailVerificationStatus = "expired"
	EmailVerificationNotFound EmailVerificationStatus = "not_found"
)

type EmailVerificationStatusResult struct {
	Status  EmailVerificationStatus
	Pending *PendingEmailVerification
}

type LoginInput struct {
	Email          string
	Password       string
	Client         ClientKind
	ClientMetadata map[string]any
}

type RefreshInput struct {
	RefreshToken   string
	Client         ClientKind
	ClientMetadata map[string]any
	RotationID     *uuid.UUID
}

type Result struct {
	User   User
	Client ClientKind
	Tokens IssuedTokens
}

type RefreshResult struct {
	Auth     *Result
	Mutation SessionMutationResult
}

// UserResponse is the user's public projection: one shape for the REST read
// and the user.updated frame, so a field added here reaches both.
type UserResponse struct {
	ID               uuid.UUID `json:"id"`
	Email            string    `json:"email"`
	EmailVerifiedAt  *string   `json:"emailVerifiedAt"`
	HasPassword      bool      `json:"hasPassword"`
	LinkedProviders  []string  `json:"linkedProviders"`
	DisplayName      string    `json:"displayName"`
	AvatarURL        *string   `json:"avatarUrl"`
	Biography        string    `json:"biography"`
	PhoneCountryISO2 *string   `json:"phoneCountryIso2"`
	PhoneNumber      *string   `json:"phoneNumber"`
	Revision         int64     `json:"revision"`
	CreatedAt        string    `json:"createdAt"`
	UpdatedAt        string    `json:"updatedAt"`
}

// SessionRevocationReason says why a session ended. It is on the wire, so a
// client can tell a routine refresh rotation from a forced sign-out.
type SessionRevocationReason string

// Session revocation reasons.
const (
	SessionRevocationRefreshRotated    SessionRevocationReason = "refresh_rotated"
	SessionRevocationLogout            SessionRevocationReason = "logout"
	SessionRevocationRefreshTokenReuse SessionRevocationReason = "refresh_token_reuse"
	SessionRevocationAdministrative    SessionRevocationReason = "administrative_revocation"
	SessionRevocationUserDisabled      SessionRevocationReason = "user_disabled"
	SessionRevocationPasswordReset     SessionRevocationReason = "password_reset"
)
