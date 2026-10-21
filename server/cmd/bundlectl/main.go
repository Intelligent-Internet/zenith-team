// Command bundlectl maintains the platform content zenith-team pins from
// Intelligent-Internet/boardly-plugins (ADR 0016): a tar and lock per Workflow
// component in internal/platformbundle, or a single JSON metadata lock per
// managed Tool component in internal/daemon/tool_releases.
//
//   - import pins a release tag, verifies every pin against its tag again
//     (--verify), or pins a local checkout for development (--dev);
//   - check validates plugin folders with the server's own parser and loader
//     and reports every problem, as text or as GitHub annotations;
//   - diff shows what the working tree pin changes against a git revision.
//
// import and diff hold every change to the release rules against the pinned
// release: never a lower version, never other content under a pinned
// version, a removed Workflow retired in a MAJOR release, and a retirement
// never undone.
//
// It is a maintainer and CI tool and never ships. It runs git with the
// caller's own credentials and never reads or stores a token. The root
// Makefile's bundle-* targets wrap it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

const usageText = `Usage:
  bundlectl import --tag <plugin>--vX.Y.Z [--repo <url-or-path>] [--out <dir>] [--summary <file>]
  bundlectl import --verify [--repo <url-or-path>] [--out <dir>]
  bundlectl import --dev --from <plugins-checkout> --plugin <name> [--out <dir>] [--summary <file>]
  bundlectl check [--format text|github] <plugin-dir>...
  bundlectl diff --base <git-rev> [--format text|markdown] [--out <dir>]
  bundlectl prepare [--pins <dir>] [--out <dir>] [--cache <dir>]

import pins a boardly-plugins release: it reads the tag's commit with git
(never a checkout), refuses a tag that is not on main (in a local checkout
with an origin: not on origin's main, or not the tag origin publishes),
validates the plugin with the server's own rules, holds it to the release
rules against the pin it replaces and rewrites <out>/<plugin>.lock.json,
plus <out>/<plugin>.bundle.tar for Workflows. --verify re-imports every locked tag and
fails unless the result is byte-identical. --dev pins a local checkout; such a
pin is for development and is never committed.

check validates plugin folders, such as plugins/zenith-workflows in a
boardly-plugins checkout, and reports every problem. Inside a git work tree
it reads only what a commit would carry.

diff compares the pinned bundles at a git revision with the working tree and
fails when a change breaks the release rules.

prepare downloads checksum-pinned npm packages, verifies their reviewed skills
and writes ignored build assets. Verified package cache entries work offline.

--repo defaults to https://github.com/Intelligent-Internet/boardly-plugins.git
and --out to internal/platformbundle (run from server/). For managedTools,
pass --out internal/daemon/tool_releases. Exit status: 0 on success, 1 on
failure, 2 on a usage error.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run dispatches one command and turns its result into the exit status.
// Commands print their own output and problems; run prints the final error.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	var err error
	switch args[0] {
	case "import":
		err = runImport(ctx, args[1:], stdout, stderr)
	case "check":
		err = runCheck(ctx, args[1:], stdout, stderr)
	case "diff":
		err = runDiff(ctx, args[1:], stdout, stderr)
	case "prepare":
		err = runPrepare(ctx, args[1:], stdout)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usageText)
		return 0
	default:
		err = usageErrorf("unknown command %q", args[0])
	}
	var usage *usageError
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.As(err, &usage):
		fmt.Fprintf(stderr, "bundlectl: %v\n\n%s", err, usageText)
		return 2
	default:
		fmt.Fprintf(stderr, "bundlectl: %v\n", err)
		return 1
	}
}

// usageError is a command line bundlectl cannot run; it exits 2.
type usageError struct{ message string }

func (err *usageError) Error() string { return err.message }

func usageErrorf(format string, args ...any) error {
	return &usageError{message: fmt.Sprintf(format, args...)}
}

// newFlagSet reports flag errors through run instead of printing them, so a
// bad flag prints its error and the usage once and exits 2.
func newFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet("bundlectl "+name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

// parseFlags parses a command's flags. -h prints the usage to stdout; any
// other flag error, or a positional argument where none is taken, is a usage
// error.
func parseFlags(flags *flag.FlagSet, args []string, stdout io.Writer, positional bool) error {
	err := flags.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(stdout, usageText)
		return err
	case err != nil:
		return usageErrorf("%v", err)
	case !positional && flags.NArg() > 0:
		return usageErrorf("%s takes no arguments, got %q", flags.Name(), flags.Args())
	}
	return nil
}
