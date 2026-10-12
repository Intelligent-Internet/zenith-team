//go:build integration

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	provideragent "boardly.dev/boardly/server/pkg/agent"
)

// publishTestProviderPlugins publishes one complete Plugin generation for a
// Runtime of the given provider.
func publishTestProviderPlugins(
	t *testing.T,
	ctx context.Context,
	store *db.Store,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
	daemonID string,
	provider ConnectorProvider,
	items []PluginObservationInput,
) {
	t.Helper()
	service, err := NewPluginService(store, mustAgentTestBus(t))
	if err != nil {
		t.Fatal(err)
	}
	canonical := make([]PluginObservationInput, len(items))
	for index := range items {
		items[index].CatalogRank = index
		normalized, err := normalizePluginObservation(provider, items[index])
		if err != nil {
			t.Fatalf("normalize Plugin %q: %v", items[index].NativeID, err)
		}
		canonical[index] = normalized
	}
	digest, err := pluginManifestDigest(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := service.PublishDiscovery(ctx, principal, PluginDiscoveryInput{
		CompanyID: principal.CompanyID, RuntimeID: runtimeID, DaemonID: daemonID,
		Generation: uuid.New(), ManifestDigest: digest, ObservedAt: time.Now().UTC(), Items: items,
	}); err != nil || !result.Applied {
		t.Fatalf("Plugin PublishDiscovery() = %+v, %v", result, err)
	}
}

func testPluginID(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID uuid.UUID,
	nativeID string,
) uuid.UUID {
	t.Helper()
	var pluginID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM plugin_catalog_items WHERE company_id = $1 AND native_id = $2`,
		companyID, nativeID).Scan(&pluginID); err != nil {
		t.Fatalf("read Plugin %s: %v", nativeID, err)
	}
	return pluginID
}

func insertTestClaudeRuntime(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture agentCompanyFixture,
) uuid.UUID {
	t.Helper()
	var runtimeID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO runtimes (company_id, daemon_id, name, provider, status, owner_user_id)
		VALUES ($1, 'claude-daemon', 'Claude computer', 'claude', 'online', $2)
		RETURNING id`, fixture.companyID, fixture.userID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert Claude Runtime: %v", err)
	}
	return runtimeID
}

// joinTestCompany adds an active member of the fixture Company in role.
func joinTestCompany(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture agentCompanyFixture,
	role auth.CompanyRole,
) auth.UserPrincipal {
	t.Helper()
	userID := uuid.Must(uuid.NewV7())
	mustAgentExec(t, ctx, pool, `INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Colleague')`,
		userID, userID.String()+"@boardly.test")
	mustAgentExec(t, ctx, pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, $3)`,
		fixture.companyID, userID, string(role))
	return auth.UserPrincipal{
		UserID: userID, SessionID: uuid.Must(uuid.NewV7()), AuthKind: auth.TokenKindBearer,
		CompanyID: fixture.companyID, Role: role,
	}
}

// insertTestOwnRuntime registers an online computer of provider for owner.
func insertTestOwnRuntime(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID, ownerID uuid.UUID,
	daemonID, provider string,
) uuid.UUID {
	t.Helper()
	runtimeID := uuid.Must(uuid.NewV7())
	mustAgentExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, $3, $3, $4, 'online', $5, clock_timestamp())`,
		runtimeID, companyID, daemonID, provider, ownerID)
	return runtimeID
}

func testText(value string) *string { return &value }

// testJSON renders a value, pointers included, for a failure message.
func testJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// The Codex presentation a daemon publishes is stored as sent and read back on
// the Plugin's page; a republication without it clears it.
func TestPluginDiscoveryRoundTripsTheCodexPresentation(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewPluginService(store, mustAgentTestBus(t))
	if err != nil {
		t.Fatal(err)
	}
	daemonID := "daemon-" + fixture.companyID.String()
	mustAgentExec(t, ctx, pool, `UPDATE runtimes SET last_seen_at = clock_timestamp() WHERE id = $1`,
		fixture.runtimeID)
	presented := PluginObservationInput{
		NativeID: testGmailPlugin, Name: "Gmail", Status: PluginStatusInstalled, Installed: true,
		Enabled: true, Action: PluginActionUninstall, IconURL: "https://cdn.test/gmail.png",
		LongDescription: "Read mail.\n\nDraft replies.",
		DefaultPrompts:  []string{"Summarize my inbox", "Draft a reply"},
		BrandColor:      "#EA4335", DeveloperName: "Google",
		WebsiteURL:        "https://mail.google.com",
		PrivacyPolicyURL:  "https://policies.google.com/privacy",
		TermsOfServiceURL: "https://policies.google.com/terms",
	}
	publishTestPlugins(t, ctx, store, fixture.principal, fixture.runtimeID, daemonID,
		[]PluginObservationInput{presented})
	pluginID := testPluginID(t, ctx, pool, fixture.companyID, testGmailPlugin)

	var prompts []byte
	var brand string
	if err := pool.QueryRow(ctx, `
		SELECT default_prompts, brand_color FROM plugin_runtime_observations
		WHERE runtime_id = $1 AND plugin_id = $2`, fixture.runtimeID, pluginID).Scan(&prompts, &brand); err != nil {
		t.Fatalf("read stored presentation: %v", err)
	}
	var storedPrompts []string
	if err := json.Unmarshal(prompts, &storedPrompts); err != nil ||
		!reflect.DeepEqual(storedPrompts, presented.DefaultPrompts) || brand != "#EA4335" {
		t.Fatalf("stored presentation = %s / %s (%v)", prompts, brand, err)
	}

	detail, err := service.Get(ctx, fixture.principal, PluginDetailInput{PluginID: pluginID})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if detail.SchemaVersion != 1 || detail.Plugin.ID != pluginID || detail.Plugin.Name != "Gmail" ||
		detail.Plugin.IconURL == nil || *detail.Plugin.IconURL != "https://cdn.test/gmail.png" ||
		detail.Runtime == nil || detail.Runtime.ID != fixture.runtimeID || detail.Stale ||
		!reflect.DeepEqual(detail.LongDescription, testText(presented.LongDescription)) ||
		!reflect.DeepEqual(detail.DefaultPrompts, presented.DefaultPrompts) ||
		!reflect.DeepEqual(detail.BrandColor, testText("#EA4335")) ||
		!reflect.DeepEqual(detail.DeveloperName, testText("Google")) ||
		!reflect.DeepEqual(detail.WebsiteURL, testText(presented.WebsiteURL)) ||
		!reflect.DeepEqual(detail.PrivacyPolicyURL, testText(presented.PrivacyPolicyURL)) ||
		!reflect.DeepEqual(detail.TermsOfServiceURL, testText(presented.TermsOfServiceURL)) {
		t.Fatalf("Get() = %+v", detail)
	}

	// An older daemon publishes the same Plugin without the presentation.
	publishTestPlugins(t, ctx, store, fixture.principal, fixture.runtimeID, daemonID, []PluginObservationInput{{
		NativeID: testGmailPlugin, Name: "Gmail", Status: PluginStatusInstalled, Installed: true,
		Enabled: true, Action: PluginActionUninstall,
	}})
	detail, err = service.Get(ctx, fixture.principal, PluginDetailInput{PluginID: pluginID})
	if err != nil {
		t.Fatalf("Get() after an older daemon error = %v", err)
	}
	if detail.LongDescription != nil || detail.DefaultPrompts == nil || len(detail.DefaultPrompts) != 0 ||
		detail.BrandColor != nil || detail.DeveloperName != nil || detail.WebsiteURL != nil ||
		detail.PrivacyPolicyURL != nil || detail.TermsOfServiceURL != nil {
		t.Fatalf("Get() after an older daemon = %+v", detail)
	}
}

