// Command mcp is boardly-mcp, the Boardly MCP stdio server shipped inside
// the desktop bundle. The binary is named boardly-mcp by every build site's
// -o flag; desktop packaging and agent discovery key on that name.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"boardly.dev/boardly/server/internal/mcp"
)

func main() {
	os.Exit(mainExitCode())
}

// mainExitCode holds the deferred signal-handler teardown so main can call
// os.Exit without skipping it.
func mainExitCode() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := mcp.Run(ctx, os.Getenv); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "boardly-mcp: %v\n", err)
		return 1
	}
	return 0
}
