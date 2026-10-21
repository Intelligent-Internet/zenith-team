//go:build integration

package agent

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

func TestToolReportsOnlyAnnounceChangedToolState(t *testing.T) {
	t.Parallel()
	pool, store := startAgentPostgres(t)
	ctx := t.Context()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, log := mustRuntimeLifecycleService(t, store)
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	observedAt := time.Now().Add(-time.Minute).UTC()
	studio := protocol.ToolObservation{
		AgentID: fixture.AgentA.String(), Key: "zenith-studio", Version: "0.1.11", State: "installed",
		AuthState: "needs_auth", ObservedAt: observedAt.Format(time.RFC3339Nano),
	}
	renderer := protocol.ToolObservation{
		Key: "hyperframes", Version: "0.8.36", State: "installed",
		AuthState: "not_required", ObservedAt: studio.ObservedAt,
	}
	refreshed := studio
	refreshed.ObservedAt = observedAt.Add(time.Second).Format(time.RFC3339Nano)
	authenticated := refreshed
	authenticated.AuthState = "authenticated"
	failed := authenticated
	failed.State, failed.ReasonCode = "error", "setup_failed"
	upgraded := failed
	upgraded.Version = "0.1.12-beta"

	for _, test := range []struct {
		name  string
		items []protocol.ToolObservation
		want  int
	}{
		{"first report", []protocol.ToolObservation{studio, renderer}, 1},
		{"identical report", []protocol.ToolObservation{studio, renderer}, 1},
		{"fresh observation", []protocol.ToolObservation{refreshed, renderer}, 1},
		{"reordered report", []protocol.ToolObservation{renderer, refreshed}, 1},
		{"auth changed", []protocol.ToolObservation{renderer, authenticated}, 2},
		{"setup failed", []protocol.ToolObservation{renderer, failed}, 3},
		{"version changed", []protocol.ToolObservation{renderer, upgraded}, 4},
		{"tool removed", []protocol.ToolObservation{renderer}, 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := service.ReportTools(ctx, principal, ToolObservationReport{
				RuntimeID: fixture.RuntimeA, DaemonID: fixture.DaemonA, Items: test.items,
			})
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := service.Get(ctx, principal, fixture.RuntimeA)
			if err != nil || !slices.Equal(runtime.Tools, test.items) {
				t.Fatalf("stored tools = %+v, %v, want %+v", runtime.Tools, err, test.items)
			}
			roster := rosterEventsForCompany(log, fixture.CompanyA)
			if len(roster) != test.want {
				t.Errorf("roster events = %d, want %d", len(roster), test.want)
			}
			for _, event := range roster {
				if event.Action != "tools_changed" || event.DaemonID != fixture.DaemonA ||
					!slices.Equal(event.RuntimeIDs, []string{fixture.RuntimeA.String()}) {
					t.Errorf("roster event = %+v, want tools_changed for reporting runtime", event)
				}
			}
		})
	}
}

