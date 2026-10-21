//go:build integration

package agent

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// seedRuntime inserts one more runtime for CompanyA, registered by daemonID
// for provider, owned by owner (NULL when uuid.Nil). The fixture's RuntimeA
// is codex on daemon-a, so a claude sibling on daemon-a is the same machine.
func seedRuntime(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
	daemonID, provider string,
	owner uuid.UUID,
) uuid.UUID {
	t.Helper()
	id := dbtest.NewUUID(t)
	ownerRef := uuid.NullUUID{UUID: owner, Valid: owner != uuid.Nil}
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes
			(id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, 'online', $6, now())`,
		id, fixture.CompanyA, daemonID, provider+" ("+daemonID+")", provider, ownerRef)
	return id
}

// seedMember adds a user with the given role to CompanyA.
func seedMember(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
	email, role string,
) uuid.UUID {
	t.Helper()
	id := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, $2, $3)`, id, email, email)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, $3)`,
		fixture.CompanyA, id, role)
	return id
}

func customNames(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ids ...uuid.UUID) map[uuid.UUID]*string {
	t.Helper()
	names := make(map[uuid.UUID]*string, len(ids))
	for _, id := range ids {
		var name *string
		if err := pool.QueryRow(ctx, `SELECT custom_name FROM runtimes WHERE id = $1`, id).
			Scan(&name); err != nil {
			t.Fatalf("read custom_name of %s: %v", id, err)
		}
		names[id] = name
	}
	return names
}

func sortedIDs(ids []string) []string {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	return sorted
}

func TestRenameRuntimeAppliesToEveryRuntimeOnTheMachine(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log := mustRuntimeLifecycleService(t, store)
	sibling := seedRuntime(t, ctx, pool, fixture, fixture.DaemonA, "claude", fixture.UserA)
	otherMachine := seedRuntime(t, ctx, pool, fixture, "daemon-x", "codex", fixture.UserA)
	// The same machine registered in another company: applyToMachine is
	// fenced to the company, so this row keeps its own name.
	otherCompany := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes
			(id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, $3, 'codex (daemon-a, company B)', 'codex', 'online', $4, now())`,
		otherCompany, fixture.CompanyB, fixture.DaemonA, fixture.UserB)
	owner := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}

	renamed, err := service.Rename(ctx, owner, fixture.RuntimeA, RuntimeRenameInput{
		CustomName: "  Office Mac  ", ApplyToMachine: true,
	})
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	// The returned projection is the full user-plane row: the daemon's name
	// is untouched, the custom name sits beside it, and the owner is joined.
	if renamed.ID != fixture.RuntimeA || renamed.Name != "Runtime A" ||
		renamed.CustomName == nil || *renamed.CustomName != "Office Mac" {
		t.Fatalf("Rename() = %+v, want Runtime A renamed to Office Mac", renamed)
	}
	if renamed.Owner == nil || renamed.Owner.ID != fixture.UserA {
		t.Fatalf("Rename().Owner = %+v, want the joined owner", renamed.Owner)
	}

	names := customNames(t, ctx, pool, fixture.RuntimeA, sibling, otherMachine, otherCompany)
	if names[fixture.RuntimeA] == nil || *names[fixture.RuntimeA] != "Office Mac" ||
		names[sibling] == nil || *names[sibling] != "Office Mac" {
		t.Fatalf("daemon-a custom names = %v / %v, want both Office Mac",
			names[fixture.RuntimeA], names[sibling])
	}
	if names[otherMachine] != nil {
		t.Fatalf("daemon-x custom name = %q, want NULL: another machine is not renamed", *names[otherMachine])
	}
	if names[otherCompany] != nil {
		t.Fatalf("company B daemon-a custom name = %q, want NULL: another company's row is not renamed",
			*names[otherCompany])
	}
	var daemonName string
	if err := pool.QueryRow(ctx, `SELECT name FROM runtimes WHERE id = $1`, sibling).Scan(&daemonName); err != nil {
		t.Fatalf("read sibling name: %v", err)
	}
	if daemonName != "claude (daemon-a)" {
		t.Fatalf("sibling name = %q, want the daemon's name untouched", daemonName)
	}

	// The roster serves the custom name on every renamed row.
	page, err := service.List(ctx, owner, RuntimeListOptions{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	listed := make(map[uuid.UUID]*string, len(page.Items))
	for _, item := range page.Items {
		listed[item.ID] = item.CustomName
	}
	if listed[fixture.RuntimeA] == nil || listed[sibling] == nil || listed[otherMachine] != nil {
		t.Fatalf("List() custom names = %+v, want the two daemon-a rows renamed", listed)
	}
	// So does the Agent projection's runtime ref, which is what the agent
	// roster renders.
	rows, err := ListProjectionsForMember(ctx, store.Queries(), owner, ListOptions{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListProjectionsForMember() = %+v, %v; want AgentA", rows, err)
	}
	if rows[0].Binding == nil || rows[0].Binding.Runtime.Name != "Runtime A" ||
		rows[0].Binding.Runtime.CustomName == nil || *rows[0].Binding.Runtime.CustomName != "Office Mac" {
		t.Fatalf("agent binding runtime ref = %+v, want name kept and customName Office Mac", rows[0].Binding)
	}

	// One refetch signal for the company, naming the machine and every row
	// it touched; nothing about availability changed.
	roster := rosterEvents(log, fixture.CompanyA, protocol.DaemonRosterActionUpdate)
	if len(roster) != 1 || roster[0].DaemonID != fixture.DaemonA {
		t.Fatalf("update roster events = %+v, want one for daemon-a", roster)
	}
	wantIDs := sortedIDs([]string{fixture.RuntimeA.String(), sibling.String()})
	if got := sortedIDs(roster[0].RuntimeIDs); strings.Join(got, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("update roster runtimeIds = %v, want %v", got, wantIDs)
	}
	if statuses := agentStatusEvents(log, fixture.CompanyA); len(statuses) != 0 {
		t.Fatalf("agent.status events = %+v, want none: a rename changes no availability", statuses)
	}
	if published := log.total(); published != 1 {
		t.Fatalf("events published = %d, want exactly the roster update", published)
	}
}

func TestRenameRuntimeWithoutApplyToMachineTouchesOneRow(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log := mustRuntimeLifecycleService(t, store)
	sibling := seedRuntime(t, ctx, pool, fixture, fixture.DaemonA, "claude", fixture.UserA)
	owner := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}

	renamed, err := service.Rename(ctx, owner, fixture.RuntimeA, RuntimeRenameInput{CustomName: "Office Mac"})
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	if renamed.CustomName == nil || *renamed.CustomName != "Office Mac" {
		t.Fatalf("Rename() = %+v, want Office Mac", renamed)
	}
	names := customNames(t, ctx, pool, fixture.RuntimeA, sibling)
	if names[fixture.RuntimeA] == nil || names[sibling] != nil {
		t.Fatalf(
			"custom names = %v / %v, want only the named runtime renamed",
			names[fixture.RuntimeA],
			names[sibling],
		)
	}
	roster := rosterEvents(log, fixture.CompanyA, protocol.DaemonRosterActionUpdate)
	if len(roster) != 1 || roster[0].DaemonID != fixture.DaemonA ||
		len(roster[0].RuntimeIDs) != 1 || roster[0].RuntimeIDs[0] != fixture.RuntimeA.String() {
		t.Fatalf("update roster events = %+v, want one naming only RuntimeA", roster)
	}
}

// TestRenameRuntimeLeavesRemovedSiblingsAlone: applyToMachine writes over the
// machine's LIVE rows. A removed sibling is hidden from every runtime read,
// so renaming one would store a name nobody can ever see — and would name it
// in the rename's own answer and in daemon.roster_changed{action:"update"},
// which would send clients after ids no read serves.
func TestRenameRuntimeLeavesRemovedSiblingsAlone(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log := mustRuntimeLifecycleService(t, store)
	// One machine that registered both providers; the owner removed the
	// claude one. (daemon-a already holds the fixture's codex runtime, and
	// (company, daemon, provider) is unique, so the pair lives on daemon-b.)
	live := seedRuntime(t, ctx, pool, fixture, "daemon-b", "codex", fixture.UserA)
	removed := seedRuntime(t, ctx, pool, fixture, "daemon-b", "claude", fixture.UserA)
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runtimes SET status = 'offline', removed_at = now() WHERE id = $1`, removed)
	owner := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}

	renamed, err := service.Rename(ctx, owner, live, RuntimeRenameInput{
		CustomName: "Office Mac", ApplyToMachine: true,
	})
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	if renamed.ID != live || renamed.CustomName == nil || *renamed.CustomName != "Office Mac" {
		t.Fatalf("Rename() = %+v, want the live daemon-b runtime renamed", renamed)
	}

	names := customNames(t, ctx, pool, live, removed)
	if names[live] == nil || *names[live] != "Office Mac" {
		t.Fatalf("live custom name = %v, want Office Mac", names[live])
	}
	if names[removed] != nil {
		t.Fatalf("removed sibling custom name = %q, want NULL: a removed runtime is not renamed",
			*names[removed])
	}
	roster := rosterEvents(log, fixture.CompanyA, protocol.DaemonRosterActionUpdate)
	if len(roster) != 1 || roster[0].DaemonID != "daemon-b" {
		t.Fatalf("update roster events = %+v, want one for daemon-b", roster)
	}
	if len(roster[0].RuntimeIDs) != 1 || roster[0].RuntimeIDs[0] != live.String() {
		t.Fatalf("update roster runtimeIds = %v, want only the live runtime %s",
			roster[0].RuntimeIDs, live)
	}
}

func TestRenameRuntimeClearsCustomNameWithBlankInput(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log := mustRuntimeLifecycleService(t, store)
	owner := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}

	if _, err := service.Rename(
		ctx,
		owner,
		fixture.RuntimeA,
		RuntimeRenameInput{CustomName: "Office Mac"},
	); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	cleared, err := service.Rename(ctx, owner, fixture.RuntimeA, RuntimeRenameInput{CustomName: "   "})
	if err != nil {
		t.Fatalf("Rename(blank) error = %v", err)
	}
	if cleared.CustomName != nil || cleared.Name != "Runtime A" {
		t.Fatalf("Rename(blank) = %+v, want the custom name cleared and the daemon's name back", cleared)
	}
	if names := customNames(t, ctx, pool, fixture.RuntimeA); names[fixture.RuntimeA] != nil {
		t.Fatalf("custom_name after clearing = %q, want NULL", *names[fixture.RuntimeA])
	}
	page, err := service.List(ctx, owner, RuntimeListOptions{})
	if err != nil || len(page.Items) != 1 || page.Items[0].CustomName != nil {
		t.Fatalf("List() after clearing = %+v, %v; want customName nil", page.Items, err)
	}
	if roster := rosterEvents(log, fixture.CompanyA, protocol.DaemonRosterActionUpdate); len(roster) != 2 {
		t.Fatalf("update roster events = %d, want one per rename", len(roster))
	}
}

func TestRenameRuntimeGate(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log := mustRuntimeLifecycleService(t, store)
	machineOwner := seedMember(t, ctx, pool, fixture, "machine-owner@example.com", "member")
	otherMember := seedMember(t, ctx, pool, fixture, "other-member@example.com", "member")
	// Every runtime is its registrant's alone (ADR 0014), so every refusal
	// below is the same not-found: nobody else's machine is visible to
	// anyone, whatever their role.
	ownedByMember := seedRuntime(t, ctx, pool, fixture, "daemon-c", "codex", machineOwner)
	secondOfMember := seedRuntime(t, ctx, pool, fixture, "daemon-p", "claude", machineOwner)
	unowned := seedRuntime(t, ctx, pool, fixture, "daemon-u", "claude", uuid.Nil)
	memberPrincipal := func(userID uuid.UUID) auth.UserPrincipal {
		return auth.UserPrincipal{UserID: userID, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember}
	}
	input := RuntimeRenameInput{CustomName: "Office Mac", ApplyToMachine: true}

	refusals := []struct {
		name      string
		principal auth.UserPrincipal
		runtimeID uuid.UUID
		input     RuntimeRenameInput
		want      error
	}{
		{
			name:      "a member cannot rename another member's machine",
			principal: memberPrincipal(otherMember), runtimeID: secondOfMember, input: input,
			want: ErrRuntimeNotFound,
		},
		{
			name:      "nobody renames a machine with no registrant",
			principal: memberPrincipal(otherMember), runtimeID: unowned, input: input,
			want: ErrRuntimeNotFound,
		},
		{
			name: "the company owner does not see a member's machine either",
			principal: auth.UserPrincipal{
				UserID:    fixture.UserA,
				CompanyID: fixture.CompanyA,
				Role:      auth.CompanyRoleOwner,
			},
			runtimeID: ownedByMember,
			input:     input,
			want:      ErrRuntimeNotFound,
		},
		{
			name:      "and an owner's machine is not the member's to rename",
			principal: memberPrincipal(machineOwner), runtimeID: fixture.RuntimeA, input: input,
			want: ErrRuntimeNotFound,
		},
		{
			name: "not even for the company owner, on an unowned row",
			principal: auth.UserPrincipal{
				UserID:    fixture.UserA,
				CompanyID: fixture.CompanyA,
				Role:      auth.CompanyRoleOwner,
			},
			runtimeID: unowned,
			input:     input,
			want:      ErrRuntimeNotFound,
		},
		{
			name: "another company's owner sees no runtime",
			principal: auth.UserPrincipal{
				UserID:    fixture.UserB,
				CompanyID: fixture.CompanyB,
				Role:      auth.CompanyRoleOwner,
			},
			runtimeID: fixture.RuntimeA,
			input:     input,
			want:      ErrRuntimeNotFound,
		},
		{
			name: "a non-member acting in the company sees no runtime",
			principal: auth.UserPrincipal{
				UserID:    fixture.UserB,
				CompanyID: fixture.CompanyA,
				Role:      auth.CompanyRoleOwner,
			},
			runtimeID: fixture.RuntimeA,
			input:     input,
			want:      ErrRuntimeNotFound,
		},
		{
			name: "an unknown runtime",
			principal: auth.UserPrincipal{
				UserID:    fixture.UserA,
				CompanyID: fixture.CompanyA,
				Role:      auth.CompanyRoleOwner,
			},
			runtimeID: dbtest.NewUUID(t),
			input:     input,
			want:      ErrRuntimeNotFound,
		},
		{
			name: "a name past 100 runes is refused before any write",
			principal: auth.UserPrincipal{
				UserID:    fixture.UserA,
				CompanyID: fixture.CompanyA,
				Role:      auth.CompanyRoleOwner,
			},
			runtimeID: fixture.RuntimeA,
			input:     RuntimeRenameInput{CustomName: strings.Repeat("a", 101)},
			want:      ErrRuntimeInvalidInput,
		},
	}
	for _, refusal := range refusals {
		if _, err := service.Rename(
			ctx,
			refusal.principal,
			refusal.runtimeID,
			refusal.input,
		); !errors.Is(
			err,
			refusal.want,
		) {
			t.Fatalf("%s: Rename() error = %v, want %v", refusal.name, err, refusal.want)
		}
	}
	for id, name := range customNames(t, ctx, pool, fixture.RuntimeA, ownedByMember, secondOfMember, unowned) {
		if name != nil {
			t.Fatalf("custom_name of %s = %q after refusals, want NULL", id, *name)
		}
	}
	if published := log.total(); published != 0 {
		t.Fatalf("events published after refusals = %d, want none", published)
	}

	// The machine's owner renames their own machine even as a plain member,
	// and the role the gate judges is the stored membership, not the
	// principal's claim.
	renamed, err := service.Rename(ctx, memberPrincipal(machineOwner), ownedByMember, input)
	if err != nil {
		t.Fatalf("Rename() by the machine owner error = %v", err)
	}
	if renamed.CustomName == nil || *renamed.CustomName != "Office Mac" || renamed.Owner == nil ||
		renamed.Owner.ID != machineOwner {
		t.Fatalf("Rename() by the machine owner = %+v", renamed)
	}
	claimedOwner := auth.UserPrincipal{
		UserID:    otherMember,
		CompanyID: fixture.CompanyA,
		Role:      auth.CompanyRoleOwner,
	}
	if _, err := service.Rename(ctx, claimedOwner, unowned, input); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf(
			"Rename() with a claimed owner role error = %v, want ErrRuntimeNotFound from the stored registrant",
			err,
		)
	}
	if roster := rosterEvents(log, fixture.CompanyA, protocol.DaemonRosterActionUpdate); len(roster) != 1 ||
		roster[0].DaemonID != "daemon-c" {
		t.Fatalf("update roster events = %+v, want one for daemon-c", roster)
	}

	// applyToMachine reaches every row the same daemon registered for the
	// registrant, and nothing else: the second machine, registered under the
	// same member but a different daemon, keeps its name.
	if names := customNames(t, ctx, pool, ownedByMember, secondOfMember); names[ownedByMember] == nil ||
		names[secondOfMember] != nil {
		t.Fatalf("custom names after the machine owner's rename = %+v, want only their daemon-c row", names)
	}
}

func TestRegisterRuntimesPreservesCustomName(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, _ := mustRuntimeLifecycleService(t, store)
	owner := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}

	if _, err := service.Rename(
		ctx,
		owner,
		fixture.RuntimeA,
		RuntimeRenameInput{CustomName: "Office Mac"},
	); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	// The daemon re-registers the same (company, daemon, provider) identity
	// with a new name and version: the upsert rewrites what it owns and
	// leaves the user's choice alone.
	registered, err := service.RegisterRuntimes(ctx, RuntimeRegistration{
		CompanyID:   fixture.CompanyA,
		OwnerUserID: fixture.UserA,
		DaemonID:    fixture.DaemonA,
		DeviceName:  "MacBook Pro",
		Runtimes: []RuntimeRegistrationSpec{
			{Name: "Codex (MacBook Pro)", Provider: "codex", Version: "9.9.9"},
		},
	})
	if err != nil {
		t.Fatalf("RegisterRuntimes() error = %v", err)
	}
	if len(registered) != 1 || registered[0].ID != fixture.RuntimeA ||
		registered[0].Name != "Codex (MacBook Pro)" || registered[0].DeviceInfo != "MacBook Pro · 9.9.9" ||
		registered[0].CustomName == nil || *registered[0].CustomName != "Office Mac" {
		t.Fatalf("RegisterRuntimes() = %+v, want the daemon's fields replaced and Office Mac kept", registered)
	}
	if names := customNames(t, ctx, pool, fixture.RuntimeA); names[fixture.RuntimeA] == nil ||
		*names[fixture.RuntimeA] != "Office Mac" {
		t.Fatalf("custom_name after re-registration = %v, want Office Mac", names[fixture.RuntimeA])
	}
}
