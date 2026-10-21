//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestBoundedCollectionQueriesApplyOptionalFilters(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)

	offlineRuntimeID := dbtest.NewUUID(t)
	firstChildID := dbtest.NewUUID(t)
	secondChildID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes (
			id, company_id, daemon_id, name, provider,
			status, owner_user_id, last_seen_at
		) VALUES ($1, $2, 'daemon-a-offline', 'Offline Claude', 'claude',
			'offline', $3, now())`, offlineRuntimeID, fixture.CompanyA, fixture.UserA)
	// Templates (ADR 0014): the agents carry a provider and no machine; the
	// viewer's binding, when any, is joined per row.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO agents (
			id, company_id, name, title, reports_to_agent_id, provider
		) VALUES
			($1, $2, 'First child', 'Agent', $3, 'codex'),
			($4, $2, 'Second child', 'Agent', $3, 'codex')`,
		firstChildID, fixture.CompanyA, fixture.AgentA, secondChildID)

	queries := generated.New(pool)

	directReports, err := queries.ListAgentProjectionsForActor(ctx, generated.ListAgentProjectionsForActorParams{
		ActorUserID: fixture.UserA, CompanyID: fixture.CompanyA,
		ReportsToAgentID: uuid.NullUUID{UUID: fixture.AgentA, Valid: true},
	})
	if err != nil {
		t.Fatalf("ListAgentProjectionsForActor(reports_to_agent_id) error = %v", err)
	}
	if len(directReports) != 2 {
		t.Fatalf("ListAgentProjectionsForActor(reports_to_agent_id) = %+v", directReports)
	}
	directReportIDs := map[uuid.UUID]bool{}
	for _, report := range directReports {
		directReportIDs[report.Agent.ID] = true
	}
	if !directReportIDs[firstChildID] || !directReportIDs[secondChildID] {
		t.Fatalf("ListAgentProjectionsForActor(reports_to_agent_id) = %+v", directReports)
	}

	firstResourceID := dbtest.NewUUID(t)
	secondResourceID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_resources (
			id, company_id, resource_type, daemon_id, local_path, label,
			position, metadata, created_by_user_id
		) VALUES
			($1, $2, 'local_directory', $3, '/workspace/first', 'First', 1, '{}', $4),
			($5, $2, 'local_directory', 'daemon-a-offline', '/workspace/second', 'Second', 2, '{}', $4)`,
		firstResourceID, fixture.CompanyA, fixture.DaemonA, fixture.UserA, secondResourceID)
	resources, err := queries.ListCompanyResourcesForOwner(
		ctx,
		generated.ListCompanyResourcesForOwnerParams{
			ActorRoles:  []string{"owner"},
			ActorUserID: fixture.UserA,
			CompanyID:   fixture.CompanyA,
			DaemonID:    pgtype.Text{String: fixture.DaemonA, Valid: true},
		},
	)
	if err != nil {
		t.Fatalf("ListCompanyResourcesForOwner(daemon_id) error = %v", err)
	}
	if len(resources) != 1 || resources[0].CompanyResource.ID != firstResourceID {
		t.Fatalf("ListCompanyResourcesForOwner(daemon_id) = %+v", resources)
	}

	runtimes, err := queries.ListRuntimesForCompany(ctx, generated.ListRuntimesForCompanyParams{
		CompanyID:    fixture.CompanyA,
		ViewerUserID: fixture.UserA,
		Status:       pgtype.Text{String: "offline", Valid: true},
		Provider:     pgtype.Text{String: "claude", Valid: true},
	})
	if err != nil {
		t.Fatalf("ListRuntimesForCompany(status, provider) error = %v", err)
	}
	if len(runtimes) != 1 || runtimes[0].Runtime.ID != offlineRuntimeID {
		t.Fatalf("ListRuntimesForCompany(status, provider) = %+v", runtimes)
	}
}
