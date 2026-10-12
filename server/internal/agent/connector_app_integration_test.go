//go:build integration

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	provideragent "boardly.dev/boardly/server/pkg/agent"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// Gmail, Google Calendar, Slack, Notion and Atlassian Rovo (Legacy) are the
// real pinned Codex identities; "figma-reissued" stands for an app id the
// provider changed, reachable only through the installed Figma Plugin.
const (
	testGmailID        = "connector_2128aebfecb84f64a069897515042a44"
	testCalendarID     = "connector_947e0d954944416db111db556030eea6"
	testSlackID        = "asdk_app_69a1d78e929881919bba0dbda1f6436d"
	testNotionID       = "asdk_app_69c18c28f1188191bf5b8445c4ab0a2e"
	testRovoLegacyID   = "connector_692de805e3ec8191834719067174a384"
	testFigmaReissued  = "figma-reissued"
	testFigmaPlugin    = "figma@openai-curated-remote"
	testGitHubPlugin   = "github@openai-curated-remote"
	testGmailPlugin    = "gmail@openai-curated-remote"
	testSalesforcePlug = "salesforce@openai-curated-remote"
)

func executableTestConnector(nativeID, name string) ConnectorObservationInput {
	return executableTestConnectorWithTools(nativeID, name,
		[]provideragent.ConnectorTool{{Name: "search", Enabled: true, ReadOnly: true}})
}

func executableTestConnectorWithTools(
	nativeID, name string,
	tools []provideragent.ConnectorTool,
) ConnectorObservationInput {
	digest, err := provideragent.ConnectorToolContractDigest(tools)
	if err != nil {
		panic(err)
	}
	return ConnectorObservationInput{
		Source: ConnectorSourceCodexApp, NativeID: nativeID, Name: name,
		Status: ConnectorStatusConnected, Action: ConnectorActionNone, CanExecute: true,
		ContractDigest: digest, Tools: tools,
	}
}

// publishTestConnectors publishes one complete Codex Connector generation for
// a Runtime and returns the input, so a test can reaffirm the same generation.
func publishTestConnectors(
	t *testing.T,
	ctx context.Context,
	service *DefaultConnectorService,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
	daemonID string,
	items []ConnectorObservationInput,
) ConnectorDiscoveryInput {
	t.Helper()
	return publishTestProviderConnectors(
		t, ctx, service, principal, runtimeID, daemonID, ConnectorProviderCodex, items)
}

// publishTestProviderConnectors is publishTestConnectors for a Runtime of the
// given provider.
func publishTestProviderConnectors(
	t *testing.T,
	ctx context.Context,
	service *DefaultConnectorService,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
	daemonID string,
	provider ConnectorProvider,
	items []ConnectorObservationInput,
) ConnectorDiscoveryInput {
	t.Helper()
	canonical := make([]ConnectorObservationInput, len(items))
	for index, item := range items {
		normalized, err := normalizeConnectorObservation(provider, item)
		if err != nil {
			t.Fatalf("normalize Connector %q: %v", item.NativeID, err)
		}
		canonical[index] = normalized
	}
	slices.SortFunc(canonical, func(left, right ConnectorObservationInput) int {
		if order := strings.Compare(string(left.Source), string(right.Source)); order != 0 {
			return order
		}
		return strings.Compare(left.NativeID, right.NativeID)
	})
	digest, err := connectorManifestDigest(canonical)
	if err != nil {
		t.Fatal(err)
	}
	input := ConnectorDiscoveryInput{
		CompanyID: principal.CompanyID, RuntimeID: runtimeID, DaemonID: daemonID,
		Generation: uuid.New(), ManifestDigest: digest, ObservedAt: time.Now().UTC(), Items: items,
	}
	if result, err := service.PublishDiscovery(ctx, principal, input); err != nil || !result.Applied {
		t.Fatalf("PublishDiscovery() = %+v, %v", result, err)
	}
	return input
}

