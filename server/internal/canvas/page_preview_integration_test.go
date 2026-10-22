//go:build integration

package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestPagePreviewStorageBoundsAndExpiry(t *testing.T) {
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
	principal := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	page, err := pages.CreatePage(
		ctx,
		principal,
		f.ProjectA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Preview destination", Markdown: "Saved content"},
	)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='running' WHERE id=$1`, f.RunA)
	q := generated.New(pool)
	input := generated.CreatePageSourcePreviewParams{
		CompanyID:   f.CompanyA,
		ProjectID:   f.ProjectA,
		PageID:      page.PageID,
		RunID:       f.RunA,
		ActorUserID: f.UserA,
		RequestKey:  uuid.New(),
	}
	row, err := q.CreatePageSourcePreview(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	var conversation uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT conversation_id FROM runs WHERE id=$1`, f.RunA).
		Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	if row.ID == uuid.Nil || row.ConversationID != conversation || row.Revision != 0 ||
		!row.ExpiresAt.Time.After(row.CreatedAt.Time) {
		t.Fatalf("created preview = %+v", row)
	}
	update := generated.UpdatePageSourcePreviewParams{
		CompanyID:   f.CompanyA,
		RunID:       f.RunA,
		ID:          row.ID,
		Source:      "Preview only",
		RequestKey:  uuid.New(),
		RequestHash: strings.Repeat("a", 64),
	}
	updated, err := q.UpdatePageSourcePreview(ctx, update)
	if err != nil || updated.Revision != 1 || updated.Source != "Preview only" {
		t.Fatalf("updated = %+v, %v", updated, err)
	}
	if _, err := q.UpdatePageSourcePreview(ctx, update); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale update = %v, want no row", err)
	}
	foreign := generated.GetPageSourcePreviewParams{
		CompanyID: f.CompanyB,
		ProjectID: nullable(&f.ProjectA),
		PageID:    page.PageID,
		ID:        row.ID,
	}
	if _, err := q.GetPageSourcePreview(ctx, foreign); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("foreign read = %v, want no row", err)
	}
	update.ExpectedRevision = 1
	update.Source = ""
	update.Discarded = true
	update.RequestKey = uuid.New()
	if _, err := q.UpdatePageSourcePreview(ctx, update); err != nil {
		t.Fatal(err)
	}
	update.ExpectedRevision = 2
	update.Discarded = false
	update.Source = "Cannot resume"
	if _, err := q.UpdatePageSourcePreview(ctx, update); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("discarded update = %v, want no row", err)
	}
	for range 7 {
		input.RequestKey = uuid.New()
		if _, err := q.CreatePageSourcePreview(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	input.RequestKey = uuid.New()
	if _, err := q.CreatePageSourcePreview(ctx, input); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("ninth generation = %v, want no row", err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE project_page_source_previews SET created_at=now()-interval '2 hours',expires_at=now()-interval '1 hour' WHERE id=$1`,
		row.ID,
	)
	own := foreign
	own.CompanyID = f.CompanyA
	if _, err := q.GetPageSourcePreview(ctx, own); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired read = %v, want no row", err)
	}
	deleted, err := pages.PurgeExpiredPagePreviews(ctx)
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup = %d, %v; want 1", deleted, err)
	}
	if _, err := q.CreatePageSourcePreview(ctx, input); err != nil {
		t.Fatalf("expired slot was not reclaimed: %v", err)
	}
	var markdown string
	if err := pool.QueryRow(ctx, `SELECT markdown FROM wiki_canvas_drafts WHERE company_id=$1 AND page_id=$2`, f.CompanyA, page.PageID).
		Scan(&markdown); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(markdown, "Saved content") || strings.Contains(markdown, "Preview only") {
		t.Fatal("preview storage altered canonical Page")
	}
}

func TestPagePreviewRunAdmissionAndReplay(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	outcomes := map[string]int{}
	pages := NewPageService(
		store,
		bus,
		WithPagePreviewObserver(func(operation, outcome string, duration time.Duration, bytes int) {
			if duration < 0 || bytes < 0 {
				t.Errorf(
					"invalid preview measurement: %s %s %v %d",
					operation,
					outcome,
					duration,
					bytes,
				)
			}
			if operation == "patch" {
				outcomes[outcome]++
			}
		}),
	)
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	page, err := pages.CreatePage(
		ctx,
		p,
		f.ProjectA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Preview target", Markdown: "Canonical content"},
	)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE runs SET status='running',execution_authorized_by_user_id=$2,task_execution_generation=(SELECT execution_generation FROM tasks WHERE id=$3) WHERE id=$1`,
		f.RunA,
		f.UserA,
		f.TaskA,
	)
	ref := PagePreviewRef{RunID: f.RunA, PageID: page.PageID}
	var notified []int64
	if err := bus.Subscribe(EventPagePreviewChanged, func(ctx context.Context, event events.Event) error {
		frame := event.Payload.(PagePreviewChanged)
		var savedRevision int64
		if err := pool.QueryRow(ctx, `SELECT revision FROM project_page_source_previews WHERE id=$1`, frame.GenerationID).
			Scan(&savedRevision); err != nil {
			t.Errorf("preview notification preceded commit: %v", err)
			return err
		}
		if savedRevision != frame.Revision {
			t.Errorf("notified revision %d, stored %d", frame.Revision, savedRevision)
		}
		notified = append(notified, frame.Revision)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	input := PagePreviewCreateInput{RequestKey: uuid.New(), ProtocolVersion: 1, LanguageVersion: 1}
	generation, err := pages.CreatePagePreviewForRun(ctx, p, ref, input)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := pages.CreatePagePreviewForRun(ctx, p, ref, input)
	if err != nil || retry.ID != generation.ID {
		t.Fatalf("create retry = %+v, %v", retry, err)
	}
	ref.GenerationID = generation.ID
	patch := wiki.SourcePreviewPatch{
		RequestKey:      uuid.New(),
		ProtocolVersion: 1,
		LanguageVersion: 1,
		Text:            "# Partial response\n\n<box",
	}
	updated, err := pages.PatchPagePreviewForRun(ctx, p, ref, patch)
	if err != nil || updated.Revision != 1 || updated.Preview.Complete || updated.Preview.Content == nil {
		t.Fatalf("partial preview = %+v, %v", updated, err)
	}
	retry, err = pages.PatchPagePreviewForRun(ctx, p, ref, patch)
	if err != nil || retry.Revision != 1 || !retry.ExpiresAt.Equal(updated.ExpiresAt) {
		t.Fatalf("patch retry = %+v, %v", retry, err)
	}
	read, err := pages.ReadPagePreviewForRun(ctx, p, ref)
	if err != nil || read.Source != updated.Source || read.Revision != 1 {
		t.Fatalf("reconnect = %+v, %v", read, err)
	}
	patch.RequestKey = uuid.New()
	if _, err := pages.PatchPagePreviewForRun(ctx, p, ref, patch); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale patch = %v", err)
	}
	foreign := p
	foreign.CompanyID = f.CompanyB
	if _, err := pages.ReadPagePreviewForRun(ctx, foreign, ref); err == nil {
		t.Fatal("foreign Company read accepted")
	}
	// Reassignment/generation changes revoke even a previously admitted stream.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE tasks SET execution_generation=execution_generation+1 WHERE id=$1`,
		f.TaskA,
	)
	if _, err := pages.ReadPagePreviewForRun(ctx, p, ref); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale Run read = %v", err)
	}
	if _, err := pages.PatchPagePreviewForRun(ctx, p, ref, patch); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale Run write = %v", err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE runs SET task_execution_generation=(SELECT execution_generation FROM tasks WHERE id=$2) WHERE id=$1`,
		f.RunA,
		f.TaskA,
	)
	discard := wiki.SourcePreviewPatch{
		RequestKey:       uuid.New(),
		ProtocolVersion:  1,
		LanguageVersion:  1,
		ExpectedRevision: 1,
		Discard:          true,
	}
	discarded, err := pages.PatchPagePreviewForRun(ctx, p, ref, discard)
	if err != nil || !discarded.Discarded || discarded.Source != "" || discarded.Preview.Content != nil {
		t.Fatalf("discard = %+v, %v", discarded, err)
	}
	if _, err := pages.PatchPagePreviewForRun(ctx, p, ref, discard); err != nil {
		t.Fatalf("discard retry: %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, f.RunA)
	if _, err := pages.ReadPagePreviewForRun(ctx, p, ref); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ended Run read = %v", err)
	}
	for outcome, count := range map[string]int{"accepted": 1, "replayed": 2, "discarded": 1, "conflict": 1, "denied": 1} {
		if outcomes[outcome] != count {
			t.Errorf("patch outcome %s = %d, want %d", outcome, outcomes[outcome], count)
		}
	}
	if len(outcomes) != 5 {
		t.Errorf("unexpected patch outcomes: %v", outcomes)
	}
	var markdown string
	if err := pool.QueryRow(ctx, `SELECT markdown FROM wiki_canvas_drafts WHERE company_id=$1 AND page_id=$2`, f.CompanyA, page.PageID).
		Scan(&markdown); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(markdown, "Canonical content") || strings.Contains(markdown, "Partial response") {
		t.Fatal("stream mutated canonical Page")
	}
	if len(notified) != 3 || notified[0] != 0 || notified[1] != 1 || notified[2] != 2 {
		t.Fatalf("committed preview hints = %v; want [0 1 2] with no retry or rejected-write hints", notified)
	}
}

