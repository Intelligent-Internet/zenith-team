//go:build integration

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestAuthRepositoryLoginSessionCreationLosesToAccountDeletion(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	user, _, err := createUserWithSession(ctx, store, NewUser{
		Email:        "login-delete-race@zenith-team.test",
		PasswordHash: "stored", DisplayName: "Login Race",
	}, NewSession{
		TokenPrefix: "root",
		TokenHash:   bytes.Repeat([]byte{210}, 32), ExpiresAt: time.Now().UTC().Add(time.Hour),
		ClientMetadata: []byte(`{"client":"web"}`),
	})
	if err != nil {
		t.Fatalf("CreateUserWithSession() error = %v", err)
	}
	userID := user.ID
	if _, err := findUserByEmail(ctx, store.Queries(), "login-delete-race@zenith-team.test"); err != nil {
		t.Fatalf("FindUserByEmail(pre-race) error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		WITH deletion_clock AS (SELECT clock_timestamp() AS now_at)
		UPDATE users
		SET deleted_at=deletion_clock.now_at, disabled_at=deletion_clock.now_at
		FROM deletion_clock
		WHERE users.id=$1`, userID); err != nil {
		t.Fatalf("soft-delete User between password read and session creation: %v", err)
	}
	if _, err := createSession(ctx, store.Queries(), NewSession{
		UserID: userID, TokenPrefix: "late-login",
		TokenHash: bytes.Repeat([]byte{211}, 32), ExpiresAt: time.Now().UTC().Add(time.Hour),
		ClientMetadata: []byte(`{"client":"web"}`),
	}); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("CreateSession(after delete) error = %v, want ErrAccountDisabled", err)
	}
	var sessions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE user_id=$1`, userID).
		Scan(&sessions); err != nil {
		t.Fatalf("count auth sessions: %v", err)
	}
	if sessions != 1 {
		t.Fatalf("auth sessions after lost login race = %d, want original root only", sessions)
	}
}

func TestAuthRepositoryLoadsUserAuthenticationCapabilitiesInOneProjection(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := createAuthTestSession(
		t,
		ctx,
		store,
		"projected-user@zenith-team.test",
		212,
		`{"client":"web"}`,
	)
	verifiedAt := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(
		ctx,
		`UPDATE users SET email_verified_at = $2 WHERE id = $1`,
		fixture.UserID,
		verifiedAt,
	); err != nil {
		t.Fatalf("mark projected User verified: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO oauth_accounts (id, user_id, provider, subject, email, profile)
		 VALUES ($1, $2, 'google', 'projected-user', 'projected-user@zenith-team.test', '{}')`,
		uuid.Must(uuid.NewV7()),
		fixture.UserID,
	); err != nil {
		t.Fatalf("link projected User provider: %v", err)
	}

	user, err := getUserByID(ctx, store.Queries(), fixture.UserID)
	if err != nil {
		t.Fatalf("getUserByID() error = %v", err)
	}
	if user.PasswordHash == "" || user.EmailVerifiedAt == nil ||
		!user.EmailVerifiedAt.Equal(verifiedAt) ||
		!reflect.DeepEqual(user.LinkedProviders, []string{"google"}) {
		t.Fatalf("getUserByID() = %#v, want complete authentication capabilities", user)
	}
}

func TestAuthRepositoryRotatesOnceAndRevokesAReusedSessionChain(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bus, eventLog := recordedAuthBus(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	user, oldSessionID, err := createUserWithSession(ctx, store, NewUser{
		Email:        "founder@zenith-team.test",
		PasswordHash: "$2a$04$abcdefghijklmnopqrstuuZ8rP7QGqR1M6CqVvOBqXxI9dV1O",
		DisplayName:  "Founder",
	}, NewSession{
		TokenPrefix:    "brf_oldtoken",
		TokenHash:      bytes.Repeat([]byte{1}, 32),
		ExpiresAt:      now.Add(24 * time.Hour),
		ClientMetadata: []byte(`{"client":"web"}`),
	})
	if err != nil {
		t.Fatalf("CreateUserWithSession() error = %v", err)
	}
	userID := user.ID
	if user.ID == uuid.Nil || user.Email != "founder@zenith-team.test" || user.Version != 1 {
		t.Fatalf("created user = %#v", user)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET version = 7 WHERE id = $1`, userID); err != nil {
		t.Fatalf("set User version: %v", err)
	}
	for operation, load := range map[string]func() (User, error){
		"FindUserByEmail": func() (User, error) {
			return findUserByEmail(ctx, store.Queries(), "founder@zenith-team.test")
		},
		"GetUserByID": func() (User, error) {
			return getUserByID(ctx, store.Queries(), userID)
		},
		"GetActiveSessionUser": func() (User, error) {
			return activeSessionUser(ctx, store, oldSessionID, userID)
		},
	} {
		loaded, err := load()
		if err != nil || loaded.Version != 7 {
			t.Fatalf("%s() = %#v, %v; want version 7", operation, loaded, err)
		}
	}

	rotated, err := rotateSession(ctx, store, bus, bytes.Repeat([]byte{1}, 32), NewSession{
		TokenPrefix:    "brf_newtoken",
		TokenHash:      bytes.Repeat([]byte{2}, 32),
		ExpiresAt:      now.Add(48 * time.Hour),
		ClientMetadata: []byte(`{"client":"web"}`),
	})
	if err != nil {
		t.Fatalf("RotateSession(first) error = %v", err)
	}
	if rotated.User.ID != userID || rotated.User.Version != 7 ||
		rotated.Mutation.Outcome != SessionMutationRotated ||
		len(rotated.Mutation.Revocations) != 1 {
		t.Fatalf("RotateSession(first) result = %#v", rotated)
	}
	newSessionID := rotated.SessionID
	rotationRevocation := rotated.Mutation.Revocations[0]
	if rotationRevocation.SessionID != oldSessionID || rotationRevocation.UserID != userID ||
		rotationRevocation.Reason != SessionRevocationRefreshRotated {
		t.Fatalf("rotation revocation = %#v", rotationRevocation)
	}
	var rootRevokedAt *time.Time
	var rootReason pgtype.Text
	var rootRevoker uuid.UUID
	var rootLastUsedAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT revoked_at, revocation_reason, revoked_by_user_id, last_used_at
		FROM auth_sessions WHERE id = $1`, oldSessionID).Scan(&rootRevokedAt, &rootReason, &rootRevoker, &rootLastUsedAt); err != nil {
		t.Fatalf("read rotated root: %v", err)
	}
	if rootRevokedAt == nil || !rootRevokedAt.Equal(rotationRevocation.EffectiveAt) ||
		rootReason.String != string(SessionRevocationRefreshRotated) ||
		(rootRevoker == uuid.Nil) ||
		rootRevoker != userID ||
		rootLastUsedAt != nil {
		t.Fatalf(
			"rotated root metadata = revoked:%v reason:%#v revoker:%#v last-used:%v",
			rootRevokedAt,
			rootReason,
			rootRevoker,
			rootLastUsedAt,
		)
	}
	rotationEvent := requireAuthRevocationEvent(t, eventLog, oldSessionID)
	if rotationEvent.Payload.UserID != userID ||
		rotationEvent.Actor != events.UserActor(userID.String()) ||
		rotationEvent.Payload.Reason != SessionRevocationRefreshRotated {
		t.Fatalf("rotation event = %#v", rotationEvent)
	}
	if _, err := activeSessionUser(ctx, store, newSessionID, userID); err != nil {
		t.Fatalf("GetActiveSessionUser(new) error = %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE auth_sessions
			 SET created_at = created_at - INTERVAL '10 seconds',
			     revoked_at = clock_timestamp() - INTERVAL '6 seconds'
			 WHERE id = $1`,
		oldSessionID,
	); err != nil {
		t.Fatalf("age root rotation using PostgreSQL clock: %v", err)
	}

	replayed, err := rotateSession(ctx, store, bus, bytes.Repeat([]byte{1}, 32), NewSession{
		TokenPrefix:    "brf_attacker",
		TokenHash:      bytes.Repeat([]byte{3}, 32),
		ExpiresAt:      now.Add(48 * time.Hour),
		ClientMetadata: []byte(`{"client":"web"}`),
	})
	if err != nil || replayed.Mutation.Outcome != SessionMutationTokenReuse ||
		len(replayed.Mutation.Revocations) != 1 {
		t.Fatalf("RotateSession(reuse) = %#v, %v; want token reuse", replayed, err)
	}
	reuseRevocation := replayed.Mutation.Revocations[0]
	if reuseRevocation.SessionID != newSessionID || reuseRevocation.Reason != SessionRevocationRefreshTokenReuse {
		t.Fatalf("reuse revocation = %#v", reuseRevocation)
	}
	reuseEvent := requireAuthRevocationEvent(t, eventLog, newSessionID)
	if reuseEvent.Payload.UserID != userID ||
		reuseEvent.Actor != events.SystemActor() ||
		reuseEvent.Payload.Reason != SessionRevocationRefreshTokenReuse {
		t.Fatalf("reuse event = %#v", reuseEvent)
	}
	if _, err := activeSessionUser(ctx, store, newSessionID, userID); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("GetActiveSessionUser(revoked descendant) error = %v, want ErrInvalidSession", err)
	}
	replayedAgain, err := rotateSession(ctx, store, bus, bytes.Repeat([]byte{1}, 32), NewSession{
		TokenPrefix: "brf_attacker-repeat",
		TokenHash:   bytes.Repeat([]byte{4}, 32), ExpiresAt: now.Add(48 * time.Hour),
		ClientMetadata: []byte(`{"client":"web"}`),
	})
	if err != nil || replayedAgain.Mutation.Outcome != SessionMutationTokenReuse ||
		len(replayedAgain.Mutation.Revocations) != 0 {
		t.Fatalf("RotateSession(repeated reuse) = %#v, %v", replayedAgain, err)
	}
	if got := countAuthRevocationEvents(t, eventLog, userID); got != 2 {
		t.Fatalf("repeated reuse event count = %d, want 2", got)
	}
	var replayChildCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE token_hash = $1`, bytes.Repeat([]byte{3}, 32)).
		Scan(&replayChildCount); err != nil ||
		replayChildCount != 0 {
		t.Fatalf("replay replacement count = %d, error = %v", replayChildCount, err)
	}
}

func TestAuthServiceRecoversALostRefreshResponseWithTheSameRotationID(t *testing.T) {
	_, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC()
	tokens, err := NewTokenManager(TokenManagerOptions{
		Secret:     []byte("01234567890123456789012345678901"),
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 7 * 24 * time.Hour,
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewTokenManager() error = %v", err)
	}
	rootRefresh, err := tokens.IssueRefresh()
	if err != nil {
		t.Fatalf("IssueRefresh() error = %v", err)
	}
	if _, _, err := createUserWithSession(ctx, store, NewUser{
		Email: "lost-refresh-response@zenith-team.test", PasswordHash: "stored",
		DisplayName: "Lost Refresh Response",
	}, NewSession{
		TokenPrefix: rootRefresh.Prefix, TokenHash: rootRefresh.Hash,
		ExpiresAt: rootRefresh.ExpiresAt, ClientMetadata: []byte(`{"client":"desktop"}`),
	}); err != nil {
		t.Fatalf("CreateUserWithSession() error = %v", err)
	}
	service := &DefaultAuthService{
		store: store, bus: authTestBus(t), tokens: tokens,
		invalidator: discardSessionInvalidator,
	}
	rotationID := uuid.MustParse("0199a143-7ac0-7d4f-b93e-4be838f5772e")
	input := RefreshInput{
		RefreshToken: rootRefresh.Token,
		Client:       ClientDesktop,
		RotationID:   &rotationID,
	}

	first, err := service.Refresh(ctx, input)
	if err != nil {
		t.Fatalf("Refresh(first) error = %v", err)
	}
	retried, err := service.Refresh(ctx, input)
	if err != nil {
		t.Fatalf("Refresh(retry) error = %v", err)
	}
	if first.Auth == nil || retried.Auth == nil {
		t.Fatalf("Refresh() results = %#v / %#v, want two sessions", first, retried)
	}
	if retried.Mutation.Outcome != SessionMutationRecovered ||
		retried.Auth.Tokens.SessionID != first.Auth.Tokens.SessionID ||
		retried.Auth.Tokens.RefreshToken != first.Auth.Tokens.RefreshToken {
		t.Fatalf("Refresh(retry) = %#v, want recovered session %#v", retried, first)
	}
}

func TestAuthRepositoryTreatsSimultaneousSameClientRotationAsAlreadyCompleted(t *testing.T) {
	_, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bus, eventLog := recordedAuthBus(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	rootHash := bytes.Repeat([]byte{11}, 32)
	metadata := []byte(`{"client":"web","details":{"installationId":"browser-a"}}`)
	user, _, err := createUserWithSession(ctx, store, NewUser{
		Email:        "race@zenith-team.test",
		PasswordHash: "$argon2id$v=19$m=64,t=1,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		DisplayName:  "Race",
	}, NewSession{
		TokenPrefix:    "brf_race-root",
		TokenHash:      rootHash,
		ExpiresAt:      now.Add(24 * time.Hour),
		ClientMetadata: metadata,
	})
	if err != nil {
		t.Fatalf("CreateUserWithSession() error = %v", err)
	}
	userID := user.ID

	type rotationResult struct {
		sessionID uuid.UUID
		outcome   SessionMutationOutcome
		err       error
	}
	start := make(chan struct{})
	results := make(chan rotationResult, 2)
	for index := range 2 {
		go func() {
			<-start
			result, err := rotateSession(ctx, store, bus, rootHash, NewSession{
				TokenPrefix:    "brf_race-child",
				TokenHash:      bytes.Repeat([]byte{byte(21 + index)}, 32),
				ExpiresAt:      now.Add(48 * time.Hour),
				ClientMetadata: metadata,
			})
			results <- rotationResult{sessionID: result.SessionID, outcome: result.Mutation.Outcome, err: err}
		}()
	}
	close(start)
	first := <-results
	second := <-results
	close(results)
	all := []rotationResult{first, second}
	var winner uuid.UUID
	var successes, alreadyRotated int
	for _, result := range all {
		switch {
		case result.err == nil && result.outcome == SessionMutationRotated:
			successes++
			winner = result.sessionID
		case result.err == nil && result.outcome == SessionMutationAlreadyRotated:
			alreadyRotated++
		default:
			t.Errorf("RotateSession() race error = %v", result.err)
		}
	}
	if successes != 1 || alreadyRotated != 1 {
		t.Fatalf("race outcomes = success:%d already-rotated:%d; results = %#v", successes, alreadyRotated, all)
	}
	if _, err := activeSessionUser(ctx, store, winner, userID); err != nil {
		t.Fatalf("winning child was revoked by race loser: %v", err)
	}
	if eventCount := countAuthRevocationEvents(t, eventLog, userID); eventCount != 1 {
		t.Fatalf("concurrent rotation events = %d, want 1", eventCount)
	}
}

func TestAuthSessionRevocationAcceptsExtensibleReasonsAndCascadesWithRawUserDelete(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	user, sessionID, err := createUserWithSession(ctx, store, NewUser{
		Email: "revocation-schema@zenith-team.test", PasswordHash: "stored", DisplayName: "Schema",
	}, NewSession{
		TokenPrefix: "brf_schema",
		TokenHash:   bytes.Repeat([]byte{91}, 32), ExpiresAt: time.Now().UTC().Add(time.Hour),
		ClientMetadata: []byte(`{"client":"web"}`),
	})
	if err != nil {
		t.Fatalf("CreateUserWithSession() error = %v", err)
	}
	userID := user.ID

	// Revocation reason labels and provenance tuples are application rules
	// now; the schema stores labels outside the current vocabulary as-is.
	if _, err := pool.Exec(ctx, `
		UPDATE auth_sessions
		SET revoked_at = clock_timestamp(), revocation_reason = 'other'
		WHERE id = $1`, sessionID); err != nil {
		t.Fatalf("revocation with out-of-vocabulary reason label: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE auth_sessions
		SET revoked_at = clock_timestamp(), revocation_reason = 'logout', revoked_by_user_id = user_id
		WHERE id = $1`, sessionID); err != nil {
		t.Fatalf("valid logout revocation update: %v", err)
	}
	var reason string
	var revoker uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT revocation_reason, revoked_by_user_id
		FROM auth_sessions
		WHERE id = $1`, sessionID).Scan(&reason, &revoker); err != nil {
		t.Fatalf("read valid revocation: %v", err)
	}
	if reason != "logout" || revoker != userID {
		t.Fatalf("revocation metadata = %q, %s", reason, revoker)
	}
	// The schema declares user_id REFERENCES users ON DELETE
	// CASCADE, so a raw parent delete removes the session rows with it.
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID); err != nil {
		t.Fatalf("delete User owning self-revoked session: %v", err)
	}
	var remainingSessions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE id = $1`, sessionID).
		Scan(&remainingSessions); err != nil ||
		remainingSessions != 0 {
		t.Fatalf(
			"sessions after raw User delete = %d, error = %v, want the cascade to remove the row",
			remainingSessions,
			err,
		)
	}
}

