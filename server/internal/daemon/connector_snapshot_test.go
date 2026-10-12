package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"boardly.dev/boardly/server/pkg/agent"
)

func TestConnectorAppsForRunPreservesExactEmptyScope(t *testing.T) {
	t.Parallel()

	apps, err := connectorAppsForRun(&RunConnectorSnapshot{
		SchemaVersion: 1,
		Connectors:    []RunConnectorContext{},
	}, "codex")
	if err != nil {
		t.Fatalf("connectorAppsForRun() error = %v", err)
	}
	if apps == nil || len(apps) != 0 {
		t.Fatalf("apps = %#v, want non-nil empty exact scope", apps)
	}
}

func TestMergeRunMCPConfigRejectsConnectorNameCollision(t *testing.T) {
	t.Parallel()

	_, err := mergeRunMCPConfig(
		json.RawMessage(`{"mcpServers":{"linear":{"command":"user"}}}`),
		map[string]json.RawMessage{"linear": json.RawMessage(`{"command":"connector"}`)},
	)
	if err == nil || !strings.Contains(err.Error(), "duplicates selected connector") {
		t.Fatalf("mergeRunMCPConfig() error = %v", err)
	}
}

func TestConnectorAppsForRunValidatesImmutableContract(t *testing.T) {
	t.Parallel()

	tools := []agent.ConnectorTool{{Name: "files.read", Enabled: true, ReadOnly: true}}
	digest, err := agent.ConnectorToolContractDigest(tools)
	if err != nil {
		t.Fatalf("ConnectorToolContractDigest() error = %v", err)
	}
	snapshot := &RunConnectorSnapshot{SchemaVersion: 1, Connectors: []RunConnectorContext{{
		ID: "01900000-0000-7000-8000-000000000001", PolicyRevision: 1,
		Source: "codex_app", NativeID: "google_drive", ContractDigest: digest, Tools: tools,
	}}}
	apps, err := connectorAppsForRun(snapshot, "codex")
	if err != nil {
		t.Fatalf("connectorAppsForRun() error = %v", err)
	}
	if len(apps) != 1 || apps[0].ID != "google_drive" || apps[0].ContractDigest != digest {
		t.Fatalf("apps = %+v", apps)
	}

	snapshot.Connectors[0].ContractDigest = strings.Repeat("0", 64)
	if _, err := connectorAppsForRun(snapshot, "codex"); err == nil {
		t.Fatal("connectorAppsForRun() accepted a changed tool contract")
	}
}

func TestConnectorAppsForRunMentionsDisplayNames(t *testing.T) {
	t.Parallel()

	tools := []agent.ConnectorTool{{Name: "search_emails", Enabled: true, ReadOnly: true}}
	digest, err := agent.ConnectorToolContractDigest(tools)
	if err != nil {
		t.Fatalf("ConnectorToolContractDigest() error = %v", err)
	}
	snapshot := &RunConnectorSnapshot{SchemaVersion: 1, Connectors: []RunConnectorContext{
		{
			ID:             "01900000-0000-7000-8000-000000000001",
			PolicyRevision: 1,
			Source:         "codex_app",
			NativeID:       "connector_calendar",
			Name:           "  Google\n\tCalendar ",
			ContractDigest: digest,
			Tools:          tools,
		},
		{
			ID: "01900000-0000-7000-8000-000000000002", PolicyRevision: 1, Source: "codex_app",
			NativeID: "connector_gmail", Name: "Gmail", ContractDigest: digest, Tools: tools,
		},
		{
			ID: "01900000-0000-7000-8000-000000000003", PolicyRevision: 1, Source: "codex_app",
			NativeID: "connector_unnamed", ContractDigest: digest, Tools: tools,
		},
	}}
	apps, err := connectorAppsForRun(snapshot, "codex")
	if err != nil {
		t.Fatalf("connectorAppsForRun() error = %v", err)
	}
	names := make([]string, len(apps))
	for index, app := range apps {
		names[index] = app.Name
	}
	want := []string{"Google Calendar", "Gmail", "connector_unnamed"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("app names = %q, want %q", names, want)
	}
	if apps[1].ID != "connector_gmail" {
		t.Fatalf("app ID = %q, want the native identity kept for binding", apps[1].ID)
	}
}

func TestRunConnectorSnapshotDecodesDisplayNameAndFreshness(t *testing.T) {
	t.Parallel()

	var doc RunContext
	if err := json.Unmarshal([]byte(`{"connectorSnapshot":{"schemaVersion":1,"discoveryFresh":false,`+
		`"connectors":[{"id":"c","policyRevision":1,"source":"codex_app","nativeId":"connector_gmail",`+
		`"name":"Gmail","contractDigest":"d","tools":[]}]}}`), &doc); err != nil {
		t.Fatalf("decode run context: %v", err)
	}
	snapshot := doc.ConnectorSnapshot
	if snapshot == nil || snapshot.DiscoveryFresh == nil || *snapshot.DiscoveryFresh ||
		len(snapshot.Connectors) != 1 || snapshot.Connectors[0].Name != "Gmail" {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	var legacy RunContext
	if err := json.Unmarshal(
		[]byte(`{"connectorSnapshot":{"schemaVersion":1,"connectors":[]}}`),
		&legacy,
	); err != nil {
		t.Fatalf("decode legacy run context: %v", err)
	}
	if legacy.ConnectorSnapshot == nil || legacy.ConnectorSnapshot.DiscoveryFresh != nil {
		t.Fatalf("legacy snapshot = %+v, want freshness unknown", legacy.ConnectorSnapshot)
	}
}
