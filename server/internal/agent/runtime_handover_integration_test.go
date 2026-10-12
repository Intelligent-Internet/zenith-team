//go:build integration

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// handoverActivity is one runtime.handed_over Activity row as stored.
type handoverActivity struct {
	actorType, actorID string
	entityType         string
	entityID           string
	visibility         string
	visibilityUserID   uuid.UUID
	details            map[string]json.RawMessage
}

func readHandoverActivity(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID uuid.UUID,
) []handoverActivity {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT actor_type, coalesce(actor_id, ''), entity_type, entity_id,
		       coalesce(visibility, ''), visibility_user_id, details
		FROM activity_events
		WHERE company_id = $1 AND action = 'runtime.handed_over'
		ORDER BY created_at, id`, companyID)
	if err != nil {
		t.Fatalf("read runtime.handed_over activity: %v", err)
	}
	defer rows.Close()
	var out []handoverActivity
	for rows.Next() {
		var row handoverActivity
		var visibilityUser uuid.NullUUID
		var details []byte
		if err := rows.Scan(&row.actorType, &row.actorID, &row.entityType, &row.entityID,
			&row.visibility, &visibilityUser, &details); err != nil {
			t.Fatalf("scan runtime.handed_over activity: %v", err)
		}
		row.visibilityUserID = visibilityUser.UUID
		if err := json.Unmarshal(details, &row.details); err != nil {
			t.Fatalf("decode runtime.handed_over details: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read runtime.handed_over activity: %v", err)
	}
	return out
}

func countBindingActivity(t *testing.T, ctx context.Context, pool *pgxpool.Pool, companyID uuid.UUID) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM activity_events
		WHERE company_id = $1 AND action = $2`, companyID, EventAgentBindingChanged).Scan(&count); err != nil {
		t.Fatalf("count binding activity: %v", err)
	}
	return count
}

// readHandover is a runtime's owner, status and handover stamp as stored.
func readHandover(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	runtimeID uuid.UUID,
) (uuid.UUID, string, bool) {
	t.Helper()
	var owner uuid.NullUUID
	var status string
	var handedOver bool
	if err := pool.QueryRow(ctx,
		`SELECT owner_user_id, status, handed_over_at IS NOT NULL FROM runtimes WHERE id = $1`, runtimeID,
	).Scan(&owner, &status, &handedOver); err != nil {
		t.Fatalf("read runtime %s: %v", runtimeID, err)
	}
	return owner.UUID, status, handedOver
}

func registerOnMachine(
	t *testing.T,
	ctx context.Context,
	service *DefaultRuntimeService,
	companyID, owner uuid.UUID,
	daemonID string,
) []Runtime {
	t.Helper()
	registered, err := service.RegisterRuntimes(ctx, RuntimeRegistration{
		CompanyID:   companyID,
		OwnerUserID: owner,
		DaemonID:    daemonID,
		DeviceName:  "Olivia's MacBook",
		LaunchedBy:  "desktop",
		Runtimes: []RuntimeRegistrationSpec{
			{Provider: "codex", Version: "1.0.0"},
			{Provider: "claude", Version: "2.0.0"},
		},
	})
	if err != nil {
		t.Fatalf("RegisterRuntimes(%s) error = %v", owner, err)
	}
	if len(registered) != 2 {
		t.Fatalf("RegisterRuntimes(%s) = %d runtimes, want 2", owner, len(registered))
	}
	return registered
}

func runtimeIDsOf(runtimes []Runtime) map[uuid.UUID]bool {
	ids := make(map[uuid.UUID]bool, len(runtimes))
	for _, runtime := range runtimes {
		ids[runtime.ID] = true
	}
	return ids
}