func publishTestPlugins(
	t *testing.T,
	ctx context.Context,
	store *db.Store,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
	daemonID string,
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
		normalized, err := normalizePluginObservation(ConnectorProviderCodex, items[index])
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

type frozenTestSnapshot struct {
	SchemaVersion  int  `json:"schemaVersion"`
	DiscoveryFresh bool `json:"discoveryFresh"`
	Connectors     []struct {
		ID       uuid.UUID `json:"id"`
		NativeID string    `json:"nativeId"`
		Name     string    `json:"name"`
	} `json:"connectors"`
}

func (s frozenTestSnapshot) nativeIDs() []string {
	ids := make([]string, len(s.Connectors))
	for index, connector := range s.Connectors {
		ids[index] = connector.NativeID
	}
	return ids
}

// freezeTestRun claims one all-mode Run on the fixture Runtime through the
// production freeze and returns its decoded Connector snapshot.
func freezeTestRun(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	store *db.Store,
	fixture agentCompanyFixture,
	conversationID uuid.UUID,
) frozenTestSnapshot {
	t.Helper()
	var runID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs (company_id, agent_id, conversation_id, runtime_id, status, context, run_type)
		VALUES ($1, $2, $3, $4, 'dispatched', '{"prompt":"inspect"}'::jsonb, 'direct_chat')
		RETURNING id`,
		fixture.companyID, fixture.rootAgentID, conversationID, fixture.runtimeID,
	).Scan(&runID); err != nil {
		t.Fatalf("insert Run: %v", err)
	}
	var frozen generated.Run
	if err := store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var freezeErr error
		frozen, freezeErr = FreezeRunConnectorSnapshotInTx(ctx, q, runID, fixture.runtimeID)
		return freezeErr
	}); err != nil {
		t.Fatalf("FreezeRunConnectorSnapshotInTx() error = %v", err)
	}
	// The daemon reads the frozen contract from the Run context.
	var runContext struct {
		ConnectorSnapshot frozenTestSnapshot `json:"connectorSnapshot"`
	}
	if err := json.Unmarshal(frozen.Context, &runContext); err != nil {
		t.Fatalf("decode Run context %s: %v", frozen.Context, err)
	}
	return runContext.ConnectorSnapshot
}

func insertTestDirectConversation(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture agentCompanyFixture,
) uuid.UUID {
	t.Helper()
	conversationID := uuid.New()
	mustAgentExec(t, ctx, pool, `
		INSERT INTO conversations (id, company_id, kind, agent_id, created_by_user_id, title)
		VALUES ($1, $2, 'direct', $3, $4, 'Readiness run')`,
		conversationID, fixture.companyID, fixture.rootAgentID, fixture.userID)
	return conversationID
}

func readTestReadiness(
	t *testing.T,
	ctx context.Context,
	service *DefaultConnectorService,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	apps ...ConnectorAppType,
) ConnectorAppReadinessResult {
	t.Helper()
	result, err := service.ReadAppReadiness(
		ctx,
		principal,
		ConnectorAppReadinessInput{AgentID: agentID, Apps: apps},
	)
	if err != nil {
		t.Fatalf("ReadAppReadiness(%v) error = %v", apps, err)
	}
	if len(result.Apps) != len(apps) {
		t.Fatalf("ReadAppReadiness(%v) answered %d apps", apps, len(result.Apps))
	}
	for index, app := range apps {
		if result.Apps[index].App != app {
			t.Fatalf("ReadAppReadiness() order = %+v, want %v", result.Apps, apps)
		}
	}
	return result
}

func readinessStates(result ConnectorAppReadinessResult) map[ConnectorAppType]ConnectorAppState {
	states := make(map[ConnectorAppType]ConnectorAppState, len(result.Apps))
	for _, app := range result.Apps {
		states[app.App] = app.State
	}
	return states
}

// assertReadinessLockstep proves `ready` is exactly the claim freeze: an app
// is ready if and only if one of its candidate identities is in the frozen
// all-mode scope, and a ready app names that very Connector.
func assertReadinessLockstep(
	t *testing.T,
	phase string,
	result ConnectorAppReadinessResult,
	snapshot frozenTestSnapshot,
	candidates map[ConnectorAppType][]string,
) {
	t.Helper()
	frozen := snapshot.nativeIDs()
	for _, app := range result.Apps {
		inFreeze := false
		for _, nativeID := range candidates[app.App] {
			inFreeze = inFreeze || slices.Contains(frozen, nativeID)
		}
		if (app.State == ConnectorAppStateReady) != inFreeze {
			t.Errorf("%s: %s is %s but its freeze membership is %v (frozen %v)",
				phase, app.App, app.State, inFreeze, frozen)
		}
		if app.State == ConnectorAppStateReady &&
			(app.Connector == nil || !slices.Contains(frozen, app.Connector.NativeID)) {
			t.Errorf(
				"%s: ready %s names %+v, which the freeze did not admit",
				phase,
				app.App,
				app.Connector,
			)
		}
	}
}

func TestReadAppReadinessMatchesTheClaimFreeze(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}
	daemonID := "daemon-" + fixture.companyID.String()

	// Gmail reports four tools. Two are disabled by the provider, so a Run
	// can't call them and they don't count; of the two left, the one not
	// read-only changes data.
	published := publishTestConnectors(t, ctx, service, fixture.principal, fixture.runtimeID, daemonID,
		[]ConnectorObservationInput{
			executableTestConnectorWithTools(testGmailID, "Gmail", []provideragent.ConnectorTool{
				{Name: "search_emails", Enabled: true, ReadOnly: true},
				{Name: "read_thread", Enabled: false, ReadOnly: true},
				{Name: "send_email", Enabled: true, ReadOnly: false},
				{Name: "delete_email", Enabled: false, ReadOnly: false},
			}),
			executableTestConnector(testCalendarID, "Google Calendar"),
			{
				Source: ConnectorSourceCodexApp, NativeID: testSlackID, Name: "Slack",
				Status: ConnectorStatusNeedsAuth, Action: ConnectorActionAuthenticate,
			},
			executableTestConnector(testNotionID, "Notion"),
			executableTestConnector(testRovoLegacyID, "Atlassian Rovo (Legacy)"),
			executableTestConnector(testFigmaReissued, "Figma"),
		})
	publishTestPlugins(t, ctx, store, fixture.principal, fixture.runtimeID, daemonID, []PluginObservationInput{
		{
			NativeID:        testFigmaPlugin,
			Name:            "Figma",
			Status:          PluginStatusInstalled,
			Installed:       true,
			Enabled:         true,
			Action:          PluginActionUninstall,
			ComponentCounts: PluginComponentCounts{Apps: 1},
			ConnectorIdentities: []PluginConnectorIdentity{
				{Source: ConnectorSourceCodexApp, NativeID: testFigmaReissued},
			},
		},
		{
			NativeID: testGitHubPlugin, Name: "GitHub", Status: PluginStatusAvailable,
			Action: PluginActionInstall, ComponentCounts: PluginComponentCounts{Apps: 1},
		},
		{
			NativeID: testSalesforcePlug, Name: "Salesforce", Status: PluginStatusError,
			Action: PluginActionNone,
		},
	})
	// Calendar's contract awaits review; Notion is connected but not yet
	// enabled by Company policy.
	mustAgentExec(t, ctx, pool, `
		UPDATE connector_policies SET review_required = true, version = version + 1
		WHERE connector_id = (SELECT id FROM connector_catalog_items WHERE company_id = $1 AND native_id = $2)`,
		fixture.companyID, testCalendarID)
	mustAgentExec(t, ctx, pool, `
		UPDATE connector_policies SET desired_enabled = false, version = version + 1
		WHERE connector_id = (SELECT id FROM connector_catalog_items WHERE company_id = $1 AND native_id = $2)`,
		fixture.companyID, testNotionID)

	candidates := map[ConnectorAppType][]string{
		"gmail": {testGmailID}, "google_calendar": {testCalendarID}, "slack": {testSlackID},
		"notion": {testNotionID}, "jira": {"asdk_app_6a83901dde988191b3f3cefdcc19acfa", testRovoLegacyID},
		"figma":  {"connector_68df038e0ba48191908c8434991bbac2", testFigmaReissued},
		"github": {"connector_76869538009648d5b282a4bb21c3d157"}, "salesforce": {}, "hubspot": {
			"asdk_app_697acb8e53d88191bf7a79e62012ae14",
		},
		// Pinned for Codex but never observed on this computer.
		"typeform": {"asdk_app_6a3e889766b481919d6b2861e94a8afa"},
	}
	first := []ConnectorAppType{"gmail", "google_calendar", "slack", "notion", "jira", "figma"}
	second := []ConnectorAppType{"github", "salesforce", "hubspot", "typeform"}
	conversationID := insertTestDirectConversation(t, ctx, pool, fixture)

	readBoth := func() (ConnectorAppReadinessResult, ConnectorAppReadinessResult) {
		return readTestReadiness(t, ctx, service, fixture.principal, fixture.rootAgentID, first...),
			readTestReadiness(t, ctx, service, fixture.principal, fixture.rootAgentID, second...)
	}

	firstResult, secondResult := readBoth()
	if firstResult.Discovery.State != ConnectorDiscoveryStateFresh || firstResult.Discovery.ObservedAt == nil ||
		firstResult.Provider != ConnectorProviderCodex ||
		!firstResult.RuntimeID.Valid || firstResult.RuntimeID.UUID != fixture.runtimeID ||
		firstResult.AgentID != fixture.rootAgentID {
		t.Fatalf("fresh readiness header = %+v", firstResult)
	}
	want := map[ConnectorAppType]ConnectorAppState{
		"gmail": ConnectorAppStateReady, "google_calendar": ConnectorAppStateReviewRequired,
		"slack": ConnectorAppStateNeedsConnection, "notion": ConnectorAppStatePending,
		"jira": ConnectorAppStateReady, "figma": ConnectorAppStateReady,
		"github": ConnectorAppStateNeedsConnection, "salesforce": ConnectorAppStateNotOffered,
		"hubspot": ConnectorAppStateNotOffered, "typeform": ConnectorAppStateNotOffered,
	}
	for _, result := range []ConnectorAppReadinessResult{firstResult, secondResult} {
		for app, state := range readinessStates(result) {
			if state != want[app] {
				t.Errorf("fresh: %s = %s, want %s", app, state, want[app])
			}
		}
	}
	byApp := func(result ConnectorAppReadinessResult, app ConnectorAppType) ConnectorAppReadiness {
		for _, entry := range result.Apps {
			if entry.App == app {
				return entry
			}
		}
		t.Fatalf("no %s in %+v", app, result.Apps)
		return ConnectorAppReadiness{}
	}
	if gmail := byApp(firstResult, "gmail"); gmail.Connector == nil || gmail.Connector.Name != "Gmail" ||
		gmail.Connector.Source != ConnectorSourceCodexApp || gmail.Connector.Status != ConnectorStatusConnected ||
		gmail.Connector.ToolCount != 2 || gmail.Connector.WriteToolCount != 1 {
		t.Errorf("owner gmail entry = %+v", gmail.Connector)
	}
	// An app awaiting authentication reports no tools.
	if slack := byApp(firstResult, "slack"); slack.Connector == nil ||
		slack.Connector.Action != ConnectorActionAuthenticate ||
		slack.Connector.ToolCount != 0 || slack.Connector.WriteToolCount != 0 {
		t.Errorf("owner slack entry = %+v", slack.Connector)
	}
	if jira := byApp(firstResult, "jira"); jira.Connector == nil || jira.Connector.NativeID != testRovoLegacyID {
		t.Errorf("jira resolved through %+v, want Rovo Legacy", jira.Connector)
	}
	if figma := byApp(firstResult, "figma"); figma.Connector == nil ||
		figma.Connector.NativeID != testFigmaReissued ||
		figma.Connector.ToolCount != 1 || figma.Connector.WriteToolCount != 0 || figma.Plugin == nil ||
		figma.Plugin.NativeID != testFigmaPlugin || !figma.Plugin.Installed {
		t.Errorf("figma entry = %+v / %+v", figma.Connector, figma.Plugin)
	}
	if github := byApp(secondResult, "github"); github.Connector != nil || github.Plugin == nil ||
		github.Plugin.Action != PluginActionInstall || github.Plugin.Installed {
		t.Errorf("github entry = %+v / %+v", github.Connector, github.Plugin)
	}

	snapshot := freezeTestRun(t, ctx, pool, store, fixture, conversationID)
	if !snapshot.DiscoveryFresh || len(snapshot.Connectors) == 0 {
		t.Fatalf("fresh snapshot = %+v", snapshot)
	}
	for _, connector := range snapshot.Connectors {
		if connector.Name == "" {
			t.Errorf("frozen Connector %s has no display name", connector.NativeID)
		}
		if connector.NativeID == testGmailID && connector.Name != "Gmail" {
			t.Errorf("frozen Gmail name = %q", connector.Name)
		}
	}
	assertReadinessLockstep(t, "fresh", firstResult, snapshot, candidates)
	assertReadinessLockstep(t, "fresh", secondResult, snapshot, candidates)

	// Discovery ages past the claim window: nothing is ready and the freeze
	// admits nothing, and says why.
	mustAgentExec(t, ctx, pool, `
		UPDATE connector_discovery_generations
		SET observed_at = clock_timestamp() - interval '11 minutes'
		WHERE runtime_id = $1`, fixture.runtimeID)
	firstResult, secondResult = readBoth()
	if firstResult.Discovery.State != ConnectorDiscoveryStateStale {
		t.Fatalf("aged discovery = %+v", firstResult.Discovery)
	}
	if state := readinessStates(firstResult)["gmail"]; state != ConnectorAppStatePending {
		t.Errorf("stale gmail = %s, want pending", state)
	}
	snapshot = freezeTestRun(t, ctx, pool, store, fixture, conversationID)
	if snapshot.DiscoveryFresh || len(snapshot.Connectors) != 0 {
		t.Fatalf("stale snapshot = %+v", snapshot)
	}
	assertReadinessLockstep(t, "stale", firstResult, snapshot, candidates)
	assertReadinessLockstep(t, "stale", secondResult, snapshot, candidates)

	// A reaffirmation of the unchanged generation brings both back.
	if result, err := service.ReaffirmDiscovery(ctx, fixture.principal, ConnectorDiscoveryReaffirmInput{
		CompanyID: fixture.companyID, RuntimeID: fixture.runtimeID, DaemonID: daemonID,
		Generation: published.Generation, ManifestDigest: published.ManifestDigest,
		ObservedAt: time.Now().UTC(),
	}); err != nil || !result.Applied {
		t.Fatalf("ReaffirmDiscovery() = %+v, %v", result, err)
	}
	firstResult, secondResult = readBoth()
	if state := readinessStates(firstResult)["gmail"]; state != ConnectorAppStateReady {
		t.Errorf("reaffirmed gmail = %s, want ready", state)
	}
	snapshot = freezeTestRun(t, ctx, pool, store, fixture, conversationID)
	if !snapshot.DiscoveryFresh || len(snapshot.Connectors) == 0 {
		t.Fatalf("reaffirmed snapshot = %+v", snapshot)
	}
	assertReadinessLockstep(t, "reaffirmed", firstResult, snapshot, candidates)
	assertReadinessLockstep(t, "reaffirmed", secondResult, snapshot, candidates)
}

// Two Codex desktops of one Company share one Company catalog row per app,
// which the claim freeze and readiness both require to be connected. A later
// publication from the desktop that does not have Gmail connected must not
// switch Gmail off for the Agent's desktop, whose keepalive only reaffirms,
// and the two reads must agree at every step.
func TestReadAppReadinessMatchesTheClaimFreezeAcrossRuntimes(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}
	daemonID := "daemon-" + fixture.companyID.String()
	// The second desktop hosts no Agent, so its publications revise no policy.
	secondRuntimeID := uuid.Must(uuid.NewV7())
	secondDaemonID := "second-" + fixture.companyID.String()
	mustAgentExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id)
		VALUES ($1, $2, $3, 'Second Runtime', 'codex', 'online', $4)`,
		secondRuntimeID, fixture.companyID, secondDaemonID, fixture.userID)
	connectedGmail := executableTestConnector(testGmailID, "Gmail")
	unconnectedGmail := ConnectorObservationInput{
		Source: ConnectorSourceCodexApp, NativeID: testGmailID, Name: "Gmail",
		Status: ConnectorStatusAvailable, Action: ConnectorActionInstallAndAuth,
	}
	candidates := map[ConnectorAppType][]string{"gmail": {testGmailID}}
	conversationID := insertTestDirectConversation(t, ctx, pool, fixture)
	check := func(phase string, want ConnectorAppState) {
		t.Helper()
		result := readTestReadiness(t, ctx, service, fixture.principal, fixture.rootAgentID, "gmail")
		if state := readinessStates(result)["gmail"]; state != want {
			t.Errorf("%s: gmail = %s, want %s", phase, state, want)
		}
		snapshot := freezeTestRun(t, ctx, pool, store, fixture, conversationID)
		if !snapshot.DiscoveryFresh {
			t.Fatalf("%s: snapshot = %+v, want fresh discovery", phase, snapshot)
		}
		assertReadinessLockstep(t, phase, result, snapshot, candidates)
		// Either desktop can still run Gmail, so the Company row stays connected.
		var catalogStatus string
		if err := pool.QueryRow(ctx, `
			SELECT status FROM connector_catalog_items WHERE company_id = $1 AND native_id = $2`,
			fixture.companyID, testGmailID).Scan(&catalogStatus); err != nil {
			t.Fatalf("%s: read Gmail catalog row: %v", phase, err)
		}
		if catalogStatus != string(ConnectorStatusConnected) {
			t.Errorf("%s: Gmail catalog status = %s, want connected", phase, catalogStatus)
		}
	}

	published := publishTestConnectors(t, ctx, service, fixture.principal, fixture.runtimeID, daemonID,
		[]ConnectorObservationInput{connectedGmail})
	check("published", ConnectorAppStateReady)

	publishTestConnectors(t, ctx, service, fixture.principal, secondRuntimeID, secondDaemonID,
		[]ConnectorObservationInput{unconnectedGmail})
	if result, err := service.ReaffirmDiscovery(ctx, fixture.principal, ConnectorDiscoveryReaffirmInput{
		CompanyID: fixture.companyID, RuntimeID: fixture.runtimeID, DaemonID: daemonID,
		Generation: published.Generation, ManifestDigest: published.ManifestDigest,
		ObservedAt: time.Now().UTC(),
	}); err != nil || !result.Applied {
		t.Fatalf("ReaffirmDiscovery() = %+v, %v", result, err)
	}
	check("after the other desktop published", ConnectorAppStateReady)

	// The ranking never lends the Agent's desktop another desktop's app: once
	// its own observation loses Gmail, neither read admits it.
	publishTestConnectors(t, ctx, service, fixture.principal, secondRuntimeID, secondDaemonID,
		[]ConnectorObservationInput{connectedGmail})
	publishTestConnectors(t, ctx, service, fixture.principal, fixture.runtimeID, daemonID,
		[]ConnectorObservationInput{unconnectedGmail})
	check("after the Agent's desktop lost Gmail", ConnectorAppStateNeedsConnection)
}

