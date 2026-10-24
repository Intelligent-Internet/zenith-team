//go:build integration

package canvas

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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

func TestPresentationRetainsInlineScenarioCapture(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	pages := NewPageService(store, bus)
	presentations := NewPresentationService(store, bus, nil)
	principal := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	source := `{@body const [count, setCount] = DIL.useState(2);}<button onClick={() => setCount(count + 1)}>{count}</button>`
	inline, err := pages.CreatePage(ctx, principal, f.ProjectA, PageCreateInput{
		RequestKey: uuid.New(), Title: "Inline counter", Source: &source, LanguageVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"direct", "pinned", "live"} {
		t.Run(format, func(t *testing.T) {
			page := inline
			if format != "direct" {
				page, err = pages.CreatePage(ctx, principal, f.ProjectA, PageCreateInput{
					RequestKey: uuid.New(),
					Title:      "Included counter",
					Content: wiki.BlockNode{
						Type: "doc",
						Content: []wiki.BlockNode{
							{
								Type: "artifactReference",
								Attrs: map[string]any{
									"versionId": inline.VersionID.String(),
									"format":    format,
									"display":   "expanded",
								},
							},
						},
					},
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			preview, err := presentations.PreviewPresentation(
				ctx,
				principal,
				f.ProjectA,
				page.PageID,
				page.Sequence,
			)
			if err != nil {
				t.Fatal(err)
			}
			if !preview.CanExport || len(preview.Evaluations) == 0 {
				t.Fatalf("inline preview = %+v, want retained evaluation", preview)
			}
			saved, err := presentations.CapturePresentation(ctx, principal, f.ProjectA, PresentationInput{
				PageID:      page.PageID,
				Sequence:    page.Sequence,
				Fingerprint: preview.Fingerprint,
				RequestKey:  uuid.New(),
			})
			if err != nil {
				t.Fatal(err)
			}
			reopened, err := presentations.Presentation(ctx, principal, f.ProjectA, saved.ID)
			if err != nil || !reopened.CanExport || len(reopened.Evaluations) == 0 {
				t.Fatalf("reopened capture = %+v, %v", reopened, err)
			}
			exported, err := presentations.PresentationExport(ctx, principal, f.ProjectA, saved.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(exported, `"rendererVersion": 1`) ||
				!strings.Contains(exported, "useState") {
				t.Fatalf("export omitted inline evidence: %s", exported)
			}
		})
	}
}

func TestPresentationRetainsIntelligentScenario(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	pages := NewPageService(store, bus)
	observed := make(map[string]int)
	presentations := NewPresentationService(
		store,
		bus,
		nil,
		WithPresentationCaptureObserver(func(operation, outcome string, duration time.Duration) {
			observed[operation+":"+outcome]++
			if duration < 0 {
				t.Error("negative capture duration")
			}
		}),
	)
	principal := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	// Human work may have a chat-assisting Agent; the source projection names
	// only the Agent actually executing the Task, matching live summaries.
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET executed_by_agent_id=NULL WHERE id=$1`, f.TaskA)
	definition := nativeScenarioFixture(
		t,
		fmt.Sprintf(
			`{"type":"doc","content":[{"type":"uiScope","attrs":{"name":"forecast","initial":{"multiplier":2},"sources":{"tasks":{"type":"tasks","projectId":"%s","filter":"all"}},"computed":{"total":{"op":"mul","args":[{"op":"count","input":{"op":"data","name":"tasks"}},{"op":"state","name":"multiplier"}]}}},"content":[{"type":"uiNumberInput","attrs":{"label":"Multiplier","bind":"multiplier"}},{"type":"uiMetric","attrs":{"label":"Forecast","value":{"op":"state","name":"total"}}}]}]}`,
			f.ProjectA,
		),
	)
	page, err := pages.CreatePage(
		ctx,
		principal,
		f.ProjectA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Forecast", Content: definition},
	)
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := presentations.PreviewPresentation(ctx, principal, f.ProjectA, page.PageID, page.Sequence)
	if err != nil {
		t.Fatal(err)
	}
	evidence, ok := defaults.Evaluations["page"]
	if !ok || len(evidence.Scopes) != 1 || len(evidence.Sources) != 1 {
		t.Fatalf("evidence = %+v, want scope and source", defaults.Evaluations)
	}
	projected, err := json.Marshal(evidence.Sources[0].Snapshot.Value)
	if err != nil {
		t.Fatal(err)
	}
	var projectedTasks []map[string]any
	if err := json.Unmarshal(projected, &projectedTasks); err != nil {
		t.Fatal(err)
	}
	foundHuman := false
	for _, task := range projectedTasks {
		if task["id"] == f.TaskA.String() {
			foundHuman = true
			if task["agentId"] != nil || task["agentName"] != nil {
				t.Fatalf("human task attributed to chat Agent: %+v", task)
			}
		}
	}
	if !foundHuman {
		t.Fatal("human Task missing from capture source")
	}
	coverage := evidence.Sources[0].Snapshot.Coverage
	if coverage == nil || !coverage.Complete || coverage.RowCount < 1 {
		t.Fatalf("coverage = %+v, want complete nonempty tasks", coverage)
	}
	selected := PresentationSelections{"page": {evidence.Scopes[0].Path: {"multiplier": float64(3)}}}
	scenario := PresentationScenarioInput{PageID: page.PageID, Sequence: page.Sequence, Selected: selected}
	preview, err := presentations.PreviewScenario(ctx, principal, f.ProjectA, scenario)
	if err != nil || !preview.CanExport {
		t.Fatalf("preview = %+v, %v; want exportable scenario", preview, err)
	}
	repeated, err := presentations.PreviewScenario(ctx, principal, f.ProjectA, scenario)
	if err != nil || repeated.Fingerprint != preview.Fingerprint {
		t.Fatalf("repeat fingerprint = %s, %v; want %s", repeated.Fingerprint, err, preview.Fingerprint)
	}
	if preview.Fingerprint == defaults.Fingerprint {
		t.Fatal("selected inputs did not affect capture fingerprint")
	}
	want := float64(coverage.RowCount * 3)
	if preview.Evaluations["page"].Scopes[0].Computed["total"] != want {
		t.Fatalf("computed = %+v, want total %v", preview.Evaluations["page"].Scopes[0].Computed, want)
	}
	input := PresentationInput{
		PageID:      page.PageID,
		Sequence:    page.Sequence,
		Selected:    selected,
		Fingerprint: preview.Fingerprint,
		RequestKey:  uuid.New(),
	}
	saved, err := presentations.CapturePresentation(ctx, principal, f.ProjectA, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := presentations.CapturePresentation(ctx, principal, f.ProjectA, input)
	if err != nil || replay.ID != saved.ID {
		t.Fatalf("idempotent capture = %v, %v; want %v", replay.ID, err, saved.ID)
	}
	retained, err := json.Marshal(saved.Evaluations)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE tasks SET title='Changed after capture',version=version+1 WHERE company_id=$1 AND id=$2`,
		f.CompanyA,
		f.TaskA,
	)
	reopened, err := presentations.Presentation(ctx, principal, f.ProjectA, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(reopened.Evaluations)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(retained, got) || !bytes.Equal(saved.Content, reopened.Content) {
		t.Fatal("reopened capture changed with live task data")
	}
	markdown, err := presentations.PresentationExport(ctx, principal, f.ProjectA, saved.ID)
	if err != nil || !strings.Contains(markdown, "multiplier: 3") ||
		strings.Contains(markdown, "Changed after capture") {
		t.Fatalf("export = %q, %v; want retained selected inputs and data", markdown, err)
	}
	stale := input
	stale.RequestKey = uuid.New()
	if _, err := presentations.CapturePresentation(
		ctx,
		principal,
		f.ProjectA,
		stale,
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("capture from stale source observation = %v, want conflict", err)
	}
	for _, outcome := range []string{"prepare:prepared", "save:saved", "save:replayed", "save:mismatch"} {
		if observed[outcome] == 0 {
			t.Errorf("missing capture observation %s: %v", outcome, observed)
		}
	}
}

func TestPresentationRetainsIncludedSourceSelections(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"pinned", "live"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			pool, _ := dbtest.StartPostgres(t)
			ctx := t.Context()
			if err := db.MigrateUp(ctx, pool); err != nil {
				t.Fatal(err)
			}
			f := dbtest.SeedFixture(t, ctx, pool)
			store, _ := db.NewStore(pool)
			bus, _ := events.NewBus(events.BusOptions{})
			pages := NewPageService(store, bus)
			presentations := NewPresentationService(store, bus, nil)
			principal := auth.UserPrincipal{
				CompanyID: f.CompanyA,
				UserID:    f.UserA,
				Role:      auth.CompanyRoleOwner,
			}
			definition := nativeScenarioFixture(
				t,
				`{"type":"doc","content":[{"type":"uiScope","attrs":{"name":"forecast","initial":{"multiplier":2},"computed":{"total":{"op":"mul","args":[{"op":"state","name":"multiplier"},{"op":"literal","value":10}]}}},"content":[{"type":"uiNumberInput","attrs":{"label":"Multiplier","bind":"multiplier"}},{"type":"uiMetric","attrs":{"label":"Total","value":{"op":"state","name":"total"}}},{"type":"uiRepeat","attrs":{"items":{"op":"literal","value":[{"id":"one"},{"id":"two"}]},"keyField":"id"},"content":[{"type":"uiScope","attrs":{"name":"row","initial":{"amount":1}},"content":[{"type":"uiNumberInput","attrs":{"label":"Amount","bind":"amount"}},{"type":"uiMetric","attrs":{"label":"Selected","value":{"op":"state","name":"amount"}}}]}]}]}]}`,
			)
			included, err := pages.CreatePage(
				ctx,
				principal,
				f.ProjectA,
				PageCreateInput{
					RequestKey: uuid.New(),
					Title:      "Included forecast",
					Content:    definition,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			page, err := pages.CreatePage(ctx, principal, f.ProjectA, PageCreateInput{
				RequestKey: uuid.New(),
				Title:      "Report",
				Content: wiki.BlockNode{
					Type: "doc",
					Content: []wiki.BlockNode{
						{
							Type: "artifactReference",
							Attrs: map[string]any{
								"versionId": included.VersionID.String(),
								"format":    format,
								"display":   "expanded",
							},
						},
					},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			key := included.VersionID.String()
			if format == "live" {
				key += ":live"
			}
			defaults, err := presentations.PreviewPresentation(
				ctx,
				principal,
				f.ProjectA,
				page.PageID,
				page.Sequence,
			)
			if err != nil {
				t.Fatal(err)
			}
			evidence, ok := defaults.Evaluations[key]
			if !ok || len(evidence.Scopes) != 3 {
				t.Fatalf(
					"included scopes = %+v, want outer scope and two instances",
					defaults.Evaluations,
				)
			}
			selected := PresentationSelections{key: {}}
			for index, scope := range evidence.Scopes {
				if scope.Name == "forecast" {
					selected[key][scope.Path] = map[string]any{"multiplier": float64(7)}
				} else {
					selected[key][scope.Path] = map[string]any{"amount": float64(index + 10)}
				}
			}
			scenario := PresentationScenarioInput{
				PageID:   page.PageID,
				Sequence: page.Sequence,
				Selected: selected,
			}
			preview, err := presentations.PreviewScenario(ctx, principal, f.ProjectA, scenario)
			if err != nil {
				t.Fatal(err)
			}
			if !preview.CanExport || preview.Fingerprint == defaults.Fingerprint {
				t.Fatal("included selections did not produce a distinct exportable capture")
			}
			for _, scope := range preview.Evaluations[key].Scopes {
				got, _ := json.Marshal(scope.Selected)
				want, _ := json.Marshal(selected[key][scope.Path])
				if !bytes.Equal(got, want) {
					t.Fatalf("selected = %s, want %s", got, want)
				}
				if scope.Name == "forecast" && scope.Computed["total"] != float64(70) {
					t.Fatalf("computed = %+v, want total 70", scope.Computed)
				}
			}
			for _, invalid := range []struct {
				name     string
				selected PresentationSelections
			}{
				{"unknown document", PresentationSelections{uuid.NewString(): {evidence.Scopes[0].Path: {"multiplier": float64(7)}}}},
				{"unknown scope", PresentationSelections{key: {"/missing": {"multiplier": float64(7)}}}},
			} {
				t.Run(invalid.name, func(t *testing.T) {
					_, err := presentations.PreviewScenario(
						ctx,
						principal,
						f.ProjectA,
						PresentationScenarioInput{
							PageID:   page.PageID,
							Sequence: page.Sequence,
							Selected: invalid.selected,
						},
					)
					if !errors.Is(err, ErrInvalid) {
						t.Fatalf("selection error = %v, want ErrInvalid", err)
					}
				})
			}
			input := PresentationInput{
				PageID:      page.PageID,
				Sequence:    page.Sequence,
				Selected:    selected,
				Fingerprint: preview.Fingerprint,
				RequestKey:  uuid.New(),
			}
			mismatched := input
			mismatched.Selected = nil
			mismatched.RequestKey = uuid.New()
			if _, err := presentations.CapturePresentation(
				ctx,
				principal,
				f.ProjectA,
				mismatched,
			); !errors.Is(
				err,
				ErrConflict,
			) {
				t.Fatalf("changed selection capture = %v, want ErrConflict", err)
			}
			saved, err := presentations.CapturePresentation(ctx, principal, f.ProjectA, input)
			if err != nil {
				t.Fatal(err)
			}
			retained, err := json.Marshal(saved.Evaluations)
			if err != nil {
				t.Fatal(err)
			}
			changed := wiki.BlocksFromMarkdown("Source changed after capture")
			wiki.AssignBlockIDs(&changed, uuid.NewString)
			state, err := wiki.EncodeBlocks(changed)
			if err != nil {
				t.Fatal(err)
			}
			content, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			dbtest.MustExec(
				t,
				ctx,
				pool,
				`UPDATE wiki_canvas_drafts SET content=$2,state=$3,markdown=$4,head_seq=head_seq+1 WHERE page_id=$1`,
				included.PageID,
				content,
				state,
				wiki.BlocksMarkdown(changed),
			)
			reopened, err := presentations.Presentation(ctx, principal, f.ProjectA, saved.ID)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(reopened.Evaluations)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, retained) ||
				wiki.BlockHash(reopened.Documents[key]) != wiki.BlockHash(saved.Documents[key]) {
				t.Fatal("included capture changed after the live source changed")
			}
			markdown, err := presentations.PresentationExport(ctx, principal, f.ProjectA, saved.ID)
			if err != nil || !strings.Contains(markdown, "multiplier: 7") ||
				strings.Contains(markdown, "Source changed after capture") {
				t.Fatalf("export = %q, %v; want retained source selections", markdown, err)
			}
		})
	}
}

func TestPresentationRetainsReactiveTaskFilter(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	pages := NewPageService(store, bus)
	presentations := NewPresentationService(store, bus, nil)
	principal := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET status='done' WHERE id=$1`, f.TaskA)
	definition := nativeScenarioFixture(
		t,
		`{"type":"doc","content":[{"type":"uiScope","attrs":{"name":"review","initial":{"status":"active"}},"content":[{"type":"uiSelect","attrs":{"label":"Status","bind":"status","options":["all","active","done"]}},{"type":"taskCollection","attrs":{"filter":{"op":"state","name":"status"},"display":"table"}}]}]}`,
	)
	page, err := pages.CreatePage(ctx, principal, f.ProjectA, PageCreateInput{
		RequestKey: uuid.New(), Title: "Filtered tasks", Content: definition,
	})
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := presentations.PreviewPresentation(ctx, principal, f.ProjectA, page.PageID, page.Sequence)
	if err != nil || !defaults.CanExport {
		t.Fatalf("default preview = %+v, %v", defaults, err)
	}
	assertRows := func(capture wiki.UICapture, filter string, want int) {
		t.Helper()
		collection := capture.Content.Content[0].Content[1]
		if collection.Type != "taskCollection" || collection.Attrs["filter"] != filter {
			t.Fatalf("captured collection = %+v, want filter %s", collection, filter)
		}
		raw, err := json.Marshal(collection.Attrs["capturedTasks"])
		var rows []OverviewTask
		if err != nil || json.Unmarshal(raw, &rows) != nil || len(rows) != want {
			t.Fatalf("captured rows = %s, %v; want %d", raw, err, want)
		}
		if want > 0 && rows[0].ID != f.TaskA {
			t.Fatalf("captured Task = %s, want %s", rows[0].ID, f.TaskA)
		}
	}
	assertRows(defaults.Evaluations["page"], "active", 0)
	path := defaults.Evaluations["page"].Scopes[0].Path
	selected := PresentationSelections{"page": {path: {"status": "all"}}}
	preview, err := presentations.PreviewScenario(ctx, principal, f.ProjectA, PresentationScenarioInput{
		PageID: page.PageID, Sequence: page.Sequence, Selected: selected,
	})
	if err != nil || !preview.CanExport {
		t.Fatalf("selected preview = %+v, %v", preview, err)
	}
	assertRows(preview.Evaluations["page"], "all", 1)
	saved, err := presentations.CapturePresentation(ctx, principal, f.ProjectA, PresentationInput{
		PageID: page.PageID, Sequence: page.Sequence, Selected: selected,
		Fingerprint: preview.Fingerprint, RequestKey: uuid.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE tasks SET title='Changed after filtered capture',version=version+1 WHERE id=$1`,
		f.TaskA,
	)
	reopened, err := presentations.Presentation(ctx, principal, f.ProjectA, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRows(reopened.Evaluations["page"], "all", 1)
	if !bytes.Equal(saved.Content, reopened.Content) {
		t.Fatal("filtered capture changed after live Task edit")
	}
	markdown, err := presentations.PresentationExport(ctx, principal, f.ProjectA, saved.ID)
	if err != nil || !strings.Contains(markdown, "Task A") ||
		strings.Contains(markdown, "Changed after filtered capture") {
		t.Fatalf("filtered export = %q, %v; want retained Task title", markdown, err)
	}
}
