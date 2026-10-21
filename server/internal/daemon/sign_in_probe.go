package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// signInProbeTimeout bounds one provider status command. Both CLIs answer
// from local files; anything slower is a hung process, not a slow answer.
const signInProbeTimeout = 15 * time.Second

// detectAgentSignIn asks the provider CLI whether it is signed in on this
// machine: `claude auth status --json` and `codex login status`. It never
// reads or forwards a credential — only the CLI's own verdict — and a
// command that fails or prints something unexpected reports unknown rather
// than signed out, so a hung or missing subcommand cannot demote a working
// runtime. It is the default cliProbes.signIn.
func detectAgentSignIn(ctx context.Context, provider, path string) protocol.RuntimeSignIn {
	probeCtx, cancel := context.WithTimeout(ctx, signInProbeTimeout)
	defer cancel()
	checkedAt := time.Now().UTC().Format(time.RFC3339)
	switch provider {
	case "claude":
		out, err := exec.CommandContext(probeCtx, path, "auth", "status", "--json").Output()
		return stamp(parseClaudeSignIn(out, timeoutOr(probeCtx, err)), checkedAt)
	case "codex":
		cmd := exec.CommandContext(probeCtx, path, "login", "status")
		var combined bytes.Buffer
		cmd.Stdout = &combined
		cmd.Stderr = &combined
		err := cmd.Run()
		return stamp(parseCodexSignIn(combined.String(), timeoutOr(probeCtx, err)), checkedAt)
	}
	return stamp(unknownSignIn("no sign-in check for this provider"), checkedAt)
}

// timeoutOr reports the deadline rather than the exit status a killed
// process leaves behind: os/exec prefers the child's ExitError to the
// context error, so the caller would otherwise read "exited with status -1".
func timeoutOr(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func stamp(signIn protocol.RuntimeSignIn, checkedAt string) protocol.RuntimeSignIn {
	signIn.CheckedAt = checkedAt
	return signIn
}

// parseClaudeSignIn reads the JSON `claude auth status --json` prints:
// {"loggedIn":bool,"authMethod":"...","subscriptionType":"..."}. The same
// document is what the connector backend already trusts.
func parseClaudeSignIn(out []byte, runErr error) protocol.RuntimeSignIn {
	var status struct {
		LoggedIn         bool   `json:"loggedIn"`
		AuthMethod       string `json:"authMethod"`
		SubscriptionType string `json:"subscriptionType"`
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return unknownSignIn(commandFailureDetail(runErr))
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &status); err != nil {
		return unknownSignIn("could not read the status the Claude CLI printed")
	}
	if !status.LoggedIn {
		return protocol.RuntimeSignIn{
			State:       protocol.RuntimeSignInSignedOut,
			BillingMode: "unknown",
			Detail:      "Not signed in. Run `claude auth login` on this computer, then refresh.",
		}
	}
	detail := "Signed in"
	billingMode := "unknown"
	switch {
	case status.SubscriptionType != "":
		detail += " (" + status.SubscriptionType + ")"
		billingMode = "subscription"
	case status.AuthMethod != "":
		detail += " (" + status.AuthMethod + ")"
		if status.AuthMethod == "api_key" {
			billingMode = "api"
		}
	}
	return protocol.RuntimeSignIn{
		State:       protocol.RuntimeSignInSignedIn,
		Detail:      boundSignInDetail(detail) + ".",
		BillingMode: billingMode,
	}
}

// parseCodexSignIn reads the line `codex login status` prints: "Logged in
// using ChatGPT", "Logged in using an API key", or "Not logged in". The
// command is text-only, so the verdict is read from the words and any other
// output is unknown.
func parseCodexSignIn(out string, runErr error) protocol.RuntimeSignIn {
	text := strings.TrimSpace(out)
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "not logged in"):
		return protocol.RuntimeSignIn{
			State:       protocol.RuntimeSignInSignedOut,
			BillingMode: "unknown",
			Detail:      "Not signed in. Run `codex login` on this computer, then refresh.",
		}
	case strings.Contains(lower, "logged in"):
		line, _, _ := strings.Cut(text, "\n")
		billingMode := "unknown"
		if strings.Contains(lower, "using chatgpt") {
			billingMode = "subscription"
		}
		if strings.Contains(lower, "using an api key") {
			billingMode = "api"
		}
		return protocol.RuntimeSignIn{
			State:       protocol.RuntimeSignInSignedIn,
			BillingMode: billingMode,
			Detail:      boundSignInDetail(strings.TrimSpace(line)) + ".",
		}
	case text == "":
		return unknownSignIn(commandFailureDetail(runErr))
	}
	return unknownSignIn("could not read the status the Codex CLI printed")
}

func unknownSignIn(detail string) protocol.RuntimeSignIn {
	return protocol.RuntimeSignIn{
		State:       protocol.RuntimeSignInUnknown,
		BillingMode: "unknown",
		Detail:      boundSignInDetail("Could not check sign-in: "+detail) + ".",
	}
}

func commandFailureDetail(err error) string {
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return "the CLI printed nothing"
	case errors.Is(err, context.DeadlineExceeded):
		return "the CLI did not answer in time"
	case errors.As(err, &exitErr):
		return fmt.Sprintf("the CLI exited with status %d", exitErr.ExitCode())
	}
	return "the CLI could not be started"
}

func boundSignInDetail(detail string) string {
	runes := []rune(strings.TrimRight(detail, "."))
	if len(runes) > protocol.RuntimeSignInDetailMaxLen-1 {
		return string(runes[:protocol.RuntimeSignInDetailMaxLen-1])
	}
	return string(runes)
}
