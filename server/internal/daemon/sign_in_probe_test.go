package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"boardly.dev/boardly/server/pkg/protocol"
)

func TestParseClaudeSignIn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		out    string
		err    error
		state  string
		detail string
	}{
		{
			name:   "signed in with subscription",
			out:    `{"loggedIn":true,"authMethod":"claude.ai","subscriptionType":"max"}`,
			state:  protocol.RuntimeSignInSignedIn,
			detail: "Signed in (max).",
		},
		{
			name:   "signed in with method only",
			out:    `{"loggedIn":true,"authMethod":"api_key"}`,
			state:  protocol.RuntimeSignInSignedIn,
			detail: "Signed in (api_key).",
		},
		{
			name:   "signed out",
			out:    `{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty"}`,
			state:  protocol.RuntimeSignInSignedOut,
			detail: "Not signed in. Run `claude auth login` on this computer, then refresh.",
		},
		{
			name:   "empty output with exit error",
			out:    "",
			err:    errors.New("exit status 1"),
			state:  protocol.RuntimeSignInUnknown,
			detail: "Could not check sign-in: the CLI could not be started.",
		},
		{
			name:   "timed out",
			out:    "",
			err:    context.DeadlineExceeded,
			state:  protocol.RuntimeSignInUnknown,
			detail: "Could not check sign-in: the CLI did not answer in time.",
		},
		{
			name:   "oversized subscription",
			out:    `{"loggedIn":true,"subscriptionType":"` + strings.Repeat("x", 600) + `"}`,
			state:  protocol.RuntimeSignInSignedIn,
			detail: "Signed in (" + strings.Repeat("x", 244) + ".",
		},
		{
			name:   "garbage",
			out:    "Usage: claude auth",
			state:  protocol.RuntimeSignInUnknown,
			detail: "Could not check sign-in: could not read the status the Claude CLI printed.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := parseClaudeSignIn([]byte(test.out), test.err)
			if got.State != test.state || got.Detail != test.detail {
				t.Fatalf(
					"parseClaudeSignIn = %+v, want state %q detail %q",
					got,
					test.state,
					test.detail,
				)
			}
		})
	}
}

func TestParseCodexSignIn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		out    string
		state  string
		detail string
	}{
		{
			name:   "chatgpt",
			out:    "Logged in using ChatGPT\n",
			state:  protocol.RuntimeSignInSignedIn,
			detail: "Logged in using ChatGPT.",
		},
		{
			name:   "api key",
			out:    "Logged in using an API key",
			state:  protocol.RuntimeSignInSignedIn,
			detail: "Logged in using an API key.",
		},
		{
			name:   "signed out",
			out:    "Not logged in\n",
			state:  protocol.RuntimeSignInSignedOut,
			detail: "Not signed in. Run `codex login` on this computer, then refresh.",
		},
		{
			name:   "unexpected",
			out:    "error: unknown subcommand",
			state:  protocol.RuntimeSignInUnknown,
			detail: "Could not check sign-in: could not read the status the Codex CLI printed.",
		},
		{
			name:   "silent",
			out:    "",
			state:  protocol.RuntimeSignInUnknown,
			detail: "Could not check sign-in: the CLI printed nothing.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := parseCodexSignIn(test.out, nil)
			if got.State != test.state || got.Detail != test.detail {
				t.Fatalf(
					"parseCodexSignIn = %+v, want state %q detail %q",
					got,
					test.state,
					test.detail,
				)
			}
		})
	}
}

func TestSignInDetailIsBounded(t *testing.T) {
	t.Parallel()

	got := parseCodexSignIn("Logged in "+strings.Repeat("x", 600), nil)
	if n := len([]rune(got.Detail)); n > protocol.RuntimeSignInDetailMaxLen {
		t.Fatalf("detail has %d runes, want at most %d", n, protocol.RuntimeSignInDetailMaxLen)
	}
}
