//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// The Plugins list matches Codex Plugins by keys the database derives on
// every observation: a name without case, spaces or punctuation, and a
// website's host without "www.". The case-insensitive name index they
// replaced stays gone.
func TestPluginObservationsKeyTheirNameAndWebsite(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	generation := uuid.New()
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO plugin_discovery_generations
			(runtime_id, company_id, provider, generation, manifest_digest, observed_at)
		VALUES ($1, $2, 'codex', $3, $4, now())`,
		fixture.RuntimeA, fixture.CompanyA, generation, "sha256:"+uuid.NewString())

	const insertObservation = `
		WITH plugin AS (
			INSERT INTO plugin_catalog_items (company_id, provider, native_id)
			VALUES ($1, 'codex', gen_random_uuid()::text) RETURNING id
		)
		INSERT INTO plugin_runtime_observations
			(runtime_id, plugin_id, company_id, provider, generation, name, category,
			 status, action, catalog_rank, website_url, observed_at)
		SELECT $2, plugin.id, $1, 'codex', $3, $4, 'Other', 'available', 'install',
			1000 + (SELECT count(*) FROM plugin_runtime_observations WHERE runtime_id = $2)::integer,
			$5, now()
		FROM plugin
		RETURNING name_key, coalesce(website_host, '')`
	for _, test := range []struct{ name, website, wantKey, wantHost string }{
		{"Adobe for Creativity", "https://www.canva.com/", "adobeforcreativity", "canva.com"},
		{"adobe-for-creativity", "https://WWW.Canva.COM:443/path?q=1", "adobeforcreativity", "canva.com"},
		{"Record & Replay", "https://docs.www.example.com/guide", "recordreplay", "docs.www.example.com"},
		{"Bigdata.com", "https://gist.github.com/acme/snippet", "bigdatacom", "gist.github.com"},
		{"Café – Tools_2", "https://hostinger.com?utm_source=codex", "cafétools2", "hostinger.com"},
		{"+++", "", "", ""},
	} {
		var website *string
		if test.website != "" {
			website = &test.website
		}
		var gotKey, gotHost string
		row := pool.QueryRow(ctx, insertObservation,
			fixture.CompanyA, fixture.RuntimeA, generation, test.name, website)
		if err := row.Scan(&gotKey, &gotHost); err != nil {
			t.Fatalf("insert %q: %v", test.name, err)
		}
		if gotKey != test.wantKey || gotHost != test.wantHost {
			t.Errorf("keys(%q, %q) = %q, %q; want %q, %q",
				test.name, test.website, gotKey, gotHost, test.wantKey, test.wantHost)
		}
	}

	matching := []string{
		"plugin_runtime_observations_company_provider_name_key_idx",
		"plugin_runtime_observations_company_provider_website_host_idx",
		"plugin_runtime_observations_company_provider_developer_idx",
	}
	up := pluginObservationIndexes(t, ctx, pool)
	for _, name := range matching {
		if !up[name] {
			t.Errorf("index %s is missing: %v", name, up)
		}
	}
	if up["plugin_runtime_observations_company_provider_name_idx"] {
		t.Errorf("the case-insensitive name index remains: %v", up)
	}
}

func pluginObservationIndexes(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]bool {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT indexname FROM pg_indexes WHERE tablename = 'plugin_runtime_observations'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	names := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}