// A Claude Connector's namespace lists the MCP servers a Run may start, not
// their tools, and never marks one read-only; readiness reports no tool
// counts for it rather than one "tool" that "changes data".
func TestReadAppReadinessCountsNoToolsForClaudeNamespaces(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	// A Claude Agent the owner runs on their own Claude computer.
	claudeRuntimeID, claudeAgentID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	claudeDaemonID := "claude-" + fixture.companyID.String()
	mustAgentExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id)
		VALUES ($1, $2, $3, 'Claude Runtime', 'claude', 'online', $4)`,
		claudeRuntimeID, fixture.companyID, claudeDaemonID, fixture.userID)
	dbtest.SeedBoundAgent(t, ctx, pool, claudeAgentID, fixture.companyID, "Claude", "claude",
		&fixture.rootAgentID, fixture.userID, claudeRuntimeID)
	service, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}
	const slackPlugin = "slack@claude-plugins-official"
	tools := []provideragent.ConnectorTool{{Name: "mcp__plugin_slack_slack", Enabled: true}}
	digest, err := provideragent.ConnectorToolContractDigest(tools)
	if err != nil {
		t.Fatal(err)
	}
	publishTestProviderConnectors(t, ctx, service, fixture.principal, claudeRuntimeID,
		claudeDaemonID, ConnectorProviderClaude,
		[]ConnectorObservationInput{{
			Source: ConnectorSourceClaudePlugin, NativeID: slackPlugin, Name: "Slack",
			Status: ConnectorStatusConnected, Action: ConnectorActionNone, CanExecute: true,
			ContractDigest: digest, Tools: tools,
		}})

	slack := readTestReadiness(t, ctx, service, fixture.principal, claudeAgentID, "slack").Apps[0]
	if slack.State != ConnectorAppStateReady || slack.Connector == nil ||
		slack.Connector.NativeID != slackPlugin ||
		slack.Connector.ToolCount != 0 || slack.Connector.WriteToolCount != 0 {
		t.Fatalf("Claude slack entry = %s / %+v", slack.State, slack.Connector)
	}
}

// ADR 0014: readiness reads the viewer's own binding of the Agent. A member
// who has not set the Agent up is `unbound` and learns nothing of the
// owner's computer, not even its discovery time; once bound, the member
// reads their own computer's states and the Connector and Plugin behind
// them, which plugins.manage — every role's since the 2026-09-29 owner
// decision — connects and installs there, never the owner's.
func TestReadAppReadinessReadsTheViewersBindingAndPermissions(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}
	daemonID := "daemon-" + fixture.companyID.String()
	publishTestConnectors(t, ctx, service, fixture.principal, fixture.runtimeID, daemonID,
		[]ConnectorObservationInput{executableTestConnector(testGmailID, "Gmail")})
	publishTestPlugins(t, ctx, store, fixture.principal, fixture.runtimeID, daemonID, []PluginObservationInput{
		{
			NativeID:        testGmailPlugin,
			Name:            "Gmail",
			Status:          PluginStatusInstalled,
			Installed:       true,
			Enabled:         true,
			Action:          PluginActionUninstall,
			ComponentCounts: PluginComponentCounts{Apps: 1},
			ConnectorIdentities: []PluginConnectorIdentity{
				{Source: ConnectorSourceCodexApp, NativeID: testGmailID},
			},
		},
	})

	memberID := uuid.Must(uuid.NewV7())
	mustAgentExec(t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Member')`,
		memberID, fmt.Sprintf("member-%s@boardly.test", memberID))
	mustAgentExec(t, ctx, pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		fixture.companyID, memberID)
	member := auth.UserPrincipal{
		UserID: memberID, SessionID: uuid.Must(uuid.NewV7()), AuthKind: auth.TokenKindBearer,
		CompanyID: fixture.companyID, Role: auth.CompanyRoleMember,
	}

	unbound := readTestReadiness(t, ctx, service, member, fixture.rootAgentID, "gmail", "slack")
	if unbound.Discovery.State != ConnectorDiscoveryStateUnbound || unbound.Discovery.ObservedAt != nil ||
		unbound.RuntimeID.Valid || unbound.Provider != ConnectorProviderCodex {
		t.Fatalf("readiness for a member without a binding = %+v", unbound)
	}
	for _, app := range unbound.Apps {
		if app.State != ConnectorAppStateUnknown || app.Connector != nil || app.Plugin != nil {
			t.Fatalf("app for a member without a binding = %+v", app)
		}
	}

	// The member sets the Agent up on their own computer, which has not
	// reported yet: the owner's fresh discovery does not stand in for it.
	memberRuntimeID := uuid.Must(uuid.NewV7())
	memberDaemonID := "member-" + fixture.companyID.String()
	mustAgentExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id)
		VALUES ($1, $2, $3, 'Member Runtime', 'codex', 'online', $4)`,
		memberRuntimeID, fixture.companyID, memberDaemonID, memberID)
	dbtest.SeedAgentBinding(
		t,
		ctx,
		pool,
		fixture.companyID,
		fixture.rootAgentID,
		memberID,
		memberRuntimeID,
		"codex",
	)
	bound := readTestReadiness(t, ctx, service, member, fixture.rootAgentID, "gmail")
	if bound.Discovery.State != ConnectorDiscoveryStateMissing || bound.Discovery.ObservedAt != nil ||
		!bound.RuntimeID.Valid || bound.RuntimeID.UUID != memberRuntimeID ||
		bound.Apps[0].State != ConnectorAppStateUnknown {
		t.Fatalf("readiness on the member's unreported computer = %+v", bound)
	}

	publishTestConnectors(t, ctx, service, member, memberRuntimeID, memberDaemonID,
		[]ConnectorObservationInput{executableTestConnector(testGmailID, "Gmail")})
	shared := readTestReadiness(t, ctx, service, member, fixture.rootAgentID, "gmail")
	if shared.Discovery.State != ConnectorDiscoveryStateFresh || shared.Apps[0].State != ConnectorAppStateReady ||
		shared.Apps[0].Connector == nil || shared.Apps[0].Connector.NativeID != testGmailID ||
		shared.Apps[0].Plugin != nil {
		t.Fatalf("member readiness on their own computer = %+v / %+v", shared.Discovery, shared.Apps[0])
	}
	var ownerConnectorID uuid.UUID
	if owner := readTestReadiness(
		t,
		ctx,
		service,
		fixture.principal,
		fixture.rootAgentID,
		"gmail",
	); owner.Apps[0].Connector != nil {
		ownerConnectorID = owner.Apps[0].Connector.ID
	}
	// The member's computer offers the pinned plugin to install, and the
	// member gets it — theirs, not the one installed on the owner's.
	publishTestPlugins(t, ctx, store, member, memberRuntimeID, memberDaemonID, []PluginObservationInput{
		{
			NativeID:        testGmailPlugin,
			Name:            "Gmail",
			Status:          PluginStatusAvailable,
			Action:          PluginActionInstall,
			ComponentCounts: PluginComponentCounts{Apps: 1},
			ConnectorIdentities: []PluginConnectorIdentity{
				{Source: ConnectorSourceCodexApp, NativeID: testGmailID},
			},
		},
	})
	installable := readTestReadiness(t, ctx, service, member, fixture.rootAgentID, "gmail")
	if plugin := installable.Apps[0].Plugin; plugin == nil || plugin.NativeID != testGmailPlugin ||
		plugin.Installed || plugin.Action != PluginActionInstall {
		t.Fatalf("member readiness Plugin on their own computer = %+v", plugin)
	}
	if installable.Apps[0].Connector == nil || installable.Apps[0].Connector.ID != ownerConnectorID {
		t.Fatalf(
			"member readiness Connector = %+v, want the Company Connector %s",
			installable.Apps[0].Connector,
			ownerConnectorID,
		)
	}
	owner := readTestReadiness(t, ctx, service, fixture.principal, fixture.rootAgentID, "gmail")
	if owner.Apps[0].Connector == nil || owner.Apps[0].Connector.NativeID != testGmailID ||
		owner.Apps[0].Plugin == nil || owner.Apps[0].Plugin.NativeID != testGmailPlugin {
		t.Fatalf("owner readiness = %+v", owner.Apps[0])
	}

	// A non-member reads nothing at all.
	outsider := member
	outsider.UserID = uuid.Must(uuid.NewV7())
	if _, err := service.ReadAppReadiness(ctx, outsider, ConnectorAppReadinessInput{
		AgentID: fixture.rootAgentID, Apps: []ConnectorAppType{"gmail"},
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadAppReadiness(non-member) error = %v, want ErrNotFound", err)
	}
}

// The Agent's own capability denials apply exactly as the daemon applies
// them: a disabled MCP server id, or a disabled Plugin's Connector identity.
func TestReadAppReadinessReportsAgentCapabilityDenials(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}
	daemonID := "daemon-" + fixture.companyID.String()
	publishTestConnectors(t, ctx, service, fixture.principal, fixture.runtimeID, daemonID,
		[]ConnectorObservationInput{
			executableTestConnector(testGmailID, "Gmail"),
			executableTestConnector(testCalendarID, "Google Calendar"),
		})
	publishTestPlugins(t, ctx, store, fixture.principal, fixture.runtimeID, daemonID, []PluginObservationInput{
		{
			NativeID:        testGmailPlugin,
			Name:            "Gmail",
			Status:          PluginStatusInstalled,
			Installed:       true,
			Enabled:         true,
			Action:          PluginActionUninstall,
			ComponentCounts: PluginComponentCounts{Apps: 1},
			ConnectorIdentities: []PluginConnectorIdentity{
				{Source: ConnectorSourceCodexApp, NativeID: testGmailID},
			},
		},
	})
	var pluginID, calendarID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM plugin_catalog_items WHERE company_id = $1 AND native_id = $2`,
		fixture.companyID, testGmailPlugin).Scan(&pluginID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT id FROM connector_catalog_items WHERE company_id = $1 AND native_id = $2`,
		fixture.companyID, testCalendarID).Scan(&calendarID); err != nil {
		t.Fatal(err)
	}
	settings, err := json.Marshal(map[string]any{"capabilities": map[string]any{
		"enabledTools": []string{}, "disabledBuiltIns": []string{},
		"disabledPlugins": []uuid.UUID{pluginID}, "disabledMcpServers": []uuid.UUID{calendarID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Every writer sets the typed column from the same value as the payload.
	mustAgentExec(t, ctx, pool, `UPDATE agents SET settings = $2, disabled_plugin_ids = $3 WHERE id = $1`,
		fixture.rootAgentID, settings, []uuid.UUID{pluginID})

	result := readTestReadiness(t, ctx, service, fixture.principal, fixture.rootAgentID, "gmail", "google_calendar")
	for _, app := range result.Apps {
		if app.State != ConnectorAppStateDisabledForAgent {
			t.Errorf("%s = %s, want disabled_for_agent", app.App, app.State)
		}
	}
}

// Readiness never answers for an Agent the Company does not have live: an
// archived Agent, another Company's Agent and an unknown id are all absent;
// an Agent whose bound Runtime never reported discovery is missing, not an
// error.
func TestReadAppReadinessRefusesAbsentAgentsAndReportsMissingDiscovery(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	other := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}

	missing := readTestReadiness(t, ctx, service, fixture.principal, fixture.rootAgentID, "gmail")
	if missing.Discovery.State != ConnectorDiscoveryStateMissing || missing.Discovery.ObservedAt != nil ||
		missing.Apps[0].State != ConnectorAppStateUnknown {
		t.Fatalf("never-reported readiness = %+v", missing)
	}

	archivedID := uuid.Must(uuid.NewV7())
	mustAgentExec(t, ctx, pool, `
		INSERT INTO agents (id, company_id, name, title, provider, reports_to_agent_id, archived_at)
		VALUES ($1, $2, 'Archived', 'Archived', 'codex', $3, clock_timestamp())`,
		archivedID, fixture.companyID, fixture.rootAgentID)
	for name, agentID := range map[string]uuid.UUID{
		"archived": archivedID, "foreign": other.rootAgentID, "unknown": uuid.New(),
	} {
		if _, err := service.ReadAppReadiness(ctx, fixture.principal, ConnectorAppReadinessInput{
			AgentID: agentID, Apps: []ConnectorAppType{"gmail"},
		}); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s Agent: error = %v, want ErrNotFound", name, err)
		}
	}
}
