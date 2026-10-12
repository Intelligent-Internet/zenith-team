package auth

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type CompanyRole string

const (
	CompanyRoleOwner  CompanyRole = "owner"
	CompanyRoleAdmin  CompanyRole = "admin"
	CompanyRoleMember CompanyRole = "member"
)

type TokenKind string

const (
	TokenKindSession TokenKind = "session"
	TokenKindBearer  TokenKind = "bearer"
)

// SessionClient says who holds a session (auth_sessions.client, D31): a
// person, through the web app, the desktop app or the CLI, or a daemon. A
// daemon session acts for the same person but only on the daemon plane and
// beside a live Run proof whose Authorizer is that person; the auth
// middleware refuses it everywhere else.
type SessionClient string

const (
	SessionClientPerson SessionClient = "person"
	SessionClientDaemon SessionClient = "daemon"
)

// UserPrincipal is the authenticated caller. CompanyID and Role are zero until
// the company middleware resolves access for a company-scoped route.
type UserPrincipal struct {
	UserID    uuid.UUID
	SessionID uuid.UUID
	AuthKind  TokenKind
	// Client is who holds the session. Authenticate sets it; a principal
	// built without it reads as a person's.
	Client SessionClient
	// AccessExpiresAt is the validated immutable JWT expiry. Tests and internal
	// non-token principals may leave it zero; authenticated transports must
	// schedule closure when it is present.
	AccessExpiresAt time.Time

	CompanyID uuid.UUID
	Role      CompanyRole
}

// DaemonSession reports whether the caller is a daemon's own session.
func (p UserPrincipal) DaemonSession() bool {
	return p.Client == SessionClientDaemon
}

func (p UserPrincipal) valid() bool {
	return p.UserID != uuid.Nil && p.SessionID != uuid.Nil &&
		(p.AuthKind == TokenKindSession || p.AuthKind == TokenKindBearer)
}

type userPrincipalContextKey struct{}

func WithUserPrincipal(ctx context.Context, principal UserPrincipal) context.Context {
	return context.WithValue(ctx, userPrincipalContextKey{}, principal)
}

func UserPrincipalFromContext(ctx context.Context) (UserPrincipal, bool) {
	principal, ok := ctx.Value(userPrincipalContextKey{}).(UserPrincipal)
	return principal, ok && principal.valid()
}
