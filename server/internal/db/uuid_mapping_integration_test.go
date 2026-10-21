//go:build integration

package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// sqlc emits google/uuid for every uuid column and pgx encodes it with no codec
// registered. These pin that end-to-end rather than through a hand-rolled
// probe, because the conversion helpers that used to sit at this seam are gone
// and nothing else would notice if pgx stopped round-tripping the type.
func TestUUIDColumnsRoundTripThroughGeneratedQueries(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)

	t.Run("a NOT NULL id comes back byte-identical", func(t *testing.T) {
		company, err := queries.GetCompanyByID(ctx, fixture.CompanyA)
		if err != nil {
			t.Fatalf("GetCompanyByID() error = %v", err)
		}
		if company.ID != fixture.CompanyA {
			t.Fatalf("id = %s, want %s", company.ID, fixture.CompanyA)
		}
		// The wire form is what a client sees; a byte-equal id that rendered
		// differently would still break every URL built from it.
		if company.ID.String() != fixture.CompanyA.String() {
			t.Fatalf("rendered = %s, want %s", company.ID, fixture.CompanyA)
		}
	})

	t.Run("an absent nullable id reads back as NULL, not as the zero id", func(t *testing.T) {
		agent, err := queries.GetAgentForCompany(ctx, generated.GetAgentForCompanyParams{
			AgentID: fixture.AgentA, CompanyID: fixture.CompanyA,
		})
		if err != nil {
			t.Fatalf("GetAgentForCompany() error = %v", err)
		}
		// The seeded agent is a root: it reports to nobody.
		if agent.ReportsToAgentID.Valid {
			t.Fatalf("ReportsToAgentID = %+v, want NULL", agent.ReportsToAgentID)
		}
		if agent.ReportsToAgentID.UUID != uuid.Nil {
			t.Fatalf("a NULL id carried a value: %s", agent.ReportsToAgentID.UUID)
		}
	})

	t.Run("a present nullable id keeps its value", func(t *testing.T) {
		// AgentA is CompanyA's root, so a child of it is the only shape the
		// one-root-per-company index leaves available.
		child := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO agents
				(id, company_id, name, title, reports_to_agent_id, provider, model)
			VALUES ($1, $2, 'Child', 'IC', $3, 'codex', 'test')`,
			child, fixture.CompanyA, fixture.AgentA)

		agent, err := queries.GetAgentForCompany(ctx, generated.GetAgentForCompanyParams{
			AgentID: child, CompanyID: fixture.CompanyA,
		})
		if err != nil {
			t.Fatalf("GetAgentForCompany() error = %v", err)
		}
		if !agent.ReportsToAgentID.Valid {
			t.Fatal("ReportsToAgentID read back as NULL after being set")
		}
		if agent.ReportsToAgentID.UUID != fixture.AgentA {
			t.Fatalf("parent = %s, want %s", agent.ReportsToAgentID.UUID, fixture.AgentA)
		}
	})
}

// The whole risk of the pgtype-to-google/uuid move is that SQL NULL and the
// all-zero UUID stop being distinguishable in Go. Postgres keeps them apart,
// and this schema happens to react to each one differently, which makes the
// difference observable rather than asserted:
//
//   - reports_to_agent_id IS NULL marks an agent as its company's root, and a
//     partial unique index allows only one. A second NULL parent therefore
//     collides on that index — which can only happen if the value really is
//     NULL, because the index predicate is IS NULL.
//   - a non-NULL parent is checked against the self-referencing foreign key,
//     and 00000000-0000-0000-0000-000000000000 names no agent.
//
// Two inserts, two different SQLSTATEs, one conclusion: the two values did not
// collapse. This is the failure that actually happened during the migration —
// a root agent whose parent had been wrapped Valid:true.
func TestNullUUIDIsNotTheZeroUUIDAtAForeignKey(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)

	insert := func(parent uuid.NullUUID) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO agents
				(id, company_id, name, title, reports_to_agent_id, provider, model)
			VALUES ($1, $2, 'probe', 'Probe', $3, 'codex', 'test')`,
			dbtest.NewUUID(t), fixture.CompanyA, parent)
		return err
	}

	sqlState := func(t *testing.T, err error) string {
		t.Helper()
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) {
			t.Fatalf("error = %v, want a *pgconn.PgError", err)
		}
		return pgErr.Code
	}

	t.Run("NULL reaches the column as NULL", func(t *testing.T) {
		err := insert(uuid.NullUUID{})
		if err == nil {
			t.Fatal("expected the one-root-per-company index to reject a second root")
		}
		// 23505 is the unique-violation on agents_one_root_per_company_idx,
		// whose predicate is `reports_to_agent_id IS NULL`. Reaching it proves
		// the column is NULL; a zero UUID would have failed the FK instead.
		if code := sqlState(t, err); code != "23505" {
			t.Fatalf("SQLSTATE = %s, want 23505 — NULL did not reach the column as NULL", code)
		}
	})

	t.Run("a valid zero id reaches it as a value", func(t *testing.T) {
		err := insert(uuid.NullUUID{UUID: uuid.Nil, Valid: true})
		if err == nil {
			t.Fatal("the zero UUID was accepted as a parent")
		}
		if code := sqlState(t, err); code != "23503" {
			t.Fatalf("SQLSTATE = %s, want 23503 — the zero id was not treated as a real value", code)
		}
	})

	t.Run("a real parent satisfies the foreign key", func(t *testing.T) {
		if err := insert(uuid.NullUUID{UUID: fixture.AgentA, Valid: true}); err != nil {
			t.Fatalf("insert with a real parent: %v", err)
		}
	})
}
