// Command zen is the Zenith Team CLI. In an operator's terminal it signs in
// (`zen auth login`), runs the local agent daemon (`zen daemon ...`), and
// works with the product API (`zen task ...`, `zen run ...`, `zen agent ...`).
// Inside an agent process the same binary runs in Agent mode: it reads the
// run.json the daemon placed in its ZENITH_TEAM_HOME and routes every product
// call through the daemon's Run channel, so an Agent can inspect and act on
// its own Run without ever holding a credential.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// version is stamped by the build; "dev" for local builds.
var version = "dev"

const (
	groupCore       = "core"
	groupRuntime    = "runtime"
	groupAdditional = "additional"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "zen",
		Short: "Zenith Team CLI",
		Long: "Zenith Team CLI — sign in, run the local agent daemon, and work with\n" +
			"Companies, Tasks, Runs, Conversations and Agents from a terminal.\n\n" +
			"Inside a Zenith Team Run the same binary runs in Agent mode: it is bound\n" +
			"to that Run's Company, Agent, Conversation and Task and needs no login.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// The daemon probes `zen --version` to learn what a replaced binary on
	// disk reports (daemon.ParseSelfVersion); keep the two in step.
	root.SetVersionTemplate("zen {{.Version}}\n")
	root.PersistentFlags().String(
		"output",
		"",
		"output format: table or json (lists default to table, single objects to json)",
	)
	root.PersistentFlags().String(
		"company",
		"",
		"company id (default: ZENITH_TEAM_COMPANY_ID, then the saved company_id; fixed in Agent mode)",
	)
	root.PersistentFlags().String(
		"request-key",
		"",
		"idempotency key (UUID) for a mutation; derived from the command in Agent mode when omitted",
	)
	root.PersistentFlags().Bool("debug", false, "print the full error chain")

	root.AddGroup(
		&cobra.Group{ID: groupCore, Title: "Core commands:"},
		&cobra.Group{ID: groupRuntime, Title: "Runtime commands:"},
		&cobra.Group{ID: groupAdditional, Title: "Additional commands:"},
	)
	for _, cmd := range []*cobra.Command{
		newAuthCmd(),
		newCompanyCmd(),
		newTaskCmd(),
		newProjectCmd(),
		newRunCmd(),
		newConversationCmd(),
		newQuestionCmd(),
		newApprovalCmd(),
		newInboxCmd(),
		newScheduleCmd(),
		newAgentCmd(),
		newRuntimeCmd(),
		newMailCmd(),
		newAnalyticsCmd(),
	} {
		cmd.GroupID = groupCore
		root.AddCommand(cmd)
	}
	for _, cmd := range []*cobra.Command{newDaemonCmd(), newConfigCmd(), newDoctorCmd()} {
		cmd.GroupID = groupRuntime
		root.AddCommand(cmd)
	}
	for _, cmd := range []*cobra.Command{newVersionCmd(), newDesktopSessionCmd()} {
		cmd.GroupID = groupAdditional
		root.AddCommand(cmd)
	}
	root.SetHelpCommandGroupID(groupAdditional)
	root.SetCompletionCommandGroupID(groupAdditional)
	rejectUnknownSubcommands(root)
	return root
}

// rejectUnknownSubcommands makes `zen <family> <unknown>` fail the way the
// root does: "unknown command" on stderr, exit 1. Cobra rejects an unknown
// subcommand only at the root; a family with no Run accepts `zen task
// frobnicate`, prints its help on stdout and exits 0, which breaks the rule
// that a failure terminates non-zero and stdout stays parseable. Args must be
// paired with a Run, because cobra asks for help on a non-runnable command
// before it validates args; a bare `zen <family>` therefore still prints
// its help, now through that Run. A family that already runs on its own
// (`zen config` shows the config) keeps its Run and only gains the guard.
func rejectUnknownSubcommands(cmd *cobra.Command) {
	for _, sub := range cmd.Commands() {
		if sub.HasSubCommands() {
			if sub.Args == nil {
				sub.Args = cobra.NoArgs
			}
			if !sub.Runnable() {
				sub.RunE = func(c *cobra.Command, _ []string) error { return c.Help() }
				sub.DisableFlagsInUseLine = true
			}
		}
		rejectUnknownSubcommands(sub)
	}
}

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		debug := false
		if flag := root.PersistentFlags().Lookup("debug"); flag != nil {
			debug = flag.Value.String() == "true"
		}
		output := ""
		if flag := root.PersistentFlags().Lookup("output"); flag != nil {
			output = strings.ToLower(strings.TrimSpace(flag.Value.String()))
		}
		fmt.Fprintln(os.Stderr, formatCommandError(err, debug, output))
		os.Exit(exitCodeFor(err))
	}
}

// Exit codes: scripts and agents branch on these, never on message text.
const (
	exitGeneric    = 1
	exitNetwork    = 2
	exitAuth       = 3
	exitNotFound   = 4
	exitValidation = 5
	exitConflict   = 6
)

// exitCodeFor classifies an error the way a caller can act on it.
func exitCodeFor(err error) int {
	var api *apiError
	if errors.As(err, &api) {
		switch api.Status {
		case 401, 403:
			return exitAuth
		case 404:
			return exitNotFound
		case 409:
			return exitConflict
		case 400, 413, 415, 422:
			return exitValidation
		case 0:
			return exitNetwork
		}
		return exitGeneric
	}
	var usage *usageError
	if errors.As(err, &usage) {
		return exitValidation
	}
	var network *networkError
	if errors.As(err, &network) {
		return exitNetwork
	}
	var mode *modeError
	if errors.As(err, &mode) {
		return exitAuth
	}
	return exitGeneric
}

// formatCommandError is the line a failure prints on stderr. With
// `--output json` it is one JSON object instead of prose, so an Agent or a
// script that asked for JSON branches on the code and passes the message on
// without parsing a sentence; stdout stays empty either way.
func formatCommandError(err error, debug bool, output string) string {
	if output == outputJSON {
		return jsonCommandError(err)
	}
	if debug {
		return "zen: " + fmt.Sprintf("%+v", err)
	}
	return "zen: " + err.Error()
}

type commandErrorBody struct {
	Error commandErrorDetail `json:"error"`
}

// commandErrorDetail carries the server's code and its own sentence — for a
// Run's refused Task read, "Permission denied. You can only access your own
// Tasks." — next to the HTTP status and the exit code the process ends with.
type commandErrorDetail struct {
	Code      string `json:"code,omitempty"`
	Message   string `json:"message"`
	Status    int    `json:"status,omitempty"`
	ExitCode  int    `json:"exitCode"`
	RequestID string `json:"requestId,omitempty"`
}

func jsonCommandError(err error) string {
	detail := commandErrorDetail{Message: err.Error(), ExitCode: exitCodeFor(err)}
	var api *apiError
	if errors.As(err, &api) {
		detail.Code, detail.Status, detail.RequestID = api.Code, api.Status, api.RequestID
		detail.Message = api.Detail
	}
	encoded, marshalErr := json.Marshal(commandErrorBody{Error: detail})
	if marshalErr != nil {
		return "zen: " + err.Error()
	}
	return string(encoded)
}
