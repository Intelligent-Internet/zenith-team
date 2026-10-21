package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Intelligent-Internet/zenith-team/server/internal/mcp/tools"
)

type projectPageAPIStub struct {
	request map[string]any
}

func (s *projectPageAPIStub) FetchRunJSON(context.Context, string, any) error { return nil }

func (s *projectPageAPIStub) PostRunJSON(_ context.Context, _, _ string, request, result any) error {
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &s.request); err != nil {
		return err
	}
	return json.Unmarshal([]byte(`{"id":"change","status":"applied","sequence":8}`), result)
}

func TestProjectPageToolsRequireAllowlistAndAcceptNativeEdits(t *testing.T) {
	t.Parallel()
	for _, allowed := range []map[string]struct{}{
		nil,
		{"project_read": {}, "page_create": {}, "page_read": {}, "page_edit": {}, "source_read": {}, "page_preview": {}},
	} {
		name := "without authorization"
		if allowed != nil {
			name = "authorized Task"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			api := new(projectPageAPIStub)
			service, err := tools.NewPageToolService(api, tools.RunContext{
				CompanyID: "company", ConversationID: "conversation", RunID: "run", AgentID: "agent",
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "test"}, nil)
			registerPageTools(server, service, allowed)
			serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
			go func() { _ = server.Run(ctx, serverTransport) }()
			client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "test"}, nil)
			session, err := client.Connect(ctx, clientTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			var got, want []string
			for tool, err := range session.Tools(ctx, nil) {
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, tool.Name)
			}
			for tool := range allowed {
				want = append(want, tool)
			}
			slices.Sort(got)
			slices.Sort(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("advertised tools = %v, want %v", got, want)
			}
			if allowed == nil {
				return
			}
			result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
				Name: "page_edit",
				Arguments: map[string]any{
					"pageId": "page", "sequence": 7, "summary": "Add launch findings",
					"operations": []any{map[string]any{
						"type": "insert", "content": map[string]any{
							"type":  "taskReference",
							"attrs": map[string]any{"taskId": "task"},
						},
					}},
				},
			})
			if err != nil || result.IsError {
				t.Fatalf("native edit = %+v, %v", result, err)
			}
			if api.request["sequence"] != float64(7) || api.request["summary"] != "Add launch findings" {
				t.Fatalf("edit request = %v, want promoted sequence and summary", api.request)
			}

			// Reproduce a comment Run that sent only inner text nodes instead
			// of the complete replacement block. Report how to repair it.
			for _, edit := range []struct {
				name      string
				content   map[string]any
				wantError string
			}{
				{name: "missing block type", content: map[string]any{"content": []any{map[string]any{"type": "text", "text": "Translation"}}}, wantError: "type"},
				{name: "missing block identity", content: map[string]any{"type": "paragraph"}, wantError: "attrs.blockId"},
				{name: "wrong block identity", content: map[string]any{"type": "paragraph", "attrs": map[string]any{"blockId": "other"}}, wantError: "attrs.blockId"},
				{name: "complete translation", content: map[string]any{
					"type": "paragraph", "attrs": map[string]any{"blockId": "stanza", "agentProtected": false, "sourceVersionIds": []any{}},
					"content": []any{map[string]any{"type": "text", "text": "The evening breeze"}, map[string]any{"type": "hardBreak"}, map[string]any{"type": "text", "text": "Carries the scent of rice"}},
				}},
			} {
				api.request = nil
				result, err := session.CallTool(
					ctx,
					&mcpsdk.CallToolParams{Name: "page_edit", Arguments: map[string]any{
						"pageId":   "page",
						"sequence": 26,
						"summary":  "Translate the highlighted stanza",
						"operations": []any{
							map[string]any{
								"type":         "update",
								"blockId":      "stanza",
								"expectedHash": strings.Repeat("a", 64),
								"content":      edit.content,
							},
						},
					}},
				)
				if edit.wantError != "" {
					raw, _ := json.Marshal(result)
					message := string(raw)
					if err != nil {
						message += err.Error()
					}
					if (err == nil && !result.IsError) ||
						!strings.Contains(message, edit.wantError) ||
						api.request != nil {
						t.Fatalf(
							"%s = %s, request = %v; want actionable error before API call",
							edit.name,
							message,
							api.request,
						)
					}
					continue
				}
				if err != nil || result.IsError {
					t.Fatalf("%s = %v, %v", edit.name, result, err)
				}
				ops := api.request["operations"].([]any)
				if !reflect.DeepEqual(ops[0].(map[string]any)["content"], edit.content) {
					t.Fatalf("translation lost block identity or line breaks: %v", ops)
				}
			}
			for _, call := range []struct {
				name  string
				input map[string]any
				key   string
				want  string
			}{
				{"page_create", map[string]any{"title": "Research", "markdown": "## Findings\n\nEvidence summary.", "theme": "editorial"}, "markdown", "## Findings\n\nEvidence summary."},
				{"page_edit", map[string]any{"pageId": "page", "sequence": 8, "summary": "Use a compact reading layout", "theme": "compact"}, "theme", "compact"},
			} {
				result, err = session.CallTool(
					ctx,
					&mcpsdk.CallToolParams{Name: call.name, Arguments: call.input},
				)
				if err != nil || result.IsError {
					t.Fatalf("%s = %+v, %v", call.name, result, err)
				}
				if api.request[call.key] != call.want {
					t.Fatalf(
						"%s request = %v, want %s %q",
						call.name,
						api.request,
						call.key,
						call.want,
					)
				}
			}
		})
	}
}
