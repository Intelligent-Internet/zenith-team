//go:build integration

package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestAgentReadsReturnCanonicalBulkProjections(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE agents SET avatar_key = 'engineering-male', accent_color = '#58B8F2'
		WHERE id = $1`, fixture.AgentA)
	// runs.id is a bigserial: let the sequence assign it.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id,
			status
		, run_type) VALUES ($1, $2, $3, $4, $5, 'running', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))`,
		fixture.CompanyA, fixture.AgentA, fixture.TaskA,
		fixture.ConversationA, fixture.RuntimeA)

	service, _ := mustAgentService(t, store)
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	agents, err := service.List(ctx, principal, ListOptions{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("List() length = %d, want 1", len(agents))
	}
	got := agents[0]
	if got.ID != fixture.AgentA || got.ReportsTo != nil || got.Binding == nil ||
		got.Binding.AgentID != fixture.AgentA || got.Binding.Runtime.ID != fixture.RuntimeA ||
		got.Binding.Runtime.Name != "Runtime A" || got.Binding.Runtime.CustomName != nil ||
		got.Binding.Runtime.Status != "online" || got.Binding.Runtime.Provider != "codex" ||
		!got.RuntimeAvailable || got.UnavailableReason != nil || !got.Access.Execute ||
		got.AvatarKey == nil || *got.AvatarKey != "engineering-male" ||
		got.AccentColor == nil || *got.AccentColor != "#58B8F2" {
		t.Fatalf("List()[0] = %+v", got)
	}
	// The seeded running task-lane run holds one of the agent's slots. This
	// number is the capacity gate's own count, not a second tally kept for
	// display, so what a client renders is what the gate enforces. The cap is
	// the agents-table default of 3.
	if got.ActiveRunCount != 1 || got.MaxConcurrentRuns != 3 {
		t.Fatalf("List()[0] capacity = %d/%d, want 1/3",
			got.ActiveRunCount, got.MaxConcurrentRuns)
	}

	// A direct-conversation run is deliberately outside that count: chat
	// neither waits for a slot nor occupies one, so the same agent still
	// reads 1 with a second run of its own running.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runs (
			company_id, agent_id, conversation_id, runtime_id, status
		, run_type) VALUES ($1, $2, $3, $4, 'running', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))`,
		fixture.CompanyA, fixture.AgentA, fixture.DirectConversationA, fixture.RuntimeA)
	withDirectRun, err := service.List(ctx, principal, ListOptions{})
	if err != nil {
		t.Fatalf("List() after direct run error = %v", err)
	}
	if len(withDirectRun) != 1 || withDirectRun[0].ActiveRunCount != 1 {
		t.Fatalf("List() after direct run = %+v, want activeRunCount 1", withDirectRun)
	}
}

// The projection is the viewer's (ADR 0014): another member sees the same
// Agent with no binding, no execution and `not_bound`, and never the
// registrant's machine — its name and id appear nowhere in their payload.
// Setting the Agent up on a machine of their own is what changes that.
func TestAgentProjectionIsPerViewerAndNamesNoOtherMembersMachine(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `UPDATE runtimes SET custom_name = 'Quy secret laptop' WHERE id = $1`, fixture.RuntimeA)

	member := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Member')`,
		member, "member-"+member.String()+"@boardly.test")
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`, fixture.CompanyA, member)
	viewer := auth.UserPrincipal{UserID: member, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember}
	rows, err := ListProjectionsForMember(ctx, store.Queries(), viewer, ListOptions{})
	if err != nil || len(rows) != 1 || rows[0].Binding != nil || rows[0].RuntimeAvailable ||
		rows[0].Access.Execute || rows[0].UnavailableReason == nil || *rows[0].UnavailableReason != "not_bound" {
		t.Fatalf("another member's projection = %+v, %v; want not_bound with no binding", rows, err)
	}
	encoded, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatalf("encode projection: %v", err)
	}
	for _, secret := range []string{fixture.RuntimeA.String(), "Runtime A", "Quy secret laptop"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("another member's payload names the registrant's machine (%q): %s", secret, encoded)
		}
	}

	// Their own machine, their own binding: available for them, and still
	// invisible to the registrant, whose view is unchanged.
	theirs := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, 'daemon-member', 'Member machine', 'codex', 'online', $3, now())`,
		theirs, fixture.CompanyA, member)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA, member, theirs, "codex")
	rows, err = ListProjectionsForMember(ctx, store.Queries(), viewer, ListOptions{})
	if err != nil || len(rows) != 1 || rows[0].Binding == nil || rows[0].Binding.Runtime.ID != theirs ||
		!rows[0].RuntimeAvailable || !rows[0].Access.Execute || rows[0].UnavailableReason != nil {
		t.Fatalf("bound member's projection = %+v, %v; want their own binding, available", rows, err)
	}
	registrant := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	rows, err = ListProjectionsForMember(ctx, store.Queries(), registrant, ListOptions{})
	if err != nil || len(rows) != 1 || rows[0].Binding == nil || rows[0].Binding.Runtime.ID != fixture.RuntimeA {
		t.Fatalf("registrant's projection = %+v, %v; want their own binding untouched", rows, err)
	}

	// A member who leaves loses the binding the revocation deletes; a
	// binding row that outlived a membership never reads as available.
	dbtest.MustExec(t, ctx, pool, `
		UPDATE company_members
		SET status = 'revoked', revoked_at = clock_timestamp(), revoked_by_user_id = $2
		WHERE company_id = $1 AND user_id = $2`, fixture.CompanyA, member)
	rows, err = ListProjectionsForMember(ctx, store.Queries(), viewer, ListOptions{})
	if err == nil && len(rows) != 0 {
		t.Fatalf("revoked member still lists %d agents", len(rows))
	}
}