func TestPagePreviewMemberReadsRequireOriginConversation(t *testing.T) {
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
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	page, err := pages.CreatePage(
		ctx,
		p,
		f.ProjectA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Shared destination", Markdown: "Shared"},
	)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO project_grants(company_id,project_id,user_id,permission) VALUES($1,$2,$3,'project.read')`,
		f.CompanyA,
		f.ProjectA,
		f.UserB,
	)
	other := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: auth.CompanyRoleMember}
	if _, err := pages.Page(ctx, other, f.ProjectA, page.PageID); err != nil {
		t.Fatalf("second member must read destination Page: %v", err)
	}
	// Seed a private originating Run so this exercises reader policy separately
	// from the Agent admission tests above.
	var run uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO runs(company_id,agent_id,conversation_id,runtime_id,status,run_type,revision) VALUES($1,$2,$3,$4,'running','direct_chat',1) RETURNING id`, f.CompanyA, f.AgentA, f.DirectConversationA, f.RuntimeA).
		Scan(&run); err != nil {
		t.Fatal(err)
	}
	q := generated.New(pool)
	row, err := q.CreatePageSourcePreview(
		ctx,
		generated.CreatePageSourcePreviewParams{
			CompanyID:   f.CompanyA,
			ProjectID:   f.ProjectA,
			PageID:      page.PageID,
			RunID:       run,
			ActorUserID: f.UserA,
			RequestKey:  uuid.New(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ref := PagePreviewMemberRef{ProjectID: f.ProjectA, PageID: page.PageID, GenerationID: row.ID}
	hint, err := pagePreviewChangeEvent(ctx, q, row)
	if err != nil {
		t.Fatal(err)
	}
	frame := hint.Payload.(PagePreviewChanged)
	if frame.Audience.CompanyWide || len(frame.Audience.UserIDs) != 1 || frame.Audience.UserIDs[0] != f.UserA {
		t.Fatalf("private preview audience = %+v", frame.Audience)
	}
	if hint.Actor != events.RunActor(f.AgentA.String(), run.String(), f.UserA.String()) {
		t.Fatalf("preview actor = %+v", hint.Actor)
	}
	wire, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"source", "content", "Audience", "userIds", f.UserA.String()} {
		if strings.Contains(string(wire), private) {
			t.Fatalf("preview hint exposes %s: %s", private, wire)
		}
	}
	input := PagePreviewListInput{ProjectID: f.ProjectA, PageID: page.PageID, RunID: run}
	if _, err := pages.ReadPagePreview(ctx, p, ref); err != nil {
		t.Fatal(err)
	}
	own, err := pages.ListPagePreviews(ctx, p, input)
	if err != nil || len(own) != 1 || own[0].ID != row.ID {
		t.Fatalf("own list = %+v, %v", own, err)
	}
	if _, err := pages.ReadPagePreview(ctx, other, ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private preview read = %v", err)
	}
	hidden, err := pages.ListPagePreviews(ctx, other, input)
	if err != nil || len(hidden) != 0 {
		t.Fatalf("private preview list = %+v, %v", hidden, err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, run)
	if _, err := pages.ReadPagePreview(ctx, p, ref); err != nil {
		t.Fatalf("ended Run preview should remain readable until expiry: %v", err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE project_page_source_previews SET created_at=now()-interval '2 hours',expires_at=now()-interval '1 hour' WHERE id=$1`,
		row.ID,
	)
	if _, err := pages.ReadPagePreview(ctx, p, ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired member read = %v", err)
	}
}
