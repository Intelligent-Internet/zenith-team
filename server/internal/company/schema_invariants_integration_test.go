//go:build integration

package company

import (
	"context"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// Invariants the company plan depends on and that only PostgreSQL enforces.
// They live here rather than beside the migration because breaking one changes
// company behavior, not schema shape.
func TestCompanyPlanSchemaInvariants(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)

	t.Run("statistics aggregates memberships and runtimes independently", func(t *testing.T) {
		disabledUserID := dbtest.NewUUID(t)
		offlineUserID := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO users (id, email, display_name, disabled_at) VALUES
				($1, 'disabled-statistics@example.com', 'Disabled Statistics', now()),
				($2, 'offline-statistics@example.com', 'Offline Statistics', NULL)`,
			disabledUserID, offlineUserID)
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO company_members (company_id, user_id, role) VALUES
				($1, $2, 'member'), ($1, $3, 'member')`,
			fixture.CompanyA, disabledUserID, offlineUserID)
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO runtimes (
				id, company_id, daemon_id, name, provider,
				status, owner_user_id, last_seen_at
			) VALUES
				($1, $2, 'daemon-disabled-statistics', 'Disabled Runtime', 'claude', 'online', $3, now()),
				($4, $2, 'daemon-offline-statistics', 'Offline Runtime', 'codex', 'offline', $5, now())`,
			dbtest.NewUUID(t), fixture.CompanyA, disabledUserID,
			dbtest.NewUUID(t), offlineUserID)

		statistics, err := generated.New(pool).GetCompanyStatistics(ctx, fixture.CompanyA)
		if err != nil {
			t.Fatalf("GetCompanyStatistics() error = %v", err)
		}
		if statistics.MemberCount != 3 || statistics.RuntimeCount != 3 ||
			statistics.RuntimeOnlineCount != 1 {
			t.Fatalf("GetCompanyStatistics() = %+v, want members 3, runtimes 3, online 1", statistics)
		}
	})

	t.Run("a new Company has no contact profile", func(t *testing.T) {
		companyID := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO companies (id, name, slug, task_prefix)
			VALUES ($1, 'Profile Company', 'profile-company', 'PROFILE')`, companyID)

		var emailMissing, countryMissing, numberMissing bool
		if err := pool.QueryRow(ctx, `
			SELECT contact_email IS NULL, phone_country_iso2 IS NULL, phone_number IS NULL
			FROM companies WHERE id = $1`, companyID,
		).Scan(&emailMissing, &countryMissing, &numberMissing); err != nil {
			t.Fatalf("read default Company contact profile: %v", err)
		}
		if !emailMissing || !countryMissing || !numberMissing {
			t.Fatalf(
				"default Company contacts present = email:%t country:%t number:%t, want all absent",
				!emailMissing, !countryMissing, !numberMissing,
			)
		}
	})
}
