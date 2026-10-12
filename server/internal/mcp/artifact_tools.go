package mcp

import (
	"context"

	"boardly.dev/boardly/server/internal/mcp/tools"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerArtifactTools(server *mcpsdk.Server, service tools.ArtifactChannelService, allowed map[string]struct{}) {
	if service == nil {
		return
	}
	closed, destructive := false, false
	if toolExplicitlyAllowed(allowed, "artifact_inspect") {
		mcpsdk.AddTool(
			server,
			&mcpsdk.Tool{
				Name:        "artifact_inspect",
				Title:       "Inspect an attached Artifact",
				Description: "Resolve an artifactId to its immutable saved Output and pinned file items. Returns item IDs, Asset Version IDs and document destinations or app URL where available. Does not download files. Access is checked for the person this Run works for: you can read exactly the Artifacts they can read.",
				Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
			},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in tools.ArtifactInspectInput) (*mcpsdk.CallToolResult, tools.ArtifactManifest, error) {
				result, err := service.InspectArtifact(ctx, in)
				return nil, result, err
			},
		)
	}
	if toolExplicitlyAllowed(allowed, "artifact_fetch") {
		mcpsdk.AddTool(
			server,
			&mcpsdk.Tool{
				Name:        "artifact_fetch",
				Title:       "Fetch an Artifact file",
				Description: "Download a pinned file from an artifactId into this Run workspace and return its absolute localPath. Use an itemId from artifact_inspect, or omit it for the primary file. Rechecks current access even when reusing verified cached bytes. Does not fetch app URLs; inspect non-file destinations instead. Read the returned file with your normal file tools.",
				Annotations: &mcpsdk.ToolAnnotations{
					IdempotentHint:  true,
					DestructiveHint: &destructive,
					OpenWorldHint:   &closed,
				},
			},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, in tools.ArtifactFetchInput) (*mcpsdk.CallToolResult, tools.ArtifactFetchResult, error) {
				result, err := service.FetchArtifact(ctx, in)
				return nil, result, err
			},
		)
	}
}
