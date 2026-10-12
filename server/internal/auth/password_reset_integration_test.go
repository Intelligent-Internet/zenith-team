//go:build integration

package auth

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/events"
)

func TestPasswordResetIsSingleUseAndRevokesLiveSessions(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bus, eventLog := recordedAuthBus(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	user, sessionID, err := createUserWithSession(ctx, store, NewUser{
		Email:        "password-reset@boardly.test",
		PasswordHash: "old-password-hash", DisplayName: "Password Reset",
	}, NewSession{
		TokenPrefix: "brf_password_reset",
		TokenHash:   bytes.Repeat([]byte{3}, 32), ExpiresAt: now.Add(24 * time.Hour),
		ClientMetadata: []byte(`{"platform":"web"}`),
	})
	if err != nil {
		t.Fatalf("CreateUserWithSession() error = %v", err)
	}
	userID := user.ID
	tokenHash := bytes.Repeat([]byte{7}, 32)
	started, err := startPasswordReset(ctx, store.Queries(), NewPasswordReset{
		UserID: userID, Email: "password-reset@boardly.test", DisplayName: "Password Reset",
		TokenHash: tokenHash, ExpiresAt: now.Add(30 * time.Minute),
		ResendAvailableAt: now.Add(time.Minute), Now: now,
	})
	if err != nil || !started.TokenChanged || started.Flow.Generation != 1 {
		t.Fatalf("StartPasswordReset() = %#v, %v", started, err)
	}

	consumed, err := consumePasswordReset(ctx, store, bus, PasswordResetConsumption{
		TokenHash: tokenHash, PasswordHash: "new-password-hash", Now: now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("ConsumePasswordReset() error = %v", err)
	}
	if consumed.UserID != userID || len(consumed.Revocations) != 1 ||
		consumed.Revocations[0].SessionID != sessionID ||
		consumed.Revocations[0].Reason != SessionRevocationPasswordReset {
		t.Fatalf("ConsumePasswordReset() = %#v", consumed)
	}
	var passwordHash string
	var verifiedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT password_hash, email_verified_at FROM users WHERE id = $1`, userID).
		Scan(&passwordHash, &verifiedAt); err != nil {
		t.Fatalf("read reset user: %v", err)
	}
	if passwordHash != "new-password-hash" || verifiedAt == nil {
		t.Fatalf("reset user = password:%q verified:%v", passwordHash, verifiedAt)
	}
	event := requireAuthRevocationEvent(t, eventLog, sessionID)
	if event.Actor != events.SystemActor() ||
		event.Payload.Reason != SessionRevocationPasswordReset {
		t.Fatalf("password reset revocation event = %#v", event)
	}
	if _, err := consumePasswordReset(ctx, store, bus, PasswordResetConsumption{
		TokenHash: tokenHash, PasswordHash: "replayed-password-hash", Now: now.Add(2 * time.Second),
	}); !errors.Is(err, ErrPasswordResetInvalid) {
		t.Fatalf("replayed ConsumePasswordReset() error = %v", err)
	}
}
