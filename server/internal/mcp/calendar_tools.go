package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Intelligent-Internet/zenith-team/server/internal/mcp/tools"
)

func registerCalendarTools(server *mcpsdk.Server, service tools.CalendarToolService, allowed map[string]struct{}) {
	if service == nil {
		return
	}
	closedWorld := false
	if toolExplicitlyAllowed(allowed, "calendar_read") {
		mcpsdk.AddTool(
			server,
			&mcpsdk.Tool{
				Name:        "calendar_read",
				Title:       "Read calendars and members",
				Description: "Read the current user's personal calendar, invitations and this company's shared calendar. Discover calendar and member IDs before creating events. Available to direct chat Runs only. All-day end dates are exclusive; seriesStartLocal/seriesEndLocal are the values to preserve when editing recurring events.",
				Annotations: &mcpsdk.ToolAnnotations{
					ReadOnlyHint:   true,
					IdempotentHint: true,
					OpenWorldHint:  &closedWorld,
				},
			},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, input tools.ReadCalendarInput) (*mcpsdk.CallToolResult, tools.ReadCalendarResult, error) {
				result, err := service.ReadCalendar(ctx, input)
				return nil, result, err
			},
		)
	}
	if toolExplicitlyAllowed(allowed, "calendar_write") {
		mcpsdk.AddTool(
			server,
			&mcpsdk.Tool{
				Name:        "calendar_write",
				Title:       "Manage calendar events",
				Description: "Create or edit events/reminders, invite active company members, respond to invitations, or remove events at the user's request. Uses the same permissions as the Calendar UI. Changes are applied immediately, and company-calendar entries are visible to all company members. Read first to resolve IDs and current revisions; ask about ambiguous people, dates or scope. Does not schedule agent work, publish posts, send email or create meeting links.",
				Annotations: &mcpsdk.ToolAnnotations{IdempotentHint: true, OpenWorldHint: &closedWorld},
			},
			func(ctx context.Context, _ *mcpsdk.CallToolRequest, input tools.WriteCalendarInput) (*mcpsdk.CallToolResult, tools.WriteCalendarResult, error) {
				result, err := service.WriteCalendar(ctx, input)
				return nil, result, err
			},
		)
	}
}
