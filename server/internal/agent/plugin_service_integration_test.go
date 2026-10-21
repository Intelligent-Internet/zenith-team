//go:build integration

package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
)

func TestPluginDiscoveryPersistsPresentationWithoutGrantingConnectorAuthority(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	bus := mustAgentTestBus(t)
	var changes []events.Event
	if err := bus.Subscribe(TopicRuntime, func(_ context.Context, event events.Event) error {
		changes = append(changes, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewPluginService(store, bus)
	if err != nil {
		t.Fatal(err)
	}

	item := PluginObservationInput{
		NativeID: "github-plugin", Name: "GitHub Plugin", Status: PluginStatusInstalled,
		Installed: true, Enabled: true, Action: PluginActionNone, CatalogRank: 0,
		ComponentCounts: PluginComponentCounts{Apps: 1},
		ConnectorIdentities: []PluginConnectorIdentity{{
			Source: ConnectorSourceCodexApp, NativeID: "github",
		}},
	}
	canonical, err := normalizePluginObservation(ConnectorProviderCodex, item)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := pluginManifestDigest([]PluginObservationInput{canonical})
	if err != nil {
		t.Fatal(err)
	}
	input := PluginDiscoveryInput{
		CompanyID: fixture.companyID, RuntimeID: fixture.runtimeID,
		DaemonID: "daemon-" + fixture.companyID.String(), Generation: uuid.New(),
		ManifestDigest: digest, ObservedAt: time.Now().UTC(),
		Items: []PluginObservationInput{item},
	}
	result, err := service.PublishDiscovery(ctx, fixture.principal, input)
	if err != nil || !result.Applied {
		t.Fatalf("PublishDiscovery() = %+v, %v", result, err)
	}

	var pluginID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id FROM plugin_catalog_items
		WHERE company_id = $1 AND native_id = 'github-plugin'`,
		fixture.companyID,
	).Scan(&pluginID); err != nil {
		t.Fatalf("read persisted Plugin: %v", err)
	}
	if pluginID == uuid.Nil || pluginID.Version() != 4 {
		t.Fatalf("persisted Plugin id = %s", pluginID)
	}

	page, err := service.List(ctx, fixture.principal, PluginListInput{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page.Plugins) != 1 || page.Plugins[0].ID != pluginID ||
		len(page.Plugins[0].ConnectorIdentities) != 1 ||
		page.Plugins[0].ConnectorIdentities[0].NativeID != "github" {
		t.Fatalf("List() = %+v", page)
	}
	var connectorPolicies int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connector_policies`).Scan(&connectorPolicies); err != nil {
		t.Fatalf("count Connector policies: %v", err)
	}
	if connectorPolicies != 0 {
		t.Fatalf("Plugin discovery created %d Connector policies", connectorPolicies)
	}
	if replay, err := service.PublishDiscovery(ctx, fixture.principal, input); err != nil || replay.Applied {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if len(changes) != 1 || changes[0].Name != EventRuntimeCapabilitiesChanged {
		t.Fatalf("plugin discovery events = %+v", changes)
	}
	change, err := events.PayloadOf[CapabilitiesChanged](changes[0])
	if err != nil || change.UserID != fixture.principal.UserID || change.RuntimeID != fixture.runtimeID ||
		change.Catalog != "plugin" {
		t.Fatalf("plugin discovery routing = %+v, %v", change, err)
	}
}

func TestPluginDiscoveryRechecksMembershipAfterCompanyWait(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewPluginService(store, mustAgentTestBus(t))
	if err != nil {
		t.Fatal(err)
	}

	item := PluginObservationInput{
		NativeID: "github-plugin", Name: "GitHub Plugin", Status: PluginStatusInstalled,
		Installed: true, Enabled: true, Action: PluginActionNone,
		ComponentCounts: PluginComponentCounts{Apps: 1},
	}
	canonical, err := normalizePluginObservation(ConnectorProviderCodex, item)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest, err := pluginManifestDigest([]PluginObservationInput{canonical})
	if err != nil {
		t.Fatal(err)
	}
	input := PluginDiscoveryInput{
		CompanyID: fixture.companyID, RuntimeID: fixture.runtimeID,
		DaemonID: "daemon-" + fixture.companyID.String(), Generation: uuid.New(),
		ManifestDigest: manifestDigest, ObservedAt: time.Now().UTC(),
		Items: []PluginObservationInput{item},
	}

	holder, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin Plugin discovery membership race: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := holder.Exec(
		ctx,
		`SELECT id FROM companies WHERE id = $1 FOR UPDATE`,
		fixture.companyID,
	); err != nil {
		t.Fatalf("lock Company before Plugin discovery membership race: %v", err)
	}
	if _, err := holder.Exec(ctx, `
		UPDATE company_members
		SET status = 'revoked', revoked_at = clock_timestamp(),
			revoked_by_user_id = user_id, updated_at = clock_timestamp(),
			version = version + 1
		WHERE company_id = $1 AND user_id = $2`,
		fixture.companyID,
		fixture.userID,
	); err != nil {
		t.Fatalf("revoke Plugin discovery publisher during race: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		_, publishErr := service.PublishDiscovery(ctx, fixture.principal, input)
		result <- publishErr
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity
				WHERE pid <> pg_backend_pid()
				  AND datname = current_database()
				  AND state = 'active'
				  AND wait_event_type = 'Lock'
				  AND query LIKE '%LockCompanyForMembershipMutation%'
			)`).Scan(&waiting); err != nil {
			t.Fatalf("observe blocked Plugin discovery publication: %v", err)
		}
		if waiting {
			break
		}
		select {
		case publishErr := <-result:
			t.Fatalf("PublishDiscovery() returned before Company mutation committed: %v", publishErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("PublishDiscovery() did not reach the held Company lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatalf("commit Plugin discovery membership race: %v", err)
	}
	select {
	case publishErr := <-result:
		if !errors.Is(publishErr, ErrRuntimeNotFound) {
			t.Fatalf(
				"PublishDiscovery() after membership revocation error = %v, want ErrRuntimeNotFound",
				publishErr,
			)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("PublishDiscovery() did not resume after membership revocation")
	}

	var generationCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM plugin_discovery_generations
		WHERE runtime_id = $1`, fixture.runtimeID).Scan(&generationCount); err != nil {
		t.Fatalf("count Plugin generations after rejected stale principal: %v", err)
	}
	if generationCount != 0 {
		t.Fatalf("Plugin generations after rejected stale principal = %d, want 0", generationCount)
	}
}

func TestPluginCatalogRejectsRuntimeWithIneligibleOwner(t *testing.T) {
	tests := []struct {
		name     string
		mutation string
	}{
		{
			name: "revoked membership",
			mutation: `UPDATE company_members
				SET status = 'revoked', revoked_at = clock_timestamp(),
				    revoked_by_user_id = user_id
				WHERE company_id = $1 AND user_id = $2`,
		},
		{
			name: "disabled account",
			mutation: `UPDATE users
				SET disabled_at = clock_timestamp()
				WHERE id = $2
				  AND EXISTS (
				      SELECT 1 FROM company_members
				      WHERE company_id = $1 AND user_id = $2
				  )`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool, store := startAgentPostgres(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			fixture := seedAgentCompany(t, ctx, pool, "owner")
			service, err := NewPluginService(store, mustAgentTestBus(t))
			if err != nil {
				t.Fatal(err)
			}

			runtimeOwnerID := uuid.Must(uuid.NewV7())
			mustAgentExec(t, ctx, pool,
				`INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Runtime Owner')`,
				runtimeOwnerID,
				runtimeOwnerID.String()+"@zenith-team.test",
			)
			mustAgentExec(t, ctx, pool, `
				INSERT INTO company_members (company_id, user_id, role)
				VALUES ($1, $2, 'member')`, fixture.companyID, runtimeOwnerID)
			mustAgentExec(t, ctx, pool,
				`UPDATE runtimes SET owner_user_id = $2 WHERE id = $1`,
				fixture.runtimeID,
				runtimeOwnerID,
			)
			mustAgentExec(t, ctx, pool, test.mutation, fixture.companyID, runtimeOwnerID)

			_, err = service.List(ctx, fixture.principal, PluginListInput{})
			if !errors.Is(err, ErrPluginProviderUnavailable) {
				t.Fatalf("List() error = %v, want ErrPluginProviderUnavailable", err)
			}
		})
	}
}

// Same consent rule as the Connector catalog: another member's private
// registration is not a Plugin discovery context until it is shared.
func TestPluginCatalogSelectsAnotherMembersRuntimeOnlyWhenShared(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewPluginService(store, mustAgentTestBus(t))
	if err != nil {
		t.Fatal(err)
	}

	registrantID := uuid.Must(uuid.NewV7())
	mustAgentExec(t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Registrant')`,
		registrantID,
		registrantID.String()+"@zenith-team.test",
	)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`, fixture.companyID, registrantID)
	mustAgentExec(t, ctx, pool,
		`UPDATE runtimes SET owner_user_id = $2 WHERE id = $1`,
		fixture.runtimeID,
		registrantID,
	)

	// Named explicitly, another member's machine is not found, exactly like
	// on the runtime routes (ADR 0014); left to the default it is simply
	// not a candidate.
	explicit := PluginListInput{RuntimeID: &fixture.runtimeID}
	if _, err := service.List(ctx, fixture.principal, explicit); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("List() on another member's runtime error = %v, want ErrRuntimeNotFound", err)
	}
	if _, err := service.List(
		ctx,
		fixture.principal,
		PluginListInput{},
	); !errors.Is(
		err,
		ErrPluginProviderUnavailable,
	) {
		t.Fatalf("List() with no runtime of the caller's error = %v, want ErrPluginProviderUnavailable", err)
	}

	// The machine comes back to the caller: their own registration is the
	// only discovery context there is (ADR 0014).
	mustAgentExec(t, ctx, pool,
		`UPDATE runtimes SET owner_user_id = $2 WHERE id = $1`,
		fixture.runtimeID,
		fixture.userID,
	)
	result, err := service.List(ctx, fixture.principal, explicit)
	if err != nil {
		t.Fatalf("List() on the caller's own runtime error = %v", err)
	}
	if result.Runtime.ID != fixture.runtimeID {
		t.Fatalf("List() selected runtime %s, want the caller's %s", result.Runtime.ID, fixture.runtimeID)
	}
}

func TestRuntimeCatalogsHonorExplicitRuntimeWithoutAnAssignedAgent(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	otherCompany := seedAgentCompany(t, ctx, pool, "owner")
	var unboundRuntime uuid.UUID
	if err := pool.QueryRow(ctx, `
  INSERT INTO runtimes (company_id, daemon_id, name, provider, status, owner_user_id)
  VALUES ($1, 'second-computer', 'Second computer', 'codex', 'offline', $2)
  RETURNING id`, fixture.companyID, fixture.userID).Scan(&unboundRuntime); err != nil {
		t.Fatal(err)
	}
	plugins, err := NewPluginService(store, mustAgentTestBus(t))
	if err != nil {
		t.Fatal(err)
	}
	connectors, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}
	item := PluginObservationInput{
		NativeID:        "first-computer-plugin",
		Name:            "First computer plugin",
		Status:          PluginStatusInstalled,
		Installed:       true,
		Enabled:         true,
		Action:          PluginActionNone,
		ComponentCounts: PluginComponentCounts{Skills: 1},
	}
	canonical, err := normalizePluginObservation(ConnectorProviderCodex, item)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := pluginManifestDigest([]PluginObservationInput{canonical})
	if err != nil {
		t.Fatal(err)
	}
	// The catalog reports when its Runtime last reported, and nothing for a
	// Runtime that never has.
	observedAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	_, err = plugins.PublishDiscovery(
		ctx,
		fixture.principal,
		PluginDiscoveryInput{
			CompanyID:      fixture.companyID,
			RuntimeID:      fixture.runtimeID,
			DaemonID:       "daemon-" + fixture.companyID.String(),
			Generation:     uuid.New(),
			ManifestDigest: digest,
			ObservedAt:     observedAt,
			Items:          []PluginObservationInput{item},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	// The caller's own runtime under another provider is the catalog's
	// provider conflict; a runtime_id that is not the caller's at all is
	// not found, like on every runtime route (ADR 0014).
	for _, tc := range []struct {
		name            string
		runtimeID       *uuid.UUID
		provider        ConnectorProvider
		wantID          uuid.UUID
		wantUnavailable bool
		wantNotFound    bool
		wantPlugins     int
		wantObservedAt  *time.Time
	}{
		{name: "existing default", wantID: fixture.runtimeID, wantPlugins: 1, wantObservedAt: &observedAt},
		{name: "explicit first", runtimeID: &fixture.runtimeID, wantID: fixture.runtimeID, wantPlugins: 1, wantObservedAt: &observedAt},
		{name: "unbound offline computer", runtimeID: &unboundRuntime, wantID: unboundRuntime},
		{name: "provider mismatch", runtimeID: &unboundRuntime, provider: ConnectorProviderClaude, wantUnavailable: true},
		{name: "other company", runtimeID: &otherCompany.runtimeID, wantNotFound: true},
		{name: "other company with a provider", runtimeID: &otherCompany.runtimeID, provider: ConnectorProviderCodex, wantNotFound: true},
		{name: "missing runtime", runtimeID: new(uuid.New()), wantNotFound: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pluginPage, pluginErr := plugins.List(
				ctx,
				fixture.principal,
				PluginListInput{RuntimeID: tc.runtimeID, Provider: tc.provider},
			)
			connectorPage, connectorErr := connectors.List(
				ctx,
				fixture.principal,
				ConnectorListInput{RuntimeID: tc.runtimeID, Provider: tc.provider},
			)
			if tc.wantUnavailable {
				if !errors.Is(pluginErr, ErrPluginProviderUnavailable) ||
					!errors.Is(connectorErr, ErrConnectorProviderUnavailable) {
					t.Fatalf("catalog errors = %v / %v, want unavailable", pluginErr, connectorErr)
				}
				return
			}
			if tc.wantNotFound {
				if !errors.Is(pluginErr, ErrRuntimeNotFound) ||
					!errors.Is(connectorErr, ErrRuntimeNotFound) {
					t.Fatalf(
						"catalog errors = %v / %v, want ErrRuntimeNotFound",
						pluginErr,
						connectorErr,
					)
				}
				return
			}
			if pluginErr != nil || connectorErr != nil {
				t.Fatalf("catalog errors = %v / %v, want nil", pluginErr, connectorErr)
			}
			if pluginPage.Runtime == nil || pluginPage.Runtime.ID != tc.wantID ||
				len(pluginPage.Plugins) != tc.wantPlugins {
				t.Fatalf(
					"plugin catalog = %+v, want runtime %s with %d plugins",
					pluginPage,
					tc.wantID,
					tc.wantPlugins,
				)
			}
			if (pluginPage.ObservedAt == nil) != (tc.wantObservedAt == nil) ||
				(tc.wantObservedAt != nil && !pluginPage.ObservedAt.Equal(*tc.wantObservedAt)) {
				t.Fatalf(
					"plugin catalog observedAt = %v, want %v",
					pluginPage.ObservedAt,
					tc.wantObservedAt,
				)
			}
			if connectorPage.Runtime == nil || connectorPage.Runtime.ID != tc.wantID {
				t.Fatalf("connector runtime = %+v, want %s", connectorPage.Runtime, tc.wantID)
			}
		})
	}
}

// A plain member refreshes the catalogs of the computer they registered, as
// the registrant (ADR 0014): no plugins.manage or connectors.manage is asked.
// The cloud cannot reach the machine, so the answer is the same "action
// unavailable" an owner gets, never a role refusal. With no runtime of their
// own there is nothing to refresh.
func TestCatalogRefreshIsTheRegistrantsWhateverTheirRole(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "member")
	member := fixture.principal
	member.Role = auth.CompanyRoleMember
	plugins, err := NewPluginService(store, mustAgentTestBus(t))
	if err != nil {
		t.Fatal(err)
	}
	connectors, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}

	if err := plugins.Refresh(ctx, member); !errors.Is(err, ErrPluginActionUnavailable) {
		t.Fatalf("Plugin Refresh() by the registrant error = %v, want ErrPluginActionUnavailable", err)
	}
	if err := connectors.Refresh(ctx, member); !errors.Is(err, ErrConnectorActionUnavailable) {
		t.Fatalf("Connector Refresh() by the registrant error = %v, want ErrConnectorActionUnavailable", err)
	}

	// The machine now belongs to someone else: the member has no computer
	// of their own, so there is no catalog to refresh.
	otherID := uuid.Must(uuid.NewV7())
	mustAgentExec(t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Other')`,
		otherID, otherID.String()+"@zenith-team.test",
	)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'owner')`, fixture.companyID, otherID)
	mustAgentExec(t, ctx, pool,
		`UPDATE runtimes SET owner_user_id = $2 WHERE id = $1`,
		fixture.runtimeID, otherID,
	)
	if err := plugins.Refresh(ctx, member); !errors.Is(err, ErrPluginProviderUnavailable) {
		t.Fatalf(
			"Plugin Refresh() with no runtime of the caller's error = %v, want ErrPluginProviderUnavailable",
			err,
		)
	}
	if err := connectors.Refresh(ctx, member); !errors.Is(err, ErrConnectorProviderUnavailable) {
		t.Fatalf(
			"Connector Refresh() with no runtime of the caller's error = %v, want ErrConnectorProviderUnavailable",
			err,
		)
	}
}

// Codex lists the Claude marketplace's copies of many curated Plugins without
// a logo or description. The catalog leaves out a Plugin of a
// marketplace outside OpenAI's that is not installed when the same Runtime
// observes an openai-curated-remote Plugin with the same name, ignoring case,
// spaces and punctuation, that is installed or can be installed, and
// something besides the name agrees: the slug, a curated slug that is an
// opaque app id, the developer, or a website host other than a code hosting
// site's. Its counts and pages leave out exactly those rows, and the Plugin's
// page still reads it. OpenAI's own marketplaces, installed copies, copies
// whose twin cannot be installed, another product that only shares the name,
// another Runtime's curated Plugin and Claude catalogs keep theirs.
func TestPluginCatalogLeavesOutCuratedDuplicates(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewPluginService(store, mustAgentTestBus(t))
	if err != nil {
		t.Fatal(err)
	}
	daemonID := "daemon-" + fixture.companyID.String()
	available := func(nativeID, name, category string) PluginObservationInput {
		return PluginObservationInput{
			NativeID: nativeID, Name: name, Category: category,
			Status: PluginStatusAvailable, Action: PluginActionInstall,
		}
	}
	installed := func(nativeID, name, category string) PluginObservationInput {
		return PluginObservationInput{
			NativeID: nativeID, Name: name, Category: category,
			Status: PluginStatusInstalled, Installed: true, Enabled: true, Action: PluginActionUninstall,
		}
	}
	published := func(nativeID, name, website, developer string) PluginObservationInput {
		item := available(nativeID, name, "Developer Tools")
		item.WebsiteURL, item.DeveloperName = website, developer
		return item
	}
	unavailable := published("app-6a69a1fd1a3c81919924cbac1cb59d63@openai-curated-remote", "Pigment", "", "")
	unavailable.Status, unavailable.Action = PluginStatusError, PluginActionNone
	publishTestPlugins(t, ctx, store, fixture.principal, fixture.runtimeID, daemonID, []PluginObservationInput{
		installed("documents@openai-primary-runtime", "Documents", "Productivity"),
		available("browser@openai-bundled", "Browser", "Productivity"),
		available("canva@claude-plugins-official", "canva", "Creativity"),
		available("adobe-for-creativity@claude-plugins-official", "adobe-for-creativity", "Creativity"),
		installed("figma@claude-plugins-official", "figma", "Creativity"),
		available("notion@claude-plugins-official", "notion", "Productivity"),
		available("hyperframes@claude-plugins-official", "hyperframes", "Creativity"),
		available("plus@claude-plugins-official", "+++", "Productivity"),
		// Its only twin cannot be installed.
		published("pigment@claude-plugins-official", "pigment", "", ""),
		// Only the name agrees.
		published("data@claude-plugins-official", "Data", "https://github.com/astronomer/agents", "Astronomer"),
		published("neon@claude-plugins-official", "Neon", "https://github.com/neondatabase/skills", ""),
		// The twin's slug is an opaque app id.
		published("aiven@claude-plugins-official", "aiven", "https://github.com/aiven/skills", "Aiven"),
		// The developer agrees.
		published("boltz@claude-plugins-official", "boltz", "https://boltz.bio", "Boltz"),
		// The website's host agrees.
		published("rootly-tools@claude-plugins-official", "Rootly", "https://rootly.com/docs", ""),
		available("canva@team-marketplace", "Canva!", "Creativity"),
		available("zapier", "Zapier", "Productivity"),
		available("canva@openai-curated-remote", "Canva", "Creativity"),
		available("adobe-for-creativity@openai-curated-remote", "Adobe for Creativity", "Creativity"),
		available("figma@openai-curated-remote", "Figma", "Creativity"),
		available("browser@openai-curated-remote", "Browser", "Productivity"),
		available("stars@openai-curated-remote", "***", "Productivity"),
		available("zapier@openai-curated-remote", "Zapier", "Productivity"),
		unavailable,
		published("data-analytics@openai-curated-remote", "Data", "https://openai.com/", "OpenAI"),
		published("neon-postgres@openai-curated-remote", "Neon", "https://github.com/neondatabase",
			"Databricks Inc."),
		published("app-6a3004c9cc2481918ff93adfd94559eb@openai-curated-remote", "Aiven",
			"https://aiven.io", "Aiven Oy"),
		published("boltz-api-cli@openai-curated-remote", "Boltz", "https://docs.boltz.bio", "Boltz"),
		published("rootly-incident@openai-curated-remote", "Rootly", "https://www.rootly.com",
			"Rootly Inc."),
	})
	// Notion's curated Plugin is on another computer only.
	secondRuntime := uuid.Must(uuid.NewV7())
	mustAgentExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id)
		VALUES ($1, $2, 'second-daemon', 'Second computer', 'codex', 'online', $3)`,
		secondRuntime, fixture.companyID, fixture.userID)
	publishTestPlugins(t, ctx, store, fixture.principal, secondRuntime, "second-daemon",
		[]PluginObservationInput{available("notion@openai-curated-remote", "Notion", "Productivity")})

	wantListed := []string{
		"documents@openai-primary-runtime", "browser@openai-bundled", "figma@claude-plugins-official",
		"notion@claude-plugins-official", "hyperframes@claude-plugins-official",
		"plus@claude-plugins-official", "pigment@claude-plugins-official",
		"data@claude-plugins-official", "neon@claude-plugins-official", "zapier",
		"canva@openai-curated-remote", "adobe-for-creativity@openai-curated-remote",
		"figma@openai-curated-remote", "browser@openai-curated-remote", "stars@openai-curated-remote",
		"zapier@openai-curated-remote", "app-6a69a1fd1a3c81919924cbac1cb59d63@openai-curated-remote",
		"data-analytics@openai-curated-remote", "neon-postgres@openai-curated-remote",
		"app-6a3004c9cc2481918ff93adfd94559eb@openai-curated-remote",
		"boltz-api-cli@openai-curated-remote", "rootly-incident@openai-curated-remote",
	}
	list := func(input PluginListInput) PluginCatalogResult {
		t.Helper()
		input.RuntimeID = &fixture.runtimeID
		page, err := service.List(ctx, fixture.principal, input)
		if err != nil {
			t.Fatalf("List(%+v) error = %v", input, err)
		}
		return page
	}
	nativeIDs := func(plugins []PluginItemV1) []string {
		ids := make([]string, len(plugins))
		for index, plugin := range plugins {
			ids[index] = plugin.NativeID
		}
		return ids
	}

	page := list(PluginListInput{})
	if got := nativeIDs(page.Plugins); !reflect.DeepEqual(got, wantListed) {
		t.Fatalf("listed = %v\nwant %v", got, wantListed)
	}
	wantCategories := []PluginCategoryCount{
		{Category: "Productivity", Count: 8},
		{Category: "Creativity", Count: 5},
		{Category: "Developer Tools", Count: 9},
	}
	if page.TotalCount != int64(len(wantListed)) || page.InstalledCount != 2 ||
		!reflect.DeepEqual(page.CategoryCounts, wantCategories) || page.NextCursor != nil {
		t.Fatalf("counts = total %d, installed %d, categories %+v, next %v",
			page.TotalCount, page.InstalledCount, page.CategoryCounts, page.NextCursor)
	}

	// Seek pages of three walk the same rows.
	var paged []string
	var cursor *PluginCursor
	for pages := 0; ; pages++ {
		if pages > len(wantListed) {
			t.Fatal("List() pages do not end")
		}
		next := list(PluginListInput{Limit: 3, Cursor: cursor})
		if next.TotalCount != page.TotalCount || !reflect.DeepEqual(next.CategoryCounts, wantCategories) {
			t.Fatalf("page counts = %d / %+v", next.TotalCount, next.CategoryCounts)
		}
		paged = append(paged, nativeIDs(next.Plugins)...)
		if next.NextCursor == nil {
			break
		}
		if cursor, err = ParsePluginCursor(*next.NextCursor); err != nil {
			t.Fatalf("ParsePluginCursor() error = %v", err)
		}
	}
	if !reflect.DeepEqual(paged, wantListed) {
		t.Fatalf("paged = %v\nwant %v", paged, wantListed)
	}

	// A search and a category read the same catalog.
	if got := nativeIDs(list(PluginListInput{Query: "canva"}).Plugins); !reflect.DeepEqual(got,
		[]string{"canva@openai-curated-remote"}) {
		t.Fatalf("search canva = %v", got)
	}
	if got := nativeIDs(list(PluginListInput{Category: "Creativity"}).Plugins); !reflect.DeepEqual(got, []string{
		"figma@claude-plugins-official", "hyperframes@claude-plugins-official",
		"canva@openai-curated-remote", "adobe-for-creativity@openai-curated-remote",
		"figma@openai-curated-remote",
	}) {
		t.Fatalf("Creativity = %v", got)
	}

	// A link to a left-out duplicate still opens its page.
	hiddenID := testPluginID(t, ctx, pool, fixture.companyID, "canva@claude-plugins-official")
	detail, err := service.Get(ctx, fixture.principal, PluginDetailInput{PluginID: hiddenID})
	if err != nil || detail.Plugin.ID != hiddenID || detail.Plugin.Name != "canva" {
		t.Fatalf("Get(left-out duplicate) = %+v, %v", detail.Plugin, err)
	}

	// Installing the copy lists it again.
	publishTestPlugins(t, ctx, store, fixture.principal, fixture.runtimeID, daemonID, []PluginObservationInput{
		installed("canva@claude-plugins-official", "canva", "Creativity"),
		available("canva@openai-curated-remote", "Canva", "Creativity"),
	})
	if got := list(PluginListInput{}); !reflect.DeepEqual(nativeIDs(got.Plugins), []string{
		"canva@claude-plugins-official", "canva@openai-curated-remote",
	}) || got.TotalCount != 2 || got.InstalledCount != 1 {
		t.Fatalf("after install = %v (%d, %d)", nativeIDs(got.Plugins), got.TotalCount, got.InstalledCount)
	}

	// A Claude catalog is never deduplicated.
	claudeRuntime := insertTestClaudeRuntime(t, ctx, pool, fixture)
	publishTestProviderPlugins(t, ctx, store, fixture.principal, claudeRuntime, "claude-daemon",
		ConnectorProviderClaude, []PluginObservationInput{
			available("canva@claude-plugins-official", "canva", "Creativity"),
			available("canva@openai-curated-remote", "Canva", "Creativity"),
		})
	claude, err := service.List(ctx, fixture.principal, PluginListInput{RuntimeID: &claudeRuntime})
	if err != nil || claude.TotalCount != 2 || len(claude.Plugins) != 2 {
		t.Fatalf("Claude catalog = %+v, %v", claude, err)
	}
}
