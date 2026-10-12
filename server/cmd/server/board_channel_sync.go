package main

import (
	"context"

	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/events"
)

// registerBoardChannelSync brings a Board's chat in step with the Board after
// its access changes commit: people who lost the Board leave the chat, and
// its Agents follow the Board's invitations. The Board's own transaction
// never touches the chat, which a Run drawing on the Board may hold.
func registerBoardChannelSync(bus *events.Bus, channels conversation.BoardChannelService) error {
	return bus.Subscribe(canvas.EventBoardChanged, func(ctx context.Context, e events.Event) error {
		change, err := events.PayloadOf[canvas.BoardChanged](e)
		if err != nil || !change.Access {
			return err
		}
		return channels.Sync(ctx, e.CompanyID, change.BoardID)
	})
}
