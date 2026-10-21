package mcp

import (
	"context"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Intelligent-Internet/zenith-team/server/internal/mcp/tools"
)

type calendarToolsStub struct{}

func (*calendarToolsStub) ReadCalendar(context.Context, tools.ReadCalendarInput) (tools.ReadCalendarResult, error) {
	return tools.ReadCalendarResult{
		Items:   []tools.CalendarEvent{},
		Catalog: tools.CalendarCatalog{Members: []tools.CalendarMember{}},
	}, nil
}

func (*calendarToolsStub) WriteCalendar(context.Context, tools.WriteCalendarInput) (tools.WriteCalendarResult, error) {
	return tools.WriteCalendarResult{}, nil
}

func TestCalendarToolsRequireExplicitAllowlistAndExposeTypedSchemas(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{true: "enabled", false: "not allowed"}[enabled], func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			allowed := map[string]struct{}{"company_plan_export": {}}
			if enabled {
				allowed["calendar_read"] = struct{}{}
				allowed["calendar_write"] = struct{}{}
			}
			stub := new(toolsStub)
			server := newServer(stub, stub, nil, allowed, optionalTools{calendar: &calendarToolsStub{}})
			st, ct := mcpsdk.NewInMemoryTransports()
			go func() { _ = server.Run(ctx, st) }()
			client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "calendar-test", Version: "test"}, nil)
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
				if tool.Name == "calendar_read" || tool.Name == "calendar_write" {
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
				t.Fatalf("calendar tool count = %d, want %d", found, want)
			}
			if enabled {
				result, err := session.CallTool(
					ctx,
					&mcpsdk.CallToolParams{
						Name: "calendar_write",
						Arguments: map[string]any{
							"action":           "delete",
							"eventId":          "event",
							"expectedRevision": 1,
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
