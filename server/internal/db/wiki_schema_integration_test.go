//go:build integration

package db

import (
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestWikiSchemaKeepsCollectionsPagesRevisionsAndLinksInsideCompany(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)

	var collectionA, collectionB, pageA, pageB, revisionA uuid.UUID
	for _, item := range []struct {
		companyID uuid.UUID
		out       *uuid.UUID
	}{
		{fixture.CompanyA, &collectionA},
		{fixture.CompanyB, &collectionB},
	} {
		if err := pool.QueryRow(ctx, `INSERT INTO wiki_collections(company_id,name) VALUES($1,'Handbook') RETURNING id`, item.companyID).
			Scan(item.out); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO wiki_pages(company_id,collection_id,slug,title,created_by_user_id) VALUES($1,$2,'wrong','Wrong',$3)`,
		fixture.CompanyA,
		collectionB,
		fixture.UserA,
	); err == nil {
		t.Fatal("cross-company collection accepted")
	}
	for _, item := range []struct {
		companyID    uuid.UUID
		collectionID uuid.UUID
		userID       uuid.UUID
		out          *uuid.UUID
	}{
		{fixture.CompanyA, collectionA, fixture.UserA, &pageA},
		{fixture.CompanyB, collectionB, fixture.UserB, &pageB},
	} {
		if err := pool.QueryRow(ctx, `INSERT INTO wiki_pages(company_id,collection_id,slug,title,created_by_user_id) VALUES($1,$2,'welcome','Welcome',$3) RETURNING id`, item.companyID, item.collectionID, item.userID).
			Scan(item.out); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `INSERT INTO wiki_revisions(company_id,page_id,number,title,body,author_user_id) VALUES($1,$2,1,'Welcome','Hello',$3) RETURNING id`, fixture.CompanyA, pageA, fixture.UserA).
		Scan(&revisionA); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO wiki_revisions(company_id,page_id,number,title,body,author_user_id) VALUES($1,$2,1,'Wrong','Wrong',$3)`,
		fixture.CompanyA,
		pageB,
		fixture.UserA,
	); err == nil {
		t.Fatal("cross-company revision accepted")
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO wiki_links(company_id,from_page_id,to_page_id) VALUES($1,$2,$3)`,
		fixture.CompanyA,
		pageA,
		pageB,
	); err == nil {
		t.Fatal("cross-company link accepted")
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE wiki_pages SET current_revision_id=$1 WHERE id=$2`,
		revisionA,
		pageB,
	); err == nil {
		t.Fatal("cross-page current revision accepted")
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO wiki_favorites(company_id,page_id,user_id) VALUES($1,$2,$3)`,
		fixture.CompanyA,
		pageB,
		fixture.UserA,
	); err == nil {
		t.Fatal("cross-company favorite accepted")
	}
}

func TestRunWikiReadsKeepTheirShapeAndGoWithTheirPageAndRun(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)

	var pageA, pageB, revisionA, directRun uuid.UUID
	for _, item := range []struct {
		companyID, userID uuid.UUID
		out               *uuid.UUID
	}{
		{f.CompanyA, f.UserA, &pageA},
		{f.CompanyB, f.UserB, &pageB},
	} {
		if err := pool.QueryRow(ctx, `INSERT INTO wiki_pages(company_id,slug,title,created_by_user_id) VALUES($1,'handbook','Handbook',$2) RETURNING id`, item.companyID, item.userID).
			Scan(item.out); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `INSERT INTO wiki_revisions(company_id,page_id,number,title,body,author_user_id) VALUES($1,$2,1,'Handbook','Text',$3) RETURNING id`, f.CompanyA, pageA, f.UserA).
		Scan(&revisionA); err != nil {
		t.Fatal(err)
	}
	const read = `INSERT INTO run_wiki_reads(company_id,run_id,agent_id,kind,page_id,revision_id,revision_number) VALUES($1,$2,$3,'read',$4,$5,1)`
	for _, tc := range []struct {
		name, code, sql string
		args            []any
	}{
		{"read without a revision", "23514", `INSERT INTO run_wiki_reads(company_id,run_id,agent_id,kind,page_id) VALUES($1,$2,$3,'read',$4)`, []any{f.CompanyA, f.RunA, f.AgentA, pageA}},
		{"search naming a page", "23514", `INSERT INTO run_wiki_reads(company_id,run_id,agent_id,kind,result_count,page_id) VALUES($1,$2,$3,'search',0,$4)`, []any{f.CompanyA, f.RunA, f.AgentA, pageA}},
		{"search without a count", "23514", `INSERT INTO run_wiki_reads(company_id,run_id,agent_id,kind) VALUES($1,$2,$3,'search')`, []any{f.CompanyA, f.RunA, f.AgentA}},
		{"list naming a page", "23514", `INSERT INTO run_wiki_reads(company_id,run_id,agent_id,kind,result_count,page_id) VALUES($1,$2,$3,'list',0,$4)`, []any{f.CompanyA, f.RunA, f.AgentA, pageA}},
		{"list without a count", "23514", `INSERT INTO run_wiki_reads(company_id,run_id,agent_id,kind) VALUES($1,$2,$3,'list')`, []any{f.CompanyA, f.RunA, f.AgentA}},
		{"unknown kind", "23514", `INSERT INTO run_wiki_reads(company_id,run_id,agent_id,kind,result_count) VALUES($1,$2,$3,'edit',0)`, []any{f.CompanyA, f.RunA, f.AgentA}},
		{"page of another Company", "23503", read, []any{f.CompanyA, f.RunA, f.AgentA, pageB, revisionA}},
	} {
		t.Run(
			tc.name,
			func(t *testing.T) { dbtest.AssertStatementRejected(t, ctx, pool, tc.code, tc.sql, tc.args...) },
		)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO run_wiki_reads(company_id,run_id,agent_id,kind,query,result_count) VALUES($1,$2,$3,'list','Handbook',3)`,
		f.CompanyA,
		f.RunA,
		f.AgentA,
	)

	// Purging a page or a Run purges the trail that points at it.
	evidence := func(where string, id uuid.UUID) int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM run_wiki_reads WHERE `+where+` = $1`, id).
			Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	dbtest.MustExec(t, ctx, pool, read, f.CompanyA, f.RunA, f.AgentA, pageA, revisionA)
	dbtest.MustExec(t, ctx, pool, `DELETE FROM wiki_pages WHERE id=$1`, pageA)
	if got := evidence("page_id", pageA); got != 0 {
		t.Fatalf("%d reads outlived their page", got)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO runs(company_id,agent_id,conversation_id,runtime_id,status,run_type,revision) VALUES($1,$2,$3,$4,'running','direct_chat',1) RETURNING id`, f.CompanyA, f.AgentA, f.DirectConversationA, f.RuntimeA).
		Scan(&directRun); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO run_wiki_reads(company_id,run_id,agent_id,kind,result_count) VALUES($1,$2,$3,'search',0)`,
		f.CompanyA,
		directRun,
		f.AgentA,
	)
	dbtest.MustExec(t, ctx, pool, `DELETE FROM runs WHERE id=$1`, directRun)
	if got := evidence("run_id", directRun); got != 0 {
		t.Fatalf("%d reads outlived their Run", got)
	}
}
