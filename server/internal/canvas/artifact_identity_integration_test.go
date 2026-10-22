//go:build integration

package canvas

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestProjectPageOwnsOneArtifactIdentity(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	service := NewPageService(store, bus)
	principal := auth.UserPrincipal{CompanyID: fixture.CompanyA, UserID: fixture.UserA, Role: auth.CompanyRoleOwner}
	page, err := service.CreatePage(
		ctx,
		principal,
		fixture.ProjectA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Working page", Markdown: "Shared content"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if page.PageID != page.ArtifactID {
		t.Fatalf("Page %s has wrapper Artifact %s", page.PageID, page.ArtifactID)
	}
	var snapshotID uuid.UUID
	if err = pool.QueryRow(ctx, `SELECT wiki_revision_id FROM artifact_snapshots WHERE id=$1`, page.VersionID).
		Scan(&snapshotID); err != nil {
		t.Fatal(err)
	}
	if snapshotID != page.VersionID {
		t.Fatalf("Page snapshot %s has wrapper version %s", snapshotID, page.VersionID)
	}
	snapshot, err := store.Queries().
		GetArtifactSnapshot(ctx, generated.GetArtifactSnapshotParams{CompanyID: fixture.CompanyA, ID: page.VersionID})
	if err != nil || snapshot.SourceSequence != page.Sequence || snapshot.Title != page.Title ||
		snapshot.Theme != "default" ||
		snapshot.SchemaVersion != 1 {
		t.Fatalf("snapshot metadata does not describe the saved Page: %+v, %v", snapshot, err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM company_wiki_pages WHERE id=$1`, page.PageID).
		Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("Project Page leaked into Company Wiki")
	}
}

func TestCompanyWikiIdentitySurvivesProjectDeletion(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	q := generated.New(pool)
	wikiID, err := q.CreateWikiPage(
		ctx,
		generated.CreateWikiPageParams{
			CompanyID:       fixture.CompanyA,
			Type:            "wiki",
			Title:           "Company knowledge",
			CreatedByUserID: fixture.UserA,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	pageID, err := q.CreateWikiPage(
		ctx,
		generated.CreateWikiPageParams{
			CompanyID:       fixture.CompanyA,
			Type:            "page",
			Title:           "Personal work",
			CreatedByUserID: fixture.UserA,
			OwnerUserID:     uuid.NullUUID{UUID: fixture.UserA, Valid: true},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.GetWikiPageForUpdate(
		ctx,
		generated.GetWikiPageForUpdateParams{CompanyID: fixture.CompanyA, PageID: pageID},
	); !errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		t.Fatalf("Wiki mutation accepted standalone Page: %v", err)
	}

	snapshot, err := q.CreateWikiRevision(
		ctx,
		generated.CreateWikiRevisionParams{
			CompanyID:    fixture.CompanyA,
			PageID:       wikiID,
			Number:       1,
			Title:        "Company knowledge",
			Body:         "Retained guidance",
			AuthorUserID: fixture.UserA,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO review_threads(company_id,project_id,artifact_version_id,created_by_user_id) VALUES($1,$2,$3,$4)`,
		fixture.CompanyA,
		fixture.ProjectA,
		snapshot.ID,
		fixture.UserA,
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO artifact_review_decisions(company_id,project_id,artifact_id,version_id,action,actor_user_id) VALUES($1,$2,$3,$4,'reject',$5)`,
		fixture.CompanyA,
		fixture.ProjectA,
		wikiID,
		snapshot.ID,
		fixture.UserA,
	)
	dbtest.MustExec(t, ctx, pool, `DELETE FROM projects WHERE id=$1`, fixture.ProjectA)
	var orphaned int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM review_threads WHERE project_id=$1)+(SELECT count(*) FROM artifact_review_decisions WHERE project_id=$1)`, fixture.ProjectA).
		Scan(&orphaned); err != nil ||
		orphaned != 0 {
		t.Fatalf("Project deletion left reviews behind: %d, %v", orphaned, err)
	}
	if _, err = q.GetArtifactSnapshot(
		ctx,
		generated.GetArtifactSnapshotParams{CompanyID: fixture.CompanyA, ID: snapshot.ID},
	); err != nil {
		t.Fatalf("Project deletion removed Company knowledge snapshot: %v", err)
	}
	root, err := q.GetArtifactIdentity(
		ctx,
		generated.GetArtifactIdentityParams{CompanyID: fixture.CompanyA, ID: wikiID},
	)
	if err != nil || root.Type != "wiki" || root.ProjectID.Valid || root.OwnerUserID.Valid {
		t.Fatalf("Wiki ownership changed with Project deletion: %+v, %v", root, err)
	}
	if _, err = q.GetWikiPageForUpdate(
		ctx,
		generated.GetWikiPageForUpdateParams{CompanyID: fixture.CompanyA, PageID: wikiID},
	); err != nil {
		t.Fatal(err)
	}
}
