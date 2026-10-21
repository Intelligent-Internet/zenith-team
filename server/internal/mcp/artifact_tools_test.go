package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Intelligent-Internet/zenith-team/server/internal/mcp/tools"
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

type artifactInspectStub struct{ tools.ArtifactChannelService }

func (*artifactInspectStub) InspectArtifact(
	_ context.Context,
	in tools.ArtifactInspectInput,
) (tools.ArtifactManifest, error) {
	kind := "file"
	if in.ArtifactID == "board" {
		kind = "whiteboard"
	}
	return tools.ArtifactManifest{ID: in.ArtifactID, Type: kind, Title: "Plan", Items: []tools.ArtifactItem{}}, nil
}

type whiteboardVisualStub struct{ tools.WhiteboardToolService }

func (*whiteboardVisualStub) Read(context.Context, tools.ArtifactInspectInput) (tools.WhiteboardReading, error) {
	return tools.WhiteboardReading{
		Text: "Board \"Plan\", you can edit, sequence 7.\n",
		Images: []tools.WhiteboardImage{
			{
				Label:    "The whole Board at low resolution, 0,0 4000x2000:",
				MIMEType: "image/png",
				Data:     []byte("overview PNG"),
			},
			{Label: "Board region 0,0 1200x1200:", MIMEType: "image/png", Data: []byte("first PNG")},
		},
	}, nil
}

func artifactToolSession(t *testing.T, allowed ...string) *mcpsdk.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "artifact-test", Version: "test"}, nil)
	names := map[string]struct{}{}
	for _, name := range allowed {
		names[name] = struct{}{}
	}
	registerArtifactTools(server, &artifactInspectStub{}, &whiteboardVisualStub{}, names)
	st, ct := mcpsdk.NewInMemoryTransports()
	go func() { _ = server.Run(ctx, st) }()
	session, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "reader", Version: "test"}, nil).
		Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// A Whiteboard is read and changed through the Artifact tools; no tool of its
// own is offered.
func TestWhiteboardsUseTheArtifactTools(t *testing.T) {
	t.Parallel()
	session := artifactToolSession(
		t,
		"artifact_inspect",
		"artifact_fetch",
		"artifact_edit",
		"whiteboard_read",
		"whiteboard_draw",
	)
	result, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"artifact_edit", "artifact_fetch", "artifact_inspect"}) {
		t.Fatalf("tools = %v; want the three Artifact tools", names)
	}
}

func TestArtifactInspectReadsAWhiteboardOnceBesideNativeImages(t *testing.T) {
	t.Parallel()
	session := artifactToolSession(t, "artifact_inspect")
	got, err := session.CallTool(
		t.Context(),
		&mcpsdk.CallToolParams{Name: "artifact_inspect", Arguments: map[string]any{"artifactId": "board"}},
	)
	if err != nil || got.IsError {
		t.Fatalf("read = %+v, %v", got, err)
	}
	var images, texts []string
	for _, content := range got.Content {
		switch c := content.(type) {
		case *mcpsdk.ImageContent:
			if c.MIMEType != "image/png" {
				t.Fatal(c.MIMEType)
			}
			images = append(images, string(c.Data))
		case *mcpsdk.TextContent:
			texts = append(texts, c.Text)
		}
	}
	if len(images) != 2 || images[0] != "overview PNG" || images[1] != "first PNG" {
		t.Fatalf("images = %q; want native images in order", images)
	}
	// The reading travels once: a client that prefers structured content must
	// not drop the images or read the Board twice.
	if got.StructuredContent != nil {
		raw, _ := json.Marshal(got.StructuredContent)
		t.Fatalf("structured content duplicates the read: %s", raw)
	}
	all := strings.Join(texts, "\n")
	if !strings.HasPrefix(texts[0], `Board "Plan"`) || !strings.Contains(all, "whole Board at low resolution") ||
		strings.Contains(all, base64.StdEncoding.EncodeToString([]byte("first PNG"))) {
		t.Fatalf("texts = %q; want the reading first, then each image's label, never image bytes", texts)
	}
	saved, err := session.CallTool(
		t.Context(),
		&mcpsdk.CallToolParams{Name: "artifact_inspect", Arguments: map[string]any{"artifactId": "report"}},
	)
	if err != nil || saved.IsError {
		t.Fatalf("inspect a saved Output = %+v, %v", saved, err)
	}
	raw, _ := json.Marshal(saved.StructuredContent)
	if !strings.Contains(string(raw), `"type":"file"`) {
		t.Fatalf("saved Output manifest = %s; want its structured manifest", raw)
	}
}
