//go:build integration

package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOAuthConcurrentFirstSigninsWithSameEmailNeverMergeSubjects(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	const email = "oauth-race@zenith-team.test"
	calls := make([]func() (User, error), 8)
	for index := range calls {
		input := oauthRaceInput(t, email, fmt.Sprintf("subject-%d", index), byte(index+100))
		calls[index] = func() (User, error) {
			user, _, err := loginWithOAuthAndCreateSession(ctx, store, input)
			return user, err
		}
	}
	results := runAccountRace(calls)
	var winner uuid.UUID
	var successful, conflicted int
	for _, result := range results {
		switch {
		case result.err == nil:
			successful++
			winner = result.user.ID
		case errors.Is(result.err, ErrOAuthAccountConflict):
			conflicted++
		default:
			t.Fatalf("concurrent OAuth login error = %v", result.err)
		}
	}
	if successful != 1 || conflicted != 7 || winner == uuid.Nil {
		t.Fatalf("same-email results: successes=%d conflicts=%d winner=%s", successful, conflicted, winner)
	}
	assertAccountRaceState(t, pool, email, 1, 1)
}

func TestOAuthConcurrentExistingSubjectRejectsOtherSubjects(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	const email = "linked-race@zenith-team.test"
	fixture := oauthRaceInput(t, email, "already-linked", 110)
	existing, _, err := loginWithOAuthAndCreateSession(ctx, store, fixture)
	if err != nil {
		t.Fatalf("create linked account: %v", err)
	}
	calls := make([]func() (User, error), 12)
	for index := range calls {
		subject := "already-linked"
		if index%2 != 0 {
			subject = fmt.Sprintf("other-subject-%d", index)
		}
		input := oauthRaceInput(t, email, subject, byte(index+111))
		calls[index] = func() (User, error) {
			user, _, err := loginWithOAuthAndCreateSession(ctx, store, input)
			return user, err
		}
	}
	for index, result := range runAccountRace(calls) {
		if index%2 == 0 {
			if result.err != nil || result.user.ID != existing.ID {
				t.Errorf("linked subject result: user=%s error=%v", result.user.ID, result.err)
			}
		} else if !errors.Is(result.err, ErrOAuthAccountConflict) {
			t.Errorf("unlinked subject error = %v, want ErrOAuthAccountConflict", result.err)
		}
	}
	assertAccountRaceState(t, pool, email, 1, 7)
}

func TestOAuthConcurrentPasswordSignupNeverSharesTheWinningAccount(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	// Small Argon2 cost keeps this transaction-race test cheap. Production-cost
	// hashing and HTTP throughput belong to the separate deployment load test.
	passwords, err := NewPasswordHasher(PasswordHasherOptions{
		MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatalf("construct password manager: %v", err)
	}
	tokens, err := NewTokenManager(TokenManagerOptions{
		Secret: []byte("01234567890123456789012345678901"), AccessTTL: time.Minute, RefreshTTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("construct token manager: %v", err)
	}
	service := storelessService(t, ServiceOptions{Store: store, Passwords: passwords, Tokens: tokens})
	const email = "password-oauth-race@zenith-team.test"
	calls := make([]func() (User, error), 8)
	for index := range calls {
		if index%2 == 0 {
			calls[index] = func() (User, error) {
				result, err := service.Signup(ctx, SignupInput{
					Email:       email,
					Password:    "password-for-test",
					DisplayName: "Password Account",
					Client:      ClientWeb,
				})
				if err != nil {
					return User{}, err
				}
				if result.Auth == nil {
					return User{}, errors.New("signup did not return an authenticated account")
				}
				return result.Auth.User, nil
			}
			continue
		}
		input := oauthRaceInput(t, email, fmt.Sprintf("google-owner-%d", index), byte(index+125))
		calls[index] = func() (User, error) {
			user, _, err := loginWithOAuthAndCreateSession(ctx, store, input)
			return user, err
		}
	}
	var successful, conflicted, wantAccounts int
	for index, result := range runAccountRace(calls) {
		if result.err == nil {
			successful++
			if index%2 != 0 {
				wantAccounts = 1
				if result.user.PasswordHash != "" {
					t.Error("OAuth winner inherited password credentials")
				}
			} else if result.user.PasswordHash == "" {
				t.Error("password signup winner has no password")
			}
			continue
		}
		want := ErrEmailExists
		if index%2 != 0 {
			want = ErrOAuthAccountConflict
		}
		if !errors.Is(result.err, want) {
			t.Errorf("racing account creation error = %v, want %v", result.err, want)
		}
		conflicted++
	}
	if successful != 1 || conflicted != 7 {
		t.Fatalf("mixed race results: successes=%d conflicts=%d", successful, conflicted)
	}
	assertAccountRaceState(t, pool, email, wantAccounts, 1)
	t.Logf("winning account method: oauth=%t", wantAccounts == 1)
}

type accountRaceResult struct {
	user User
	err  error
}

func runAccountRace(calls []func() (User, error)) []accountRaceResult {
	start := make(chan struct{})
	results := make([]accountRaceResult, len(calls))
	var workers sync.WaitGroup
	for index, call := range calls {
		workers.Go(func() {
			<-start
			results[index].user, results[index].err = call()
		})
	}
	close(start)
	workers.Wait()
	return results
}

func oauthRaceInput(t *testing.T, email, subject string, seed byte) OAuthLoginInput {
	t.Helper()
	return OAuthLoginInput{
		Identity: VerifiedOAuthIdentity{
			Provider: "google",
			Subject:  subject,
			Email:    email,
			Profile:  OAuthProfileSnapshot{Name: "Account"},
		},
		InitialDisplayName: "Account", Session: newOAuthSession(t, seed),
	}
}

func assertAccountRaceState(t *testing.T, pool *pgxpool.Pool, email string, wantAccounts, wantSessions int) {
	t.Helper()
	var users, accounts, sessions int
	if err := pool.QueryRow(t.Context(), `
		SELECT (SELECT count(*) FROM users WHERE lower(email) = lower($1)),
		       (SELECT count(*) FROM oauth_accounts WHERE user_id IN
		           (SELECT id FROM users WHERE lower(email) = lower($1))),
		       (SELECT count(*) FROM auth_sessions WHERE user_id IN
		           (SELECT id FROM users WHERE lower(email) = lower($1)))`, email).
		Scan(&users, &accounts, &sessions); err != nil {
		t.Fatalf("read final account state: %v", err)
	}
	if users != 1 || accounts != wantAccounts || sessions != wantSessions {
		t.Fatalf("account state users/accounts/sessions = %d/%d/%d, want 1/%d/%d",
			users, accounts, sessions, wantAccounts, wantSessions)
	}
}
