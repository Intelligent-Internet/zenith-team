package agent

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestNormalizeConnectorDiscovery(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 12, 3, 4, 5, 0, time.UTC)
	tools := []agent.ConnectorTool{{Name: "repositories/search", Enabled: true, ReadOnly: true}}
	contractDigest, err := agent.ConnectorToolContractDigest(tools)
	if err != nil {
		t.Fatal(err)
	}
	valid := ConnectorDiscoveryInput{
		CompanyID:  uuid.New(),
		RuntimeID:  uuid.New(),
		DaemonID:   "daemon-a",
		Generation: uuid.New(),
		ObservedAt: now,
		Items: []ConnectorObservationInput{
			{
				Source:   ConnectorSourceCodexApp,
				NativeID: "github",
				Name:     " GitHub ",
				Description: connectorStringPointer(
					" Repository tools ",
				),
				Category:       connectorStringPointer("Developer tools"),
				IconURL:        connectorStringPointer("https://cdn.example.test/github.png"),
				Status:         ConnectorStatusConnected,
				Action:         ConnectorActionNone,
				CanExecute:     true,
				ContractDigest: contractDigest,
				Tools:          tools,
			},
		},
	}
	canonicalItem, err := normalizeConnectorObservation(ConnectorProviderCodex, valid.Items[0])
	if err != nil {
		t.Fatal(err)
	}
	valid.ManifestDigest, err = connectorManifestDigest([]ConnectorObservationInput{canonicalItem})
	if err != nil {
		t.Fatal(err)
	}

	normalized, err := normalizeConnectorDiscovery(ConnectorProviderCodex, valid, now)
	if err != nil {
		t.Fatalf("normalizeConnectorDiscovery() error = %v", err)
	}
	if normalized.Items[0].Name != "GitHub" || normalized.Items[0].Description == nil ||
		*normalized.Items[0].Description != "Repository tools" {
		t.Fatalf("normalized item = %+v", normalized.Items[0])
	}

	tests := []struct {
		name   string
		mutate func(*ConnectorDiscoveryInput)
	}{
		{
			name: "manifest digest does not match canonical items",
			mutate: func(input *ConnectorDiscoveryInput) {
				input.ManifestDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
			},
		},
		{
			name: "duplicate identity",
			mutate: func(input *ConnectorDiscoveryInput) {
				input.Items = append(input.Items, input.Items[0])
			},
		},
		{
			name: "cross-provider source",
			mutate: func(input *ConnectorDiscoveryInput) {
				input.Items[0].Source = ConnectorSourceClaudeMCP
			},
		},
		{
			name: "executable while disconnected",
			mutate: func(input *ConnectorDiscoveryInput) {
				input.Items[0].Status = ConnectorStatusNeedsAuth
			},
		},
		{
			name: "unsupported source claims executable",
			mutate: func(input *ConnectorDiscoveryInput) {
				input.Items[0].Source = ConnectorSourceCodexMCP
			},
		},
		{
			name: "unsafe icon url",
			mutate: func(input *ConnectorDiscoveryInput) {
				input.Items[0].IconURL = connectorStringPointer("file:///tmp/secret")
			},
		},
		{
			name: "tracking query in icon url",
			mutate: func(input *ConnectorDiscoveryInput) {
				input.Items[0].IconURL = connectorStringPointer(
					"https://cdn.example.test/github.png?account=secret",
				)
			},
		},
		{
			name: "future observation",
			mutate: func(input *ConnectorDiscoveryInput) {
				input.ObservedAt = now.Add(6 * time.Minute)
			},
		},
		{
			name: "generation exceeds connector bound",
			mutate: func(input *ConnectorDiscoveryInput) {
				input.Items = make([]ConnectorObservationInput, maxConnectorItems+1)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			input.Items = append([]ConnectorObservationInput(nil), valid.Items...)
			test.mutate(&input)
			if _, err := normalizeConnectorDiscovery(
				ConnectorProviderCodex,
				input,
				now,
			); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("normalizeConnectorDiscovery() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestEncodeConnectorDiscoveryBatchUsesCamelCase(t *testing.T) {
	t.Parallel()

	description := "Repository tools"
	category := "Developer tools"
	iconURL := "https://cdn.example.test/github.png"
	installCount := int64(42)
	encoded, err := encodeConnectorDiscoveryBatch([]ConnectorObservationInput{{
		Source: ConnectorSourceCodexApp, NativeID: "github", Name: "GitHub",
		Description: &description, Category: &category, IconURL: &iconURL,
		InstallCount: &installCount, Status: ConnectorStatusConnected,
		Action: ConnectorActionNone, CanExecute: true, ContractDigest: "digest",
		Tools: []agent.ConnectorTool{{Name: "repositories/search", Enabled: true, ReadOnly: true}},
	}})
	if err != nil {
		t.Fatalf("encodeConnectorDiscoveryBatch() error = %v", err)
	}
	want := `[{"source":"codex_app","nativeId":"github","name":"GitHub",` +
		`"description":"Repository tools","category":"Developer tools",` +
		`"iconUrl":"https://cdn.example.test/github.png","installCount":42,` +
		`"status":"connected","action":"none","canExecute":true,` +
		`"contractDigest":"digest","toolNamespace":[` +
		`{"name":"repositories/search","enabled":true,"readOnly":true}]}]`
	if string(encoded) != want {
		t.Fatalf("encoded Connector discovery batch = %s, want %s", encoded, want)
	}
}

func TestConnectorIdentityColumnsKeepRequestOrder(t *testing.T) {
	t.Parallel()

	sources, nativeIDs := connectorIdentityColumns([]ConnectorIdentityInput{
		{Source: ConnectorSourceClaudePlugin, NativeID: "slack@official"},
		{Source: ConnectorSourceClaudeMCP, NativeID: "github"},
	})
	if !slices.Equal(sources, []string{"claude_plugin", "claude_mcp"}) ||
		!slices.Equal(nativeIDs, []string{"slack@official", "github"}) {
		t.Fatalf("identity columns = %v, %v", sources, nativeIDs)
	}
}

func TestNormalizeConnectorListSource(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		input     ConnectorListInput
		wantValid bool
	}{
		{name: "unfiltered", wantValid: true},
		{name: "implicit provider", input: ConnectorListInput{Source: ConnectorSourceCodexMCP}, wantValid: true},
		{name: "codex MCP", input: ConnectorListInput{Provider: ConnectorProviderCodex, Source: ConnectorSourceCodexMCP}, wantValid: true},
		{name: "claude MCP", input: ConnectorListInput{Provider: ConnectorProviderClaude, Source: ConnectorSourceClaudeMCP}, wantValid: true},
		{name: "unknown source", input: ConnectorListInput{Source: "mcp"}},
		{name: "wrong provider", input: ConnectorListInput{Provider: ConnectorProviderClaude, Source: ConnectorSourceCodexMCP}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeConnectorListInput(test.input)
			if (err == nil) != test.wantValid {
				t.Fatalf(
					"normalizeConnectorListInput() error = %v, want valid = %v",
					err,
					test.wantValid,
				)
			}
			if err == nil && got.Source != test.input.Source {
				t.Fatalf("Source = %q, want %q", got.Source, test.input.Source)
			}
		})
	}
}

func TestNormalizeConnectorObservationAcceptsSelectiveClaudeNamespaces(t *testing.T) {
	t.Parallel()

	tools := []agent.ConnectorTool{
		{Name: "calendar", Enabled: true},
		{Name: "mail", Enabled: true},
	}
	digest, err := agent.ConnectorToolContractDigest(tools)
	if err != nil {
		t.Fatal(err)
	}

	for _, source := range []ConnectorSource{
		ConnectorSourceClaudePlugin,
		ConnectorSourceClaudeMCP,
	} {
		t.Run(string(source), func(t *testing.T) {
			t.Parallel()
			item, err := normalizeConnectorObservation(ConnectorProviderClaude, ConnectorObservationInput{
				Source: source, NativeID: "workspace", Name: "Workspace",
				Status: ConnectorStatusConnected, Action: ConnectorActionManageInProvider,
				CanExecute: true, ContractDigest: digest, Tools: tools,
			})
			if err != nil {
				t.Fatalf("normalizeConnectorObservation() error = %v", err)
			}
			if !item.CanExecute || item.ContractDigest != digest || len(item.Tools) != 2 {
				t.Fatalf("normalizeConnectorObservation() = %+v", item)
			}
		})
	}

	for _, source := range []ConnectorSource{
		ConnectorSourceClaudeCloud,
		ConnectorSourceCodexMCP,
	} {
		t.Run("reject "+string(source), func(t *testing.T) {
			t.Parallel()
			provider := ConnectorProviderClaude
			if source == ConnectorSourceCodexMCP {
				provider = ConnectorProviderCodex
			}
			_, err := normalizeConnectorObservation(provider, ConnectorObservationInput{
				Source: source, NativeID: "workspace", Name: "Workspace",
				Status: ConnectorStatusConnected, Action: ConnectorActionManageInProvider,
				CanExecute: true, ContractDigest: digest, Tools: tools,
			})
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("normalizeConnectorObservation() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestConnectorItemFromRowSupportsProviderExecutableSources(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		provider ConnectorProvider
		source   ConnectorSource
		valid    bool
	}{
		{name: "Codex app", provider: ConnectorProviderCodex, source: ConnectorSourceCodexApp, valid: true},
		{name: "Claude plugin", provider: ConnectorProviderClaude, source: ConnectorSourceClaudePlugin, valid: true},
		{name: "Claude MCP", provider: ConnectorProviderClaude, source: ConnectorSourceClaudeMCP, valid: true},
		{name: "Codex MCP", provider: ConnectorProviderCodex, source: ConnectorSourceCodexMCP},
		{name: "Claude Cloud", provider: ConnectorProviderClaude, source: ConnectorSourceClaudeCloud},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			row := generated.ConnectorCatalogItem{
				ID: uuid.New(), CompanyID: uuid.New(),
				Provider: string(test.provider), Source: string(test.source),
				NativeID: "native", Name: "Connector", Status: "connected", Action: "none",
				CanExecute: true,
				ObservedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
			}
			item := connectorItemFromRow(row, true, 2, false, true)
			if test.valid {
				if !item.EffectiveEnabled {
					t.Fatalf("connectorItemFromRow() = %+v", item)
				}
				return
			}
			if item.EffectiveEnabled {
				t.Fatalf("connectorItemFromRow() enabled executable %s/%s", test.provider, test.source)
			}
		})
	}
}

func TestConnectorCursorRoundTripIsOpaqueAndCanonical(t *testing.T) {
	t.Parallel()

	want := ConnectorCursor{Name: "github", ID: uuid.New()}
	raw, err := FormatConnectorCursor(want)
	if err != nil {
		t.Fatalf("FormatConnectorCursor() error = %v", err)
	}
	if raw == "" || raw == want.Name+"|"+want.ID.String() {
		t.Fatalf("FormatConnectorCursor() = %q, want opaque value", raw)
	}
	got, err := ParseConnectorCursor(raw)
	if err != nil {
		t.Fatalf("ParseConnectorCursor() error = %v", err)
	}
	if got == nil || *got != want {
		t.Fatalf("ParseConnectorCursor() = %+v, want %+v", got, want)
	}
	if _, err := ParseConnectorCursor(raw + "="); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ParseConnectorCursor(noncanonical) error = %v, want ErrInvalidInput", err)
	}
}

func connectorStringPointer(value string) *string {
	return &value
}
