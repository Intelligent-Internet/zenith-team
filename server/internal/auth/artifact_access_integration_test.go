//go:build integration

package auth

import (
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestArtifactAccessKeepsOwnershipDomainsSeparate(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	q := generated.New(pool)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
	owner := UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: CompanyRoleOwner}
	member := UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: CompanyRoleMember}
	create := func(kind string, project, person uuid.NullUUID) uuid.UUID {
		t.Helper()
		id, err := q.CreateWikiPage(ctx, generated.CreateWikiPageParams{
			CompanyID: f.CompanyA, Type: kind, Title: "Access boundary", CreatedByUserID: f.UserA,
			ProjectID: project, OwnerUserID: person,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	check := func(p UserPrincipal, id uuid.UUID, want ArtifactAccess) {
		t.Helper()
		got, err := ResolveArtifactAccess(ctx, q, p, id)
		if err != nil || got != want {
			t.Fatalf("access to %s = %+v, %v; want %+v", id, got, err, want)
		}
	}
	grant := func(id uuid.UUID) {
		t.Helper()
		if _, err := q.UpsertArtifactGrant(ctx, generated.UpsertArtifactGrantParams{
			CompanyID:   f.CompanyA,
			ArtifactID:  id,
			UserID:      f.UserB,
			ActorUserID: f.UserA,
			Permission:  "manage",
		}); err != nil {
			t.Fatal(err)
		}
	}
	standalone := create("page", uuid.NullUUID{}, uuid.NullUUID{UUID: f.UserA, Valid: true})
	check(owner, standalone, ArtifactAccess{Read: true, Edit: true, Manage: true})
	check(member, standalone, ArtifactAccess{})
	grant(standalone)
	check(member, standalone, ArtifactAccess{Read: true, Edit: true, Manage: true})
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET reactivated_at=clock_timestamp() WHERE company_id=$1 AND user_id=$2`,
		f.CompanyA,
		f.UserB,
	)
	check(member, standalone, ArtifactAccess{})
	grant(standalone)
	check(member, standalone, ArtifactAccess{Read: true, Edit: true, Manage: true})

	project := create("page", uuid.NullUUID{UUID: f.ProjectA, Valid: true}, uuid.NullUUID{})
	grant(project)
	check(member, project, ArtifactAccess{})
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO project_grants(company_id,project_id,user_id,permission,granted_by_user_id) VALUES($1,$2,$3,'project.read',$4)`,
		f.CompanyA,
		f.ProjectA,
		f.UserB,
		f.UserA,
	)
	check(member, project, ArtifactAccess{Read: true})
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO project_grants(company_id,project_id,user_id,permission,granted_by_user_id) VALUES($1,$2,$3,'project.tasks.write',$4)`,
		f.CompanyA,
		f.ProjectA,
		f.UserB,
		f.UserA,
	)
	check(member, project, ArtifactAccess{Read: true, Edit: true})
	dbtest.MustExec(t, ctx, pool, `UPDATE projects SET archived_at=clock_timestamp() WHERE id=$1`, f.ProjectA)
	check(member, project, ArtifactAccess{Read: true})

	wiki := create("wiki", uuid.NullUUID{}, uuid.NullUUID{})
	grant(wiki)
	check(member, wiki, ArtifactAccess{Read: true})
	check(owner, wiki, ArtifactAccess{Read: true, Edit: true, Manage: true})
	check(
		UserPrincipal{CompanyID: f.CompanyB, UserID: f.UserB, Role: CompanyRoleOwner},
		standalone,
		ArtifactAccess{},
	)
}
