package auth

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/db"
)

// storelessService builds a DefaultAuthService over a store that owns no
// pool: every query handle it hands out is nil, so any storage call panics.
// Tests that assert a decision is reached *before* any storage side effect
// use it — the panic is the assertion.
func storelessService(t *testing.T, options ServiceOptions) *DefaultAuthService {
	t.Helper()
	if options.Store == nil {
		options.Store = &db.Store{}
	}
	if options.Passwords == nil {
		options.Passwords = newTestPasswordHasher(t)
	}
	if options.Tokens == nil {
		tokens, err := NewTokenManager(TokenManagerOptions{
			Secret:     []byte("01234567890123456789012345678901"),
			AccessTTL:  15 * time.Minute,
			RefreshTTL: time.Hour,
			Now:        func() time.Time { return time.Date(2026, time.July, 11, 9, 0, 0, 0, time.UTC) },
			Random:     bytes.NewReader(make([]byte, 256)),
		})
		if err != nil {
			t.Fatalf("NewTokenManager() error = %v", err)
		}
		options.Tokens = tokens
	}
	if options.SessionInvalidator == nil {
		options.SessionInvalidator = discardSessionInvalidator
	}
	if options.Bus == nil {
		options.Bus = authTestBus(t)
	}
	service, err := NewAuthService(options)
	if err != nil {
		t.Fatalf("NewAuthService() error = %v", err)
	}
	return service
}

func TestNewAuthServiceRequiresSessionInvalidator(t *testing.T) {
	t.Parallel()

	tokens, err := NewTokenManager(TokenManagerOptions{
		Secret: []byte("01234567890123456789012345678901"), AccessTTL: time.Minute, RefreshTTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewTokenManager() error = %v", err)
	}
	service, err := NewAuthService(ServiceOptions{
		Store: &db.Store{}, Passwords: newTestPasswordHasher(t), Tokens: tokens,
	})
	if err == nil || service != nil {
		t.Fatalf("NewAuthService() = %#v, %v; want missing invalidator error", service, err)
	}
	for name, invalidator := range map[string]SessionInvalidator{
		"typed nil function": SessionInvalidatorFunc(nil),
		"typed nil pointer":  (*recordingSessionInvalidator)(nil),
	} {
		t.Run(name, func(t *testing.T) {
			service, err := NewAuthService(ServiceOptions{
				Store: &db.Store{}, Passwords: newTestPasswordHasher(t), Tokens: tokens,
				SessionInvalidator: invalidator,
			})
			if err == nil || service != nil {
				t.Fatalf("NewAuthService() = %#v, %v; want typed nil invalidator error", service, err)
			}
		})
	}
	var nilPasswords *passwordManagerStub
	for name, options := range map[string]ServiceOptions{
		"missing store": {
			Passwords: newTestPasswordHasher(t), Tokens: tokens,
			SessionInvalidator: discardSessionInvalidator,
		},
		"typed nil password manager": {
			Store: &db.Store{}, Passwords: nilPasswords, Tokens: tokens,
			SessionInvalidator: discardSessionInvalidator,
		},
	} {
		t.Run(name, func(t *testing.T) {
			service, err := NewAuthService(options)
			if err == nil || service != nil {
				t.Fatalf("NewAuthService() = %#v, %v; want typed nil dependency error", service, err)
			}
		})
	}
}

// TestServiceInvalidatesCommittedSessionsAfterRequestCancellation pins the two
// properties every committed revocation depends on: the invalidator runs on a
// context detached from the (already cancelled) request, and it receives a
// defensive copy that a caller mutating the returned result cannot reach.
func TestServiceInvalidatesCommittedSessionsAfterRequestCancellation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 11, 11, 30, 0, 0, time.UTC)
	revocations := []SessionRevocation{{
		SessionID: uuid.MustParse("0197f590-70bd-78c3-825f-5f6e4df341af"),
		UserID:    uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c"),
		Reason:    SessionRevocationRefreshTokenReuse, EffectiveAt: now,
	}}
	invalidator := &recordingSessionInvalidator{}
	service := storelessService(t, ServiceOptions{SessionInvalidator: invalidator})

	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	service.invalidateCommittedSessions(requestContext, revocations)

	if len(invalidator.calls) != 1 || len(invalidator.calls[0]) != 1 || invalidator.calls[0][0] != revocations[0] {
		t.Fatalf("invalidation calls = %#v, want %#v", invalidator.calls, revocations)
	}
	if invalidator.contextErrors[0] != nil {
		t.Fatalf(
			"invalidation context error = %v, want detached post-commit context",
			invalidator.contextErrors[0],
		)
	}
	revocations[0].Reason = SessionRevocationLogout
	if invalidator.calls[0][0].Reason != SessionRevocationRefreshTokenReuse {
		t.Fatal("invalidator did not receive a defensive revocation copy")
	}

	service.invalidateCommittedSessions(context.Background(), nil)
	if len(invalidator.calls) != 1 {
		t.Fatalf("empty revocation set invoked the invalidator: %#v", invalidator.calls)
	}
}

