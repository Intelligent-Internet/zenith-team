//go:build integration

package canvas

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/reearth/ygo/crdt"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestPageCommentsSurviveVersionsAndRemainSeparateFromReview(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	service := newCanvasServices(store, bus, nil)
	owner := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	page, err := service.CreatePage(
		ctx,
		owner,
		f.ProjectA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Campaign", Markdown: "A finding to discuss."},
	)
	if err != nil {
		t.Fatal(err)
	}
	command := Command{
		RequestKey: uuid.New(),
		Type:       "comment",
		PageID:     &page.PageID,
		Body:       "Can we explain this finding?",
		Anchor:     json.RawMessage(`{"type":"textQuote","exact":"A finding"}`),
	}
	created, err := service.Mutate(ctx, owner, f.ProjectA, command)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.Mutate(ctx, owner, f.ProjectA, command)
	if err != nil || replay != created {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, created)
	}
	snapshot, err := service.Snapshot(ctx, owner, f.ProjectA, 0)
	if err != nil {
		t.Fatal(err)
	}
	var revision int64
	for _, artifact := range snapshot.Artifacts {
		if artifact.ID == page.ArtifactID {
			revision = artifact.Revision
		}
	}
	if _, err = service.Mutate(
		ctx,
		owner,
		f.ProjectA,
		Command{
			RequestKey:       uuid.New(),
			Type:             "checkpoint",
			VersionID:        &page.VersionID,
			ArtifactID:       &page.ArtifactID,
			WikiPageID:       &page.PageID,
			ExpectedRevision: revision,
		},
	); err != nil {
		t.Fatal(err)
	}
	current, err := service.Page(ctx, owner, f.ProjectA, page.PageID)
	if err != nil || current.VersionID == page.VersionID {
		t.Fatalf("checkpoint did not advance version: %s, %v", current.VersionID, err)
	}
	threads, err := service.PageThreads(ctx, owner, f.ProjectA, page.PageID)
	if err != nil || len(threads) != 1 || threads[0].ID != created.ID {
		t.Fatalf("Page threads after checkpoint = %+v, %v", threads, err)
	}
	exact, err := service.Threads(ctx, owner, f.ProjectA, page.VersionID)
	if err != nil || len(exact) != 0 {
		t.Fatalf("snapshot threads = %+v, %v; want none", exact, err)
	}
	_, err = service.Mutate(
		ctx,
		owner,
		f.ProjectA,
		Command{RequestKey: uuid.New(), Type: "reply", ThreadID: &created.ID, Body: "Here is the context."},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Mutate(
		ctx,
		owner,
		f.ProjectA,
		Command{
			RequestKey:       uuid.New(),
			Type:             "resolve",
			ThreadID:         &created.ID,
			ExpectedRevision: threads[0].Revision,
			Action:           "resolve",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	threads, err = service.PageThreads(ctx, owner, f.ProjectA, page.PageID)
	if err != nil || len(threads) != 1 || threads[0].Status != "resolved" || len(threads[0].Comments) != 2 {
		t.Fatalf("resolved discussion = %+v, %v", threads, err)
	}
	_, err = service.Mutate(
		ctx,
		owner,
		f.ProjectA,
		Command{
			RequestKey:       uuid.New(),
			Type:             "resolve",
			ThreadID:         &created.ID,
			ExpectedRevision: threads[0].Revision,
			Action:           "reopen",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	// Neither a foreign Project nor a revoked member can read or reply.
	if _, err = service.PageThreads(ctx, owner, f.ProjectB, page.PageID); err == nil {
		t.Fatal("cross-Project Page read succeeded")
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		f.CompanyA,
		f.UserA,
	)
	if _, err = service.PageThreads(ctx, owner, f.ProjectA, page.PageID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked read = %v", err)
	}
	if _, err = service.Mutate(
		ctx,
		owner,
		f.ProjectA,
		Command{RequestKey: uuid.New(), Type: "reply", ThreadID: &created.ID, Body: "Revoked"},
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("revoked reply = %v", err)
	}
}

func TestPageCommentRetainedWithoutSelectedText(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	service := newCanvasServices(store, bus, nil)
	owner := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	page, err := service.CreatePage(
		ctx,
		owner,
		f.ProjectA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Notes", Markdown: "Hello world"},
	)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Mutate(
		ctx,
		owner,
		f.ProjectA,
		Command{
			RequestKey: uuid.New(),
			Type:       "comment",
			PageID:     &page.PageID,
			Body:       "Discuss this",
			Anchor:     json.RawMessage(`{"type":"textQuote","exact":"world"}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Mutate(
		ctx,
		owner,
		f.ProjectA,
		Command{RequestKey: uuid.New(), Type: "reply", ThreadID: &created.ID, Body: "Reply"},
	)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := service.Document(ctx, owner, f.ProjectA, page.VersionID, -1)
	if err != nil {
		t.Fatal(err)
	}
	doc := crdt.New()
	defer doc.Destroy()
	if err := crdt.ApplyUpdateV1(doc, saved.State, nil); err != nil {
		t.Fatal(err)
	}
	element, ok := doc.GetXmlFragment("content").Children()[0].(*crdt.YXmlElement)
	if !ok {
		t.Fatal("missing paragraph")
	}
	text, ok := element.Children()[0].(*crdt.YXmlText)
	if !ok {
		t.Fatal("missing text")
	}
	for _, count := range []int{3, 2} {
		doc.Transact(func(tx *crdt.Transaction) { text.Delete(tx, 6, count) })
		saved, err = service.SyncDocument(
			ctx,
			owner,
			f.ProjectA,
			page.VersionID,
			DocumentSyncInput{Update: doc.EncodeStateAsUpdate(), BaseSequence: saved.Sequence},
		)
		if err != nil {
			t.Fatal(err)
		}
		threads, err := service.PageThreads(ctx, owner, f.ProjectA, page.PageID)
		if err != nil {
			t.Fatal(err)
		}
		if count == 3 {
			if len(threads) != 1 || len(threads[0].Comments) != 2 {
				t.Fatalf("partial edit lost discussion: %+v", threads)
			}
			var anchor struct {
				Exact string `json:"exact"`
			}
			if err := json.Unmarshal(threads[0].Anchor, &anchor); err != nil {
				t.Fatal(err)
			}
			if anchor.Exact != "ld" {
				t.Fatalf("quote = %q, want ld", anchor.Exact)
			}
		} else if len(threads) != 1 || len(threads[0].Comments) != 2 ||
			string(threads[0].Anchor) != `{"type": "version"}` {
			t.Fatalf("deleted selection threads = %+v, want retained page-level discussion", threads)
		}
	}
	var remaining int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM review_comments WHERE thread_id=$1", created.ID).
		Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Fatalf("comments = %d, want 2", remaining)
	}
}
