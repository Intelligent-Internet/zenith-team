//go:build integration

package wiki_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

// seedArtifact inserts an Output owned by owner with the given visibility and
// items (ordinals 0..items-1) and returns its id.
func seedArtifact(t *testing.T, h wikiHarness, owner uuid.UUID, visibility string, items int) uuid.UUID {
	t.Helper()
	ctx := t.Context()
	var id uuid.UUID
	if err := h.pool.QueryRow(ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id,visibility,presentation_number, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,'Report','Summary',gen_random_uuid()::text,$3,$4,
			(SELECT COALESCE(max(presentation_number),0)+1 FROM outputs WHERE run_id=$2), origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`,
		h.fixture.CompanyA, h.fixture.RunA, owner, visibility).
		Scan(&id); err != nil {
		t.Fatal(err)
	}
	for ordinal := range items {
		var attachmentID, assetID, versionID uuid.UUID
		if err := h.pool.QueryRow(ctx, `INSERT INTO attachments(company_id,uploaded_by_user_id,request_key,filename,content_type,size_bytes,status)
			VALUES($1,$2,gen_random_uuid(),'report.md','text/markdown',4,'ready') RETURNING id`,
			h.fixture.CompanyA, owner).
			Scan(&attachmentID); err != nil {
			t.Fatal(err)
		}
		if err := h.pool.QueryRow(ctx, `INSERT INTO assets(company_id,title,media_class,created_by_user_id)
			VALUES($1,'Report','file',$2) RETURNING id`, h.fixture.CompanyA, owner).Scan(&assetID); err != nil {
			t.Fatal(err)
		}
		if err := h.pool.QueryRow(ctx, `INSERT INTO asset_versions(company_id,asset_id,version_number,attachment_id,source_run_id,filename,content_type,size_bytes,integrity_status)
			VALUES($1,$2,1,$3,$4,'report.md','text/markdown',4,'verified') RETURNING id`,
			h.fixture.CompanyA, assetID, attachmentID, h.fixture.RunA).
			Scan(&versionID); err != nil {
			t.Fatal(err)
		}
		dbtest.MustExec(
			t,
			ctx,
			h.pool,
			`INSERT INTO output_items(company_id,output_id,ordinal,role,label,asset_version_id)
			VALUES($1,$2,$3,'final','Report',$4)`,
			h.fixture.CompanyA,
			id,
			ordinal,
			versionID,
		)
	}
	return id
}

type citedRow struct {
	outputID uuid.UUID
	ordinal  *int32
}

func (h wikiHarness) citations(t *testing.T, pageID uuid.UUID) map[string]bool {
	t.Helper()
	rows, err := h.pool.Query(t.Context(), `SELECT output_id, ordinal FROM wiki_artifact_refs
		WHERE company_id=$1 AND page_id=$2`, h.fixture.CompanyA, pageID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var row citedRow
		if err := rows.Scan(&row.outputID, &row.ordinal); err != nil {
			t.Fatal(err)
		}
		key := row.outputID.String()
		if row.ordinal != nil {
			key += fmt.Sprintf("#%d", *row.ordinal)
		}
		got[key] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

func cite(id uuid.UUID, ordinal ...int) string {
	if len(ordinal) == 0 {
		return "[[artifact:" + id.String() + "]]"
	}
	return fmt.Sprintf("[[artifact:%s#%d]]", id, ordinal[0])
}

func TestArtifactCitationsAreIndexedAndNeedAuthorAccess(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	private := seedArtifact(t, h, h.fixture.UserA, "private", 2)
	shared := seedArtifact(t, h, h.fixture.UserA, "company", 1)

	page, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{
		Title: "Churn",
		Body:  "See " + cite(private, 1) + " and " + cite(shared) + ", again " + cite(private, 1) + ".",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.citations(t, page.ID); len(got) != 2 || !got[private.String()+"#1"] || !got[shared.String()] {
		t.Fatalf("citations = %v", got)
	}

	for name, body := range map[string]string{
		"missing item":     cite(private, 5),
		"missing Artifact": cite(uuid.New()),
	} {
		if _, err := h.service.CreatePage(
			ctx,
			h.owner,
			wiki.CreatePageInput{Title: name, Body: body},
		); !errors.Is(
			err,
			wiki.ErrArtifactRefUnavailable,
		) {
			t.Fatalf("%s = %v", name, err)
		}
	}

	// An admin who cannot open the private Artifact may keep its citation but
	// not add a new one.
	dbtest.MustExec(t, ctx, h.pool, `INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'admin')`,
		h.fixture.CompanyA, h.fixture.UserB)
	admin := auth.UserPrincipal{UserID: h.fixture.UserB, CompanyID: h.fixture.CompanyA, Role: auth.CompanyRoleAdmin}
	// RunA works in restricted ProjectA: its Company-visible Artifact opens
	// only for the Project's readers, and an admin is one only once granted
	// (ADR 0017, management is not content).
	if _, err := h.service.UpdatePage(ctx, admin, page.ID, updatePageInput{
		ExpectedRevision: page.Revision, Title: page.Title,
		Body: "Kept " + cite(private, 1) + " plus " + cite(shared, 0),
	}); !errors.Is(err, wiki.ErrArtifactRefUnavailable) {
		t.Fatalf("citation from an unread Project = %v", err)
	}
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`INSERT INTO project_grants(company_id,project_id,user_id,permission,granted_by_user_id)
		VALUES($1,$2,$3,'project.read',$4)`,
		h.fixture.CompanyA,
		h.fixture.ProjectA,
		h.fixture.UserB,
		h.fixture.UserA,
	)
	page, err = h.service.UpdatePage(ctx, admin, page.ID, updatePageInput{
		ExpectedRevision: page.Revision, Title: page.Title,
		Body: "Kept " + cite(private, 1) + " plus " + cite(shared, 0),
	})
	if err != nil {
		t.Fatalf("carried citation = %v", err)
	}
	if got := h.citations(t, page.ID); len(got) != 2 || !got[private.String()+"#1"] || !got[shared.String()+"#0"] {
		t.Fatalf("citations after edit = %v", got)
	}
	if _, err := h.service.UpdatePage(ctx, admin, page.ID, updatePageInput{
		ExpectedRevision: page.Revision, Title: page.Title, Body: cite(private, 1) + cite(private, 0),
	}); !errors.Is(err, wiki.ErrArtifactRefUnavailable) {
		t.Fatalf("new private citation = %v", err)
	}
}

func TestRestoringARevisionKeepsItsArtifactCitations(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	private := seedArtifact(t, h, h.fixture.UserA, "private", 1)
	page, err := h.service.CreatePage(
		ctx,
		h.owner,
		wiki.CreatePageInput{Title: "Brief", Body: "Source " + cite(private, 0)},
	)
	if err != nil {
		t.Fatal(err)
	}
	first := *page.CurrentRevisionID
	page, err = h.service.UpdatePage(ctx, h.owner, page.ID, updatePageInput{
		ExpectedRevision: page.Revision, Title: page.Title, Body: "No sources",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.citations(t, page.ID); len(got) != 0 {
		t.Fatalf("citations after removal = %v", got)
	}
	dbtest.MustExec(t, ctx, h.pool, `INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'admin')`,
		h.fixture.CompanyA, h.fixture.UserB)
	admin := auth.UserPrincipal{UserID: h.fixture.UserB, CompanyID: h.fixture.CompanyA, Role: auth.CompanyRoleAdmin}
	page, err = h.service.RestoreRevision(ctx, admin, page.ID, first, page.Revision)
	if err != nil {
		t.Fatalf("restore by an admin without Artifact access = %v", err)
	}
	if got := h.citations(t, page.ID); !got[private.String()+"#0"] {
		t.Fatalf("citations after restore = %v", got)
	}
}

func TestDeletedArtifactLeavesTokenButDropsIndex(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	artifact := seedArtifact(t, h, h.fixture.UserA, "company", 1)
	body := "Deck " + cite(artifact, 0)
	page, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Deck", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, h.pool, `DELETE FROM outputs WHERE id=$1`, artifact)
	if got := h.citations(t, page.ID); len(got) != 0 {
		t.Fatalf("citations after delete = %v", got)
	}
	// The dangling token is carried, so unrelated edits still save.
	page, err = h.service.UpdatePage(ctx, h.owner, page.ID, updatePageInput{
		ExpectedRevision: page.Revision, Title: page.Title, Body: body + "\n\nMore notes.",
	})
	if err != nil {
		t.Fatalf("edit beside a deleted citation = %v", err)
	}
	if got, _ := h.service.GetPage(ctx, h.owner, page.ID); !strings.Contains(got.Body, cite(artifact, 0)) {
		t.Fatalf("body lost the citation token: %q", got.Body)
	}
}

func TestArtifactCitationLimit(t *testing.T) {
	h := startWiki(t)
	var body strings.Builder
	for range 51 {
		body.WriteString(cite(uuid.New()))
	}
	if _, err := h.service.CreatePage(
		t.Context(),
		h.owner,
		wiki.CreatePageInput{Title: "Too many", Body: body.String()},
	); !errors.Is(
		err,
		wiki.ErrArtifactRefLimit,
	) {
		t.Fatalf("51 citations = %v", err)
	}
}

func TestWikiDraftPublicationMaintainsArtifactCitations(t *testing.T) {
	h := startWiki(t)
	ctx := t.Context()
	original := seedArtifact(t, h, h.fixture.UserA, "private", 1)
	added := seedArtifact(t, h, h.fixture.UserA, "private", 1)
	page, err := h.service.CreatePage(ctx, h.owner, wiki.CreatePageInput{Title: "Research", Body: cite(original)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.service.UpdatePage(
		ctx,
		h.owner,
		page.ID,
		updatePageInput{ExpectedRevision: page.Revision, Title: "Research", Body: cite(added)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.citations(t, page.ID); len(got) != 1 || !got[added.String()] {
		t.Fatalf("published citations=%v", got)
	}
}
