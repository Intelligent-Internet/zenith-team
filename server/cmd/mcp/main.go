// Command mcp is zenith-team-mcp, the Zenith Team MCP stdio server shipped inside
// the desktop bundle. The binary is named zenith-team-mcp by every build site's
// -o flag; desktop packaging and agent discovery key on that name.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Intelligent-Internet/zenith-team/server/internal/mcp"
)

// version is the release version, set at build time with
// -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(version)
		return
	}
	os.Exit(mainExitCode())
}

// mainExitCode holds the deferred signal-handler teardown so main can call
// os.Exit without skipping it.
func mainExitCode() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := mcp.Run(ctx, os.Getenv); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "zenith-team-mcp: %v\n", err)
		return 1
	}
	return 0
}