func TestToolReportRequiresLiveMembershipAndOwningDaemon(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx := t.Context()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, err := NewRuntimeService(
		RuntimeServiceOptions{
			Store:            store,
			Bus:              mustAgentTestBus(t),
			ModelDiscoveries: NewInMemoryModelDiscoveryStore(ModelDiscoveryStoreOptions{}),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	report := ToolObservationReport{
		RuntimeID: fixture.RuntimeA,
		DaemonID:  fixture.DaemonA,
		Items: []protocol.ToolObservation{
			{
				AgentID:    fixture.AgentA.String(),
				Key:        "zenith-studio",
				Version:    "0.1.11",
				State:      "installed",
				AuthState:  "needs_auth",
				ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
			},
		},
	}
	if err := service.ReportTools(ctx, principal, report); err != nil {
		t.Fatal(err)
	}
	runtime, err := service.Get(ctx, principal, fixture.RuntimeA)
	if err != nil || len(runtime.Tools) != 1 || runtime.Tools[0] != report.Items[0] {
		t.Fatalf("runtime tools = %+v, %v", runtime.Tools, err)
	}
	for _, test := range []struct {
		name      string
		principal auth.UserPrincipal
		report    ToolObservationReport
	}{
		{"other daemon", principal, ToolObservationReport{RuntimeID: fixture.RuntimeA, DaemonID: fixture.DaemonB, Items: report.Items}},
		{"other company", auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyB}, report},
		{"other owner", auth.UserPrincipal{UserID: fixture.UserB, CompanyID: fixture.CompanyA}, report},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := service.ReportTools(
				ctx,
				test.principal,
				test.report,
			); !errors.Is(
				err,
				ErrRuntimeNotFound,
			) {
				t.Fatalf("report error = %v", err)
			}
		})
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		fixture.CompanyA,
		fixture.UserA,
	)
	if err := service.ReportTools(ctx, principal, report); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("revoked report error = %v", err)
	}
}

func TestToolReportKeepsAgentAccountsSeparateOnOneRuntime(t *testing.T) {
	t.Parallel()
	pool, store := startAgentPostgres(t)
	ctx := t.Context()
	f := dbtest.SeedFixture(t, ctx, pool)
	service, _ := mustRuntimeLifecycleService(t, store)
	principal := auth.UserPrincipal{UserID: f.UserA, CompanyID: f.CompanyA, Role: auth.CompanyRoleOwner}
	var peer uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO agents(company_id,name,title,model,provider,created_by_user_id,reports_to_agent_id) VALUES($1,'Studio peer','Artist','test','codex',$2,$3) RETURNING id`, f.CompanyA, f.UserA, f.AgentA).
		Scan(&peer); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO agent_runtime_bindings(company_id,agent_id,user_id,runtime_id,provider) VALUES($1,$2,$3,$4,'codex')`,
		f.CompanyA,
		peer,
		f.UserA,
		f.RuntimeA,
	)
	base := protocol.ToolObservation{
		Key:        "zenith-studio",
		Version:    "0.1.11",
		State:      "installed",
		AuthState:  "unknown",
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	a, b := base, base
	a.AgentID = f.AgentA.String()
	a.AuthState = "authenticated"
	b.AgentID = peer.String()
	b.AuthState = "needs_auth"
	renderer := protocol.ToolObservation{
		Key:        "hyperframes",
		Version:    "0.8.36",
		State:      "installed",
		AuthState:  "not_required",
		ObservedAt: base.ObservedAt,
	}
	report := ToolObservationReport{
		RuntimeID: f.RuntimeA,
		DaemonID:  f.DaemonA,
		Items:     []protocol.ToolObservation{base, renderer, a, b},
	}
	if err := service.ReportTools(ctx, principal, report); err != nil {
		t.Fatal(err)
	}
	runtime, err := service.Get(ctx, principal, f.RuntimeA)
	if err != nil || !slices.Equal(runtime.Tools, report.Items) {
		t.Fatalf("scoped tools=%+v, %v", runtime.Tools, err)
	}
	report.Items[2].AgentID = f.AgentB.String()
	if err := service.ReportTools(ctx, principal, report); !errors.Is(err, ErrRuntimeInvalidInput) {
		t.Fatalf("another Company's profile accepted: %v", err)
	}
	report.Items[2].AgentID = f.AgentA.String()
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`DELETE FROM agent_runtime_bindings WHERE company_id=$1 AND agent_id=$2`,
		f.CompanyA,
		f.AgentA,
	)
	if err := service.ReportTools(ctx, principal, report); !errors.Is(err, ErrRuntimeInvalidInput) {
		t.Fatalf("unbound profile accepted: %v", err)
	}
	report.Items = []protocol.ToolObservation{base, renderer, b}
	if err := service.ReportTools(ctx, principal, report); err != nil {
		t.Fatal(err)
	}
}