type recordingSessionInvalidator struct {
	calls         [][]SessionRevocation
	contextErrors []error
}

var discardSessionInvalidator = SessionInvalidatorFunc(func(context.Context, []SessionRevocation) {})

func (r *recordingSessionInvalidator) InvalidateSessions(ctx context.Context, revocations []SessionRevocation) {
	r.calls = append(r.calls, append([]SessionRevocation(nil), revocations...))
	r.contextErrors = append(r.contextErrors, ctx.Err())
}

func TestValidatePasswordCountsUnicodeCharacters(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		password string
		wantErr  bool
	}{
		{name: "seven ASCII characters", password: "a123456", wantErr: true},
		{name: "eight ASCII characters", password: "a1234567"},
		{name: "seven Unicode characters", password: "🔒🔒🔒🔒🔒🔒🔒", wantErr: true},
		{name: "eight Unicode characters", password: "🔒🔒🔒🔒🔒🔒🔒🔒"},
		{name: "128 Unicode characters", password: strings.Repeat("🔒", 128)},
		{name: "129 Unicode characters", password: strings.Repeat("🔒", 129), wantErr: true},
		{name: "invalid UTF-8", password: string([]byte{0xff}), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validatePassword(test.password)
			if test.wantErr && !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("validatePassword() error = %v, want ErrInvalidInput", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("validatePassword() error = %v", err)
			}
		})
	}
}

type passwordManagerStub struct {
	verifyCalls int
}

func (s *passwordManagerStub) Hash(string) (string, error) { return "dummy", nil }

func (s *passwordManagerStub) Verify(string, string) bool {
	s.verifyCalls++
	return false
}

// TestServiceAuthenticateRejectsUnusableTokensBeforeLookup pins that the
// cheap, storage-free rejections happen first: an unknown auth kind, an
// unverifiable access token, and a session ID that is not a valid uuid fail
// without a live-session lookup. The service holds a pool-less store, so any
// lookup would panic instead of returning.
func TestServiceAuthenticateRejectsUnusableTokensBeforeLookup(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 11, 10, 30, 0, 0, time.UTC)
	userID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c")
	sessionID := uuid.MustParse("0197f590-70bd-78c3-825f-5f6e4df341af")
	tokens, err := NewTokenManager(TokenManagerOptions{
		Secret:     []byte("01234567890123456789012345678901"),
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 30 * 24 * time.Hour,
		Now:        func() time.Time { return now },
		Random:     bytes.NewReader(make([]byte, 64)),
	})
	if err != nil {
		t.Fatalf("NewTokenManager() error = %v", err)
	}
	issued, err := tokens.Issue(userID, sessionID)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	service := storelessService(t, ServiceOptions{Tokens: tokens})

	if _, err := service.Authenticate(
		context.Background(),
		issued.AccessToken,
		TokenKind("bogus"),
	); !errors.Is(
		err,
		ErrInvalidSession,
	) {
		t.Fatalf("Authenticate(invalid kind) error = %v, want ErrInvalidSession", err)
	}
	if _, err := service.Authenticate(
		context.Background(),
		"not-an-access-token",
		TokenKindBearer,
	); !errors.Is(
		err,
		ErrInvalidSession,
	) {
		t.Fatalf("Authenticate(unverifiable token) error = %v, want ErrInvalidSession", err)
	}
	for name, badSessionID := range map[string]uuid.UUID{
		"wrong variant": uuid.MustParse("0197f590-70bd-78c3-025f-5f6e4df341af"),
	} {
		accessToken, _, err := tokens.IssueAccess(userID, badSessionID)
		if err != nil {
			t.Fatalf("IssueAccess(%s) error = %v", name, err)
		}
		if _, err := service.Authenticate(
			context.Background(),
			accessToken,
			TokenKindBearer,
		); !errors.Is(
			err,
			ErrInvalidSession,
		) {
			t.Fatalf("Authenticate(%s session) error = %v, want ErrInvalidSession", name, err)
		}
	}
}

// TestServiceLogoutIgnoresUnusableRefreshTokensBeforeLookup pins that a
// refresh token the token manager cannot even parse never reaches storage:
// the service's store owns no pool, so a revoke attempt would panic.
func TestServiceLogoutIgnoresUnusableRefreshTokensBeforeLookup(t *testing.T) {
	t.Parallel()

	service := storelessService(t, ServiceOptions{})
	mutation, err := service.Logout(context.Background(), "not-a-refresh-token")
	if err != nil || mutation.Outcome != SessionMutationNoChange || len(mutation.Revocations) != 0 {
		t.Fatalf("Logout(invalid) = %#v, %v", mutation, err)
	}
}
