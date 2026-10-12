package mcp

import (
	"testing"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"boardly.dev/boardly/server/internal/mcp/tools"
)

func TestArtifactToolsRequireChannelAndExplicitAllowlist(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"enabled", "no channel", "not allowed"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			var service tools.ArtifactChannelService
			if scenario != "no channel" {
				var err error
				service, err = tools.NewArtifactChannelService(
					"http://127.0.0.1:19999",
					"capability",
					uuid.NewString(),
				)
				if err != nil {
					t.Fatal(err)
				}
			}
			allowed := map[string]struct{}{"artifact_inspect": {}, "artifact_fetch": {}}
			if scenario == "not allowed" {
				allowed = nil
			}
			stub := new(toolsStub)
			server := newServer(stub, stub, nil, allowed, optionalTools{artifacts: service})
			transport, peer := mcpsdk.NewInMemoryTransports()
			go func() { _ = server.Run(t.Context(), transport) }()
			client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "test"}, nil)
			session, err := client.Connect(t.Context(), peer, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			count := 0
			for tool, err := range session.Tools(t.Context(), nil) {
				if err != nil {
					t.Fatal(err)
				}
				if tool.Name == "artifact_inspect" || tool.Name == "artifact_fetch" {
					count++
				}
			}
			want := 0
			if scenario == "enabled" {
				want = 2
			}
			if count != want {
				t.Fatalf("tools=%d, want %d", count, want)
			}
		})
	}
}
