// Command testpostgres owns one PostgreSQL container while a child test command
// runs. The separate process is required because Go executes each tested package
// in its own process; no package-local singleton can span the full test command.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

const ryukDisabledEnv = "TESTCONTAINERS_RYUK_DISABLED"

type databaseServer struct {
	url   string
	close func(context.Context) error
}

type (
	startDatabaseServerFunc func(context.Context) (databaseServer, error)
	runCommandFunc          func(context.Context, []string, string) error
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: testpostgres <command> [args...]")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	go func() {
		<-ctx.Done()
		stop()
	}()
	err := runWithDatabase(ctx, os.Args[1:], startDatabaseServer, runCommand)
	stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "testpostgres: %v\n", err)
		os.Exit(exitCode(err))
	}
}

func exitCode(err error) int {
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		return 1
	}
	return portableExitCode(exitError.ExitCode())
}

func portableExitCode(code int) int {
	if code < 1 || code > 125 {
		return 1
	}
	return code
}

func runWithDatabase(
	ctx context.Context,
	args []string,
	start startDatabaseServerFunc,
	run runCommandFunc,
) error {
	if len(args) == 0 {
		return errors.New("child command is required")
	}
	server, err := start(ctx)
	if err != nil {
		return fmt.Errorf("start shared PostgreSQL test server: %w", err)
	}
	runErr := run(ctx, args, server.url)

	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cleanupCancel()
	closeErr := server.close(cleanupCtx)
	if closeErr != nil {
		closeErr = fmt.Errorf("stop shared PostgreSQL test server: %w", closeErr)
	}
	return errors.Join(runErr, closeErr)
}

func startDatabaseServer(ctx context.Context) (databaseServer, error) {
	restoreRyuk, err := disableRyukForParent()
	if err != nil {
		return databaseServer{}, err
	}

	startCtx, startCancel := context.WithTimeout(ctx, 90*time.Second)
	defer startCancel()
	container, err := tcpostgres.Run(startCtx, "postgres:17-alpine",
		tcpostgres.WithDatabase("boardly"),
		tcpostgres.WithUsername("boardly"),
		tcpostgres.WithPassword("boardly"),
		tcpostgres.BasicWaitStrategies(),
		testcontainers.WithCmd("postgres", "-c", "fsync=off", "-c", "max_connections=300"),
		testcontainers.WithLabels(map[string]string{"boardly.test.service": "postgres"}),
	)
	restoreErr := restoreRyuk()
	if err != nil {
		return databaseServer{}, errors.Join(
			err,
			restoreErr,
			terminateAfterFailure(ctx, container),
		)
	}
	if restoreErr != nil {
		return databaseServer{}, errors.Join(
			restoreErr,
			terminateAfterFailure(ctx, container),
		)
	}
	databaseURL, err := container.ConnectionString(startCtx, "sslmode=disable")
	if err != nil {
		return databaseServer{}, errors.Join(
			fmt.Errorf("get PostgreSQL connection string: %w", err),
			terminateAfterFailure(ctx, container),
		)
	}
	return databaseServer{
		url: databaseURL,
		close: func(closeCtx context.Context) error {
			return container.Terminate(closeCtx)
		},
	}, nil
}

func terminateAfterFailure(ctx context.Context, container *tcpostgres.PostgresContainer) error {
	if container == nil {
		return nil
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cleanupCancel()
	if err := container.Terminate(cleanupCtx); err != nil {
		return fmt.Errorf("stop failed PostgreSQL test container: %w", err)
	}
	return nil
}

func disableRyukForParent() (func() error, error) {
	previous, existed := os.LookupEnv(ryukDisabledEnv)
	if err := os.Setenv(ryukDisabledEnv, "true"); err != nil {
		return nil, fmt.Errorf("disable Ryuk for parent-owned container: %w", err)
	}
	return func() error {
		if existed {
			if err := os.Setenv(ryukDisabledEnv, previous); err != nil {
				return fmt.Errorf("restore Ryuk environment: %w", err)
			}
			return nil
		}
		if err := os.Unsetenv(ryukDisabledEnv); err != nil {
			return fmt.Errorf("restore Ryuk environment: %w", err)
		}
		return nil
	}, nil
}

func runCommand(ctx context.Context, args []string, databaseURL string) error {
	command := exec.CommandContext(ctx, args[0], args[1:]...)
	configureChildProcess(command)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Env = commandEnvironment(os.Environ(), databaseURL)
	return command.Run()
}

func commandEnvironment(base []string, databaseURL string) []string {
	environment := append([]string(nil), base...)
	prefix := dbtest.SharedDatabaseURLEnv + "="
	for index, variable := range environment {
		if strings.HasPrefix(variable, prefix) {
			environment[index] = prefix + databaseURL
			return environment
		}
	}
	return append(environment, prefix+databaseURL)
}
