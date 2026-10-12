//go:build integration

package company

import (
	"strings"
	"testing"

	"boardly.dev/boardly/server/internal/agent/voice"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
)

func TestVoiceCompanyContextIsBoundedAndProjectsAreScoped(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	q := generated.New(pool)
	dbtest.MustExec(t, ctx, pool, `UPDATE companies SET business_context=$2 WHERE id=$1`, f.CompanyA, strings.Repeat("x", 2100))
	got, err := ReadVoiceContext(ctx, q, f.CompanyA, voice.ContextRequest{Tool: "get_company_context"})
	if err != nil || len(got.Items) != 1 || got.Items[0].ID != f.CompanyA || len(got.Items[0].Description) != 2000 || !got.Items[0].Truncated {
		t.Fatalf("context = %+v, %v", got, err)
	}
	dbtest.MustExec(t, ctx, pool, `INSERT INTO projects(company_id,key,title) SELECT $1,'voice-'||n,'Project '||n FROM generate_series(1,24) n`, f.CompanyA)
	// The voice assistant reads with the access of the person who shared
	// Company context, its Authorizer, alone (D31): no chat audience narrows
	// it. Only UserA holds grants on the 24 above, and they are listed; the
	// hidden Project, which UserA cannot read, is not.
	dbtest.MustExec(t, ctx, pool, `INSERT INTO project_grants(company_id,project_id,user_id,permission,granted_by_user_id)
		SELECT company_id,id,$2,'project.read',$2 FROM projects WHERE company_id=$1 AND key LIKE 'voice-%'`, f.CompanyA, f.UserA)
	var hidden uuid.UUID
	if err = pool.QueryRow(ctx, `INSERT INTO projects(company_id,key,title) VALUES($1,'hidden','Hidden') RETURNING id`, f.CompanyA).Scan(&hidden); err != nil {
		t.Fatal(err)
	}
	// With no Authorizer, nothing is read.
	if got, err = ReadVoiceContext(ctx, q, f.CompanyA, voice.ContextRequest{Tool: "list_projects"}); err != nil || len(got.Items) != 0 {
		t.Fatalf("no Authorizer = %+v, %v", got, err)
	}
	got, err = ReadVoiceContext(ctx, q, f.CompanyA, voice.ContextRequest{
		Tool: "list_projects", AuthorizerUserID: f.UserA,
	})
	if err != nil || len(got.Items) != 20 || !got.HasMore || got.NextAfterID == nil {
		t.Fatalf("page = %+v, %v", got, err)
	}
	seen := map[string]bool{}
	for _, item := range got.Items {
		seen[item.ID.String()] = true
		if item.ID == f.ProjectB || item.ID == hidden {
			t.Fatalf("foreign or hidden project %s", item.ID)
		}
	}
	next, err := ReadVoiceContext(ctx, q, f.CompanyA, voice.ContextRequest{
		Tool: "list_projects", AfterID: *got.NextAfterID, AuthorizerUserID: f.UserA,
	})
	if err != nil || len(next.Items) != 5 || next.HasMore {
		t.Fatalf("next = %+v, %v", next, err)
	}
	for _, item := range next.Items {
		if seen[item.ID.String()] || item.ID == hidden {
			t.Fatal("repeated or hidden project")
		}
	}
}
