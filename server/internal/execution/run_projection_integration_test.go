//go:build integration

package execution

import (
	"context"
	"errors"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestRunUsageProjectionRespectsCurrentMembership(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET execution_authorized_by_user_id=$2 WHERE id=$1`, f.RunA, f.UserA)
	service := NewRunService(RunServiceOptions{Queries: generated.New(pool)})
	owner := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	view, err := service.Get(ctx, owner, f.RunA)
	if err != nil || view.Usage != nil {
		t.Fatalf("unreported usage = %+v, error = %v", view.Usage, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO run_usage(run_id,provider,model,input_tokens,output_tokens,raw_json) VALUES($1,'codex','test-model',100,20,'{}')`,
		f.RunA,
	)
	member := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: auth.CompanyRoleMember}
	// RunA works in ProjectA, which is restricted (ADR 0017): a member who
	// cannot read the Project does not see its Run at all. Once they read
	// it, the usage rules below decide what they see of its cost.
	if _, err = service.Get(ctx, member, f.RunA); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("Run outside the member's Projects = %v, want %v", err, ErrRunNotFound)
	}
	dbtest.GrantProject(t, ctx, pool, f.CompanyA, f.ProjectA, f.UserB, "project.read")
	view, err = service.Get(ctx, member, f.RunA)
	if err != nil || view.Usage != nil || !view.UsageRestricted {
		t.Fatalf("member usage = %+v, restricted = %v, error = %v", view.Usage, view.UsageRestricted, err)
	}
	view, err = service.Get(ctx, owner, f.RunA)
	if err != nil || view.Usage == nil || view.Usage.InputTokens != "100" {
		t.Fatalf("own usage = %+v, error = %v", view.Usage, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET role='admin' WHERE company_id=$1 AND user_id=$2`,
		f.CompanyA,
		f.UserB,
	)
	view, err = service.Get(ctx, member, f.RunA)
	if err != nil || view.Usage == nil || view.UsageRestricted {
		t.Fatalf("admin usage = %+v, restricted = %v, error = %v", view.Usage, view.UsageRestricted, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET role='member' WHERE company_id=$1 AND user_id=$2`,
		f.CompanyA,
		f.UserB,
	)
	member.Role = auth.CompanyRoleAdmin
	view, err = service.Get(ctx, member, f.RunA)
	if err != nil || view.Usage != nil || !view.UsageRestricted {
		t.Fatalf(
			"revoked permission usage = %+v, restricted = %v, error = %v",
			view.Usage,
			view.UsageRestricted,
			err,
		)
	}
}
