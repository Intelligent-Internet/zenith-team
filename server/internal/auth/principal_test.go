package auth

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestUserPrincipalRoundTripsThroughContext(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]UserPrincipal{
		"user scoped": {
			UserID:    uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c"),
			SessionID: uuid.MustParse("0197f590-70bd-78c3-825f-5f6e4df341af"),
			AuthKind:  TokenKindSession,
		},
		"company scoped": {
			UserID:    uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c"),
			SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
			AuthKind:  TokenKindBearer,
			CompanyID: uuid.MustParse("0197f590-70bd-78c3-825f-5f6e4df341af"),
			Role:      CompanyRoleOwner,
		},
	} {
		got, ok := UserPrincipalFromContext(WithUserPrincipal(context.Background(), want))
		if !ok {
			t.Fatalf("%s UserPrincipalFromContext() ok = false", name)
		}
		if got != want {
			t.Errorf("%s UserPrincipalFromContext() = %#v, want %#v", name, got, want)
		}
	}
}

func TestPrincipalContextsRejectMissingOrIncompleteValues(t *testing.T) {
	t.Parallel()

	if _, ok := UserPrincipalFromContext(context.Background()); ok {
		t.Fatal("empty context unexpectedly has a user principal")
	}
	userID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c")
	sessionID := uuid.MustParse("0197f590-70bd-78c3-825f-5f6e4df341af")
	companyID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19")
	for name, principal := range map[string]UserPrincipal{
		"missing user":    {SessionID: sessionID, AuthKind: TokenKindSession},
		"missing session": {UserID: userID, AuthKind: TokenKindSession},
		"invalid kind":    {UserID: userID, SessionID: sessionID, AuthKind: TokenKind("bogus")},
		"company scope without session": {
			UserID:    userID,
			AuthKind:  TokenKindSession,
			CompanyID: companyID,
			Role:      CompanyRoleOwner,
		},
	} {
		ctx := WithUserPrincipal(context.Background(), principal)
		if _, ok := UserPrincipalFromContext(ctx); ok {
			t.Errorf("%s UserPrincipal unexpectedly accepted", name)
		}
	}
	ctx := WithUserPrincipal(context.Background(), UserPrincipal{UserID: userID, SessionID: sessionID})
	if _, ok := UserPrincipalFromContext(ctx); ok {
		t.Fatal("user principal without auth kind unexpectedly accepted")
	}
}