// D33: a second person signing in on the same computer gets runtimes of
// their own, and the first person's setup stays exactly where it was. The
// computer is one person's at a time, so the first person's rows go offline,
// and they are told by an Inbox notice and a private Activity row that names
// the person who signed in — not themselves.
func TestRegisteringOnAnotherPersonsComputerKeepsTheirSetup(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, agentLog, notices := mustRuntimeNoticeService(t, store)
	olivia := fixture.UserA
	oliviaClaude := seedRuntime(t, ctx, pool, fixture, fixture.DaemonA, "claude", olivia)
	queued := seedRun(t, ctx, pool, fixture, fixture.AgentA, fixture.RuntimeA, olivia, "queued")
	mia := seedMember(t, ctx, pool, fixture, "mia@example.com", "member")
	dbtest.MustExec(t, ctx, pool, `UPDATE users SET display_name = 'Mia' WHERE id = $1`, mia)
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runtimes SET custom_name = 'Olivia''s Mac' WHERE id = $1`, fixture.RuntimeA)

	// Olivia signs out (the daemon deregisters on its way down) and Mia
	// signs in on the same desktop.
	setRuntimeOffline(t, ctx, pool, fixture.RuntimeA)
	setRuntimeOffline(t, ctx, pool, oliviaClaude)
	miasRuntimes := registerOnMachine(t, ctx, service, fixture.CompanyA, mia, fixture.DaemonA)

	for _, runtime := range miasRuntimes {
		if runtime.ID == fixture.RuntimeA || runtime.ID == oliviaClaude {
			t.Fatalf("Mia's registration took over Olivia's runtime %s", runtime.ID)
		}
		if owner, status, handedOver := readHandover(t, ctx, pool, runtime.ID); owner != mia ||
			status != "online" || handedOver {
			t.Fatalf("Mia's runtime %s = owner %s, %s, handed over %t; want hers, online, not handed over",
				runtime.ID, owner, status, handedOver)
		}
	}
	for _, id := range []uuid.UUID{fixture.RuntimeA, oliviaClaude} {
		if owner, status, handedOver := readHandover(t, ctx, pool, id); owner != olivia ||
			status != "offline" || !handedOver {
			t.Fatalf("Olivia's runtime %s = owner %s, %s, handed over %t; want hers, offline, handed over",
				id, owner, status, handedOver)
		}
	}
	if got := readBindingRuntime(t, ctx, pool, fixture.AgentA, olivia); got != fixture.RuntimeA {
		t.Fatalf("Olivia's binding = %s, want kept on her runtime %s", got, fixture.RuntimeA)
	}
	if runtimeID, status, reason := readRun(t, ctx, pool, queued); runtimeID != fixture.RuntimeA ||
		status != "queued" || reason != "" {
		t.Fatalf(
			"Olivia's queued run = %s / %s / %q, want still queued on her runtime",
			runtimeID,
			status,
			reason,
		)
	}
	if bindings := agentLog.binding.snapshot(); len(bindings) != 0 {
		t.Fatalf("agent.binding_changed = %+v, want none: nobody's setup changed", bindings)
	}
	if count := countBindingActivity(t, ctx, pool, fixture.CompanyA); count != 0 {
		t.Fatalf("binding Activity rows = %d, want none", count)
	}

	// Each person reads their own computer and nobody else's.
	for viewer, want := range map[uuid.UUID]map[uuid.UUID]bool{
		mia:    runtimeIDsOf(miasRuntimes),
		olivia: {fixture.RuntimeA: true, oliviaClaude: true},
	} {
		listed, err := service.List(ctx, auth.UserPrincipal{
			UserID: viewer, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember,
		}, RuntimeListOptions{})
		if err != nil {
			t.Fatalf("List(%s) error = %v", viewer, err)
		}
		if len(listed.Items) != len(want) {
			t.Fatalf("List(%s) = %d runtimes, want %d", viewer, len(listed.Items), len(want))
		}
		for _, runtime := range listed.Items {
			if !want[runtime.ID] {
				t.Fatalf("List(%s) includes %s, which is not theirs", viewer, runtime.ID)
			}
		}
	}

	// Olivia is told once, about the computer, by name of who signed in.
	stored := readRuntimeNotices(t, ctx, pool, fixture.CompanyA)
	if len(stored) != 1 {
		t.Fatalf("runtime notices = %+v, want one for Olivia", stored)
	}
	notice := stored[0]
	if notice.recipient != olivia || notice.kind != string(inbox.TypeRuntimeOffline) ||
		notice.entityType != string(protocol.EntityRuntime) ||
		(notice.entityID != fixture.RuntimeA.String() && notice.entityID != oliviaClaude.String()) ||
		notice.title != "Mia is using Olivia's Mac" || notice.archived ||
		!strings.HasPrefix(notice.deepLink, "/settings/agents/") ||
		!strings.Contains(notice.dedupeKey, ":handed_over:") {
		t.Fatalf("handover notice = %+v", notice)
	}
	if created := notices.created.snapshot(); len(created) != 1 {
		t.Fatalf("inbox.created events = %d, want one", len(created))
	}
	if presented := notices.presentations(); len(presented) != 1 {
		t.Fatalf("presented notices = %d, want one", len(presented))
	}

	activity := readHandoverActivity(t, ctx, pool, fixture.CompanyA)
	if len(activity) != 1 {
		t.Fatalf("runtime.handed_over activity = %+v, want one", activity)
	}
	row := activity[0]
	if row.actorType != string(events.ActorTypeUser) || row.actorID != mia.String() ||
		row.entityType != "runtime" || row.entityID != notice.entityID ||
		row.visibility != "user" || row.visibilityUserID != olivia {
		t.Fatalf("handover activity = %+v, want Mia's act, visible to Olivia alone", row)
	}
	var labels struct {
		ActorDisplayName *string `json:"actorDisplayName"`
		EntityTitle      *string `json:"entityTitle"`
	}
	if err := json.Unmarshal(row.details["context"], &labels); err != nil ||
		labels.ActorDisplayName == nil || *labels.ActorDisplayName != "Mia" ||
		labels.EntityTitle == nil || *labels.EntityTitle != "Olivia's Mac" {
		t.Fatalf("handover activity context = %s, %v", row.details["context"], err)
	}

	// Olivia's clients refetch her roster and see her Agent unavailable; Mia's
	// clients hear about her own registration.
	if roster := rosterEvents(
		agentLog,
		fixture.CompanyA,
		protocol.DaemonRosterActionDeregister,
	); len(
		roster,
	) != 1 ||
		roster[0].UserID != olivia {
		t.Fatalf("deregister roster = %+v, want one for Olivia", roster)
	}
	if roster := rosterEvents(agentLog, fixture.CompanyA, protocol.DaemonRosterActionRegister); len(roster) != 1 ||
		roster[0].UserID != mia {
		t.Fatalf("register roster = %+v, want one for Mia", roster)
	}
	unavailable := false
	for _, status := range agentStatusEvents(agentLog, fixture.CompanyA) {
		for _, entry := range status.Agents {
			if status.UserID == olivia && entry.AgentID == fixture.AgentA && !entry.RuntimeAvailable {
				unavailable = true
			}
		}
	}
	if !unavailable {
		t.Fatalf(
			"agent.status = %+v, want AgentA unavailable for Olivia",
			agentStatusEvents(agentLog, fixture.CompanyA),
		)
	}

	// Mia restarting her daemon is not another handover.
	registerOnMachine(t, ctx, service, fixture.CompanyA, mia, fixture.DaemonA)
	if stored := readRuntimeNotices(t, ctx, pool, fixture.CompanyA); len(stored) != 1 {
		t.Fatalf("runtime notices after Mia's restart = %d, want still one", len(stored))
	}
	if activity := readHandoverActivity(t, ctx, pool, fixture.CompanyA); len(activity) != 1 {
		t.Fatalf("handover activity after Mia's restart = %d, want still one", len(activity))
	}
}

// Signing back in is a return: the same runtimes come back online with every
// binding and queued Run where it was, the notice closes, and the computer
// is handed over to Olivia again — so Mia, who used it meanwhile, is the one
// told now. Someone who had handed the computer over earlier is not told a
// second time.
func TestSigningBackInOnTheComputerRestoresTheSetup(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, agentLog, _ := mustRuntimeNoticeService(t, store)
	olivia := fixture.UserA
	oliviaClaude := seedRuntime(t, ctx, pool, fixture, fixture.DaemonA, "claude", olivia)
	queued := seedRun(t, ctx, pool, fixture, fixture.AgentA, fixture.RuntimeA, olivia, "queued")
	mia := seedMember(t, ctx, pool, fixture, "mia@example.com", "member")
	ben := seedMember(t, ctx, pool, fixture, "ben@example.com", "member")
	dbtest.MustExec(t, ctx, pool, `UPDATE users SET display_name = 'Olivia' WHERE id = $1`, olivia)
	// Ben used the computer before Olivia did and was told then.
	bensCodex := seedRuntime(t, ctx, pool, fixture, fixture.DaemonA, "codex", ben)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE runtimes SET status = 'offline', handed_over_at = now() - interval '1 day' WHERE id = $1`,
		bensCodex,
	)

	setRuntimeOffline(t, ctx, pool, fixture.RuntimeA)
	setRuntimeOffline(t, ctx, pool, oliviaClaude)
	miasRuntimes := registerOnMachine(t, ctx, service, fixture.CompanyA, mia, fixture.DaemonA)
	for _, runtime := range miasRuntimes {
		setRuntimeOffline(t, ctx, pool, runtime.ID)
	}

	returned := registerOnMachine(t, ctx, service, fixture.CompanyA, olivia, fixture.DaemonA)
	if ids := runtimeIDsOf(returned); !ids[fixture.RuntimeA] || !ids[oliviaClaude] {
		t.Fatalf(
			"Olivia's return = %v, want her own runtimes %s and %s back",
			ids,
			fixture.RuntimeA,
			oliviaClaude,
		)
	}
	for _, id := range []uuid.UUID{fixture.RuntimeA, oliviaClaude} {
		if owner, status, handedOver := readHandover(t, ctx, pool, id); owner != olivia ||
			status != "online" || handedOver {
			t.Fatalf(
				"Olivia's runtime %s after her return = owner %s, %s, handed over %t",
				id,
				owner,
				status,
				handedOver,
			)
		}
	}
	if got := readBindingRuntime(t, ctx, pool, fixture.AgentA, olivia); got != fixture.RuntimeA {
		t.Fatalf("Olivia's binding after her return = %s, want %s", got, fixture.RuntimeA)
	}
	if runtimeID, status, _ := readRun(t, ctx, pool, queued); runtimeID != fixture.RuntimeA || status != "queued" {
		t.Fatalf(
			"Olivia's queued run after her return = %s / %s, want queued on her runtime",
			runtimeID,
			status,
		)
	}
	// Nothing of hers has to be set up again: the Run is claimable at once.
	claimable, err := store.Queries().ListQueuedClaimCandidatesByRuntimes(ctx, claimCandidatesFor(fixture.RuntimeA))
	if err != nil || len(claimable) != 1 || claimable[0].RuntimeID != fixture.RuntimeA ||
		claimable[0].AgentID != fixture.AgentA {
		t.Fatalf("claim candidates on Olivia's runtime = %+v, %v; want her queued run", claimable, err)
	}
	if bindings := agentLog.binding.snapshot(); len(bindings) != 0 {
		t.Fatalf("agent.binding_changed = %+v, want none across the whole round trip", bindings)
	}

	var olivias, mias, bens []storedNotice
	for _, notice := range readRuntimeNotices(t, ctx, pool, fixture.CompanyA) {
		switch notice.recipient {
		case olivia:
			olivias = append(olivias, notice)
		case mia:
			mias = append(mias, notice)
		case ben:
			bens = append(bens, notice)
		}
	}
	if len(olivias) != 1 || !olivias[0].archived || !olivias[0].read {
		t.Fatalf("Olivia's notice after her return = %+v, want resolved", olivias)
	}
	if len(mias) != 1 || mias[0].archived || mias[0].title != "Olivia is using Olivia's MacBook" {
		t.Fatalf("Mia's notice = %+v, want one open notice naming Olivia", mias)
	}
	if len(bens) != 0 {
		t.Fatalf("Ben's notices = %+v, want none: he was told when he handed over", bens)
	}
	if owner, _, handedOver := readHandover(t, ctx, pool, bensCodex); owner != ben || !handedOver {
		t.Fatalf("Ben's runtime = owner %s, handed over %t; want untouched", owner, handedOver)
	}

	activity := readHandoverActivity(t, ctx, pool, fixture.CompanyA)
	if len(activity) != 2 {
		t.Fatalf("handover activity = %+v, want Mia's takeover and Olivia's return", activity)
	}
	if activity[1].actorID != olivia.String() || activity[1].visibilityUserID != mia {
		t.Fatalf("return activity = %+v, want Olivia's act, visible to Mia alone", activity[1])
	}
	for _, runtime := range miasRuntimes {
		if owner, status, handedOver := readHandover(t, ctx, pool, runtime.ID); owner != mia ||
			status != "offline" || !handedOver {
			t.Fatalf("Mia's runtime %s after Olivia's return = owner %s, %s, handed over %t",
				runtime.ID, owner, status, handedOver)
		}
	}
}

