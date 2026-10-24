//go:build integration

package canvas

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

func TestPageSourcePreviewGuardsAndDoesNotWrite(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	observed := map[string]int{}
	service := NewPageService(
		store,
		bus,
		WithPageSourceObserver(func(operation, outcome string, duration time.Duration, bytes int) {
			observed[operation+":"+outcome]++
			if duration < 0 || bytes < 0 {
				t.Errorf("invalid source observation: %v, %d", duration, bytes)
			}
		}),
	)
	principal := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	content := nativeScenarioFixture(
		t,
		`{"type":"doc","content":[{"type":"uiScope","attrs":{"name":"scenario","initial":{"quantity":2}},"content":[{"type":"uiNumberInput","attrs":{"label":"Quantity","bind":"quantity"}},{"type":"uiMetric","attrs":{"label":"Total","value":{"op":"mul","args":[{"op":"state","name":"quantity"},{"op":"literal","value":10}]}}}]}]}`,
	)
	page, err := service.CreatePage(
		ctx,
		principal,
		f.ProjectA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Preview", Content: content},
	)
	if err != nil {
		t.Fatal(err)
	}
	read := func() ([]byte, []byte, int64) {
		t.Helper()
		var state, content []byte
		var sequence int64
		if err := pool.QueryRow(ctx, `SELECT state,content,head_seq FROM wiki_canvas_drafts WHERE company_id=$1 AND page_id=$2`, f.CompanyA, page.PageID).
			Scan(&state, &content, &sequence); err != nil {
			t.Fatal(err)
		}
		return state, content, sequence
	}
	beforeState, beforeContent, beforeSequence := read()
	var root wiki.BlockNode
	if err := json.Unmarshal(beforeContent, &root); err != nil {
		t.Fatal(err)
	}
	metric := root.Content[0].Content[1]
	id := metric.Attrs["blockId"].(string)
	base, err := service.PreviewPageSource(
		ctx,
		principal,
		f.ProjectA,
		page.PageID,
		PageSourcePreviewInput{BlockID: id},
	)
	if err != nil || base.Content == nil || base.Sequence != beforeSequence || base.SchemaVersion != 2 ||
		len(base.Diagnostics) != 0 {
		t.Fatalf("source read = %+v, %v; want schema-2 block and guards", base, err)
	}
	metric.Attrs["value"] = map[string]any{
		"op": "add",
		"args": []any{
			map[string]any{"op": "state", "name": "quantity"},
			map[string]any{"op": "literal", "value": float64(1)},
		},
	}
	changed := wiki.BlocksSource(metric)
	input := PageSourcePreviewInput{
		BlockID:          id,
		Source:           &changed,
		LanguageVersion:  1,
		ExpectedSequence: &base.Sequence,
		ExpectedHash:     base.ExpectedHash,
	}
	preview, err := service.PreviewPageSource(ctx, principal, f.ProjectA, page.PageID, input)
	if err != nil || preview.Content == nil || len(preview.Diagnostics) != 0 {
		t.Fatalf("preview = %+v, %v; want compiled subtree", preview, err)
	}
	result, err := wiki.EvaluateUIExpression(
		preview.Content.Attrs["value"],
		wiki.UIEnvironment{State: map[string]any{"quantity": float64(2)}},
	)
	if err != nil || result != float64(3) {
		t.Fatalf("value = %v, %v; want 3", result, err)
	}
	badSequence := base.Sequence + 1
	stale := input
	stale.ExpectedSequence = &badSequence
	if _, err := service.PreviewPageSource(
		ctx,
		principal,
		f.ProjectA,
		page.PageID,
		stale,
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale sequence = %v, want conflict", err)
	}
	stale = input
	stale.ExpectedHash = strings.Repeat("0", 64)
	if _, err := service.PreviewPageSource(
		ctx,
		principal,
		f.ProjectA,
		page.PageID,
		stale,
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale hash = %v, want conflict", err)
	}
	missing := strings.Replace(changed, `"quantity"`, `"missing"`, 1)
	invalid := input
	invalid.Source = &missing
	rejected, err := service.PreviewPageSource(ctx, principal, f.ProjectA, page.PageID, invalid)
	if err != nil || rejected.Content != nil || len(rejected.Diagnostics) == 0 {
		t.Fatalf("invalid binding = %+v, %v; want diagnostic without content", rejected, err)
	}
	for _, outcome := range []string{"check:accepted", "check:rejected", "check:conflict"} {
		if observed[outcome] == 0 {
			t.Errorf("missing source observation %s: %v", outcome, observed)
		}
	}
	diagnostic := rejected.Diagnostics[0]
	if diagnostic.BlockID != id || diagnostic.Code != "state.undeclared" ||
		diagnostic.Start < 0 || diagnostic.End > len(missing) || diagnostic.End < diagnostic.Start {
		t.Fatalf("binding diagnostic = %+v", diagnostic)
	}
	if selected := missing[diagnostic.Start:diagnostic.End]; selected != `"missing"` {
		t.Fatalf("binding diagnostic selected %q, want missing state name", selected)
	}

	outsider := auth.UserPrincipal{CompanyID: f.CompanyB, UserID: f.UserB, Role: auth.CompanyRoleOwner}
	denied, err := service.PreviewPageSource(
		ctx,
		outsider,
		f.ProjectA,
		page.PageID,
		PageSourcePreviewInput{BlockID: id},
	)
	if err == nil || denied.Content != nil || denied.Source != "" {
		t.Fatalf("foreign read = %+v, %v; want no disclosure", denied, err)
	}
	afterState, afterContent, afterSequence := read()
	if !bytes.Equal(beforeState, afterState) || !bytes.Equal(beforeContent, afterContent) ||
		beforeSequence != afterSequence {
		t.Fatal("source preview changed canonical document state")
	}

	// A member can restructure descendants and insert an unaddressed child.
	// Compilation reserves the new identity but leaves the canonical CRDT alone.
	scope := root.Content[0]
	scopeID := scope.Attrs["blockId"].(string)
	scopeBase, err := service.PreviewPageSource(
		ctx,
		principal,
		f.ProjectA,
		page.PageID,
		PageSourcePreviewInput{BlockID: scopeID},
	)
	if err != nil {
		t.Fatal(err)
	}
	scope.Content = []wiki.BlockNode{scope.Content[1], {
		Type: "paragraph", Content: []wiki.BlockNode{{Type: "text", Text: "New explanation"}},
	}}
	structuralSource := wiki.BlocksSource(scope)
	structural, err := service.PreviewPageSource(ctx, principal, f.ProjectA, page.PageID, PageSourcePreviewInput{
		BlockID: scopeID, Source: &structuralSource, LanguageVersion: 1,
		ExpectedSequence: &scopeBase.Sequence, ExpectedHash: scopeBase.ExpectedHash,
	})
	if err != nil || structural.Content == nil || len(structural.Diagnostics) != 0 {
		t.Fatalf("structural preview = %+v, %v", structural, err)
	}
	newID := structural.Content.Content[1].Attrs["blockId"].(string)
	var reserved bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM wiki_block_ids WHERE company_id=$1 AND page_id=$2 AND id=$3)`, f.CompanyA, page.PageID, newID).
		Scan(&reserved); err != nil ||
		!reserved {
		t.Fatalf("new block identity not reserved: %v", err)
	}
	afterState, afterContent, afterSequence = read()
	if !bytes.Equal(beforeState, afterState) || !bytes.Equal(beforeContent, afterContent) ||
		beforeSequence != afterSequence {
		t.Fatal("structural preview changed canonical document state")
	}
	update, _, err := wiki.ApplyBlockOperations(beforeState, []wiki.BlockOperation{{
		Type: "update", BlockID: scopeID, ExpectedHash: scopeBase.ExpectedHash, Content: structural.Content,
	}})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := service.SyncDocument(ctx, principal, f.ProjectA, page.VersionID, DocumentSyncInput{
		SchemaVersion: 2, Update: update, BaseSequence: beforeSequence,
	})
	if err != nil {
		t.Fatalf("apply structural preview through sync: %v", err)
	}
	var applied wiki.BlockNode
	if err := json.Unmarshal(saved.Content, &applied); err != nil {
		t.Fatal(err)
	}
	if wiki.FindBlock(applied, newID) == nil || wiki.FindBlock(applied, id) == nil ||
		wiki.FindBlock(applied, root.Content[0].Content[0].Attrs["blockId"].(string)) != nil {
		t.Fatal(
			"structural sync did not preserve retained identities, add the new block and remove the omitted block",
		)
	}
}

func TestInlinePageSourcePreviewRoundTrip(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	created := false
	pages := NewPageService(
		store,
		bus,
		WithPageSourceObserver(func(operation, outcome string, _ time.Duration, _ int) {
			if operation == "create" && outcome == "accepted" {
				created = true
			}
		}),
	)
	principal := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	source := `{@body const [quantity, setQuantity] = DIL.useState(2);}<box><text>{quantity * 10}</text></box>`
	page, err := pages.CreatePage(
		ctx,
		principal,
		f.ProjectA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Inline source", Source: &source, LanguageVersion: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("missing accepted inline source creation observation")
	}
	before, err := pages.Document(ctx, principal, f.ProjectA, page.VersionID, -1)
	if err != nil {
		t.Fatal(err)
	}
	var root wiki.BlockNode
	if err := json.Unmarshal(before.Content, &root); err != nil {
		t.Fatal(err)
	}
	block := root.Content[0]
	if block.Type != "uiDocument" {
		t.Fatalf("block = %s, want uiDocument", block.Type)
	}
	id := block.Attrs["blockId"].(string)
	base, err := pages.PreviewPageSource(
		ctx,
		principal,
		f.ProjectA,
		page.PageID,
		PageSourcePreviewInput{BlockID: id},
	)
	if err != nil || strings.TrimSpace(base.Source) != source {
		t.Fatalf("source read = %+v, %v", base, err)
	}
	changed := strings.Replace(source, "quantity * 10", "quantity * 25", 1)
	input := PageSourcePreviewInput{
		BlockID:          id,
		Source:           &changed,
		LanguageVersion:  1,
		ExpectedSequence: &base.Sequence,
		ExpectedHash:     base.ExpectedHash,
	}
	preview, err := pages.PreviewPageSource(ctx, principal, f.ProjectA, page.PageID, input)
	if err != nil || preview.Content == nil || wiki.InlineSource(*preview.Content) != changed ||
		preview.Content.Attrs["blockId"] != id {
		t.Fatalf("inline preview = %+v, %v", preview, err)
	}
	after, err := pages.Document(ctx, principal, f.ProjectA, page.VersionID, -1)
	if err != nil || after.Sequence != before.Sequence || !bytes.Equal(after.State, before.State) {
		t.Fatal("preview mutated the saved document", err)
	}
	update, _, err := wiki.ApplyBlockOperations(
		before.State,
		[]wiki.BlockOperation{
			{Type: "update", BlockID: id, ExpectedHash: base.ExpectedHash, Content: preview.Content},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := pages.SyncDocument(
		ctx,
		principal,
		f.ProjectA,
		page.VersionID,
		DocumentSyncInput{SchemaVersion: 2, Update: update, BaseSequence: before.Sequence},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(saved.Content, &root); err != nil {
		t.Fatal(err)
	}
	if wiki.InlineSource(root.Content[0]) != changed {
		t.Fatal("source did not persist through document sync")
	}
}
