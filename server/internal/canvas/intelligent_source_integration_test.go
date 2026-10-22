//go:build integration

package canvas

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

func TestSourcePageProposalAndReplay(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	s := NewPageService(store, bus)
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE runs SET status='running',execution_authorized_by_user_id=$2,task_execution_generation=(SELECT execution_generation FROM tasks WHERE id=$3) WHERE id=$1`,
		f.RunA,
		f.UserA,
		f.TaskA,
	)
	source := "# Launch review\n\nObserved finding.\n\n<TaskCollection filter=\"all\" display=\"table\" />"
	input := PageCreateInput{RequestKey: uuid.New(), Title: "Source review", Source: &source, LanguageVersion: 1}
	page, err := s.CreatePage(ctx, p, f.ProjectA, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreatePage(ctx, p, f.ProjectA, input)
	if err != nil || replay.PageID != page.PageID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	read, err := s.ReadPageForRun(ctx, p, f.RunA, page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	root, err := wiki.CompileSource(read.Source, read.LanguageVersion)
	if err != nil {
		t.Fatal(err)
	}
	var canonical wiki.BlockNode
	if err = json.Unmarshal(read.Content, &canonical); err != nil {
		t.Fatal(err)
	}
	if wiki.BlockHash(root) != wiki.BlockHash(canonical) {
		t.Fatal("source read lost native semantics")
	}
	target := root.Content[1]
	target.Content = []wiki.BlockNode{{Type: "text", Text: "Revised finding."}}
	replacement := wiki.BlocksSource(target)
	id := target.Attrs["blockId"].(string)
	edit := PageEditInput{
		RequestKey: uuid.New(),
		Sequence:   read.Sequence,
		Summary:    "Revise finding",
		Operations: []wiki.BlockOperation{
			{
				Type:            "update",
				BlockID:         id,
				ExpectedHash:    read.BlockHashes[id],
				Source:          &replacement,
				LanguageVersion: 1,
			},
		},
	}
	proposal, err := s.EditPageForRun(ctx, p, f.RunA, page.PageID, edit)
	if err != nil || proposal.Status != "proposed" {
		t.Fatalf("proposal = %+v, %v", proposal, err)
	}
	again, err := s.EditPageForRun(ctx, p, f.RunA, page.PageID, edit)
	if err != nil || again.ID != proposal.ID {
		t.Fatalf("proposal replay = %+v, %v", again, err)
	}
	if edit.Operations[0].Source == nil || edit.Operations[0].Content != nil {
		t.Fatal("compilation mutated caller's replay input")
	}
	after, err := s.ReadPageForRun(ctx, p, f.RunA, page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Source != read.Source {
		t.Fatal("unaccepted source changed canonical content")
	}
	broken := "<Unknown />"
	edit.RequestKey = uuid.New()
	edit.Operations[0].Source = &broken
	_, err = s.EditPageForRun(ctx, p, f.RunA, page.PageID, edit)
	var diagnostic *wiki.SourceDiagnostic
	if !errors.As(err, &diagnostic) || diagnostic.OperationIndex == nil || *diagnostic.OperationIndex != 0 {
		t.Fatalf("got = %v, want indexed source diagnostic", err)
	}
	after, err = s.ReadPageForRun(ctx, p, f.RunA, page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Changes) != len(read.Changes)+1 {
		t.Fatal("invalid source created a proposal")
	}
}
