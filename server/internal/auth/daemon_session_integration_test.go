//go:build integration

package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// D31 (000098): a person's session starts the daemon's own session for the
// same person. It authenticates as a daemon session, keeps that through every
// refresh, stays signed in when the person's session ends, and cannot start
// another. A person's refresh stays a person's.
func TestDaemonSessionKeepsItsClientAndCannotStartAnother(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tokens, err := NewTokenManager(TokenManagerOptions{
		Secret:     []byte("01234567890123456789012345678901"),
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 7 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewTokenManager() error = %v", err)
	}
	personRefresh, err := tokens.IssueRefresh()
	if err != nil {
		t.Fatalf("IssueRefresh() error = %v", err)
	}
	user, personSession, err := createUserWithSession(ctx, store, NewUser{
		Email: "daemon-session@boardly.test", PasswordHash: "stored", DisplayName: "Mia",
	}, NewSession{
		TokenPrefix: personRefresh.Prefix, TokenHash: personRefresh.Hash,
		ExpiresAt: personRefresh.ExpiresAt, ClientMetadata: []byte(`{"client":"desktop"}`),
	})
	if err != nil {
		t.Fatalf("CreateUserWithSession() error = %v", err)
	}
	service := &DefaultAuthService{
		store: store, bus: authTestBus(t), tokens: tokens, invalidator: discardSessionInvalidator,
	}
	authenticate := func(accessToken string) UserPrincipal {
		t.Helper()
		principal, err := service.Authenticate(ctx, accessToken, TokenKindBearer)
		if err != nil {
			t.Fatalf("Authenticate() error = %v", err)
		}
		return principal
	}
	storedClient := func(sessionID uuid.UUID) string {
		t.Helper()
		var client string
		if err := pool.QueryRow(ctx, `SELECT client FROM auth_sessions WHERE id = $1`, sessionID).
			Scan(&client); err != nil {
			t.Fatalf("read session client: %v", err)
		}
		return client
	}
	countSessions := func() int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE user_id = $1`, user.ID).
			Scan(&count); err != nil {
			t.Fatalf("count sessions: %v", err)
		}
		return count
	}

	personAccess, _, err := tokens.issueAccessAt(user.ID, personSession, time.Now().UTC())
	if err != nil {
		t.Fatalf("issue the person's access token: %v", err)
	}
	person := authenticate(personAccess)
	if person.DaemonSession() || person.Client != SessionClientPerson || storedClient(personSession) != "person" {
		t.Fatalf(
			"the person's session reads %q (stored %q); want person",
			person.Client,
			storedClient(personSession),
		)
	}

	minted, err := service.MintDaemonSession(ctx, person)
	if err != nil {
		t.Fatalf("MintDaemonSession() error = %v", err)
	}
	if minted.User.ID != user.ID || minted.Tokens.SessionID == personSession ||
		minted.Tokens.RefreshToken == "" || storedClient(minted.Tokens.SessionID) != "daemon" {
		t.Fatalf("minted %+v; want a new daemon session for %s", minted.Tokens, user.ID)
	}
	var rotatedFrom uuid.NullUUID
	if err := pool.QueryRow(ctx, `SELECT rotated_from_session_id FROM auth_sessions WHERE id = $1`,
		minted.Tokens.SessionID).Scan(&rotatedFrom); err != nil || rotatedFrom.Valid {
		t.Fatalf("the daemon session rotates from %v (%v); want a session of its own", rotatedFrom, err)
	}
	daemon := authenticate(minted.Tokens.AccessToken)
	if !daemon.DaemonSession() || daemon.UserID != user.ID || daemon.SessionID != minted.Tokens.SessionID {
		t.Fatalf(
			"the daemon's access token authenticates as %+v; want the daemon session of %s",
			daemon,
			user.ID,
		)
	}

	before := countSessions()
	if _, err := service.MintDaemonSession(ctx, daemon); !errors.Is(err, ErrDaemonSessionCannotMint) {
		t.Fatalf("MintDaemonSession(daemon) error = %v, want ErrDaemonSessionCannotMint", err)
	}
	if after := countSessions(); after != before {
		t.Fatalf("a refused mint stored a session: %d sessions, want %d", after, before)
	}

	rotated, err := service.Refresh(
		ctx,
		RefreshInput{RefreshToken: minted.Tokens.RefreshToken, Client: ClientDesktop},
	)
	if err != nil || rotated.Auth == nil {
		t.Fatalf("Refresh(daemon) = %+v, %v", rotated, err)
	}
	if storedClient(rotated.Auth.Tokens.SessionID) != "daemon" ||
		!authenticate(rotated.Auth.Tokens.AccessToken).DaemonSession() {
		t.Fatal("a refreshed daemon session became a person's")
	}
	personRotated, err := service.Refresh(
		ctx,
		RefreshInput{RefreshToken: personRefresh.Token, Client: ClientDesktop},
	)
	if err != nil || personRotated.Auth == nil {
		t.Fatalf("Refresh(person) = %+v, %v", personRotated, err)
	}
	if storedClient(personRotated.Auth.Tokens.SessionID) != "person" ||
		authenticate(personRotated.Auth.Tokens.AccessToken).DaemonSession() {
		t.Fatal("a refreshed person's session became a daemon's")
	}

	if _, err := service.Logout(ctx, personRotated.Auth.Tokens.RefreshToken); err != nil {
		t.Fatalf("Logout(person) error = %v", err)
	}
	if !authenticate(rotated.Auth.Tokens.AccessToken).DaemonSession() {
		t.Fatal("the daemon session ended with the person's")
	}

	if _, err := pool.Exec(ctx, `UPDATE users SET disabled_at = now() WHERE id = $1`, user.ID); err != nil {
		t.Fatalf("disable the account: %v", err)
	}
	if _, err := service.MintDaemonSession(ctx, person); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("MintDaemonSession(disabled account) error = %v, want ErrInvalidSession", err)
	}
}
