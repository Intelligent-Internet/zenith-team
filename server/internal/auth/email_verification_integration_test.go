//go:build integration

package auth

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestEmailVerificationConsumeCreatesExactlyOneUserSession(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Microsecond)
	codeHash := bytes.Repeat([]byte{7}, 32)
	started, err := startEmailVerification(ctx, store.Queries(), NewEmailVerification{
		Email: "verify-once@boardly.test", PasswordHash: "stored-password-hash",
		DisplayName: "Verify Once", Client: ClientWeb, ClientMetadata: []byte(`{"platform":"web"}`),
		CodeHash: codeHash, ExpiresAt: now.Add(10 * time.Minute),
		ResendAvailableAt: now.Add(time.Minute), Now: now,
	})
	if err != nil {
		t.Fatalf("StartEmailVerification() error = %v", err)
	}
	if !started.CodeChanged || started.Flow.ID == uuid.Nil || started.Flow.Generation != 1 {
		t.Fatalf("StartEmailVerification() = %#v", started)
	}
	flowID := started.Flow.ID
	var usersBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE lower(email) = lower($1)`, started.Flow.Email).
		Scan(&usersBefore); err != nil ||
		usersBefore != 0 {
		t.Fatalf("users before verification = %d, error = %v", usersBefore, err)
	}

	user, sessionID, err := consumeEmailVerification(ctx, store, EmailVerificationMatch{
		ID: flowID, Email: started.Flow.Email, Client: ClientWeb, Generation: 1,
		CodeHash: codeHash, Now: now.Add(time.Second),
	}, NewUser{}, NewSession{
		TokenPrefix: "brf_verified",
		TokenHash:   bytes.Repeat([]byte{9}, 32), ExpiresAt: now.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("ConsumeEmailVerification() error = %v", err)
	}
	if user.ID == uuid.Nil || user.Email != started.Flow.Email || user.DisplayName != "Verify Once" {
		t.Fatalf("created user = %#v", user)
	}
	if user.EmailVerifiedAt == nil {
		t.Fatal("new password user was not marked email-verified")
	}
	if _, err := activeSessionUser(ctx, store, sessionID, user.ID); err != nil {
		t.Fatalf("GetActiveSessionUser() error = %v", err)
	}
	if _, err := getEmailVerification(ctx, store.Queries(), EmailVerificationLookup{
		ID: flowID, Email: started.Flow.Email, Client: ClientWeb,
	}); !errors.Is(err, ErrVerificationNotFound) {
		t.Fatalf("consumed verification lookup error = %v", err)
	}
	if _, _, err := consumeEmailVerification(ctx, store, EmailVerificationMatch{
		ID: flowID, Email: started.Flow.Email, Client: ClientWeb, Generation: 1,
		CodeHash: codeHash, Now: now.Add(2 * time.Second),
	}, NewUser{}, NewSession{
		TokenPrefix: "brf_replay",
		TokenHash:   bytes.Repeat([]byte{10}, 32), ExpiresAt: now.Add(24 * time.Hour),
	}); !errors.Is(err, ErrVerificationCodeInvalid) {
		t.Fatalf("replayed ConsumeEmailVerification() error = %v", err)
	}
}

func TestEmailVerificationMarksLegacyUserWithoutCreatingADuplicate(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, display_name)
		VALUES ($1, 'legacy-verify@boardly.test', 'stored-password-hash', 'Legacy Verify')
	`, userID); err != nil {
		t.Fatalf("insert legacy user: %v", err)
	}
	codeHash := bytes.Repeat([]byte{11}, 32)
	started, err := startEmailVerification(ctx, store.Queries(), NewEmailVerification{
		UserID: &userID, Email: "legacy-verify@boardly.test",
		PasswordHash: "stored-password-hash", DisplayName: "Legacy Verify", Client: ClientWeb,
		ClientMetadata: []byte(`{"platform":"web"}`), CodeHash: codeHash,
		ExpiresAt: now.Add(10 * time.Minute), ResendAvailableAt: now.Add(time.Minute), Now: now,
	})
	if err != nil {
		t.Fatalf("StartEmailVerification() error = %v", err)
	}
	verified, _, err := consumeEmailVerification(ctx, store, EmailVerificationMatch{
		ID: started.Flow.ID, Email: started.Flow.Email, Client: ClientWeb,
		Generation: started.Flow.Generation, CodeHash: codeHash, Now: now.Add(time.Second),
	}, NewUser{}, NewSession{
		TokenPrefix: "brf_legacy_verified",
		TokenHash:   bytes.Repeat([]byte{12}, 32), ExpiresAt: now.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("ConsumeEmailVerification() error = %v", err)
	}
	if verified.ID != userID || verified.EmailVerifiedAt == nil {
		t.Fatalf("verified legacy user = %#v", verified)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE lower(email) = 'legacy-verify@boardly.test'`).
		Scan(&count); err != nil ||
		count != 1 {
		t.Fatalf("legacy user count = %d, error = %v", count, err)
	}
}
