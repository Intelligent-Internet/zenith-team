package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type claudePluginTransportStub struct {
	catalog claudePluginCatalog
	reads   int
}

func (s *claudePluginTransportStub) Read(context.Context, string) (claudePluginCatalog, error) {
	s.reads++
	return s.catalog, nil
}

func (*claudePluginTransportStub) Install(context.Context, string, string) error { return nil }

func (*claudePluginTransportStub) Uninstall(context.Context, string, string) error { return nil }

func TestClaudePluginBackendReadDetailReportsKnownMCPServers(t *testing.T) {
	t.Parallel()

	catalog, err := parseClaudePluginCatalog([]byte(`{
		"installed":[
			{"id":"figma@claude-plugins-official","scope":"user","enabled":true,
			 "installPath":"/must/not/leak",
			 "mcpServers":{"figma":{"url":"https://mcp.figma.com/mcp","headers":{"Authorization":"secret"}}}},
			{"id":"broken@claude-plugins-official","scope":"user","enabled":true,
			 "mcpServers":{"bad name!":{}}}
		],
		"available":[
			{"pluginId":"figma@claude-plugins-official","name":"figma","description":"Design",
			 "marketplaceName":"claude-plugins-official"},
			{"pluginId":"slack@claude-plugins-official","name":"slack","description":"Chat",
			 "marketplaceName":"claude-plugins-official","mcpServers":{"slack":{"command":"slack-mcp"}}}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	transport := &claudePluginTransportStub{catalog: catalog}
	backend := newClaudePluginBackend("claude", transport)

	tests := []struct {
		nativeID string
		want     ClaudePluginDetail
	}{
		{
			nativeID: "figma@claude-plugins-official",
			want: ClaudePluginDetail{
				NativeID: "figma@claude-plugins-official", Name: "figma", MCPServers: []string{"figma"},
			},
		},
		{
			nativeID: "broken@claude-plugins-official",
			want:     ClaudePluginDetail{NativeID: "broken@claude-plugins-official", Name: "broken"},
		},
		{
			nativeID: "slack@claude-plugins-official",
			want: ClaudePluginDetail{
				NativeID: "slack@claude-plugins-official", Name: "slack", MCPServers: []string{"slack"},
			},
		},
	}
	for _, test := range tests {
		got, err := backend.ReadDetail(t.Context(), test.nativeID)
		if err != nil {
			t.Fatalf("ReadDetail(%s) error = %v", test.nativeID, err)
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("ReadDetail(%s) = %#v, want %#v", test.nativeID, got, test.want)
		}
	}
	if transport.reads != 1 {
		t.Fatalf("catalog reads = %d, want one read cached for later details", transport.reads)
	}
	_, err = backend.ReadDetail(t.Context(), "asana@claude-plugins-official")
	if !errors.Is(err, ErrClaudePluginNotFound) {
		t.Fatalf("ReadDetail(unknown) error = %v, want ErrClaudePluginNotFound", err)
	}

	detail, err := claudeNativePluginBackend{backend: backend}.ReadDetail(
		t.Context(), "figma@claude-plugins-official",
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"schemaVersion":0,"nativeId":"figma@claude-plugins-official","name":"figma",` +
		`"defaultPrompts":null,"apps":null,"skills":null,"mcpServers":["figma"]}`
	if string(encoded) != want {
		t.Fatalf("native detail = %s, want %s", encoded, want)
	}
}

// heldClaudePluginTransport holds its first read until release closes and
// answers it with stale; every other read answers the stub's catalog.
type heldClaudePluginTransport struct {
	claudePluginTransportStub
	stale   claudePluginCatalog
	held    atomic.Bool
	started chan struct{}
	release chan struct{}
}

func (s *heldClaudePluginTransport) Read(ctx context.Context, path string) (claudePluginCatalog, error) {
	if s.held.CompareAndSwap(false, true) {
		close(s.started)
		<-s.release
		return s.stale, nil
	}
	return s.claudePluginTransportStub.Read(ctx, path)
}

// ReadDetail reads the catalog without operationMu when none is cached. A
// read that an install's invalidation or the Discover after it overtook must
// answer from its own catalog without restoring it, or uninstalling the
// plugin just installed is refused.
func TestClaudePluginBackendReadDetailKeepsANewerCatalog(t *testing.T) {
	t.Parallel()

	const nativeID = "figma@claude-plugins-official"
	catalog := func(action ClaudePluginAction) claudePluginCatalog {
		return claudePluginCatalog{
			plugins: []ClaudePlugin{{NativeID: nativeID, Name: "figma", Action: action}},
			actions: map[string]ClaudePluginAction{nativeID: action},
		}
	}
	for name, overtake := range map[string]func(*ClaudePluginBackend) error{
		"a newer discovery": func(backend *ClaudePluginBackend) error {
			_, err := backend.Discover(t.Context(), true)
			return err
		},
		"an invalidation": func(backend *ClaudePluginBackend) error {
			backend.invalidate()
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			transport := &heldClaudePluginTransport{
				claudePluginTransportStub: claudePluginTransportStub{
					catalog: catalog(ClaudePluginActionUninstall),
				},
				stale:   catalog(ClaudePluginActionInstall),
				started: make(chan struct{}),
				release: make(chan struct{}),
			}
			backend := newClaudePluginBackend("claude", transport)
			detailErr := make(chan error, 1)
			go func() {
				_, err := backend.ReadDetail(t.Context(), nativeID)
				detailErr <- err
			}()
			<-transport.started
			if err := overtake(backend); err != nil {
				t.Fatal(err)
			}
			close(transport.release)
			if err := <-detailErr; err != nil {
				t.Fatalf("ReadDetail() error = %v", err)
			}
			if err := backend.Uninstall(t.Context(), nativeID); err != nil {
				t.Fatalf("Uninstall() after an overtaken detail read error = %v", err)
			}
		})
	}
}

// claudeCommunityPlugins renders count available plugins of an added
// community marketplace, with descriptions long enough that the list passes
// 1 MiB the way claude-community's 2,281 plugins do.
func claudeCommunityPlugins(count int) string {
	description := strings.Repeat("A community plugin. ", 40)
	entries := make([]string, count)
	for index := range entries {
		name := fmt.Sprintf("plugin-%04d", index)
		entries[index] = fmt.Sprintf(
			`{"pluginId":"%s@claude-community","name":"%s","description":"%s",`+
				`"marketplaceName":"claude-community"}`,
			name, name, description,
		)
	}
	return strings.Join(entries, ",")
}

// Claude lists every plugin of every added marketplace, 1.8 MB with
// claude-community added in September 2026, so that list has its own
// ceiling; every other Claude command keeps 1 MiB.
func TestOSClaudePluginTransportReadsACatalogPastOneMiB(t *testing.T) {
	t.Parallel()

	body := `{"installed":[],"available":[` + claudeCommunityPlugins(1500) + `]}`
	if len(body) <= claudeConnectorOutputLimit {
		t.Fatalf("catalog is %d bytes, want more than %d", len(body), claudeConnectorOutputLimit)
	}
	catalogPath := filepath.Join(t.TempDir(), "available.json")
	if err := os.WriteFile(catalogPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	claude := writeCodexCapabilityFixture(t, "cat '"+catalogPath+"'\n")
	runner := osClaudeConnectorCommandRunner{}

	catalog, err := osClaudePluginTransport{runner: runner}.Read(t.Context(), claude)
	if err != nil || len(catalog.plugins) != 1500 {
		t.Fatalf("Read() = %d plugins, %v; want 1500", len(catalog.plugins), err)
	}
	if _, err := runner.Output(t.Context(), claude, []string{"plugin", "list", "--json"}); !errors.Is(
		err, errClaudeConnectorCommand,
	) {
		t.Fatalf("installed list past 1 MiB error = %v, want errClaudeConnectorCommand", err)
	}
	available := []string{"plugin", "list", "--available", "--json"}
	if claudeCommandOutputFits(available, claudeCommandOutputLimit(available)+1, 0) {
		t.Fatal("an available list past its own ceiling fit")
	}
}
