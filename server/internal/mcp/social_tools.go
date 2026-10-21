package mcp

import (
	"context"

	"github.com/Intelligent-Internet/zenith-team/server/internal/mcp/tools"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerSocialTools(server *mcpsdk.Server, service tools.SocialToolService, allowed map[string]struct{}) {
	if service == nil {
		return
	}
	openWorld := true
	if toolExplicitlyAllowed(allowed, "social_read") {
		mcpsdk.AddTool(
			server,
			&mcpsdk.Tool{
				Name:        "social_read",
				Title:       "Read social accounts and posts",
				Description: "Read the current user's connected social accounts and Page targets, recent publish receipts and images/videos attached to this direct conversation. Credentials never leave the server. Read before publishing; unavailable accounts need connection/reconnection through Settings > Plugins > Social accounts.",
				Annotations: &mcpsdk.ToolAnnotations{
					ReadOnlyHint:   true,
					IdempotentHint: true,
					OpenWorldHint:  &openWorld,
				},
			},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, input tools.ReadSocialInput) (*mcpsdk.CallToolResult, tools.ReadSocialResult, error) {
				result, err := service.ReadSocial(ctx, input)
				return nil, result, err
			},
		)
	}
	if toolExplicitlyAllowed(allowed, "social_publish") {
		mcpsdk.AddTool(
			server,
			&mcpsdk.Tool{
				Name:        "social_publish",
				Title:       "Publish a social post now",
				Description: "Publish the exact socialPost draft that the human confirmed in ask_human, using its questionId. First call social_read, then ask_human with a socialPost preview and publish/cancel choices; end the Run and wait for the answer. Generic chat requests and plain-text questions do not authorize publication. Changes require a fresh preview and confirmation. The same questionId reuses its receipt across Runs. Only published confirms success; unknown or publishing means DO NOT resend or create another review to bypass deduplication. There is no scheduling.",
				Annotations: &mcpsdk.ToolAnnotations{IdempotentHint: true, OpenWorldHint: &openWorld},
			},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, input tools.PublishSocialInput) (*mcpsdk.CallToolResult, tools.SocialPost, error) {
				result, err := service.PublishSocial(ctx, input)
				return nil, result, err
			},
		)
	}
}
