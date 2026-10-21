package mcp

import (
	"context"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Intelligent-Internet/zenith-team/server/internal/mcp/tools"
)

type socialToolsStub struct{}

func (*socialToolsStub) ReadSocial(context.Context, tools.ReadSocialInput) (tools.ReadSocialResult, error) {
	return tools.ReadSocialResult{}, nil
}

func (*socialToolsStub) PublishSocial(context.Context, tools.PublishSocialInput) (tools.SocialPost, error) {
	return tools.SocialPost{}, nil
}

func TestSocialToolsRequireExplicitAllowlistAndExposeTypedSchemas(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{true: "enabled", false: "not allowed"}[enabled], func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			allowed := map[string]struct{}{"company_plan_export": {}}
			if enabled {
				allowed["social_read"] = struct{}{}
				allowed["social_publish"] = struct{}{}
			}
			stub := new(toolsStub)
			server := newServer(stub, stub, nil, allowed, optionalTools{social: &socialToolsStub{}})
			st, ct := mcpsdk.NewInMemoryTransports()
			go func() { _ = server.Run(ctx, st) }()
			client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "social-test", Version: "test"}, nil)
			session, err := client.Connect(ctx, ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			found := 0
			for tool, err := range session.Tools(ctx, nil) {
				if err != nil {
					t.Fatal(err)
				}
				if tool.Name == "social_read" || tool.Name == "social_publish" {
					found++
					if tool.InputSchema == nil || tool.OutputSchema == nil {
						t.Fatal("missing typed schema")
					}
				}
			}
			want := 0
			if enabled {
				want = 2
			}
			if found != want {
				t.Fatalf("social tool count = %d, want %d", found, want)
			}
			if enabled {
				result, err := session.CallTool(
					ctx,
					&mcpsdk.CallToolParams{
						Name: "social_publish",
						Arguments: map[string]any{
							"questionId": "review",
						},
					},
				)
				if err != nil || result.IsError {
					t.Fatalf("tool call = %#v, %v", result, err)
				}
			}
		})
	}
}