func TestAuthRepositorySessionMutationBoundariesAndAtomicity(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Run("natural expiry is not revocation", func(t *testing.T) {
		bus, eventLog := recordedAuthBus(t)
		fixture := createAuthTestSession(t, ctx, store, "expired@zenith-team.test", 101, `{"client":"web"}`)
		var lastUsedAt time.Time
		if err := pool.QueryRow(ctx, `
			UPDATE auth_sessions
			SET created_at = clock_timestamp() - INTERVAL '2 hours',
			    expires_at = clock_timestamp() - INTERVAL '1 hour',
			    last_used_at = clock_timestamp() - INTERVAL '90 minutes'
			WHERE id = $1
			RETURNING last_used_at`, fixture.SessionID).Scan(&lastUsedAt); err != nil {
			t.Fatalf("expire auth session: %v", err)
		}
		result, err := rotateSession(ctx, store, bus, fixture.TokenHash, NewSession{
			TokenPrefix: "brf_expired-child",
			TokenHash:   bytes.Repeat([]byte{102}, 32), ExpiresAt: time.Now().UTC().Add(time.Hour),
			ClientMetadata: fixture.ClientMetadata,
		})
		if err != nil || result.Mutation.Outcome != SessionMutationInvalid ||
			len(result.Mutation.Revocations) != 0 {
			t.Fatalf("RotateSession(expired) = %#v, %v", result, err)
		}
		mismatched, err := rotateSession(ctx, store, bus, fixture.TokenHash, NewSession{
			TokenPrefix: "brf_expired-mismatch-child",
			TokenHash:   bytes.Repeat([]byte{103}, 32), ExpiresAt: time.Now().UTC().Add(time.Hour),
			ClientMetadata: []byte(`{"client":"desktop"}`),
		})
		if err != nil || mismatched.Mutation.Outcome != SessionMutationInvalid ||
			len(mismatched.Mutation.Revocations) != 0 {
			t.Fatalf("RotateSession(expired client mismatch) = %#v, %v", mismatched, err)
		}
		logout, err := revokeSession(ctx, store, bus, fixture.TokenHash)
		if err != nil || logout.Outcome != SessionMutationNoChange || len(logout.Revocations) != 0 {
			t.Fatalf("RevokeSession(expired) = %#v, %v", logout, err)
		}
		var revokedAt *time.Time
		var reason pgtype.Text
		var revoker uuid.UUID
		var storedLastUsedAt *time.Time
		if err := pool.QueryRow(ctx, `
			SELECT revoked_at, revocation_reason, revoked_by_user_id, last_used_at
			FROM auth_sessions WHERE id = $1`, fixture.SessionID).Scan(&revokedAt, &reason, &revoker, &storedLastUsedAt); err != nil {
			t.Fatalf("read expired auth session: %v", err)
		}
		if revokedAt != nil || reason.Valid || (revoker != uuid.Nil) || storedLastUsedAt == nil ||
			!storedLastUsedAt.Equal(lastUsedAt) {
			t.Fatalf(
				"expired session mutated = revoked:%v reason:%#v revoker:%#v last-used:%v want %v",
				revokedAt,
				reason,
				revoker,
				storedLastUsedAt,
				lastUsedAt,
			)
		}
		if countAuthRevocationEvents(t, eventLog, fixture.UserID) != 0 {
			t.Fatal("natural expiry emitted auth.session_revoked")
		}
	})

	t.Run("disabled User cleanup is not mislabeled as replay", func(t *testing.T) {
		bus, eventLog := recordedAuthBus(t)
		fixture := createAuthTestSession(
			t,
			ctx,
			store,
			"disabled-session@zenith-team.test",
			106,
			`{"client":"web"}`,
		)
		if _, err := pool.Exec(
			ctx,
			`UPDATE users SET disabled_at = clock_timestamp() WHERE id = $1`,
			fixture.UserID,
		); err != nil {
			t.Fatalf("disable User fixture: %v", err)
		}
		result, err := rotateSession(ctx, store, bus, fixture.TokenHash, NewSession{
			TokenPrefix: "brf_disabled-child",
			TokenHash:   bytes.Repeat([]byte{107}, 32), ExpiresAt: time.Now().UTC().Add(time.Hour),
			ClientMetadata: fixture.ClientMetadata,
		})
		if err != nil || result.Mutation.Outcome != SessionMutationInvalid ||
			len(result.Mutation.Revocations) != 0 {
			t.Fatalf("RotateSession(disabled User) = %#v, %v", result, err)
		}
		var stillLive bool
		if err := pool.QueryRow(ctx, `SELECT revoked_at IS NULL FROM auth_sessions WHERE id = $1`, fixture.SessionID).
			Scan(&stillLive); err != nil {
			t.Fatalf("read disabled User session: %v", err)
		}
		if !stillLive || countAuthRevocationEvents(t, eventLog, fixture.UserID) != 0 {
			t.Fatal("disabled User fallback lazily revoked or emitted a session event")
		}
	})

	t.Run("logout follows a stale root and is idempotent", func(t *testing.T) {
		bus, eventLog := recordedAuthBus(t)
		root := createAuthTestSession(t, ctx, store, "logout@zenith-team.test", 111, `{"client":"desktop"}`)
		childHash := bytes.Repeat([]byte{112}, 32)
		rotation, err := rotateSession(ctx, store, bus, root.TokenHash, NewSession{
			TokenPrefix: "brf_logout-child", TokenHash: childHash,
			ExpiresAt: time.Now().UTC().Add(2 * time.Hour), ClientMetadata: root.ClientMetadata,
		})
		if err != nil || rotation.Mutation.Outcome != SessionMutationRotated {
			t.Fatalf("RotateSession(logout fixture) = %#v, %v", rotation, err)
		}
		childID := rotation.SessionID
		loggedOut, err := revokeSession(ctx, store, bus, root.TokenHash)
		if err != nil || loggedOut.Outcome != SessionMutationLoggedOut || len(loggedOut.Revocations) != 1 ||
			loggedOut.Revocations[0].SessionID != childID {
			t.Fatalf("RevokeSession(stale root) = %#v, %v", loggedOut, err)
		}
		event := requireAuthRevocationEvent(t, eventLog, childID)
		if event.Payload.UserID != root.UserID ||
			event.Actor != events.UserActor(root.UserID.String()) ||
			event.Payload.Reason != SessionRevocationLogout {
			t.Fatalf("logout event = %#v", event)
		}
		repeated, err := revokeSession(ctx, store, bus, root.TokenHash)
		if err != nil || repeated.Outcome != SessionMutationNoChange || len(repeated.Revocations) != 0 {
			t.Fatalf("RevokeSession(repeated) = %#v, %v", repeated, err)
		}
		unknown, err := revokeSession(ctx, store, bus, bytes.Repeat([]byte{113}, 32))
		if err != nil || unknown.Outcome != SessionMutationNoChange {
			t.Fatalf("RevokeSession(unknown) = %#v, %v", unknown, err)
		}
		if got := countAuthRevocationEvents(t, eventLog, root.UserID); got != 2 {
			t.Fatalf("logout family event count = %d, want 2", got)
		}
	})

	t.Run("client mismatch is confirmed reuse", func(t *testing.T) {
		bus, eventLog := recordedAuthBus(t)
		root := createAuthTestSession(t, ctx, store, "mismatch@zenith-team.test", 121, `{"client":"web"}`)
		childHash := bytes.Repeat([]byte{122}, 32)
		result, err := rotateSession(ctx, store, bus, root.TokenHash, NewSession{
			TokenPrefix: "brf_mismatch-child", TokenHash: childHash,
			ExpiresAt: time.Now().UTC().Add(time.Hour), ClientMetadata: []byte(`{"client":"desktop"}`),
		})
		if err != nil || result.Mutation.Outcome != SessionMutationTokenReuse ||
			len(result.Mutation.Revocations) != 1 ||
			result.Mutation.Revocations[0].SessionID != root.SessionID {
			t.Fatalf("RotateSession(client mismatch) = %#v, %v", result, err)
		}
		event := requireAuthRevocationEvent(t, eventLog, root.SessionID)
		if event.Actor != events.SystemActor() ||
			event.Payload.Reason != SessionRevocationRefreshTokenReuse {
			t.Fatalf("client-mismatch event = %#v", event)
		}
		var childCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE token_hash = $1`, childHash).
			Scan(&childCount); err != nil ||
			childCount != 0 {
			t.Fatalf("client-mismatch child count = %d, error = %v", childCount, err)
		}
	})

	t.Run("recent child grace requires a rotation root", func(t *testing.T) {
		root := createAuthTestSession(t, ctx, store, "grace-reason@zenith-team.test", 131, `{"client":"web"}`)
		if _, err := pool.Exec(ctx, `
			UPDATE auth_sessions
			SET revoked_at = clock_timestamp(), revocation_reason = 'logout', revoked_by_user_id = user_id
			WHERE id = $1`, root.SessionID); err != nil {
			t.Fatalf("prepare logout-revoked root: %v", err)
		}
		childHash := bytes.Repeat([]byte{132}, 32)
		childID, err := createSession(ctx, store.Queries(), NewSession{
			UserID: root.UserID, TokenPrefix: "brf_grace-child", TokenHash: childHash,
			ExpiresAt: time.Now().UTC().Add(time.Hour), RotatedFromSessionID: &root.SessionID,
			ClientMetadata: root.ClientMetadata,
		})
		if err != nil {
			t.Fatalf("CreateSession(grace child) error = %v", err)
		}
		result, err := rotateSession(ctx, store, authTestBus(t), root.TokenHash, NewSession{
			TokenPrefix: "brf_grace-attempt",
			TokenHash:   bytes.Repeat([]byte{133}, 32), ExpiresAt: time.Now().UTC().Add(time.Hour),
			ClientMetadata: root.ClientMetadata,
		})
		if err != nil || result.Mutation.Outcome != SessionMutationTokenReuse ||
			len(result.Mutation.Revocations) != 1 ||
			result.Mutation.Revocations[0].SessionID != childID {
			t.Fatalf("RotateSession(logout-revoked root) = %#v, %v", result, err)
		}
	})

	t.Run("recent child grace tolerates refresh metadata reduced to client kind", func(t *testing.T) {
		root := createAuthTestSession(
			t,
			ctx,
			store,
			"grace-metadata@zenith-team.test",
			181,
			`{"client":"web","details":{"installationId":"browser-a"}}`,
		)
		first, err := rotateSession(ctx, store, authTestBus(t), root.TokenHash, NewSession{
			TokenPrefix: "brf_grace-metadata-child",
			TokenHash:   bytes.Repeat([]byte{182}, 32), ExpiresAt: time.Now().UTC().Add(time.Hour),
			ClientMetadata: root.ClientMetadata,
		})
		if err != nil || first.Mutation.Outcome != SessionMutationRotated {
			t.Fatalf("RotateSession(grace metadata fixture) = %#v, %v", first, err)
		}
		childID := first.SessionID
		retried, err := rotateSession(ctx, store, authTestBus(t), root.TokenHash, NewSession{
			TokenPrefix: "brf_grace-metadata-retry",
			TokenHash:   bytes.Repeat([]byte{183}, 32), ExpiresAt: time.Now().UTC().Add(time.Hour),
			ClientMetadata: []byte(`{"client":"web"}`),
		})
		if err != nil || retried.Mutation.Outcome != SessionMutationAlreadyRotated ||
			len(retried.Mutation.Revocations) != 0 {
			t.Fatalf("RotateSession(reduced refresh metadata) = %#v, %v", retried, err)
		}
		var revokedAt *time.Time
		if err := pool.QueryRow(ctx, `SELECT revoked_at FROM auth_sessions WHERE id = $1`, childID).
			Scan(&revokedAt); err != nil {
			t.Fatalf("query grace child: %v", err)
		}
		if revokedAt != nil {
			t.Fatalf("grace child revoked_at = %v, want nil", *revokedAt)
		}
	})

	t.Run("replay traverses an already-revoked multi-generation family", func(t *testing.T) {
		bus, eventLog := recordedAuthBus(t)
		root := createAuthTestSession(
			t,
			ctx,
			store,
			"multi-generation@zenith-team.test",
			161,
			`{"client":"web"}`,
		)
		childHash := bytes.Repeat([]byte{162}, 32)
		first, err := rotateSession(ctx, store, bus, root.TokenHash, NewSession{
			TokenPrefix: "brf_multi-child", TokenHash: childHash,
			ExpiresAt: time.Now().UTC().Add(2 * time.Hour), ClientMetadata: root.ClientMetadata,
		})
		if err != nil || first.Mutation.Outcome != SessionMutationRotated {
			t.Fatalf("RotateSession(multi root) = %#v, %v", first, err)
		}
		childID := first.SessionID
		grandchildHash := bytes.Repeat([]byte{163}, 32)
		second, err := rotateSession(ctx, store, bus, childHash, NewSession{
			TokenPrefix: "brf_multi-grandchild", TokenHash: grandchildHash,
			ExpiresAt: time.Now().UTC().Add(2 * time.Hour), ClientMetadata: root.ClientMetadata,
		})
		if err != nil || second.Mutation.Outcome != SessionMutationRotated {
			t.Fatalf("RotateSession(multi child) = %#v, %v", second, err)
		}
		grandchildID := second.SessionID
		if _, err := pool.Exec(ctx, `
			UPDATE auth_sessions
			SET created_at = created_at - INTERVAL '10 seconds',
			    revoked_at = clock_timestamp() - INTERVAL '6 seconds'
			WHERE id = $1`, root.SessionID); err != nil {
			t.Fatalf("age multi-generation root: %v", err)
		}
		var rootBefore, childBefore time.Time
		if err := pool.QueryRow(ctx, `SELECT revoked_at FROM auth_sessions WHERE id = $1`, root.SessionID).
			Scan(&rootBefore); err != nil {
			t.Fatalf("read multi root timestamp: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT revoked_at FROM auth_sessions WHERE id = $1`, childID).
			Scan(&childBefore); err != nil {
			t.Fatalf("read multi child timestamp: %v", err)
		}
		replayed, err := rotateSession(ctx, store, bus, root.TokenHash, NewSession{
			TokenPrefix: "brf_multi-replay",
			TokenHash:   bytes.Repeat([]byte{164}, 32), ExpiresAt: time.Now().UTC().Add(2 * time.Hour),
			ClientMetadata: root.ClientMetadata,
		})
		if err != nil || replayed.Mutation.Outcome != SessionMutationTokenReuse ||
			len(replayed.Mutation.Revocations) != 1 ||
			replayed.Mutation.Revocations[0].SessionID != grandchildID {
			t.Fatalf("RotateSession(multi replay) = %#v, %v", replayed, err)
		}
		var rootAfter, childAfter time.Time
		if err := pool.QueryRow(ctx, `SELECT revoked_at FROM auth_sessions WHERE id = $1`, root.SessionID).
			Scan(&rootAfter); err != nil {
			t.Fatalf("read multi root after replay: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT revoked_at FROM auth_sessions WHERE id = $1`, childID).
			Scan(&childAfter); err != nil {
			t.Fatalf("read multi child after replay: %v", err)
		}
		if !rootAfter.Equal(rootBefore) || !childAfter.Equal(childBefore) ||
			countAuthRevocationEvents(t, eventLog, root.UserID) != 3 {
			t.Fatalf(
				"multi replay changed ancestors or event cardinality: root %v/%v child %v/%v",
				rootBefore,
				rootAfter,
				childBefore,
				childAfter,
			)
		}
	})

	t.Run("ancestor replay fences concurrent descendant rotation", func(t *testing.T) {
		bus, eventLog := recordedAuthBus(t)
		root := createAuthTestSession(t, ctx, store, "family-race@zenith-team.test", 151, `{"client":"web"}`)
		childHash := bytes.Repeat([]byte{152}, 32)
		initial, err := rotateSession(ctx, store, bus, root.TokenHash, NewSession{
			TokenPrefix: "brf_family-race-child", TokenHash: childHash,
			ExpiresAt: time.Now().UTC().Add(2 * time.Hour), ClientMetadata: root.ClientMetadata,
		})
		if err != nil || initial.Mutation.Outcome != SessionMutationRotated {
			t.Fatalf("RotateSession(family race fixture) = %#v, %v", initial, err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE auth_sessions
			SET created_at = created_at - INTERVAL '10 seconds',
			    revoked_at = clock_timestamp() - INTERVAL '6 seconds'
			WHERE id = $1`, root.SessionID); err != nil {
			t.Fatalf("age family-race root: %v", err)
		}

		type mutationCall struct {
			result RotateSessionResult
			err    error
		}
		start := make(chan struct{})
		ancestorResult := make(chan mutationCall, 1)
		descendantResult := make(chan mutationCall, 1)
		go func() {
			<-start
			result, err := rotateSession(ctx, store, bus, root.TokenHash, NewSession{
				TokenPrefix: "brf_family-race-replay",
				TokenHash: bytes.Repeat(
					[]byte{153},
					32,
				),
				ExpiresAt:      time.Now().UTC().Add(2 * time.Hour),
				ClientMetadata: root.ClientMetadata,
			})
			ancestorResult <- mutationCall{result: result, err: err}
		}()
		go func() {
			<-start
			result, err := rotateSession(ctx, store, bus, childHash, NewSession{
				TokenPrefix: "brf_family-race-grandchild",
				TokenHash: bytes.Repeat(
					[]byte{154},
					32,
				),
				ExpiresAt:      time.Now().UTC().Add(2 * time.Hour),
				ClientMetadata: root.ClientMetadata,
			})
			descendantResult <- mutationCall{result: result, err: err}
		}()
		close(start)
		ancestor := <-ancestorResult
		descendant := <-descendantResult
		if ancestor.err != nil || ancestor.result.Mutation.Outcome != SessionMutationTokenReuse {
			t.Fatalf("ancestor replay = %#v, %v", ancestor.result, ancestor.err)
		}
		if descendant.err != nil ||
			(descendant.result.Mutation.Outcome != SessionMutationRotated && descendant.result.Mutation.Outcome != SessionMutationTokenReuse) {
			t.Fatalf("descendant rotation = %#v, %v", descendant.result, descendant.err)
		}
		var liveCount, revokedCount int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE revoked_at IS NULL AND expires_at > clock_timestamp()),
			       count(*) FILTER (WHERE revoked_at IS NOT NULL)
			FROM auth_sessions WHERE user_id = $1`, root.UserID).Scan(&liveCount, &revokedCount); err != nil {
			t.Fatalf("count family-race sessions: %v", err)
		}
		if eventCount := countAuthRevocationEvents(
			t,
			eventLog,
			root.UserID,
		); liveCount != 0 ||
			revokedCount != eventCount {
			t.Fatalf("family race live=%d revoked=%d events=%d", liveCount, revokedCount, eventCount)
		}
	})

	t.Run("User lock is acquired before any session-family row lock", func(t *testing.T) {
		root := createAuthTestSession(t, ctx, store, "lock-order@zenith-team.test", 171, `{"client":"web"}`)
		holder, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin User lock holder: %v", err)
		}
		defer func() { _ = holder.Rollback(context.Background()) }()
		var lockedUserID uuid.UUID
		if err := holder.QueryRow(ctx, `SELECT id FROM users WHERE id = $1 FOR UPDATE`, root.UserID).
			Scan(&lockedUserID); err != nil {
			t.Fatalf("lock User row: %v", err)
		}

		rotationDone := make(chan error, 1)
		go func() {
			_, err := rotateSession(ctx, store, authTestBus(t), root.TokenHash, NewSession{
				TokenPrefix: "brf_lock-order-child",
				TokenHash:   bytes.Repeat([]byte{172}, 32), ExpiresAt: time.Now().UTC().Add(time.Hour),
				ClientMetadata: root.ClientMetadata,
			})
			rotationDone <- err
		}()
		waitDeadline := time.Now().Add(2 * time.Second)
		for {
			select {
			case err := <-rotationDone:
				t.Fatalf("RotateSession() bypassed held User row: %v", err)
			default:
			}
			var waitingOnLock bool
			if err := pool.QueryRow(ctx, `
				SELECT EXISTS (
				    SELECT 1
				    FROM pg_stat_activity
				    WHERE datname = current_database()
				      AND state = 'active'
				      AND wait_event_type = 'Lock'
				      AND (query LIKE '%auth_sessions%' OR query LIKE '%FROM users%')
				)`).Scan(&waitingOnLock); err != nil {
				t.Fatalf("observe blocked session mutation: %v", err)
			}
			if waitingOnLock {
				break
			}
			if time.Now().After(waitDeadline) {
				t.Fatal("session mutation did not reach the held User lock")
			}
			time.Sleep(10 * time.Millisecond)
		}

		checker, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin session-lock checker: %v", err)
		}
		var checkedSessionID uuid.UUID
		checkErr := checker.QueryRow(ctx, `
			SELECT id FROM auth_sessions WHERE id = $1 FOR UPDATE NOWAIT`, root.SessionID).Scan(&checkedSessionID)
		_ = checker.Rollback(context.Background())
		if checkErr != nil {
			t.Fatalf("session row was locked before canonical User lock completed: %v", checkErr)
		}
		if err := holder.Rollback(ctx); err != nil {
			t.Fatalf("release User lock holder: %v", err)
		}
		select {
		case err := <-rotationDone:
			if err != nil {
				t.Fatalf("RotateSession() after User lock release: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("RotateSession() remained blocked after User lock release")
		}
	})
}

type authTestSession struct {
	UserID         uuid.UUID
	SessionID      uuid.UUID
	TokenHash      []byte
	ClientMetadata json.RawMessage
}

// activeSessionUser answers "is this session still live for this user?"
// straight from the schema, so the session-mutation tests below assert the
// committed row rather than a service-layer translation of it.
func activeSessionUser(ctx context.Context, store *db.Store, sessionID, userID uuid.UUID) (User, error) {
	row, err := store.Queries().GetActiveSessionUser(ctx, generated.GetActiveSessionUserParams{
		SessionID: sessionID,
		UserID:    userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrInvalidSession
		}
		return User{}, err
	}
	return userFromDB(row.User), nil
}

func createAuthTestSession(
	t *testing.T,
	ctx context.Context,
	store *db.Store,
	email string,
	tokenByte byte,
	metadata string,
) authTestSession {
	t.Helper()
	fixture := authTestSession{
		TokenHash:      bytes.Repeat([]byte{tokenByte}, 32),
		ClientMetadata: json.RawMessage(metadata),
	}
	user, sessionID, err := createUserWithSession(ctx, store, NewUser{
		Email: email, PasswordHash: "stored", DisplayName: "Session Test",
	}, NewSession{
		TokenPrefix: fmt.Sprintf("brf_%d", tokenByte), TokenHash: fixture.TokenHash,
		ExpiresAt: time.Now().UTC().Add(2 * time.Hour), ClientMetadata: fixture.ClientMetadata,
	})
	if err != nil {
		t.Fatalf("CreateUserWithSession(%s) error = %v", email, err)
	}
	fixture.UserID = user.ID
	fixture.SessionID = sessionID
	return fixture
}

func recordedAuthBus(t *testing.T) (*events.Bus, *topicLog[RevokedSession]) {
	t.Helper()
	bus := authTestBus(t)
	return bus, recordTopic[RevokedSession](t, bus, Topic)
}

func countAuthRevocationEvents(
	t *testing.T,
	log *topicLog[RevokedSession],
	userID uuid.UUID,
) int {
	t.Helper()
	count := 0
	for _, event := range log.snapshot() {
		// The routing fact rides the payload now: this frame belongs to one
		// user, and that is what the realtime bridge reads off it.
		if event.Payload.UserID == userID {
			count++
		}
	}
	return count
}

type storedAuthRevocationEvent struct {
	Actor   events.Actor
	Payload RevokedSession
}

func requireAuthRevocationEvent(
	t *testing.T,
	log *topicLog[RevokedSession],
	sessionID uuid.UUID,
) storedAuthRevocationEvent {
	t.Helper()
	var found *recordedEvent[RevokedSession]
	for _, event := range log.snapshot() {
		if event.Payload.SessionID != sessionID {
			continue
		}
		if found != nil {
			t.Fatalf("multiple auth session revocation events found for %s", sessionID)
		}
		copied := event
		found = &copied
	}
	if found == nil {
		t.Fatalf("auth session revocation event for %s not found", sessionID)
	}

	// Two fields on the wire: the routing user id must stay off it.
	encoded, err := json.Marshal(found.Payload)
	if err != nil {
		t.Fatalf("encode auth session revocation payload: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil || len(fields) != 2 {
		t.Fatalf("auth session revocation payload fields = %#v, error = %v", fields, err)
	}
	if found.Payload.UserID == uuid.Nil {
		t.Fatal("auth session revocation carries no routing user id, so the bridge could not deliver it")
	}
	return storedAuthRevocationEvent{Actor: found.Actor, Payload: found.Payload}
}

func startAuthPostgres(t *testing.T) (*pgxpool.Pool, *db.Store) {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return pool, store
}
