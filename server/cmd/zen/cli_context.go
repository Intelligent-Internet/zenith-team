package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	daemonpkg "boardly.dev/boardly/server/internal/daemon"
	"boardly.dev/boardly/server/internal/runchannel"
)

// executionMode says who is holding the binary.
//
// Operator mode is a person at a terminal: credentials come from the
// authfile written by `zen auth login`, and the Company comes from a flag,
// the environment or the saved config. Agent mode is a provider process the
// daemon launched for one Run: run.json in BOARDLY_HOME names the Run, the
// daemon's Run channel carries every product call, and no flag or variable
// can move the command to another Company, Agent, Conversation or Task.
// The two never fall back to each other — an Agent shell that lost its
// channel fails, it does not borrow the operator's session.
type executionMode int

const (
	modeOperator executionMode = iota
	modeAgent
)

func (m executionMode) String() string {
	if m == modeAgent {
		return "agent"
	}
	return "operator"
}

// commandContext is the resolved mode plus, in Agent mode, the Run binding.
type commandContext struct {
	mode executionMode
	run  runchannel.RunControlConfig
	// channelErr explains why Agent mode has no usable channel (the Run
	// environment names a Run but the daemon left no run.json). API calls
	// surface it; identity commands can still explain the situation.
	channelErr error
	// explicitRequestKey is the operator's --request-key, validated.
	explicitRequestKey string
}

// modeError is a command refused because of the execution mode.
type modeError struct{ msg string }

func (e *modeError) Error() string { return e.msg }

// usageError is a command-line mistake the caller can fix.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// resolveCommandContext decides the mode. The presence of run.json is the
// authority; the Run environment variables only prevent a silent slide
// into Operator mode when that file is missing.
func resolveCommandContext() commandContext {
	home, err := daemonpkg.BoardlyHome()
	var control runchannel.RunControlConfig
	if err == nil {
		control, err = runchannel.LoadRunControlConfig(home)
	}
	if err == nil {
		return commandContext{mode: modeAgent, run: control}
	}
	if !runchannel.IsRunControlUnavailable(err) {
		return commandContext{mode: modeAgent, channelErr: &modeError{msg: "Run channel is unavailable: " + err.Error()}}
	}
	if runID := strings.TrimSpace(os.Getenv("BOARDLY_RUN_ID")); runID != "" ||
		strings.TrimSpace(os.Getenv("BOARDLY_RUN_TOKEN")) != "" {
		return commandContext{
			mode: modeAgent,
			channelErr: &modeError{msg: "this shell belongs to a Zenith Team Run but the daemon left no run.json in BOARDLY_HOME; " +
				"the Run channel is unavailable and Operator credentials are never used here"},
		}
	}
	return commandContext{mode: modeOperator}
}

// operatorOnly marks a command (and its subcommands) as unavailable inside a
// Run: signing in, driving the daemon and rewriting saved config are the
// operator's, never an Agent's.
func operatorOnly(cmd *cobra.Command) *cobra.Command {
	previous := cmd.PersistentPreRunE
	cmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if resolveCommandContext().mode == modeAgent {
			return &modeError{msg: fmt.Sprintf(
				"`zen %s` is an operator command and is not available inside a Zenith Team Run",
				cmd.CommandPath()[len("zen "):],
			)}
		}
		if previous != nil {
			return previous(cmd, args)
		}
		return nil
	}
	return cmd
}

// resolveCompanyID applies the selection ladder. In Agent mode the Run's
// Company is the only answer; a conflicting --company is an error rather
// than a silent override, so a wrong flag can never widen the scope.
func resolveCompanyID(cmd *cobra.Command, context commandContext) (string, error) {
	flagValue, _ := cmd.Flags().GetString("company")
	flagValue = strings.TrimSpace(flagValue)
	if context.mode == modeAgent {
		if context.channelErr != nil {
			return "", context.channelErr
		}
		if flagValue != "" && flagValue != context.run.CompanyID {
			return "", &modeError{msg: fmt.Sprintf(
				"this Run is bound to company %s; --company %s is not allowed in Agent mode",
				context.run.CompanyID, flagValue,
			)}
		}
		return context.run.CompanyID, nil
	}
	companyID := flagValue
	if companyID == "" {
		companyID = strings.TrimSpace(os.Getenv("BOARDLY_COMPANY_ID"))
	}
	if companyID == "" {
		saved, err := daemonpkg.LoadCLIConfig()
		if err != nil {
			return "", err
		}
		companyID = saved.CompanyID
	}
	if companyID == "" {
		return "", usagef(
			"no company selected: pass --company, set BOARDLY_COMPANY_ID, or run `zen company use <id>`",
		)
	}
	if _, err := uuid.Parse(companyID); err != nil {
		return "", usagef("company id %q is not a UUID", companyID)
	}
	return companyID, nil
}

// companySource explains where the effective Company came from, for
// `zen doctor` and `zen auth status`. It never prints a secret.
func companySource(cmd *cobra.Command, context commandContext) string {
	if context.mode == modeAgent {
		return "run.json (Agent mode)"
	}
	if value, _ := cmd.Flags().GetString("company"); strings.TrimSpace(value) != "" {
		return "--company flag"
	}
	if strings.TrimSpace(os.Getenv("BOARDLY_COMPANY_ID")) != "" {
		return "BOARDLY_COMPANY_ID"
	}
	if saved, err := daemonpkg.LoadCLIConfig(); err == nil && saved.CompanyID != "" {
		return "config.json company_id"
	}
	return "not set"
}
