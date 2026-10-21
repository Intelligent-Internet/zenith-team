// Command server is the Zenith Team cloud API process.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Intelligent-Internet/zenith-team/server/internal/config"
)

// version is the release version, set at build time with
// -ldflags "-X main.version=1.2.3".
var version = "dev"

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
	slog.Info("Zenith Team server starting", "version", version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.LookupEnv, os.Stdout); err != nil {
		slog.Error("Zenith Team server stopped with an error", "error", err)
		return 1
	}
	return 0
}
