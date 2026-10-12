//go:build integration

package auth

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDesktopHandoffCodeCreateThenConsumeReturnsRowAndIsSingleUse(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	userID := createDesktopHandoffTestUser(t, ctx, pool, "handoff-single-use@boardly.test")
	now := time.Now().UTC().Truncate(time.Microsecond)
	codeHash := bytes.Repeat([]byte{31}, 32)

	if err := createDesktopHandoffCode(ctx, store, NewDesktopHandoffCode{
		UserID: userID, CodeHash: codeHash, ExpiresAt: now.Add(60 * time.Second),
	}); err != nil {
		t.Fatalf("CreateDesktopHandoffCode() error = %v", err)
	}

	consumed, err := store.Queries().ConsumeDesktopHandoffCode(ctx, codeHash)
	if err != nil {
		t.Fatalf("ConsumeDesktopHandoffCode() error = %v", err)
	}
	if consumed.ID == uuid.Nil || consumed.UserID != userID ||
		!bytes.Equal(consumed.CodeHash, codeHash) {
		t.Fatalf("ConsumeDesktopHandoffCode() = %#v", consumed)
	}
	if !consumed.ExpiresAt.Time.Equal(now.Add(60 * time.Second)) {
		t.Fatalf(
			"ConsumeDesktopHandoffCode() ExpiresAt = %v, want %v",
			consumed.ExpiresAt,
			now.Add(60*time.Second),
		)
	}

	if _, err := store.Queries().ConsumeDesktopHandoffCode(ctx, codeHash); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("replayed ConsumeDesktopHandoffCode() error = %v, want no rows", err)
	}
}

func TestDesktopHandoffCodeConsumeIgnoresExpiryAtStorageLayer(t *testing.T) {
	// The delete-returning query is unconditional, with no expires_at
	// predicate, so DesktopHandoffService.Complete can distinguish an expired
	// code from an invalid one. This test pins that deliberate behavior: a code
	// whose expiry has already passed still consumes successfully here.
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	userID := createDesktopHandoffTestUser(t, ctx, pool, "handoff-expired@boardly.test")
	now := time.Now().UTC().Truncate(time.Microsecond)
	past := now.Add(-time.Hour)
	codeHash := bytes.Repeat([]byte{32}, 32)

	if err := createDesktopHandoffCode(ctx, store, NewDesktopHandoffCode{
		UserID: userID, CodeHash: codeHash, ExpiresAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("CreateDesktopHandoffCode() error = %v", err)
	}
	var codeID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM desktop_handoff_codes WHERE code_hash = $1`, codeHash).
		Scan(&codeID); err != nil {
		t.Fatalf("load database-generated handoff code ID: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE desktop_handoff_codes
		SET created_at = $2::timestamptz - interval '1 minute',
		    expires_at = $2::timestamptz
		WHERE id = $1`, codeID, past); err != nil {
		t.Fatalf("age desktop handoff code: %v", err)
	}

	consumed, err := store.Queries().ConsumeDesktopHandoffCode(ctx, codeHash)
	if err != nil {
		t.Fatalf(
			"ConsumeDesktopHandoffCode(expired) error = %v, want success (expiry is a service-layer concern)",
			err,
		)
	}
	if consumed.ID != codeID || !consumed.ExpiresAt.Time.Equal(past) {
		t.Fatalf("ConsumeDesktopHandoffCode(expired) = %#v", consumed)
	}
}

func TestDesktopHandoffCodeConcurrentConsumeHasExactlyOneWinner(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	userID := createDesktopHandoffTestUser(t, ctx, pool, "handoff-race@boardly.test")
	codeHash := bytes.Repeat([]byte{41}, 32)
	if err := createDesktopHandoffCode(ctx, store, NewDesktopHandoffCode{
		UserID: userID, CodeHash: codeHash,
		ExpiresAt: time.Now().UTC().Add(60 * time.Second),
	}); err != nil {
		t.Fatalf("CreateDesktopHandoffCode() error = %v", err)
	}

	const attempts = 8
	start := make(chan struct{})
	results := make(chan error, attempts)
	for range attempts {
		go func() {
			<-start
			_, err := store.Queries().ConsumeDesktopHandoffCode(ctx, codeHash)
			results <- err
		}()
	}
	close(start)
	var successes, invalids int
	for range attempts {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, pgx.ErrNoRows):
			invalids++
		default:
			t.Errorf("ConsumeDesktopHandoffCode() race error = %v", err)
		}
	}
	if successes != 1 || invalids != attempts-1 {
		t.Fatalf(
			"concurrent consume outcomes = success:%d invalid:%d, want 1/%d",
			successes,
			invalids,
			attempts-1,
		)
	}
}

func TestDesktopHandoffCodeCreateReplacesPriorCodeForUser(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	userID := createDesktopHandoffTestUser(t, ctx, pool, "handoff-replace@boardly.test")
	now := time.Now().UTC().Truncate(time.Microsecond)
	firstHash := bytes.Repeat([]byte{51}, 32)
	secondHash := bytes.Repeat([]byte{52}, 32)
	if err := createDesktopHandoffCode(ctx, store, NewDesktopHandoffCode{
		UserID: userID, CodeHash: firstHash, ExpiresAt: now.Add(60 * time.Second),
	}); err != nil {
		t.Fatalf("CreateDesktopHandoffCode(first) error = %v", err)
	}
	if err := createDesktopHandoffCode(ctx, store, NewDesktopHandoffCode{
		UserID: userID, CodeHash: secondHash, ExpiresAt: now.Add(60 * time.Second),
	}); err != nil {
		t.Fatalf("CreateDesktopHandoffCode(second) error = %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM desktop_handoff_codes WHERE user_id = $1`, userID).
		Scan(&count); err != nil ||
		count != 1 {
		t.Fatalf("desktop handoff codes for user = %d, error = %v", count, err)
	}
	if _, err := store.Queries().ConsumeDesktopHandoffCode(ctx, firstHash); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("consume replaced code error = %v, want no rows", err)
	}
	consumed, err := store.Queries().ConsumeDesktopHandoffCode(ctx, secondHash)
	if err != nil || consumed.ID == uuid.Nil {
		t.Fatalf("consume replacement code = %#v, %v", consumed, err)
	}
}

func TestDesktopHandoffCodeCascadesWithRawUserDelete(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	userID := createDesktopHandoffTestUser(t, ctx, pool, "handoff-cascade@boardly.test")
	codeHash := bytes.Repeat([]byte{61}, 32)
	if err := createDesktopHandoffCode(ctx, store, NewDesktopHandoffCode{
		UserID: userID, CodeHash: codeHash,
		ExpiresAt: time.Now().UTC().Add(60 * time.Second),
	}); err != nil {
		t.Fatalf("CreateDesktopHandoffCode() error = %v", err)
	}

	// The schema declares user_id REFERENCES users ON DELETE
	// CASCADE, so a raw parent delete removes the handoff code with it.
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID); err != nil {
		t.Fatalf("delete user owning handoff code: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM desktop_handoff_codes WHERE user_id = $1`, userID).
		Scan(&count); err != nil ||
		count != 0 {
		t.Fatalf(
			"desktop handoff codes after raw user delete = %d, error = %v, want the cascade to remove the row",
			count,
			err,
		)
	}
}

func createDesktopHandoffTestUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, email string) uuid.UUID {
	t.Helper()
	userID := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, display_name)
		VALUES ($1, $2, 'stored-password-hash', 'Handoff Test')
	`, userID, email); err != nil {
		t.Fatalf("insert desktop handoff test user: %v", err)
	}
	return userID
}