// A Plugin's page resolves its apps against the selected Runtime's Connector
// observations and lists the Agents that can use it: unarchived, bound there
// by the viewer (ADR 0014), and not having turned it off.
func TestPluginDetailResolvesAppsAndAgentUse(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	other := seedAgentCompany(t, ctx, pool, "owner")
	plugins, err := NewPluginService(store, mustAgentTestBus(t))
	if err != nil {
		t.Fatal(err)
	}
	connectors, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}
	daemonID := "daemon-" + fixture.companyID.String()

	gmail := executableTestConnector(testGmailID, "Gmail")
	gmail.IconURL = testText("https://cdn.test/gmail-app.png")
	gmail.Description = testText("Email")
	publishTestConnectors(t, ctx, connectors, fixture.principal, fixture.runtimeID, daemonID,
		[]ConnectorObservationInput{gmail, {
			Source: ConnectorSourceCodexApp, NativeID: testCalendarID, Name: "Google Calendar",
			Status: ConnectorStatusNeedsAuth, Action: ConnectorActionAuthenticate,
		}})
	publishTestPlugins(t, ctx, store, fixture.principal, fixture.runtimeID, daemonID, []PluginObservationInput{
		{
			NativeID: "google-workspace@openai-curated-remote", Name: "Google Workspace",
			Status: PluginStatusInstalled, Installed: true, Enabled: true, Action: PluginActionUninstall,
			ComponentCounts: PluginComponentCounts{Apps: 3},
			ConnectorIdentities: []PluginConnectorIdentity{
				{Source: ConnectorSourceCodexApp, NativeID: testGmailID},
				{Source: ConnectorSourceCodexApp, NativeID: testCalendarID},
				{Source: ConnectorSourceCodexApp, NativeID: "connector_never_reported"},
			},
		},
		{
			NativeID: testGitHubPlugin, Name: "GitHub", Status: PluginStatusAvailable,
			Action: PluginActionInstall,
		},
	})
	workspaceID := testPluginID(t, ctx, pool, fixture.companyID, "google-workspace@openai-curated-remote")
	githubID := testPluginID(t, ctx, pool, fixture.companyID, testGitHubPlugin)

	// Agents the owner runs: the root (uses it), one that turned it off, an
	// archived one, and one on another computer.
	// The default selection prefers the computer seen most recently.
	secondRuntime := uuid.Must(uuid.NewV7())
	mustAgentExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, 'second-daemon', 'Second computer', 'codex', 'online', $3,
			clock_timestamp() - interval '1 minute')`,
		secondRuntime, fixture.companyID, fixture.userID)
	mustAgentExec(t, ctx, pool, `UPDATE runtimes SET last_seen_at = clock_timestamp() WHERE id = $1`,
		fixture.runtimeID)
	analystID, archivedID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	mustAgentExec(t, ctx, pool, `
		INSERT INTO agents (id, company_id, name, title, provider, reports_to_agent_id, disabled_plugin_ids)
		VALUES ($1, $2, 'Analyst', 'Analyst', 'codex', $3, $4)`,
		analystID, fixture.companyID, fixture.rootAgentID, []uuid.UUID{workspaceID})
	mustAgentExec(t, ctx, pool, `
		INSERT INTO agents (id, company_id, name, title, provider, reports_to_agent_id, archived_at)
		VALUES ($1, $2, 'Archived', 'Archived', 'codex', $3, clock_timestamp())`,
		archivedID, fixture.companyID, fixture.rootAgentID)
	for _, agentID := range []uuid.UUID{analystID, archivedID} {
		dbtest.SeedAgentBinding(
			t,
			ctx,
			pool,
			fixture.companyID,
			agentID,
			fixture.userID,
			fixture.runtimeID,
			"codex",
		)
	}
	dbtest.SeedBoundAgent(t, ctx, pool, uuid.Must(uuid.NewV7()), fixture.companyID, "Elsewhere", "codex",
		&fixture.rootAgentID, fixture.userID, secondRuntime)
	assistantID := uuid.Must(uuid.NewV7())
	dbtest.SeedBoundAgent(t, ctx, pool, assistantID, fixture.companyID, "assistant", "codex",
		&fixture.rootAgentID, fixture.userID, fixture.runtimeID)

	detail, err := plugins.Get(ctx, fixture.principal, PluginDetailInput{PluginID: workspaceID})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	connectorID := func(nativeID string) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRow(ctx,
			`SELECT id FROM connector_catalog_items WHERE company_id = $1 AND native_id = $2`,
			fixture.companyID, nativeID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	gmailID, calendarID := connectorID(testGmailID), connectorID(testCalendarID)
	connected, needsAuth := ConnectorStatusConnected, ConnectorStatusNeedsAuth
	none, authenticate := ConnectorActionNone, ConnectorActionAuthenticate
	wantApps := []PluginDetailApp{
		{
			Source:   ConnectorSourceCodexApp,
			NativeID: "connector_never_reported",
			Name:     "connector_never_reported",
		},
		{
			Source: ConnectorSourceCodexApp, NativeID: testGmailID, ConnectorID: &gmailID, Name: "Gmail",
			Description: testText("Email"), IconURL: testText("https://cdn.test/gmail-app.png"),
			Status: &connected, Action: &none,
		},
		{
			Source: ConnectorSourceCodexApp, NativeID: testCalendarID, ConnectorID: &calendarID,
			Name: "Google Calendar", Status: &needsAuth, Action: &authenticate,
		},
	}
	if !reflect.DeepEqual(detail.Apps, wantApps) {
		gotJSON, _ := json.Marshal(detail.Apps)
		wantJSON, _ := json.Marshal(wantApps)
		t.Fatalf("apps = %s\nwant %s", gotJSON, wantJSON)
	}
	wantUses := []PluginAgentUse{
		{AgentID: assistantID, Name: "assistant"},
		{AgentID: fixture.rootAgentID, Name: "CEO"},
	}
	if !reflect.DeepEqual(detail.UsedBy, wantUses) {
		t.Fatalf("usedBy = %+v, want %+v", detail.UsedBy, wantUses)
	}

	// A Plugin that is not installed has no users and no apps.
	available, err := plugins.Get(ctx, fixture.principal, PluginDetailInput{PluginID: githubID})
	if err != nil || len(available.UsedBy) != 0 || len(available.Apps) != 0 || available.Apps == nil ||
		available.UsedBy == nil || available.Plugin.Action != PluginActionInstall {
		t.Fatalf("Get(available) = %+v, %v", available, err)
	}

	// Another computer that never observed the Plugin, another Company's
	// Plugin, an unknown id and another Company's Runtime are all absent.
	for name, input := range map[string]PluginDetailInput{
		"unobserved runtime":    {PluginID: workspaceID, RuntimeID: &secondRuntime},
		"other company plugin":  {PluginID: publishForeignTestPlugin(t, ctx, pool, store, other)},
		"unknown plugin":        {PluginID: uuid.New()},
		"other company runtime": {PluginID: workspaceID, RuntimeID: &other.runtimeID},
	} {
		if _, err := plugins.Get(ctx, fixture.principal, input); !errors.Is(err, ErrPluginNotFound) {
			t.Errorf("%s: Get() error = %v, want ErrPluginNotFound", name, err)
		}
	}

	// The page is a member read of the member's own computer (ADR 0014): a
	// member reads nothing of the owner's, not even when naming it.
	member := joinTestCompany(t, ctx, pool, fixture, auth.CompanyRoleMember)
	for name, input := range map[string]PluginDetailInput{
		"no computer":          {PluginID: workspaceID},
		"the owner's computer": {PluginID: workspaceID, RuntimeID: &fixture.runtimeID},
	} {
		if _, err := plugins.Get(ctx, member, input); !errors.Is(err, ErrPluginNotFound) {
			t.Fatalf("Get(member, %s) error = %v, want ErrPluginNotFound", name, err)
		}
	}
	// On their own computer the member reads the page, and usedBy lists only
	// the Agents they run there.
	memberRuntime := insertTestOwnRuntime(t, ctx, pool, fixture.companyID, member.UserID, "member-daemon", "codex")
	dbtest.SeedAgentBinding(
		t,
		ctx,
		pool,
		fixture.companyID,
		fixture.rootAgentID,
		member.UserID,
		memberRuntime,
		"codex",
	)
	publishTestPlugins(t, ctx, store, member, memberRuntime, "member-daemon", []PluginObservationInput{{
		NativeID: "google-workspace@openai-curated-remote", Name: "Google Workspace",
		Status: PluginStatusInstalled, Installed: true, Enabled: true, Action: PluginActionUninstall,
	}})
	memberDetail, err := plugins.Get(ctx, member, PluginDetailInput{PluginID: workspaceID})
	if err != nil {
		t.Fatalf("Get(member) error = %v", err)
	}
	if memberDetail.Runtime == nil || memberDetail.Runtime.ID != memberRuntime ||
		!reflect.DeepEqual(memberDetail.UsedBy, []PluginAgentUse{{AgentID: fixture.rootAgentID, Name: "CEO"}}) {
		t.Fatalf("Get(member) = runtime %+v usedBy %+v", memberDetail.Runtime, memberDetail.UsedBy)
	}
}

// publishForeignTestPlugin publishes one Plugin in another Company and
// returns its id.
func publishForeignTestPlugin(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	store *db.Store,
	fixture agentCompanyFixture,
) uuid.UUID {
	t.Helper()
	publishTestPlugins(t, ctx, store, fixture.principal, fixture.runtimeID, "daemon-"+fixture.companyID.String(),
		[]PluginObservationInput{{
			NativeID: "foreign@openai-curated-remote", Name: "Foreign", Status: PluginStatusAvailable,
			Action: PluginActionInstall,
		}})
	return testPluginID(t, ctx, pool, fixture.companyID, "foreign@openai-curated-remote")
}

// A Claude Plugin reports no logo: the catalog, its page and Workflow
// readiness borrow the logo of a Codex Plugin with the same case-insensitive
// name in the Company, else of a Codex app, else none (D26).
func TestClaudePluginsBorrowCodexLogosByName(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	plugins, err := NewPluginService(store, mustAgentTestBus(t))
	if err != nil {
		t.Fatal(err)
	}
	connectors, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}
	codexDaemon := "daemon-" + fixture.companyID.String()

	// The Codex computer observes a Slack Plugin and a Notion app with logos.
	notion := executableTestConnector(testNotionID, "Notion")
	notion.IconURL = testText("https://cdn.test/notion-app.png")
	publishTestConnectors(t, ctx, connectors, fixture.principal, fixture.runtimeID, codexDaemon,
		[]ConnectorObservationInput{notion})
	publishTestPlugins(t, ctx, store, fixture.principal, fixture.runtimeID, codexDaemon, []PluginObservationInput{{
		NativeID: "slack@openai-curated-remote", Name: "Slack", Status: PluginStatusAvailable,
		Action: PluginActionInstall, IconURL: "https://cdn.test/slack-plugin.png",
	}})

	claudeRuntime := insertTestClaudeRuntime(t, ctx, pool, fixture)
	claudeAgentID := uuid.Must(uuid.NewV7())
	dbtest.SeedBoundAgent(t, ctx, pool, claudeAgentID, fixture.companyID, "Claude Agent", "claude",
		&fixture.rootAgentID, fixture.userID, claudeRuntime)
	const claudeSlack, claudeNotion = "slack@claude-plugins-official", "notion@claude-plugins-official"
	const claudeSkillsOnly = "code-simplifier@claude-plugins-official"
	tools := []provideragent.ConnectorTool{{Name: "mcp__plugin_slack_slack", Enabled: true}}
	digest, err := provideragent.ConnectorToolContractDigest(tools)
	if err != nil {
		t.Fatal(err)
	}
	publishTestProviderConnectors(t, ctx, connectors, fixture.principal, claudeRuntime, "claude-daemon",
		ConnectorProviderClaude, []ConnectorObservationInput{{
			Source: ConnectorSourceClaudePlugin, NativeID: claudeSlack, Name: "slack",
			Status: ConnectorStatusConnected, Action: ConnectorActionNone, CanExecute: true,
			ContractDigest: digest, Tools: tools,
		}})
	publishTestProviderPlugins(t, ctx, store, fixture.principal, claudeRuntime, "claude-daemon",
		ConnectorProviderClaude, []PluginObservationInput{
			{
				NativeID:  claudeSlack,
				Name:      "slack",
				Status:    PluginStatusInstalled,
				Installed: true,
				Enabled:   true,
				Action:    PluginActionUninstall,
				ConnectorIdentities: []PluginConnectorIdentity{
					{Source: ConnectorSourceClaudePlugin, NativeID: claudeSlack},
				},
			},
			{
				NativeID: claudeNotion,
				Name:     "NOTION",
				Status:   PluginStatusAvailable,
				Action:   PluginActionInstall,
			},
			{
				NativeID: "unmatched@claude-plugins-official",
				Name:     "Unmatched",
				Status:   PluginStatusAvailable,
				Action:   PluginActionInstall,
			},
			{
				// Every Claude plugin names itself as a Connector identity; one
				// without an MCP server is never a Connector.
				NativeID:  claudeSkillsOnly,
				Name:      "code-simplifier",
				Status:    PluginStatusInstalled,
				Installed: true,
				Enabled:   true,
				Action:    PluginActionUninstall,
				ConnectorIdentities: []PluginConnectorIdentity{
					{Source: ConnectorSourceClaudePlugin, NativeID: claudeSkillsOnly},
				},
			},
		})

	page, err := plugins.List(ctx, fixture.principal, PluginListInput{RuntimeID: &claudeRuntime})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	icons := make(map[string]*string, len(page.Plugins))
	for _, plugin := range page.Plugins {
		icons[plugin.NativeID] = plugin.IconURL
	}
	if !reflect.DeepEqual(icons, map[string]*string{
		claudeSlack:                         testText("https://cdn.test/slack-plugin.png"),
		claudeNotion:                        testText("https://cdn.test/notion-app.png"),
		"unmatched@claude-plugins-official": nil,
		claudeSkillsOnly:                    nil,
	}) {
		t.Fatalf("Claude catalog logos = %v", page.Plugins)
	}

	slackID := testPluginID(t, ctx, pool, fixture.companyID, claudeSlack)
	detail, err := plugins.Get(ctx, fixture.principal, PluginDetailInput{PluginID: slackID})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if detail.Runtime == nil || detail.Runtime.ID != claudeRuntime ||
		!reflect.DeepEqual(detail.Plugin.IconURL, testText("https://cdn.test/slack-plugin.png")) ||
		len(detail.Apps) != 1 || detail.Apps[0].Status == nil ||
		!reflect.DeepEqual(detail.Apps[0].IconURL, testText("https://cdn.test/slack-plugin.png")) ||
		len(detail.UsedBy) != 1 || detail.UsedBy[0].AgentID != claudeAgentID ||
		detail.LongDescription != nil || len(detail.DefaultPrompts) != 0 {
		t.Fatalf("Claude Plugin page = %+v", detail)
	}
	skillsOnly, err := plugins.Get(ctx, fixture.principal, PluginDetailInput{
		PluginID: testPluginID(t, ctx, pool, fixture.companyID, claudeSkillsOnly),
	})
	if err != nil {
		t.Fatalf("Get(skills-only) error = %v", err)
	}
	if len(skillsOnly.Apps) != 0 {
		t.Fatalf("skills-only Claude Plugin apps = %+v, want none", skillsOnly.Apps)
	}

	// Readiness on the owner's binding borrows the same logo.
	slack := readTestReadiness(t, ctx, connectors, fixture.principal, claudeAgentID, "slack", "linear").Apps
	if slack[0].State != ConnectorAppStateReady ||
		!reflect.DeepEqual(slack[0].IconURL, testText("https://cdn.test/slack-plugin.png")) ||
		slack[1].IconURL != nil {
		t.Fatalf("Claude readiness = %+v / %+v", slack[0], slack[1])
	}
}

// A borrowed logo comes only from the viewer's own Runtimes (ADR 0014): the
// owner's Codex computer lends no logo to an admin's Claude catalog or to the
// admin's Workflow readiness; the admin's own Codex computer does.
func TestCodexLogoBorrowingOnlyComesFromTheViewersOwnRuntimes(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	plugins, err := NewPluginService(store, mustAgentTestBus(t))
	if err != nil {
		t.Fatal(err)
	}
	connectors, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}
	notion := executableTestConnector(testNotionID, "Notion")
	notion.IconURL = testText("https://cdn.test/notion-app.png")
	lend := func(principal auth.UserPrincipal, runtimeID uuid.UUID, daemonID string) {
		t.Helper()
		publishTestConnectors(t, ctx, connectors, principal, runtimeID, daemonID,
			[]ConnectorObservationInput{notion})
		publishTestPlugins(t, ctx, store, principal, runtimeID, daemonID, []PluginObservationInput{{
			NativeID: "slack@openai-curated-remote", Name: "Slack", Status: PluginStatusAvailable,
			Action: PluginActionInstall, IconURL: "https://cdn.test/slack-plugin.png",
		}})
	}

	// The owner's Codex computer observes a Slack Plugin and a Notion app
	// with logos.
	lend(fixture.principal, fixture.runtimeID, "daemon-"+fixture.companyID.String())

	// An admin runs a Claude Agent with the Slack Plugin on their own Claude
	// computer.
	admin := joinTestCompany(t, ctx, pool, fixture, auth.CompanyRoleAdmin)
	claudeRuntime := insertTestOwnRuntime(t, ctx, pool, fixture.companyID, admin.UserID, "admin-claude", "claude")
	claudeAgentID := uuid.Must(uuid.NewV7())
	dbtest.SeedBoundAgent(t, ctx, pool, claudeAgentID, fixture.companyID, "Claude Agent", "claude",
		&fixture.rootAgentID, admin.UserID, claudeRuntime)
	const claudeSlack, claudeNotion = "slack@claude-plugins-official", "notion@claude-plugins-official"
	tools := []provideragent.ConnectorTool{{Name: "mcp__plugin_slack_slack", Enabled: true}}
	digest, err := provideragent.ConnectorToolContractDigest(tools)
	if err != nil {
		t.Fatal(err)
	}
	publishTestProviderConnectors(t, ctx, connectors, admin, claudeRuntime, "admin-claude",
		ConnectorProviderClaude, []ConnectorObservationInput{{
			Source: ConnectorSourceClaudePlugin, NativeID: claudeSlack, Name: "slack",
			Status: ConnectorStatusConnected, Action: ConnectorActionNone, CanExecute: true,
			ContractDigest: digest, Tools: tools,
		}})
	publishTestProviderPlugins(t, ctx, store, admin, claudeRuntime, "admin-claude",
		ConnectorProviderClaude, []PluginObservationInput{
			{
				NativeID: claudeSlack, Name: "slack", Status: PluginStatusInstalled, Installed: true,
				Enabled: true, Action: PluginActionUninstall,
				ConnectorIdentities: []PluginConnectorIdentity{
					{Source: ConnectorSourceClaudePlugin, NativeID: claudeSlack},
				},
			},
			{
				NativeID: claudeNotion, Name: "Notion", Status: PluginStatusAvailable,
				Action: PluginActionInstall,
			},
		})

	catalogIcons := func() map[string]*string {
		t.Helper()
		page, err := plugins.List(ctx, admin, PluginListInput{RuntimeID: &claudeRuntime})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		icons := make(map[string]*string, len(page.Plugins))
		for _, plugin := range page.Plugins {
			icons[plugin.NativeID] = plugin.IconURL
		}
		return icons
	}
	readinessIcon := func() *string {
		t.Helper()
		result := readTestReadiness(t, ctx, connectors, admin, claudeAgentID, "slack")
		if result.Discovery.State != ConnectorDiscoveryStateFresh {
			t.Fatalf("readiness on the admin's Claude computer = %+v", result.Discovery)
		}
		return result.Apps[0].IconURL
	}

	// The owner's computer lends the admin nothing.
	if got := catalogIcons(); !reflect.DeepEqual(got, map[string]*string{
		claudeSlack: nil, claudeNotion: nil,
	}) {
		t.Fatalf("admin catalog logos from the owner's computer = %v", got)
	}
	if got := readinessIcon(); got != nil {
		t.Fatalf("admin readiness logo from the owner's computer = %q", *got)
	}

	// The admin's own Codex computer lends its logos.
	adminCodex := insertTestOwnRuntime(t, ctx, pool, fixture.companyID, admin.UserID, "admin-codex", "codex")
	lend(admin, adminCodex, "admin-codex")
	if got := catalogIcons(); !reflect.DeepEqual(got, map[string]*string{
		claudeSlack:  testText("https://cdn.test/slack-plugin.png"),
		claudeNotion: testText("https://cdn.test/notion-app.png"),
	}) {
		t.Fatalf("admin catalog logos from their own computer = %v", got)
	}
	if got := readinessIcon(); !reflect.DeepEqual(got, testText("https://cdn.test/slack-plugin.png")) {
		t.Fatalf("admin readiness logo from their own computer = %v", got)
	}
}

// Workflow readiness carries the winning Connector's logo to every member
// from their own binding's computer, and nothing about another member's
// computer to one who has not set the Agent up.
func TestReadAppReadinessCarriesAppLogos(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}
	gmail := executableTestConnector(testGmailID, "Gmail")
	gmail.IconURL = testText("https://cdn.test/gmail-app.png")
	observe := func(principal auth.UserPrincipal, runtimeID uuid.UUID, daemonID string) {
		t.Helper()
		publishTestConnectors(t, ctx, service, principal, runtimeID, daemonID,
			[]ConnectorObservationInput{gmail})
		publishTestPlugins(t, ctx, store, principal, runtimeID, daemonID, []PluginObservationInput{{
			NativeID: testGitHubPlugin, Name: "GitHub", Status: PluginStatusAvailable,
			Action: PluginActionInstall, IconURL: "https://cdn.test/github-plugin.png",
		}})
	}
	observe(fixture.principal, fixture.runtimeID, "daemon-"+fixture.companyID.String())

	member := joinTestCompany(t, ctx, pool, fixture, auth.CompanyRoleMember)
	unbound := readTestReadiness(t, ctx, service, member, fixture.rootAgentID, "gmail")
	if unbound.Discovery.State != ConnectorDiscoveryStateUnbound || unbound.Apps[0].IconURL != nil {
		t.Fatalf("unbound readiness = %+v / %+v", unbound.Discovery, unbound.Apps[0])
	}
	memberRuntime := insertTestOwnRuntime(t, ctx, pool, fixture.companyID, member.UserID, "member-daemon", "codex")
	dbtest.SeedAgentBinding(
		t,
		ctx,
		pool,
		fixture.companyID,
		fixture.rootAgentID,
		member.UserID,
		memberRuntime,
		"codex",
	)
	observe(member, memberRuntime, "member-daemon")
	bound := readTestReadiness(t, ctx, service, member, fixture.rootAgentID, "gmail", "github", "typeform")
	// The Connector and Plugin come with the logos: every member installs
	// and connects on their own computer (owner decision, 2026-09-29).
	if bound.Apps[0].Connector == nil || bound.Apps[0].Connector.NativeID != testGmailID ||
		!reflect.DeepEqual(bound.Apps[0].IconURL, testText("https://cdn.test/gmail-app.png")) ||
		bound.Apps[1].Plugin == nil || bound.Apps[1].Plugin.NativeID != testGitHubPlugin ||
		!reflect.DeepEqual(bound.Apps[1].IconURL, testText("https://cdn.test/github-plugin.png")) ||
		bound.Apps[2].IconURL != nil {
		t.Fatalf("member readiness = %+v", bound.Apps)
	}
}

// Codex reports only a chatgpt.com logo for its first-party apps, and that
// loads only in a signed-in ChatGPT session. The Plugin page, the Connector
// list and read, and Workflow readiness show such an app a loadable logo
// instead: an installed Plugin's that bundles it (one named like the app
// first), else a Codex Plugin's of the same name; with neither, the app keeps
// its own (D26).
func TestSessionOnlyAppLogosYieldToLoadableOnes(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	plugins, err := NewPluginService(store, mustAgentTestBus(t))
	if err != nil {
		t.Fatal(err)
	}
	connectors, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}
	daemonID := "daemon-" + fixture.companyID.String()
	mustAgentExec(t, ctx, pool, `UPDATE runtimes SET last_seen_at = clock_timestamp() WHERE id = $1`,
		fixture.runtimeID)

	const (
		testGitHubID    = "connector_76869538009648d5b282a4bb21c3d157"
		testTeamsID     = "connector_teams"
		gmailApp        = "https://chatgpt.com/images/ecosystem/apps/gmail/icon.png"
		calendarApp     = "https://chatgpt.com/images/ecosystem/apps/google_calendar/icon.png"
		teamsApp        = "https://chatgpt.com/images/ecosystem/apps/teams/icon.png"
		gmailLogo       = "https://files.openai.com/content?id=file_gmail&sig=a"
		workspaceLogo   = "https://files.openai.com/content?id=file_workspace&sig=b"
		githubLogo      = "https://files.openai.com/content?id=file_github&sig=c"
		notionApp       = "https://files.openai.com/content?id=file_notion&sig=d"
		teamsPluginLogo = "https://chatgpt.com/backend-api/estuary/content?id=file_teams&sig=e"
	)
	gmail := executableTestConnector(testGmailID, "Gmail")
	gmail.IconURL = testText(gmailApp)
	notion := executableTestConnector(testNotionID, "Notion")
	notion.IconURL = testText(notionApp)
	needsAuth := func(nativeID, name, icon string) ConnectorObservationInput {
		return ConnectorObservationInput{
			Source: ConnectorSourceCodexApp, NativeID: nativeID, Name: name, IconURL: testText(icon),
			Status: ConnectorStatusNeedsAuth, Action: ConnectorActionAuthenticate,
		}
	}
	publishTestConnectors(t, ctx, connectors, fixture.principal, fixture.runtimeID, daemonID,
		[]ConnectorObservationInput{
			gmail, notion,
			needsAuth(testCalendarID, "Google Calendar", calendarApp),
			needsAuth(testGitHubID, "GitHub", "https://chatgpt.com/images/ecosystem/apps/github/icon.png"),
			needsAuth(testTeamsID, "Microsoft Teams", teamsApp),
		})
	publishTestPlugins(t, ctx, store, fixture.principal, fixture.runtimeID, daemonID, []PluginObservationInput{
		{
			NativeID: "google-workspace@openai-curated-remote", Name: "Google Workspace",
			Status: PluginStatusInstalled, Installed: true, Enabled: true, Action: PluginActionUninstall,
			IconURL: workspaceLogo, ComponentCounts: PluginComponentCounts{Apps: 2},
			ConnectorIdentities: []PluginConnectorIdentity{
				{Source: ConnectorSourceCodexApp, NativeID: testGmailID},
				{Source: ConnectorSourceCodexApp, NativeID: testCalendarID},
			},
		},
		{
			NativeID: testGmailPlugin, Name: "Gmail", Status: PluginStatusInstalled, Installed: true,
			Enabled: true, Action: PluginActionUninstall, IconURL: gmailLogo,
			ComponentCounts: PluginComponentCounts{Apps: 1},
			ConnectorIdentities: []PluginConnectorIdentity{
				{Source: ConnectorSourceCodexApp, NativeID: testGmailID},
			},
		},
		{
			NativeID: testGitHubPlugin, Name: "GitHub", Status: PluginStatusAvailable,
			Action: PluginActionInstall, IconURL: githubLogo,
		},
		{
			NativeID: "teams@openai-curated-remote", Name: "Microsoft Teams", Status: PluginStatusAvailable,
			Action: PluginActionInstall, IconURL: teamsPluginLogo,
		},
	})

	// The Plugin page: Gmail takes the Plugin named like it over the wider
	// Workspace bundle; Calendar takes the only bundle.
	workspaceID := testPluginID(t, ctx, pool, fixture.companyID, "google-workspace@openai-curated-remote")
	detail, err := plugins.Get(ctx, fixture.principal, PluginDetailInput{PluginID: workspaceID})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	appIcons := make(map[string]*string, len(detail.Apps))
	for _, app := range detail.Apps {
		appIcons[app.NativeID] = app.IconURL
	}
	if !reflect.DeepEqual(appIcons, map[string]*string{
		testGmailID: testText(gmailLogo), testCalendarID: testText(workspaceLogo),
	}) || !reflect.DeepEqual(detail.Plugin.IconURL, testText(workspaceLogo)) {
		t.Fatalf("Plugin page logos = %s / %s", testJSON(appIcons), testJSON(detail.Plugin.IconURL))
	}

	// The Connector list: GitHub borrows its Plugin's logo by name; Teams has
	// only session-only logos and keeps its own; Notion's own loads.
	wantList := map[string]*string{
		testGmailID: testText(gmailLogo), testCalendarID: testText(workspaceLogo),
		testGitHubID: testText(githubLogo), testNotionID: testText(notionApp), testTeamsID: testText(teamsApp),
	}
	page, err := connectors.List(ctx, fixture.principal,
		ConnectorListInput{RuntimeID: &fixture.runtimeID, Limit: 50})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	listIcons := make(map[string]*string, len(page.Connectors))
	for _, connector := range page.Connectors {
		listIcons[connector.NativeID] = connector.IconURL
	}
	if !reflect.DeepEqual(listIcons, wantList) {
		t.Fatalf("Connector list logos = %s", testJSON(listIcons))
	}
	resolved, err := connectors.Resolve(ctx, fixture.principal, ConnectorResolveInput{
		Identities: []ConnectorIdentityInput{
			{Source: ConnectorSourceCodexApp, NativeID: testGmailID},
			{Source: ConnectorSourceCodexApp, NativeID: testTeamsID},
		},
	})
	if err != nil || len(resolved.Connectors) != 2 {
		t.Fatalf("Resolve() = %+v, %v", resolved, err)
	}
	for _, connector := range resolved.Connectors {
		if !reflect.DeepEqual(connector.IconURL, wantList[connector.NativeID]) {
			t.Fatalf("resolved %s logo = %s, want %s",
				connector.Name, testJSON(connector.IconURL), testJSON(wantList[connector.NativeID]))
		}
	}

	// Workflow readiness: GitHub's pinned Plugin stands in before a borrow.
	readiness := readTestReadiness(t, ctx, connectors, fixture.principal, fixture.rootAgentID,
		"gmail", "google_calendar", "github", "notion").Apps
	for index, want := range []string{gmailLogo, workspaceLogo, githubLogo, notionApp} {
		if !reflect.DeepEqual(readiness[index].IconURL, testText(want)) {
			t.Fatalf("readiness %s logo = %s, want %s",
				readiness[index].App, testJSON(readiness[index].IconURL), want)
		}
	}
}

// A session-only app logo borrows by name only from the viewer's own Runtimes
// (ADR 0014): a member's readiness on their own computer keeps the app's own
// logo while only the owner's computers observe the lending Plugin, and
// borrows once one of the member's own does.
func TestSessionOnlyAppLogosBorrowOnlyFromTheViewersOwnRuntimes(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	connectors, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}
	const (
		githubApp  = "https://chatgpt.com/images/ecosystem/apps/github/icon.png"
		githubLogo = "https://files.openai.com/content?id=file_github&sig=c"
	)
	observeApp := func(principal auth.UserPrincipal, runtimeID uuid.UUID, daemonID string) {
		t.Helper()
		publishTestConnectors(t, ctx, connectors, principal, runtimeID, daemonID,
			[]ConnectorObservationInput{{
				Source: ConnectorSourceCodexApp, NativeID: "connector_76869538009648d5b282a4bb21c3d157",
				Name: "GitHub", IconURL: testText(githubApp),
				Status: ConnectorStatusNeedsAuth, Action: ConnectorActionAuthenticate,
			}})
	}
	observePlugin := func(principal auth.UserPrincipal, runtimeID uuid.UUID, daemonID string) {
		t.Helper()
		publishTestPlugins(t, ctx, store, principal, runtimeID, daemonID,
			[]PluginObservationInput{{
				NativeID: testGitHubPlugin, Name: "GitHub", Status: PluginStatusAvailable,
				Action: PluginActionInstall, IconURL: githubLogo,
			}})
	}
	observeApp(fixture.principal, fixture.runtimeID, "daemon-"+fixture.companyID.String())
	// The owner's second Codex computer observes the GitHub Plugin.
	ownerSecond := insertTestOwnRuntime(t, ctx, pool, fixture.companyID, fixture.userID, "owner-second", "codex")
	observePlugin(fixture.principal, ownerSecond, "owner-second")

	// A member runs the same Agent on their own computer, which observes the
	// app with its session-only logo.
	member := joinTestCompany(t, ctx, pool, fixture, auth.CompanyRoleMember)
	memberRuntime := insertTestOwnRuntime(t, ctx, pool, fixture.companyID, member.UserID, "member-daemon", "codex")
	dbtest.SeedAgentBinding(
		t,
		ctx,
		pool,
		fixture.companyID,
		fixture.rootAgentID,
		member.UserID,
		memberRuntime,
		"codex",
	)
	observeApp(member, memberRuntime, "member-daemon")
	githubIcon := func(viewer auth.UserPrincipal) *string {
		t.Helper()
		result := readTestReadiness(t, ctx, connectors, viewer, fixture.rootAgentID, "github")
		if result.Discovery.State != ConnectorDiscoveryStateFresh {
			t.Fatalf("readiness on the viewer's own computer = %+v", result.Discovery)
		}
		return result.Apps[0].IconURL
	}

	if got := githubIcon(fixture.principal); !reflect.DeepEqual(got, testText(githubLogo)) {
		t.Fatalf("owner readiness logo = %s, want their second computer's Plugin logo", testJSON(got))
	}
	if got := githubIcon(member); !reflect.DeepEqual(got, testText(githubApp)) {
		t.Fatalf("member readiness logo = %s, want the app's own", testJSON(got))
	}
	memberSecond := insertTestOwnRuntime(t, ctx, pool, fixture.companyID, member.UserID, "member-second", "codex")
	observePlugin(member, memberSecond, "member-second")
	if got := githubIcon(member); !reflect.DeepEqual(got, testText(githubLogo)) {
		t.Fatalf("member readiness logo from their own second computer = %s", testJSON(got))
	}
}

// A Plugin without a loadable logo borrows a Codex Plugin's (D26): by name,
// ignoring case, spaces and punctuation; else by its website's host, when one
// Plugin with a loadable logo has that host and it is not a code hosting or
// package site; else by its developer, when one such Plugin has that
// developer; by host or developer only from a Plugin whose name agrees. A
// shared host or developer, another product of the same publisher, and a
// session-only logo lend nothing, and only the viewer's own Runtimes lend at
// all (ADR 0014). The catalog and the Plugin's page agree, and a Claude
// Plugin borrows the same way.
func TestPluginsBorrowLogosByNameWebsiteAndDeveloper(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	plugins, err := NewPluginService(store, mustAgentTestBus(t))
	if err != nil {
		t.Fatal(err)
	}
	const (
		canvaLogo   = "https://cdn.test/canva.png"
		runwayLogo  = "https://cdn.test/runway.png"
		designLogo  = "https://files.openai.com/content?id=file_design&sig=a"
		adobeLogo   = "https://cdn.test/adobe.png"
		sessionOnly = "https://chatgpt.com/backend-api/estuary/content?id=file_contoso&sig=b"
	)
	lender := func(nativeID, name, logo, website, developer string) PluginObservationInput {
		return PluginObservationInput{
			NativeID: nativeID + "@openai-curated-remote", Name: name, IconURL: logo,
			WebsiteURL: website, DeveloperName: developer,
			Status: PluginStatusAvailable, Action: PluginActionInstall,
		}
	}
	borrower := func(nativeID, name, website, developer string) PluginObservationInput {
		return PluginObservationInput{
			NativeID: nativeID, Name: name, WebsiteURL: website, DeveloperName: developer,
			Status: PluginStatusAvailable, Action: PluginActionInstall,
		}
	}

	// The owner's second computer observes the lenders.
	lenders := []PluginObservationInput{
		lender("canva", "Canva", canvaLogo, "https://www.canva.com", "Canva"),
		lender("runway", "Runway", runwayLogo, "https://www.runwayml.com", "Runway AI"),
		lender("superdesign-canvas", "SuperDesign Canvas", designLogo, "https://graphic.example",
			"superdesign inc."),
		lender("bigquery", "BigQuery", "https://cdn.test/bigquery.png",
			"https://cloud.google.com/bigquery", "Google Cloud"),
		lender("adobe", "Adobe for Creativity", adobeLogo, "", ""),
		lender("hostinger-mail", "Hostinger Mail", "https://cdn.test/hostinger-mail.png",
			"https://hostinger.com/mail", "Hostinger"),
		lender("hostinger-builder", "Hostinger AI Builder", "https://cdn.test/hostinger-builder.png",
			"https://www.hostinger.com", "Hostinger"),
		lender("acme", "Acme Repo", "https://cdn.test/acme.png", "https://github.com/acme", "Acme"),
		lender("contoso", "Contoso", sessionOnly, "https://contoso.example", "Contoso Teams"),
		lender("sales", "Sales", "https://cdn.test/sales.png", "https://openai.com/", "OpenAI"),
		lender("data", "Data", "https://cdn.test/data.png", "https://openai.com/data", "OpenAI"),
	}
	lenderRuntime := insertTestOwnRuntime(t, ctx, pool, fixture.companyID, fixture.userID, "lender-daemon", "codex")
	publishTestPlugins(t, ctx, store, fixture.principal, lenderRuntime, "lender-daemon", lenders)
	// The owner's Agent computer observes the borrowers, none with a logo.
	borrowers := []PluginObservationInput{
		borrower("documents@openai-primary-runtime", "Documents", "https://openai.com/", "OpenAI"),
		borrower("canva@claude-plugins-official", "canva", "", ""),
		borrower("runway-api@claude-plugins-official", "runway-api",
			"https://runwayml.com/docs/api", "Runway"),
		borrower("superdesign@claude-plugins-official", "superdesign",
			"https://superdesign.dev", "SuperDesign Inc."),
		borrower("hostinger@claude-plugins-official", "hostinger",
			"https://hostinger.com", "Hostinger"),
		borrower("gh-tool@claude-plugins-official", "gh-tool",
			"https://github.com/acme/gh-tool", "Tool Maker"),
		borrower("teams-kit@claude-plugins-official", "teams-kit",
			"https://teams-kit.example", "Contoso Teams"),
		borrower("alloydb@claude-plugins-official", "AlloyDB",
			"https://cloud.google.com/alloydb", "Google Cloud"),
	}
	publishTestPlugins(t, ctx, store, fixture.principal, fixture.runtimeID,
		"daemon-"+fixture.companyID.String(), borrowers)
	claudeBorrowers := []PluginObservationInput{{
		NativeID: "adobe-for-creativity@claude-plugins-official", Name: "adobe-for-creativity",
		Status: PluginStatusAvailable, Action: PluginActionInstall,
	}}
	claudeRuntime := insertTestClaudeRuntime(t, ctx, pool, fixture)
	publishTestProviderPlugins(t, ctx, store, fixture.principal, claudeRuntime, "claude-daemon",
		ConnectorProviderClaude, claudeBorrowers)

	// An admin's own computers observe the same borrowers.
	admin := joinTestCompany(t, ctx, pool, fixture, auth.CompanyRoleAdmin)
	adminCodex := insertTestOwnRuntime(t, ctx, pool, fixture.companyID, admin.UserID, "admin-codex", "codex")
	publishTestPlugins(t, ctx, store, admin, adminCodex, "admin-codex", borrowers)
	adminClaude := insertTestOwnRuntime(t, ctx, pool, fixture.companyID, admin.UserID, "admin-claude", "claude")
	publishTestProviderPlugins(t, ctx, store, admin, adminClaude, "admin-claude",
		ConnectorProviderClaude, claudeBorrowers)
	catalogIcons := func(viewer auth.UserPrincipal, runtimeID uuid.UUID) map[string]*string {
		t.Helper()
		page, err := plugins.List(ctx, viewer, PluginListInput{RuntimeID: &runtimeID})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		icons := make(map[string]*string, len(page.Plugins))
		for _, plugin := range page.Plugins {
			icons[plugin.NativeID] = plugin.IconURL
		}
		return icons
	}
	borrowed := map[string]*string{
		"documents@openai-primary-runtime":    nil,
		"canva@claude-plugins-official":       testText(canvaLogo),
		"runway-api@claude-plugins-official":  testText(runwayLogo),
		"superdesign@claude-plugins-official": testText(designLogo),
		"hostinger@claude-plugins-official":   nil,
		"gh-tool@claude-plugins-official":     nil,
		"teams-kit@claude-plugins-official":   nil,
		"alloydb@claude-plugins-official":     nil,
	}
	nothing := make(map[string]*string, len(borrowed))
	for nativeID := range borrowed {
		nothing[nativeID] = nil
	}
	claudeBorrowed := map[string]*string{"adobe-for-creativity@claude-plugins-official": testText(adobeLogo)}

	if got := catalogIcons(fixture.principal, fixture.runtimeID); !reflect.DeepEqual(got, borrowed) {
		t.Fatalf("owner catalog logos = %s", testJSON(got))
	}
	if got := catalogIcons(fixture.principal, claudeRuntime); !reflect.DeepEqual(got, claudeBorrowed) {
		t.Fatalf("owner Claude catalog logos = %s", testJSON(got))
	}
	for nativeID, want := range map[string]*string{
		"runway-api@claude-plugins-official":  testText(runwayLogo),
		"superdesign@claude-plugins-official": testText(designLogo),
		"hostinger@claude-plugins-official":   nil,
		"alloydb@claude-plugins-official":     nil,
	} {
		detail, err := plugins.Get(ctx, fixture.principal, PluginDetailInput{
			PluginID:  testPluginID(t, ctx, pool, fixture.companyID, nativeID),
			RuntimeID: &fixture.runtimeID,
		})
		if err != nil || !reflect.DeepEqual(detail.Plugin.IconURL, want) {
			t.Fatalf("Get(%s) logo = %s, %v; want %s",
				nativeID, testJSON(detail.Plugin.IconURL), err, testJSON(want))
		}
	}

	// The owner's lender computer lends the admin nothing; the admin's own
	// lender computer lends exactly what the owner's lends the owner.
	if got := catalogIcons(admin, adminCodex); !reflect.DeepEqual(got, nothing) {
		t.Fatalf("admin catalog logos from the owner's computer = %s", testJSON(got))
	}
	if got := catalogIcons(admin, adminClaude); !reflect.DeepEqual(got, map[string]*string{
		"adobe-for-creativity@claude-plugins-official": nil,
	}) {
		t.Fatalf("admin Claude catalog logos from the owner's computer = %s", testJSON(got))
	}
	adminLender := insertTestOwnRuntime(t, ctx, pool, fixture.companyID, admin.UserID, "admin-lender", "codex")
	publishTestPlugins(t, ctx, store, admin, adminLender, "admin-lender", lenders)
	if got := catalogIcons(admin, adminCodex); !reflect.DeepEqual(got, borrowed) {
		t.Fatalf("admin catalog logos from their own computer = %s", testJSON(got))
	}
	if got := catalogIcons(admin, adminClaude); !reflect.DeepEqual(got, claudeBorrowed) {
		t.Fatalf("admin Claude catalog logos from their own computer = %s", testJSON(got))
	}
}
