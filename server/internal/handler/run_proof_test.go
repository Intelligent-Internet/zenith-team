package handler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/execution"
)

// A Run proof acts only for the member the Run works for, in their Company
// (ADR 0014, 0016): a live proof copied onto a colleague's session, or a Run
// with no authorizer, is refused.
func TestRunProofBearerIsTheRunsAuthorizer(t *testing.T) {
	t.Parallel()
	member, colleague, company := uuid.New(), uuid.New(), uuid.New()
	principal := auth.UserPrincipal{UserID: member, CompanyID: company}
	for _, test := range []struct {
		name   string
		origin execution.RunOrigin
		want   bool
	}{
		{"the member's own Run", execution.RunOrigin{CompanyID: company, AuthorizedByUserID: &member}, true},
		{"a colleague's Run", execution.RunOrigin{CompanyID: company, AuthorizedByUserID: &colleague}, false},
		{"a Run with no authorizer", execution.RunOrigin{CompanyID: company}, false},
		{"another Company's Run", execution.RunOrigin{CompanyID: uuid.New(), AuthorizedByUserID: &member}, false},
	} {
		if got := runProofBearer(principal, test.origin); got != test.want {
			t.Errorf("%s: runProofBearer() = %t, want %t", test.name, got, test.want)
		}
	}
}

// The server alone decides which Run reads as the Company's planner
// (IA-FR12): only the root Agent's Run in its person's direct chat, with its
// Company Plan tools on and with no Task and no tagging channel. Every other
// Run reads only its own Tasks.
func TestRunProofScopeDecidesThePlanner(t *testing.T) {
	t.Parallel()
	member, colleague := uuid.New(), uuid.New()
	taskID, projectID, channelID := uuid.New(), uuid.New(), uuid.New()
	planner := execution.RunOrigin{
		RunID: uuid.New(), CompanyID: uuid.New(), AgentID: uuid.New(), ConversationKind: "direct",
		OriginUserID: &member, AuthorizedByUserID: &member, RootAgent: true,
	}
	for _, test := range []struct {
		name   string
		change func(*execution.RunOrigin)
		want   bool
	}{
		{"the root Agent in its direct chat", func(*execution.RunOrigin) {}, true},
		{"planning switched off", func(o *execution.RunOrigin) { o.PlanningDisabled = true }, false},
		{"another Agent", func(o *execution.RunOrigin) { o.RootAgent = false }, false},
		{"a Task Run", func(o *execution.RunOrigin) {
			o.ConversationKind, o.TaskID, o.ProjectID = "task", &taskID, &projectID
		}, false},
		{"a tagged Run", func(o *execution.RunOrigin) {
			o.ConversationKind, o.InvocationChannelID = "channel_agent", &channelID
		}, false},
		{"a direct chat with a tagging channel", func(o *execution.RunOrigin) { o.InvocationChannelID = &channelID }, false},
		{"another member's direct chat", func(o *execution.RunOrigin) { o.OriginUserID = &colleague }, false},
		{"a direct chat with no origin member", func(o *execution.RunOrigin) { o.OriginUserID = nil }, false},
		{"a Schedule", func(o *execution.RunOrigin) { o.ConversationKind = "schedule" }, false},
	} {
		origin := planner
		test.change(&origin)
		scope, err := runProofScope(origin)
		if err != nil {
			t.Fatalf("%s: runProofScope() = %v", test.name, err)
		}
		if scope.Planner != test.want {
			t.Errorf("%s: Planner = %t, want %t", test.name, scope.Planner, test.want)
		}
	}
}

// Every route that resolves a Run proof checks its bearer through
// runProofBearer, so no route acts for a Run whose member is someone else.
// A new proof route that compares only the Company fails here.
func TestEveryRunProofRouteChecksItsBearer(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "run_proof.go" {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(source)
		if strings.Contains(text, ".ResolveRunOrigin(") &&
			strings.Count(text, ".ResolveRunOrigin(") > strings.Count(text, "runProofBearer(") {
			t.Errorf("%s resolves a Run proof without checking its bearer (runProofBearer)", name)
		}
	}
}
