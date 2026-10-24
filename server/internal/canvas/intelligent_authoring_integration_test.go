//go:build integration

package canvas

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

func TestPageUIAuthoringPolicy(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	enabled := NewPageService(store, bus)
	disabled := NewPageService(store, bus, WithPageUIAuthoringPolicy(wiki.UIAuthoringPolicy{Disabled: true}))
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	source := `<text>{1}</text>`
	input := PageCreateInput{RequestKey: uuid.New(), Title: "Reactive", Source: &source, LanguageVersion: 1}
	page, err := enabled.CreatePage(ctx, p, f.ProjectA, input)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := disabled.CreatePage(ctx, p, f.ProjectA, input)
	if err != nil || retry.PageID != page.PageID {
		t.Fatalf("existing receipt retry = %+v, %v", retry, err)
	}
	input.RequestKey = uuid.New()
	if _, err := disabled.CreatePage(ctx, p, f.ProjectA, input); !errors.Is(err, wiki.ErrUIAuthoringDisabled) {
		t.Fatalf("new reactive page = %v", err)
	}
	before, err := disabled.Document(ctx, p, f.ProjectA, page.VersionID, -1)
	if err != nil || before.CanEdit || before.IntelligentUIAuthoringEnabled || before.SchemaVersion != 2 ||
		len(before.State) == 0 {
		t.Fatalf("read-only saved document = %+v, %v", before, err)
	}
	if _, err := disabled.SyncDocument(
		ctx,
		p,
		f.ProjectA,
		page.VersionID,
		DocumentSyncInput{SchemaVersion: 2, Update: before.State, BaseSequence: before.Sequence},
	); !errors.Is(
		err,
		wiki.ErrUIAuthoringDisabled,
	) {
		t.Fatalf("disabled reactive sync = %v", err)
	}
	after, err := enabled.Document(ctx, p, f.ProjectA, page.VersionID, -1)
	if err != nil || !after.CanEdit || !after.IntelligentUIAuthoringEnabled || before.Sequence != after.Sequence ||
		!bytes.Equal(before.State, after.State) {
		t.Fatal("authoring policy changed the canonical document or prevented re-enabling")
	}
	plain, err := disabled.CreatePage(
		ctx,
		p,
		f.ProjectA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Plain", Markdown: "Keep this prose"},
	)
	if err != nil {
		t.Fatal(err)
	}
	document, err := disabled.Document(ctx, p, f.ProjectA, plain.VersionID, -1)
	if err != nil || !document.CanEdit || document.IntelligentUIAuthoringEnabled {
		t.Fatalf("plain document = %+v, %v", document, err)
	}
	unchangedPlain, err := disabled.SyncDocument(
		ctx,
		p,
		f.ProjectA,
		plain.VersionID,
		DocumentSyncInput{SchemaVersion: 2, Update: document.State, BaseSequence: document.Sequence},
	)
	if err != nil || !unchangedPlain.CanEdit || unchangedPlain.IntelligentUIAuthoringEnabled {
		t.Fatalf("plain document sync capability = %+v, %v", unchangedPlain, err)
	}
	var plainRoot wiki.BlockNode
	if err := json.Unmarshal(document.Content, &plainRoot); err != nil {
		t.Fatal(err)
	}
	original := plainRoot.Content[0]
	edited := original
	edited.Content = []wiki.BlockNode{{Type: "text", Text: "Prose remains editable"}}
	plainUpdate, _, err := wiki.ApplyBlockOperations(document.State, []wiki.BlockOperation{
		{
			Type:         "update",
			BlockID:      original.Attrs["blockId"].(string),
			ExpectedHash: wiki.BlockHash(original),
			Content:      &edited,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err = disabled.SyncDocument(
		ctx,
		p,
		f.ProjectA,
		plain.VersionID,
		DocumentSyncInput{SchemaVersion: 2, Update: plainUpdate, BaseSequence: document.Sequence},
	)
	if err != nil || strings.TrimSpace(document.Markdown) != "Prose remains editable" ||
		document.SchemaVersion != 1 ||
		!document.CanEdit ||
		document.IntelligentUIAuthoringEnabled {
		t.Fatalf("plain text edit while reactive authoring is disabled = %+v, %v", document, err)
	}
	ids, err := disabled.ReserveBlockIDs(ctx, p, f.ProjectA, plain.VersionID, 2)
	if err != nil {
		t.Fatal(err)
	}
	scope := wiki.BlockNode{
		Type:    "uiScope",
		Attrs:   map[string]any{"blockId": ids[0].String(), "name": "scenario", "initial": map[string]any{}},
		Content: []wiki.BlockNode{{Type: "paragraph", Attrs: map[string]any{"blockId": ids[1].String()}}},
	}
	update, _, err := wiki.ApplyBlockOperations(
		document.State,
		[]wiki.BlockOperation{{Type: "insert", Content: &scope}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.SyncDocument(
		ctx,
		p,
		f.ProjectA,
		plain.VersionID,
		DocumentSyncInput{SchemaVersion: 2, Update: update, BaseSequence: document.Sequence},
	); !errors.Is(
		err,
		wiki.ErrUIAuthoringDisabled,
	) {
		t.Fatalf("schema upgrade while disabled = %v", err)
	}
	unchanged, err := disabled.Document(ctx, p, f.ProjectA, plain.VersionID, -1)
	if err != nil || unchanged.Sequence != document.Sequence || !bytes.Equal(unchanged.State, document.State) {
		t.Fatal("rejected schema upgrade changed saved prose")
	}
}