// Precedence: agent_archived > not_bound > runtime_unusable >
// runtime_offline > runtime_signed_out. An unusable runtime is structurally
// offline, so the unusable branch must fire before the offline one, because
// fixing the CLI is the recovery the reason names.
func TestAgentUnavailableReasonRanksUnusableAboveOffline(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runtimes
		SET status = 'offline', unusable_reason_code = 'not_executable',
		    unusable_reason_message = 'Codex CLI is not executable'
		WHERE id = $1`, fixture.RuntimeA)

	ownerA := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	rows, err := ListProjectionsForMember(ctx, store.Queries(), ownerA, ListOptions{})
	if err != nil || len(rows) != 1 || rows[0].RuntimeAvailable ||
		rows[0].UnavailableReason == nil || *rows[0].UnavailableReason != "runtime_unusable" {
		t.Fatalf("unusable projection = %+v, %v; want runtime_unusable", rows, err)
	}

	// A signed-out provider on an online machine is its own reason; offline
	// outranks it.
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runtimes
		SET status = 'online', unusable_reason_code = NULL, unusable_reason_message = NULL,
		    provider_sign_in_state = 'signed_out', provider_sign_in_checked_at = now()
		WHERE id = $1`, fixture.RuntimeA)
	rows, err = ListProjectionsForMember(ctx, store.Queries(), ownerA, ListOptions{})
	if err != nil || len(rows) != 1 || rows[0].RuntimeAvailable ||
		rows[0].UnavailableReason == nil || *rows[0].UnavailableReason != "runtime_signed_out" {
		t.Fatalf("signed-out projection = %+v, %v; want runtime_signed_out", rows, err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runtimes SET status = 'offline' WHERE id = $1`, fixture.RuntimeA)
	rows, err = ListProjectionsForMember(ctx, store.Queries(), ownerA, ListOptions{})
	if err != nil || len(rows) != 1 || rows[0].UnavailableReason == nil ||
		*rows[0].UnavailableReason != "runtime_offline" {
		t.Fatalf("offline + signed-out projection = %+v, %v; want runtime_offline", rows, err)
	}

	// Archiving still wins over everything.
	dbtest.MustExec(t, ctx, pool, `UPDATE agents SET archived_at = now() WHERE id = $1`, fixture.AgentA)
	rows, err = ListProjectionsForMember(ctx, store.Queries(), ownerA, ListOptions{IncludeArchived: true})
	if err != nil || len(rows) != 1 || rows[0].UnavailableReason == nil ||
		*rows[0].UnavailableReason != "agent_archived" {
		t.Fatalf("archived + offline projection = %+v, %v; want agent_archived", rows, err)
	}
}
