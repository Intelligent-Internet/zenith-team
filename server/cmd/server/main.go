// Command server is the Boardly cloud API process.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"boardly.dev/boardly/server/internal/config"
)

func main() {
	os.Exit(mainExitCode())
}

// mainExitCode holds the deferred signal-handler teardown so main can call
// os.Exit without skipping it.
func mainExitCode() int {
	bootstrapLogger, err := config.NewLogger(os.Stderr, "info")
	if err != nil {
		panic(err)
	}
	slog.SetDefault(bootstrapLogger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.LookupEnv, os.Stdout); err != nil {
		slog.Error("Zenith Team server stopped with an error", "error", err)
		return 1
	}
	return 0
}
