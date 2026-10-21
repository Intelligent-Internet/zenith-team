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

// Migration 000063 adds the keys the Plugins list matches Codex Plugins by:
// a name without case, spaces or punctuation and a website's host
// without "www.", stored on each observation, and curated_duplicate, which it
// backfills for the observations already stored. Down restores 000062's
// case-insensitive name index.
func TestPluginMatchingMigration(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	migrateTo(t, ctx, pool, 62)
	fixture := dbtest.SeedFixture(t, ctx, pool)
	generation := uuid.New()
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO plugin_discovery_generations
			(runtime_id, company_id, provider, generation, manifest_digest, observed_at)
		VALUES ($1, $2, 'codex', $3, $4, now())`,
		fixture.RuntimeA, fixture.CompanyA, generation, "sha256:"+uuid.NewString())
	insert := func(nativeID, name, status, action string, rank int, website, developer string) {
		t.Helper()
		dbtest.MustExec(t, ctx, pool, `
			WITH plugin AS (
				INSERT INTO plugin_catalog_items (company_id, provider, native_id)
				VALUES ($1, 'codex', $3) RETURNING id
			)
			INSERT INTO plugin_runtime_observations
				(runtime_id, plugin_id, company_id, provider, generation, name, category,
				 status, installed, enabled, action, catalog_rank, website_url, developer_name,
				 observed_at)
			SELECT $2, plugin.id, $1, 'codex', $4, $5, 'Other', $6, $6 = 'installed',
				$6 = 'installed', $7, $8, $9, nullif($10, ''), now()
			FROM plugin`,
			fixture.CompanyA, fixture.RuntimeA, nativeID, generation, name, status, action, rank,
			website, developer)
	}
	observe := func(nativeID, name string, installed bool, rank int) {
		t.Helper()
		status, action := "available", "install"
		if installed {
			status, action = "installed", "uninstall"
		}
		insert(nativeID, name, status, action, rank, "https://WWW.Example.COM:443/"+nativeID, "")
	}
	observe("canva@claude-plugins-official", "canva", false, 0)
	observe("figma@claude-plugins-official", "figma", true, 1)
	observe("browser@openai-bundled", "Browser", false, 2)
	observe("canva@openai-curated-remote", "Canva", false, 3)
	observe("figma@openai-curated-remote", "Figma", false, 4)
	observe("browser@openai-curated-remote", "Browser", false, 5)
	// A twin that cannot be installed hides nothing.
	observe("pigment@claude-plugins-official", "pigment", false, 6)
	insert("app-6a69a1fd1a3c81919924cbac1cb59d63@openai-curated-remote", "Pigment", "error", "none", 7,
		"https://pigment.com", "")
	// Only the name agrees, then the developer does.
	insert("data@claude-plugins-official", "Data", "available", "install", 8,
		"https://github.com/astronomer/agents", "Astronomer")
	insert("data-analytics@openai-curated-remote", "Data", "available", "install", 9,
		"https://openai.com/", "OpenAI")
	insert("boltz@claude-plugins-official", "boltz", "available", "install", 10,
		"https://github.com/boltz/skills", "Boltz")
	insert("boltz-api-cli@openai-curated-remote", "Boltz", "available", "install", 11,
		"https://docs.boltz.bio", "Boltz")
	migrateTo(t, ctx, pool, 63)

	rows, err := pool.Query(ctx, `
		SELECT plugins.native_id, observations.name_key, observations.website_host,
			observations.curated_duplicate
		FROM plugin_runtime_observations AS observations
		JOIN plugin_catalog_items AS plugins ON plugins.id = observations.plugin_id
		ORDER BY observations.catalog_rank`)
	if err != nil {
		t.Fatal(err)
	}
	type stored struct {
		nativeID, nameKey, host string
		duplicate               bool
	}
	var got []stored
	for rows.Next() {
		var row stored
		if err := rows.Scan(&row.nativeID, &row.nameKey, &row.host, &row.duplicate); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []stored{
		{"canva@claude-plugins-official", "canva", "example.com", true},
		{"figma@claude-plugins-official", "figma", "example.com", false},
		{"browser@openai-bundled", "browser", "example.com", false},
		{"canva@openai-curated-remote", "canva", "example.com", false},
		{"figma@openai-curated-remote", "figma", "example.com", false},
		{"browser@openai-curated-remote", "browser", "example.com", false},
		{"pigment@claude-plugins-official", "pigment", "example.com", false},
		{"app-6a69a1fd1a3c81919924cbac1cb59d63@openai-curated-remote", "pigment", "pigment.com", false},
		{"data@claude-plugins-official", "data", "github.com", false},
		{"data-analytics@openai-curated-remote", "data", "openai.com", false},
		{"boltz@claude-plugins-official", "boltz", "github.com", true},
		{"boltz-api-cli@openai-curated-remote", "boltz", "docs.boltz.bio", false},
	}
	if len(got) != len(want) {
		t.Fatalf("stored observations = %+v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("observation %d = %+v, want %+v", index, got[index], want[index])
		}
	}

	// Rows written after the migration key their name and website the same way.
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
			t.Errorf("after 000063 index %s is missing: %v", name, up)
		}
	}
	if up["plugin_runtime_observations_company_provider_name_idx"] {
		t.Errorf("after 000063 the case-insensitive name index remains: %v", up)
	}

	migrateTo(t, ctx, pool, 62)
	down := pluginObservationIndexes(t, ctx, pool)
	if !down["plugin_runtime_observations_company_provider_name_idx"] {
		t.Errorf("after rollback indexes = %v", down)
	}
	for _, name := range matching {
		if down[name] {
			t.Errorf("after rollback index %s remains", name)
		}
	}
	var leftovers int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'plugin_runtime_observations'
		  AND column_name IN ('name_key', 'website_host', 'curated_duplicate')`).Scan(&leftovers); err != nil ||
		leftovers != 0 {
		t.Errorf("after rollback %d matching columns remain (%v)", leftovers, err)
	}
	migrateTo(t, ctx, pool, 63)
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
