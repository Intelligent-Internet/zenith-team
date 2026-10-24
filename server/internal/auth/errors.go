package auth

import "errors"

var (
	ErrInvalidInput              = errors.New("invalid input")
	ErrEmailExists               = errors.New("email is already registered")
	ErrEmailDeliveryUnavailable  = errors.New("email delivery is unavailable")
	ErrVerificationNotFound      = errors.New("email verification was not found")
	ErrVerificationCodeInvalid   = errors.New("verification code is invalid")
	ErrVerificationCodeExpired   = errors.New("verification code is expired")
	ErrVerificationCodeLocked    = errors.New("verification code is locked")
	ErrVerificationResendTooSoon = errors.New("verification code cannot be resent yet")
	ErrPasswordResetUnavailable  = errors.New("password reset is unavailable")
	ErrPasswordResetInvalid      = errors.New("password reset token is invalid or expired")
	ErrInvalidCredentials        = errors.New("invalid credentials")
	ErrPasswordWorkBusy          = errors.New("password work capacity is exhausted")
	ErrInvalidSession            = errors.New("invalid session")
	ErrRefreshTokenReused        = errors.New("refresh token was already used")
	ErrSessionAlreadyRotated     = errors.New("session was already rotated")
	ErrAccountDisabled           = errors.New("account is disabled")
	ErrNotFound                  = errors.New("not found")
	// ErrDaemonSessionCannotMint refuses a daemon session that asks for
	// another daemon session: only a person's session starts one.
	ErrDaemonSessionCannotMint = errors.New("a daemon session cannot start another daemon session")
)

var (
	ErrOAuthNotConfigured       = errors.New("oauth provider is not configured")
	ErrOAuthFlowInvalid         = errors.New("oauth login flow is invalid or expired")
	ErrOAuthProviderDenied      = errors.New("oauth provider authorization was denied")
	ErrOAuthProviderUnavailable = errors.New("oauth provider is unavailable")
	ErrOAuthIdentityInvalid     = errors.New("oauth identity is invalid")
	ErrOAuthEmailUnverified     = errors.New("oauth email is not verified")
	ErrOAuthAccountConflict     = errors.New("oauth identity conflicts with an existing account")
)

// refusal is ErrInvalidSession or ErrRefreshTokenReused carrying why the
// credential was refused. Callers keep matching the sentinel and the client
// still gets session_invalid; only the server's request log reads the reason
// (RefusalReason), so a caller learns nothing it could probe tokens with.
type refusal struct {
	reason string
	err    error
}

func (r refusal) Error() string { return r.err.Error() + ": " + r.reason }
func (r refusal) Unwrap() error { return r.err }

// RefusalReason returns the request-log label for a refused credential:
// access_token_expired, access_token_invalid, session_ended,
// refresh_token_malformed, refresh_token_unknown, refresh_token_expired,
// refresh_token_revoked, refresh_token_reused or account_disabled. It is ""
// for any other error.
func RefusalReason(err error) string {
	var refused refusal
	if errors.As(err, &refused) {
		return refused.reason
	}
	return ""
}

// ErrDesktopHandoffCodeInvalid and ErrDesktopHandoffCodeExpired are the two
// terminal outcomes the desktop handoff service distinguishes when a code
// fails to consume. The storage layer (createDesktopHandoffCode /
// DAO.ConsumeDesktopHandoffCode) deletes unconditionally and never checks
// expiry, so only ErrDesktopHandoffCodeInvalid is ever returned there; the
// service layer compares the returned row's ExpiresAt against the current
// time to decide whether a lookup miss should instead be reported as expired.
var (
	ErrDesktopHandoffCodeInvalid = errors.New("desktop handoff code is invalid")
	ErrDesktopHandoffCodeExpired = errors.New("desktop handoff code is expired")
)
