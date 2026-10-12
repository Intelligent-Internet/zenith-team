//go:build integration

package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

// Two column-level guarantees the profile code relies on and cannot restate.
// biography is NOT NULL because the generated model types it as a plain
// string: one NULL row makes that user's profile unscannable. The phone pair
// is the opposite — nullable, because clearing a phone writes NULL rather than
// an empty string.
func TestUserProfileColumnsDefaultAsProfileCodeExpects(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}

	userID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO users (id, email, display_name)
		VALUES ($1, 'profile@example.com', 'Profile User')`, userID)

	var biography string
	var countryMissing, numberMissing bool
	if err := pool.QueryRow(ctx, `
		SELECT biography, phone_country_iso2 IS NULL, phone_number IS NULL
		FROM users WHERE id = $1`, userID,
	).Scan(&biography, &countryMissing, &numberMissing); err != nil {
		t.Fatalf("read default profile columns: %v", err)
	}
	if biography != "" {
		t.Fatalf("default biography = %q, want empty", biography)
	}
	if !countryMissing || !numberMissing {
		t.Fatalf(
			"default phone present = country:%t number:%t, want both absent",
			!countryMissing,
			!numberMissing,
		)
	}

	if _, err := pool.Exec(ctx, `UPDATE users SET biography = NULL WHERE id = $1`, userID); err == nil {
		t.Fatal("NULL biography passed the NOT NULL constraint; the generated model types it as string")
	}

	// The Go-side cap is 1,200 characters; multi-byte content makes that 4,800
	// bytes, which the column must still accept.
	valid := strings.Repeat("😀", 1200)
	dbtest.MustExec(t, ctx, pool, `UPDATE users SET biography = $2 WHERE id = $1`, userID, valid[:4800])
	dbtest.MustExec(t, ctx, pool, `
		UPDATE users SET phone_country_iso2 = 'VN', phone_number = '912345678' WHERE id = $1`, userID)
}
