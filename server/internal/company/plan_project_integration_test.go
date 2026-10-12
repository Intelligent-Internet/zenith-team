//go:build integration

package company

import (
	"context"
	"errors"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/approval"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestProjectPlanAtomicCreationAndImmutableMembership(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewPlanService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `DELETE FROM tasks WHERE company_id=$1`, f.CompanyA)
	principal := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	artifact := PlanArtifact{ArtifactVersion: 1, CompanyID: f.CompanyA.String(), CompanySlug: "schema-a", CreatedAt: time.Now(), Base: PlanBase{Projects: []PlanBaseEntity{{ID: f.ProjectA.String(), Key: "work", Revision: 1}}, Tasks: []PlanBaseEntity{}, Dependencies: []PlanBaseDependency{}}, Desired: PlanDocument{FormatVersion: 5, Company: "schema-a", Projects: []PlanProject{{Key: "work", Title: "Work"}, {Key: "website", Title: "Website"}}, Tasks: []PlanTask{{Key: "page", ProjectKey: "website", Title: "Build page", Description: "Build it", MilestoneType: "product", Status: "todo", Priority: "high", DependsOn: []PlanTaskDependency{{TaskKey: "missing"}}}}}}
	if _, err := service.ApplyCompanyPlan(ctx, principal, artifact); !errors.Is(err, ErrPlanInvalidInput) {
		t.Fatalf("invalid dependency error=%v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM projects WHERE company_id=$1 AND key='website'`, f.CompanyA).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial Project write: %d, %v", count, err)
	}
	artifact.Desired.Tasks[0].DependsOn = []PlanTaskDependency{}
	if result, err := service.ApplyCompanyPlan(ctx, principal, artifact); err != nil || result.Created != 2 {
		t.Fatalf("atomic apply=%+v,%v", result, err)
	}
	locked, err := lockPlanForApply(ctx, generated.New(pool), planScopeRule{
		actor: approval.CompanyPlanActor{Principal: principal}, writeNeeded: true,
	}, artifact)
	if err != nil {
		t.Fatal(err)
	}
	artifact.Base = locked.snapshot()
	artifact.Desired.Tasks[0].ProjectKey = "work"
	if _, err := service.ApplyCompanyPlan(ctx, principal, artifact); !errors.Is(err, ErrPlanInvalidInput) {
		t.Fatalf("membership move error=%v", err)
	}
	artifact.Desired.Tasks[0].ProjectKey = "website"
	artifact.Desired.Projects[1].Archived = true
	if _, err := service.ApplyCompanyPlan(ctx, principal, artifact); !errors.Is(err, ErrPlanConflict) {
		t.Fatalf("archive open Project error=%v", err)
	}
	artifact.Desired.Tasks[0].Status = "cancelled"
	if _, err := service.ApplyCompanyPlan(ctx, principal, artifact); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyCompanyPlan(ctx, principal, artifact); !errors.Is(err, ErrPlanConflict) {
		t.Fatalf("stale Project baseline error=%v", err)
	}
}
