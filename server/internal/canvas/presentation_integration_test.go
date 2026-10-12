//go:build integration

package canvas

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"boardly.dev/boardly/server/internal/wiki"
)

func TestPresentationAuthorizesCapturedLiveRevision(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
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
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	s := newCanvasServices(store, bus, nil)
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	var output uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id,visibility, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,'Restricted evidence','Secret','capture-history',$3,'private', origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`, f.CompanyA, f.RunA, f.UserB).
		Scan(&output); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO output_grants(company_id,output_id,user_id,permission,granted_by_user_id) VALUES($1,$2,$3,'view',$4)`,
		f.CompanyA,
		output,
		f.UserA,
		f.UserB,
	)
	evidence, err := s.Mutate(
		ctx,
		p,
		f.ProjectA,
		Command{RequestKey: uuid.New(), Type: "add_artifact", OutputID: &output, Title: "Restricted evidence"},
	)
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.CreatePage(
		ctx,
		p,
		f.ProjectA,
		PageCreateInput{
			RequestKey: uuid.New(),
			Title:      "Research",
			Content: wiki.BlockNode{
				Type: "doc",
				Content: []wiki.BlockNode{
					{
						Type: "artifactReference",
						Attrs: map[string]any{
							"versionId": evidence.ID.String(),
							"format":    "pinned",
						},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	// Model the acknowledged clean draft while retaining the original saved version.
	clean := wiki.BlocksFromMarkdown("Clean current research")
	wiki.AssignBlockIDs(&clean, uuid.NewString)
	state, err := wiki.EncodeBlocks(clean)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE wiki_canvas_drafts SET content=$2,state=$3,markdown=$4,head_seq=head_seq+1 WHERE page_id=$1`,
		source.PageID,
		raw,
		state,
		wiki.BlocksMarkdown(clean),
	)
	dbtest.MustExec(t, ctx, pool, `DELETE FROM output_grants WHERE output_id=$1`, output)
	if _, err = s.VersionBlocks(ctx, p, f.ProjectA, source.VersionID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("original restricted revision = %v, want ErrNotFound", err)
	}
	page, err := s.CreatePage(
		ctx,
		p,
		f.ProjectA,
		PageCreateInput{
			RequestKey: uuid.New(),
			Title:      "Report",
			Content: wiki.BlockNode{
				Type: "doc",
				Content: []wiki.BlockNode{
					{
						Type: "artifactReference",
						Attrs: map[string]any{
							"versionId": source.VersionID.String(),
							"format":    "live",
							"display":   "expanded",
						},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewPresentation(ctx, p, f.ProjectA, page.PageID, page.Sequence)
	if err != nil || len(preview.Issues) != 0 {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	saved, err := s.CapturePresentation(
		ctx,
		p,
		f.ProjectA,
		PresentationInput{
			PageID:      page.PageID,
			RequestKey:  uuid.New(),
			Sequence:    preview.Sequence,
			Fingerprint: preview.Fingerprint,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.Presentation(ctx, p, f.ProjectA, saved.ID)
	if err != nil || !read.CanExport || len(read.Issues) != 0 {
		t.Fatalf("captured clean revision = %+v, %v", read, err)
	}
	export, err := s.PresentationExport(ctx, p, f.ProjectA, saved.ID)
	if err != nil || !strings.Contains(export, "Clean current research") {
		t.Fatalf("export = %q, %v", export, err)
	}
}
