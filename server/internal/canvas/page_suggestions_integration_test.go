//go:build integration

package canvas

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestPageSuggestionsCompetingReviewAndReplay(t *testing.T) {
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
	page, err := s.CreatePage(
		ctx,
		p,
		f.ProjectA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Pricing", Markdown: "Price: $20\n\nIndependent note"},
	)
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.ReadPageForRun(ctx, p, f.RunA, page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	var root wiki.BlockNode
	if err = json.Unmarshal(read.Content, &root); err != nil {
		t.Fatal(err)
	}
	propose := func(text string) PageEditResult {
		t.Helper()
		target := root.Content[0]
		target.Content = []wiki.BlockNode{{Type: "text", Text: text}}
		id := target.Attrs["blockId"].(string)
		result, e := s.EditPageForRun(
			ctx,
			p,
			f.RunA,
			page.PageID,
			PageEditInput{
				RequestKey: uuid.New(),
				Sequence:   read.Sequence,
				Summary:    text,
				Operations: []wiki.BlockOperation{
					{
						Type:         "update",
						BlockID:      id,
						ExpectedHash: read.BlockHashes[id],
						Content:      &target,
					},
				},
			},
		)
		if e != nil || result.Status != "proposed" {
			t.Fatalf("proposal = %+v, %v", result, e)
		}
		return result
	}
	first := propose("Price: $19")
	preview, err := s.PageChangeDetail(ctx, p, f.ProjectA, page.PageID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	second := propose("Price: $29")
	stale := PageChangeReviewInput{RequestKey: uuid.New(), Action: "apply", Fingerprint: preview.Fingerprint}
	if _, err = s.ReviewPageChange(ctx, p, f.ProjectA, page.PageID, first.ID, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale bulk acceptance = %v, want conflict", err)
	}
	preview, err = s.PageChangeDetail(ctx, p, f.ProjectA, page.PageID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Suggestions) != 1 || len(preview.Suggestions[0].CompetingChangeIDs) != 1 {
		t.Fatalf("suggestions = %+v", preview.Suggestions)
	}
	in := PageChangeReviewInput{RequestKey: uuid.New(), Action: "apply", Fingerprint: preview.Fingerprint}
	if _, err = s.ReviewPageChange(ctx, p, f.ProjectA, page.PageID, first.ID, in); !errors.Is(err, ErrConflict) {
		t.Fatalf("competing bulk acceptance = %v, want conflict", err)
	}
	unchanged, err := s.ReadPageForRun(ctx, p, f.RunA, page.PageID)
	if err != nil || unchanged.Sequence != read.Sequence {
		t.Fatalf("proposal altered live document: %+v, %v", unchanged, err)
	}
	in.Indexes = []int{0}
	applied, err := s.ReviewPageChange(ctx, p, f.ProjectA, page.PageID, first.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.ReviewPageChange(ctx, p, f.ProjectA, page.PageID, first.ID, in)
	if err != nil || replay != applied {
		t.Fatalf("replay = %+v, %v, want %+v", replay, err, applied)
	}
	history, err := s.DocumentHistory(
		ctx,
		p,
		DocumentHistoryRef{ProjectID: f.ProjectA, VersionID: page.VersionID},
		DocumentHistoryInput{},
	)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Items []struct {
			Sequence         int64     `json:"sequence"`
			ActorID          uuid.UUID `json:"actorId"`
			SuggestionReview *struct {
				ChangeID       uuid.UUID `json:"changeId"`
				Action         string    `json:"action"`
				AgentID        uuid.UUID `json:"agentId"`
				BeforeSequence int64     `json:"beforeSequence"`
			} `json:"suggestionReview"`
		} `json:"items"`
	}
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Items) == 0 {
		t.Fatal("accepted suggestion missing from document history")
	}
	entry := wire.Items[0]
	if entry.Sequence != applied.Sequence || entry.ActorID != f.UserA || entry.SuggestionReview == nil ||
		entry.SuggestionReview.ChangeID != first.ID || entry.SuggestionReview.Action != "apply" ||
		entry.SuggestionReview.AgentID != f.AgentA || entry.SuggestionReview.BeforeSequence != read.Sequence {
		t.Fatalf("application history = %+v, want reviewer and exact Agent proposal receipt: %s", entry, raw)
	}
	saved, err := s.DocumentHistoryState(
		ctx,
		p,
		DocumentHistoryRef{ProjectID: f.ProjectA, VersionID: page.VersionID},
		applied.Sequence,
	)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		SuggestionCapture *struct {
			BeforeMarkdown string `json:"beforeMarkdown"`
			ResultMarkdown string `json:"resultMarkdown"`
		} `json:"suggestionCapture"`
	}
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	if capture.SuggestionCapture == nil ||
		!strings.Contains(capture.SuggestionCapture.BeforeMarkdown, "Price: $20") ||
		!strings.Contains(capture.SuggestionCapture.ResultMarkdown, "Price: $19") {
		t.Fatalf("saved application capture = %s, want exact before and result", raw)
	}
	other, err := s.PageChangeDetail(ctx, p, f.ProjectA, page.PageID, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if other.Suggestions[0].Status != "needs_update" || other.Status == "rejected" {
		t.Fatalf("competing alternative = %+v", other)
	}
	if _, err = s.ReviewPageChange(
		ctx,
		p,
		f.ProjectA,
		page.PageID,
		second.ID,
		PageChangeReviewInput{
			RequestKey:  uuid.New(),
			Action:      "apply",
			Fingerprint: other.Fingerprint,
			Indexes:     []int{0},
		},
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale explicit acceptance = %v", err)
	}
	if _, err = s.ReviewPageChange(
		ctx,
		p,
		f.ProjectA,
		page.PageID,
		second.ID,
		PageChangeReviewInput{
			RequestKey:  uuid.New(),
			Action:      "request_changes",
			Fingerprint: other.Fingerprint,
			Feedback:    "Use the accepted price and explain the discount.",
		},
	); err != nil {
		t.Fatal(err)
	}
	other, err = s.PageChangeDetail(ctx, p, f.ProjectA, page.PageID, second.ID)
	if err != nil || len(other.Reviews) != 1 ||
		other.Reviews[0].Feedback != "Use the accepted price and explain the discount." ||
		other.Reviews[0].ActorUserID != f.UserA {
		t.Fatalf("retained revision feedback = %+v, %v", other, err)
	}
	dbtest.MustExec(t, ctx, pool, `DELETE FROM runs WHERE id=$1`, f.RunA)
	retained, err := s.DocumentHistory(
		ctx,
		p,
		DocumentHistoryRef{ProjectID: f.ProjectA, VersionID: page.VersionID},
		DocumentHistoryInput{},
	)
	if err != nil || len(retained.Items) == 0 || retained.Items[0].SuggestionReview == nil ||
		retained.Items[0].SuggestionReview.AgentID == nil || *retained.Items[0].SuggestionReview.AgentID != f.AgentA {
		t.Fatalf("retained Agent attribution = %+v, %v", retained, err)
	}
	detail, err := s.PageChangeDetail(ctx, p, f.ProjectA, page.PageID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var author struct {
		AgentID   uuid.UUID `json:"agentId"`
		AgentName string    `json:"agentName"`
	}
	if err = json.Unmarshal(raw, &author); err != nil {
		t.Fatal(err)
	}
	if author.AgentID != f.AgentA || author.AgentName == "" {
		t.Fatalf("proposal author = %+v, want retained Agent %s", author, f.AgentA)
	}
}

func TestPageDeliveryReusesArtifactIdentity(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	s := newCanvasServices(store, bus, nil)
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	page, err := s.CreatePage(
		ctx,
		p,
		f.ProjectA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Continuing report", Markdown: "Shared report"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var before int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM project_artifacts WHERE project_id=$1`, f.ProjectA).
		Scan(&before); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		preview, e := s.PreviewPresentation(ctx, p, f.ProjectA, page.PageID, -1)
		if e != nil {
			t.Fatal(e)
		}
		capture, e := s.CapturePresentation(
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
		if e != nil {
			t.Fatal(e)
		}
		var output uuid.UUID
		if e = pool.QueryRow(ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,presentation_number,owner_user_id, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,'Report','Exact delivery',$3,$4,$5, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`, f.CompanyA, f.RunA, uuid.NewString(), i+1, f.UserA).
			Scan(&output); e != nil {
			t.Fatal(e)
		}
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`UPDATE project_presentations SET output_id=$2 WHERE id=$1`,
			capture.ID,
			output,
		)
		if e = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			_, e := RegisterOutputInTx(
				ctx,
				q,
				OutputRegistrationInput{CompanyID: f.CompanyA, OutputID: output, ActorUserID: f.UserA},
			)
			return e
		}); e != nil {
			t.Fatal(e)
		}
	}
	var after int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM project_artifacts WHERE project_id=$1`, f.ProjectA).
		Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("Artifact count = %d, want %d after repeated native delivery", after, before)
	}
}

func TestProposalCaptureDoesNotApplySuggestion(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	s := newCanvasServices(store, bus, nil)
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
	page, err := s.CreatePage(
		ctx,
		p,
		f.ProjectA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Report", Markdown: "Accepted text"},
	)
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.ReadPageForRun(ctx, p, f.RunA, page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	addition := wiki.BlocksFromMarkdown("Proposed addition").Content[0]
	var root wiki.BlockNode
	if err = json.Unmarshal(read.Content, &root); err != nil {
		t.Fatal(err)
	}
	rewrite := root.Content[0]
	rewrite.Content = []wiki.BlockNode{{Type: "text", Text: "Rejected rewrite"}}
	blockID := rewrite.Attrs["blockId"].(string)
	change, err := s.EditPageForRun(
		ctx,
		p,
		f.RunA,
		page.PageID,
		PageEditInput{
			RequestKey: uuid.New(),
			Sequence:   read.Sequence,
			Summary:    "Add finding",
			Operations: []wiki.BlockOperation{
				{
					Type:         "update",
					BlockID:      blockID,
					ExpectedHash: read.BlockHashes[blockID],
					Content:      &rewrite,
				},
				{Type: "insert", Content: &addition},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.PageChangeDetail(ctx, p, f.ProjectA, page.PageID, change.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DefaultPageService.ReviewPageChange(
		ctx,
		p,
		f.ProjectA,
		page.PageID,
		change.ID,
		PageChangeReviewInput{
			RequestKey: uuid.New(), Action: "reject", Fingerprint: detail.Fingerprint, Indexes: []int{0},
		},
	); err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewProposalForRun(ctx, p, f.RunA, page.PageID, change.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(preview.Content), "Proposed addition") ||
		strings.Contains(string(preview.Content), "Rejected rewrite") {
		t.Fatalf("proposal preview = %s", preview.Content)
	}
	capture, err := s.CapturePresentation(
		ctx,
		p,
		f.ProjectA,
		PresentationInput{
			PageID:      page.PageID,
			ChangeID:    &change.ID,
			RequestKey:  uuid.New(),
			Sequence:    preview.Sequence,
			Fingerprint: preview.Fingerprint,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.ReadPageForRun(ctx, p, f.RunA, page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(current.Content), "Proposed addition") || current.Sequence != read.Sequence {
		t.Fatalf("capture applied proposal: %+v", current)
	}
	if capture.ChangeID == nil || *capture.ChangeID != change.ID {
		t.Fatalf("capture missing proposal identity: %+v", capture)
	}
	assertRequired := func(value Presentation) {
		t.Helper()
		raw, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		var wire struct {
			RequiredSuggestionIndexes []int `json:"requiredSuggestionIndexes"`
		}
		if e = json.Unmarshal(raw, &wire); e != nil {
			t.Fatal(e)
		}
		if len(wire.RequiredSuggestionIndexes) != 1 || wire.RequiredSuggestionIndexes[0] != 1 {
			t.Fatalf("required indexes = %v, want [1]", wire.RequiredSuggestionIndexes)
		}
	}
	assertRequired(preview)
	assertRequired(capture)
	detail, err = s.PageChangeDetail(ctx, p, f.ProjectA, page.PageID, change.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DefaultPageService.ReviewPageChange(
		ctx,
		p,
		f.ProjectA,
		page.PageID,
		change.ID,
		PageChangeReviewInput{
			RequestKey: uuid.New(), Action: "reject", Fingerprint: detail.Fingerprint, Indexes: []int{1},
		},
	); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Presentation(ctx, p, f.ProjectA, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRequired(saved)
}

func TestNewAgentPageRequiresAcceptance(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	s := newCanvasServices(store, bus, nil)
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
	page, err := s.CreatePageForRun(
		ctx,
		p,
		f.RunA,
		PageCreateInput{
			RequestKey: uuid.New(),
			Title:      "Candidate",
			Markdown:   strings.Repeat("Not shared yet\n\n", 40),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if page.ChangeID == nil {
		t.Fatal("new Agent Page has no creation proposal")
	}
	library, err := s.Pages(ctx, p, f.ProjectA, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range library {
		if artifact.ID == page.ArtifactID {
			t.Fatal("unreviewed creation in shared library")
		}
	}
	current, err := s.ReadPageForRun(ctx, p, f.RunA, page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(current.Content), "Not shared yet") {
		t.Fatal("unreviewed creation installed shared text")
	}
	if current.CanEdit {
		t.Fatal("unreviewed creation allows ordinary shared editing")
	}
	if _, err = s.PreviewPresentation(ctx, p, f.ProjectA, page.PageID, -1); !errors.Is(err, ErrConflict) {
		t.Fatalf("shared capture of unaccepted creation = %v, want conflict", err)
	}
	if _, err = s.UpdateOverview(
		ctx,
		p,
		f.ProjectA,
		OverviewInput{RequestKey: uuid.New(), Action: "designate", PageID: &page.PageID},
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("unaccepted Page as Overview = %v, want conflict", err)
	}
	detail, err := s.PageChangeDetail(ctx, p, f.ProjectA, page.PageID, *page.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Suggestions) != 41 || len(detail.Suggestions[0].Group) != 41 {
		t.Fatalf(
			"creation suggestions = %d, group = %d, want 41",
			len(detail.Suggestions),
			len(detail.Suggestions[0].Group),
		)
	}
	if _, err = s.DefaultPageService.ReviewPageChange(
		ctx,
		p,
		f.ProjectA,
		page.PageID,
		*page.ChangeID,
		PageChangeReviewInput{
			RequestKey:  uuid.New(),
			Fingerprint: detail.Fingerprint,
			Action:      "apply",
			Indexes:     detail.Suggestions[0].Group,
		},
	); err != nil {
		t.Fatal(err)
	}
	current, err = s.ReadPageForRun(ctx, p, f.RunA, page.PageID)
	if err != nil || !strings.Contains(string(current.Content), "Not shared yet") {
		t.Fatalf("accepted creation = %+v, %v", current, err)
	}
	library, err = s.Pages(ctx, p, f.ProjectA, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, artifact := range library {
		found = found || artifact.ID == page.ArtifactID
		if artifact.ID == page.ArtifactID {
			content, e := s.VersionBlocks(ctx, p, f.ProjectA, artifact.LatestVersionID)
			if e != nil || !strings.Contains(string(content), "Not shared yet") {
				t.Fatalf("accepted creation snapshot = %s, %v", content, e)
			}
		}
	}
	if !found {
		t.Fatal("accepted creation absent from library")
	}
}

func TestSelectiveSuggestionDecisionsAndUndo(t *testing.T) {
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
	page, err := s.CreatePage(
		ctx,
		p,
		f.ProjectA,
		PageCreateInput{
			RequestKey: uuid.New(),
			Title:      "Independent edits",
			Markdown:   "First original\n\nSecond original",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.ReadPageForRun(ctx, p, f.RunA, page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	var root wiki.BlockNode
	if err = json.Unmarshal(read.Content, &root); err != nil {
		t.Fatal(err)
	}
	var ops []wiki.BlockOperation
	for i, target := range root.Content {
		id := target.Attrs["blockId"].(string)
		target.Content = []wiki.BlockNode{
			{Type: "text", Text: []string{"First proposed", "Second proposed"}[i]},
		}
		ops = append(
			ops,
			wiki.BlockOperation{
				Type:         "update",
				BlockID:      id,
				ExpectedHash: read.BlockHashes[id],
				Content:      &target,
			},
		)
	}
	change, err := s.EditPageForRun(
		ctx,
		p,
		f.RunA,
		page.PageID,
		PageEditInput{
			RequestKey: uuid.New(),
			Sequence:   read.Sequence,
			Summary:    "Two independent edits",
			Operations: ops,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	decide := func(action string, indexes []int, want string) {
		t.Helper()
		detail, e := s.PageChangeDetail(ctx, p, f.ProjectA, page.PageID, change.ID)
		if e != nil {
			t.Fatal(e)
		}
		result, e := s.ReviewPageChange(
			ctx,
			p,
			f.ProjectA,
			page.PageID,
			change.ID,
			PageChangeReviewInput{
				RequestKey:  uuid.New(),
				Fingerprint: detail.Fingerprint,
				Action:      action,
				Indexes:     indexes,
			},
		)
		if e != nil || result.Status != want {
			t.Fatalf("%s = %+v, %v, want %s", action, result, e, want)
		}
	}
	decide("apply", []int{0}, "partially_applied")
	var retained bool
	if err = pool.QueryRow(ctx, `SELECT before_revision_id IS NOT NULL AND result_revision_id IS NOT NULL FROM page_suggestion_reviews WHERE company_id=$1 AND change_id=$2 AND action='apply'`, f.CompanyA, change.ID).
		Scan(&retained); err != nil ||
		!retained {
		t.Fatalf("partial acceptance retained captures = %v, %v", retained, err)
	}
	read, err = s.ReadPageForRun(ctx, p, f.RunA, page.PageID)
	if err != nil || !strings.Contains(string(read.Content), "First proposed") ||
		!strings.Contains(string(read.Content), "Second original") {
		t.Fatalf("partial acceptance = %s, %v", read.Content, err)
	}
	decide("reject", []int{1}, "resolved")
	decide("undo", nil, "undone")
	read, err = s.ReadPageForRun(ctx, p, f.RunA, page.PageID)
	if err != nil || !strings.Contains(string(read.Content), "First original") ||
		!strings.Contains(string(read.Content), "Second original") {
		t.Fatalf("undo accepted subset = %s, %v", read.Content, err)
	}
}

func TestPageSuggestionInsertBelowHeading(t *testing.T) {
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

	page, err := s.CreatePage(
		ctx,
		p,
		f.ProjectA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Translation", Markdown: "## Poem\n\nOriginal stanza"},
	)
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.ReadPageForRun(ctx, p, f.RunA, page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	var root wiki.BlockNode
	if err = json.Unmarshal(read.Content, &root); err != nil {
		t.Fatal(err)
	}
	in := PageEditInput{
		RequestKey: uuid.New(),
		Sequence:   read.Sequence,
		Summary:    "Add translation",
		Operations: []wiki.BlockOperation{
			{
				Type:     "insert",
				ParentID: root.Content[0].Attrs["blockId"].(string),
				AfterID:  root.Content[1].Attrs["blockId"].(string),
				Content: &wiki.BlockNode{
					Type:    "paragraph",
					Content: []wiki.BlockNode{{Type: "text", Text: "English translation"}},
				},
			},
		},
	}
	if _, err = s.EditPageForRun(ctx, p, f.RunA, page.PageID, in); !errors.Is(err, ErrPageInvalidInsert) {
		t.Fatalf("insertion under heading = %v, want ErrPageInvalidInsert", err)
	}
	feed, err := s.PageChanges(ctx, p, f.ProjectA, page.PageID, "")
	if err != nil || len(feed.Pending) != 0 {
		t.Fatalf("rejected proposal feed = %+v, %v", feed, err)
	}
	in.RequestKey = uuid.New()
	in.Operations[0].ParentID = ""
	result, err := s.EditPageForRun(ctx, p, f.RunA, page.PageID, in)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.PageChangeDetail(ctx, p, f.ProjectA, page.PageID, result.ID)
	if err != nil {
		t.Fatal(err)
	}
	var after wiki.BlockNode
	if err = json.Unmarshal(detail.After, &after); err != nil {
		t.Fatal(err)
	}
	if len(after.Content) != 3 || after.Content[2].Content[0].Text != "English translation" {
		t.Fatalf("translation placement = %+v", detail.After)
	}
	current, err := s.ReadPageForRun(ctx, p, f.RunA, page.PageID)
	if err != nil {
		t.Fatal(err)
	}
	if string(current.Content) != string(read.Content) {
		t.Fatal("proposal changed the shared document before review")
	}
}
