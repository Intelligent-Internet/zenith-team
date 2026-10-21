package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/runchannel"
)

// A Run mentioned in a chat reads a Project only when everyone there can
// read it. The server refuses any other with the same fixed sentence it
// gives a Task that is not the Run's own, and `zen project get` prints that
// sentence as it stands, exactly like `zen task get`: no request line in
// front, so the path with the Company and Project ids never reaches the
// chat. It is an authorization failure (exit 3).
func TestProjectGetInsideARunRelaysTheRefusalAsTheBareSentence(t *testing.T) {
	const refusal = "Permission denied. You can only access your own Tasks."
	const hidden = "9df5296c-0000-4000-8000-000000000001"
	channel := newFakeRunChannel(t, func(request runchannel.Request) (int, string) {
		if request.Path == "/companies/"+testCompanyID+"/projects/"+hidden {
			return 403, `{"detail":"` + refusal + `","code":"task_outside_run_scope","requestId":"req-9"}`
		}
		return 403, `{"detail":"This Run's scope does not admit it","code":"run_scope_denied"}`
	})
	// A channel Run: it has no Task of its own and reads Company Projects.
	seedAgentShell(t, channel, "", []string{"zen project list", "zen project get <id>"})

	_, err := executeCLI(t, newRootCmd, "project", "get", hidden)
	if err == nil || exitCodeFor(err) != exitAuth {
		t.Fatalf(
			"zen project get = %v (exit %d), want the refusal with exit %d",
			err,
			exitCodeFor(err),
			exitAuth,
		)
	}
	want := "zen: " + refusal + " (code: task_outside_run_scope) [request req-9]"
	if message := formatCommandError(err, false, ""); message != want {
		t.Fatalf("zen project get printed %q, want %q", message, want)
	}
	var printed commandErrorBody
	if decodeErr := json.Unmarshal([]byte(formatCommandError(err, false, outputJSON)), &printed); decodeErr != nil {
		t.Fatalf("zen project get JSON error does not decode: %v", decodeErr)
	}
	if want := (commandErrorDetail{
		Code: "task_outside_run_scope", Message: refusal, Status: 403, ExitCode: exitAuth, RequestID: "req-9",
	}); printed.Error != want {
		t.Fatalf("zen project get JSON error = %+v, want %+v", printed.Error, want)
	}

	// Any other refusal keeps the request line: it names nothing new and
	// tells an operator which call failed.
	_, err = executeCLI(t, newRootCmd, "project", "list")
	if err == nil {
		t.Fatal("zen project list was admitted by a channel that refuses it")
	}
	if message := formatCommandError(
		err,
		false,
		"",
	); !strings.Contains(
		message,
		"GET /companies/"+testCompanyID+"/projects: ",
	) {
		t.Fatalf("zen project list lost its request line: %q", message)
	}
}