// A handover is per Company and per person: rows in another Company on the
// same machine are not touched, and a previous user who is no longer an
// active member loses the computer without a notice or a private Activity
// row nobody could read.
func TestHandoverStaysInTheCompanyAndSkipsFormerMembers(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, _, _ := mustRuntimeNoticeService(t, store)
	former := seedMember(t, ctx, pool, fixture, "former@example.com", "member")
	formerCodex := seedRuntime(t, ctx, pool, fixture, "daemon-shared", "codex", former)
	dbtest.MustExec(t, ctx, pool,
		`UPDATE company_members SET status = 'revoked' WHERE company_id = $1 AND user_id = $2`,
		fixture.CompanyA, former)
	elsewhere := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, 'daemon-shared', 'codex (shared)', 'codex', 'online', $3, now())`,
		elsewhere, fixture.CompanyB, fixture.UserB)

	registerOnMachine(t, ctx, service, fixture.CompanyA, fixture.UserA, "daemon-shared")

	if _, status, handedOver := readHandover(t, ctx, pool, formerCodex); status != "offline" || !handedOver {
		t.Fatalf("former member's runtime = %s, handed over %t; want handed over", status, handedOver)
	}
	if notices := readRuntimeNotices(t, ctx, pool, fixture.CompanyA); len(notices) != 0 {
		t.Fatalf("notices = %+v, want none for a former member", notices)
	}
	if activity := readHandoverActivity(t, ctx, pool, fixture.CompanyA); len(activity) != 0 {
		t.Fatalf("handover activity = %+v, want none for a former member", activity)
	}
	if owner, status, handedOver := readHandover(t, ctx, pool, elsewhere); owner != fixture.UserB ||
		status != "online" || handedOver {
		t.Fatalf(
			"another Company's runtime = owner %s, %s, handed over %t; want untouched",
			owner,
			status,
			handedOver,
		)
	}
}

// Renaming "this computer" renames the namer's own runtimes on it; another
// person's rows on the same machine keep the name they gave it.
func TestRenamingAComputerLeavesOtherPeoplesRowsOnIt(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, _ := mustRuntimeLifecycleService(t, store)
	oliviaClaude := seedRuntime(t, ctx, pool, fixture, fixture.DaemonA, "claude", fixture.UserA)
	mia := seedMember(t, ctx, pool, fixture, "mia@example.com", "member")
	miasCodex := seedRuntime(t, ctx, pool, fixture, fixture.DaemonA, "codex", mia)

	name := "Office Mac"
	if _, err := service.Rename(ctx, ownerOf(fixture), fixture.RuntimeA, RuntimeRenameInput{
		CustomName: name, ApplyToMachine: true,
	}); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	names := customNames(t, ctx, pool, fixture.RuntimeA, oliviaClaude, miasCodex)
	if names[fixture.RuntimeA] == nil || *names[fixture.RuntimeA] != name ||
		names[oliviaClaude] == nil || *names[oliviaClaude] != name {
		t.Fatalf("Olivia's names = %v, want both %q", names, name)
	}
	if names[miasCodex] != nil {
		t.Fatalf("Mia's runtime name = %q, want untouched", *names[miasCodex])
	}
}

// Registration always names the person signing in: without one there is no
// identity to register under.
func TestRegisterRuntimesRefusesARegistrationWithoutAPerson(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, _ := mustRuntimeLifecycleService(t, store)
	_, err := service.RegisterRuntimes(ctx, RuntimeRegistration{
		CompanyID: fixture.CompanyA,
		DaemonID:  fixture.DaemonA,
		Runtimes:  []RuntimeRegistrationSpec{{Provider: "codex"}},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("RegisterRuntimes(no owner) error = %v, want ErrInvalidInput", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtimes WHERE owner_user_id IS NULL`).
		Scan(&count); err != nil ||
		count != 0 {
		t.Fatalf("ownerless runtimes = %d, %v; want none written", count, err)
	}
}

// claimCandidatesFor is the claim-candidate read for one runtime, with the
// permission sets the claim fence binds.
func claimCandidatesFor(runtimeID uuid.UUID) generated.ListQueuedClaimCandidatesByRuntimesParams {
	return generated.ListQueuedClaimCandidatesByRuntimesParams{
		RuntimeIds:                []uuid.UUID{runtimeID},
		ExecutePermission:         string(auth.ProjectPermissionTasksExecute),
		ReadPermission:            string(auth.ProjectPermissionRead),
		CompanyDefaultPermissions: auth.ProjectPermissionHeldSets().CompanyDefaultPermissions,
	}
}
