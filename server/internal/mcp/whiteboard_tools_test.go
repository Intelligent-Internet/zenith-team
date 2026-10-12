package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"boardly.dev/boardly/server/internal/mcp/tools"
)

type whiteboardVisualStub struct{ tools.WhiteboardToolService }

func TestWhiteboardUploadToolIsRemoved(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "whiteboard-test", Version: "test"}, nil)
	registerWhiteboardTools(server, &whiteboardVisualStub{}, map[string]struct{}{"whiteboard_upload": {}})
	st, ct := mcpsdk.NewInMemoryTransports()
	go func() { _ = server.Run(ctx, st) }()
	session, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "reader", Version: "test"}, nil).
		Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 0 {
		t.Fatalf("tools = %+v, want no upload tool", result.Tools)
	}
}

func (*whiteboardVisualStub) Read(context.Context, tools.WhiteboardReadInput) (tools.WhiteboardReading, error) {
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

func TestWhiteboardReadReturnsTextOnceBesideNativeImages(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "whiteboard-test", Version: "test"}, nil)
	registerWhiteboardTools(server, &whiteboardVisualStub{}, map[string]struct{}{"whiteboard_read": {}})
	st, ct := mcpsdk.NewInMemoryTransports()
	go func() { _ = server.Run(ctx, st) }()
	session, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "reader", Version: "test"}, nil).
		Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	got, err := session.CallTool(
		ctx,
		&mcpsdk.CallToolParams{Name: "whiteboard_read", Arguments: map[string]any{"boardId": "board"}},
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
}
