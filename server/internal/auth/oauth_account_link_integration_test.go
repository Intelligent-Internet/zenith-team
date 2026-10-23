//go:build integration

package auth

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// Signup without email verification historically stamped the email as verified.
// Neither that legacy timestamp nor a verified provider email proves that the
// caller controls the existing account's password and sessions.
func TestOAuthLoginNeverLinksAnExistingAccountByEmailAlone(t *testing.T) {
	pool, store := startAuthPostgres(t)
	for index, verified := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing_email_verified_%t", verified), func(t *testing.T) {
			var verifiedAt *time.Time
			if verified {
				now := time.Now().UTC().Truncate(time.Microsecond)
				verifiedAt = &now
			}
			email := fmt.Sprintf("victim-%t@zenith-team.test", verified)
			existing, sessionID, err := createUserWithSession(t.Context(), store, NewUser{
				Email: email, PasswordHash: "attacker-password-hash", DisplayName: "Existing",
				EmailVerifiedAt: verifiedAt,
			}, newZenithTeamSession(t, byte(180+index)))
			if err != nil {
				t.Fatalf("create preexisting account: %v", err)
			}
			input := OAuthLoginInput{
				Identity: VerifiedOAuthIdentity{
					Provider: "google",
					Subject:  fmt.Sprintf("real-owner-%t", verified),
					Email:    email,
					Profile:  OAuthProfileSnapshot{Name: "Real Owner"},
				},
				InitialDisplayName: "Real Owner", Session: newOAuthSession(t, byte(182+index)),
			}
			if _, _, err := loginWithOAuthAndCreateSession(
				t.Context(),
				store,
				input,
			); !errors.Is(
				err,
				ErrOAuthAccountConflict,
			) {
				t.Fatalf("OAuth login error = %v, want ErrOAuthAccountConflict", err)
			}
			var accounts, sessions int
			if err := pool.QueryRow(t.Context(), `
				SELECT (SELECT count(*) FROM oauth_accounts WHERE user_id = $1),
				       (SELECT count(*) FROM auth_sessions WHERE user_id = $1)`, existing.ID).
				Scan(&accounts, &sessions); err != nil {
				t.Fatalf("read account state: %v", err)
			}
			if accounts != 0 || sessions != 1 {
				t.Fatalf(
					"refused login changed account: OAuth accounts=%d sessions=%d",
					accounts,
					sessions,
				)
			}
			stored, err := getUserByID(t.Context(), store.Queries(), existing.ID)
			if err != nil || stored.PasswordHash != existing.PasswordHash ||
				(stored.EmailVerifiedAt != nil) != verified {
				t.Fatalf("refused login changed existing credentials: user=%+v error=%v", stored, err)
			}
			if _, err := activeSessionUser(t.Context(), store, sessionID, existing.ID); err != nil {
				t.Fatalf("refused login changed the existing session: %v", err)
			}
		})
	}
}
