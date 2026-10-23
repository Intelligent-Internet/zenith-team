//go:build integration

package canvas

import (
	"encoding/json"
	"errors"
	"testing"
	"unicode/utf16"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestDocumentWritingRetryReviewAndUndo(t *testing.T) {
	for _, test := range []struct {
		name, response, want string
	}{
		{"Markdown", "# Generated heading\n\nGenerated body", "# Generated heading\n\nGenerated body\n\nUntouched\n\n"},
		{"Intelligent UI", `<button>Try it</button>`, "<button>Try it</button>\nUntouched\n\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool, _ := dbtest.StartPostgres(t)
			ctx := t.Context()
			if err := db.MigrateUp(ctx, pool); err != nil {
				t.Fatal(err)
			}
			f := dbtest.SeedFixture(t, ctx, pool)
			store, err := db.NewStore(pool)
			if err != nil {
				t.Fatal(err)
			}
			bus, err := events.NewBus(events.BusOptions{})
			if err != nil {
				t.Fatal(err)
			}
			s := NewPageService(store, bus)
			p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
			page, err := s.CreatePage(ctx, p, f.ProjectA, PageCreateInput{
				RequestKey: uuid.New(), Title: "Writing", Markdown: "Original\n\nUntouched",
			})
			if err != nil {
				t.Fatal(err)
			}
			q := store.Queries()
			thread, err := q.CreateReviewThread(ctx, generated.CreateReviewThreadParams{
				CompanyID: f.CompanyA, ProjectID: nullable(&f.ProjectA), PageID: nullable(&page.PageID),
				CreatedByUserID: f.UserA,
				Anchor:          []byte(`{"type":"textQuote","exact":"Original"}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			conv, err := q.EnsureDocumentAgentConversation(ctx, generated.EnsureDocumentAgentConversationParams{
				CompanyID: f.CompanyA, ThreadID: thread.ID, AgentID: f.AgentA, ActorUserID: f.UserA,
			})
			if err != nil {
				t.Fatal(err)
			}
			seed := dbtest.RunSeed{
				CompanyID:      f.CompanyA,
				AgentID:        f.AgentA,
				RuntimeID:      f.RuntimeA,
				ConversationID: conv.ID,
				AuthorizerID:   f.UserA,
			}
			originalRun := dbtest.SeedRun(t, ctx, pool, seed)
			requestKey := uuid.New()
			var before wiki.BlockNode
			err = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
				draft, err := wiki.BlockDraftInTx(ctx, q, f.CompanyA, page.PageID)
				if err != nil {
					return err
				}
				if err = json.Unmarshal(draft.Content, &before); err != nil {
					return err
				}
				block := before.Content[0]
				text, _ := writingBlockText(block)
				target := DocumentWritingTarget{
					BlockID: block.Attrs["blockId"].(string),
					Text:    text,
					To:      len(utf16.Encode([]rune(text))),
				}
				document, err := q.GetDocumentAgentContext(
					ctx,
					generated.GetDocumentAgentContextParams{CompanyID: f.CompanyA, ConversationID: conv.ID},
				)
				if err != nil {
					return err
				}
				_, snapshot, err := DocumentAgentPromptInTx(
					ctx,
					q,
					DocumentAgentInput{Principal: p, WritingTarget: &target, Body: "Write a heading"},
					document,
				)
				if err != nil {
					return err
				}
				encoded, err := json.Marshal(snapshot.Target)
				if err != nil {
					return err
				}
				return q.SaveDocumentWritingRequest(ctx, generated.SaveDocumentWritingRequestParams{
					CompanyID: f.CompanyA, RunID: originalRun, PageID: page.PageID, RequestKey: requestKey,
					Target: encoded, BeforeContent: snapshot.Content, Sequence: snapshot.Sequence,
				})
			})
			if err != nil {
				t.Fatal(err)
			}
			dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='failed' WHERE id=$1`, originalRun)
			retryID := dbtest.SeedRun(t, ctx, pool, seed)
			dbtest.MustExec(
				t,
				ctx,
				pool,
				`UPDATE runs SET parent_run_id=$2,status='completed' WHERE id=$1`,
				retryID,
				originalRun,
			)
			retry, err := q.GetRun(ctx, retryID)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				err = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
					var pending []events.Event
					return CompleteDocumentAgentInTx(
						ctx,
						q,
						&pending,
						DocumentAgentCompletion{Run: retry, Body: test.response},
					)
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			var count int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM project_page_changes WHERE page_id=$1`, page.PageID).
				Scan(&count); err != nil ||
				count != 1 {
				t.Fatalf("proposal count = %d, %v; want 1", count, err)
			}
			receipt, err := q.GetProjectPageChangeReceipt(ctx, generated.GetProjectPageChangeReceiptParams{
				CompanyID: f.CompanyA, ProjectID: f.ProjectA, ActorUserID: f.UserA, RequestKey: requestKey,
			})
			if err != nil {
				t.Fatal(err)
			}
			if receipt.RunID.UUID != retryID {
				t.Fatalf("proposal Run = %s, want %s", receipt.RunID.UUID, retryID)
			}
			unchanged, err := s.Document(ctx, p, f.ProjectA, page.VersionID, -1)
			if err != nil {
				t.Fatal(err)
			}
			if unchanged.Markdown != wiki.BlocksMarkdown(before) {
				t.Fatalf("staging changed live document: %q", unchanged.Markdown)
			}
			for _, action := range []string{"apply", "undo"} {
				detail, err := s.PageChangeDetail(ctx, p, f.ProjectA, page.PageID, receipt.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.ReviewPageChange(ctx, p, f.ProjectA, page.PageID, receipt.ID, PageChangeReviewInput{
					RequestKey: uuid.New(), Action: action, Fingerprint: detail.Fingerprint,
				}); err != nil {
					t.Fatalf("%s proposal: %v", action, err)
				}
				document, err := s.Document(ctx, p, f.ProjectA, page.VersionID, -1)
				if err != nil {
					t.Fatal(err)
				}
				want := test.want
				if action == "undo" {
					want = wiki.BlocksMarkdown(before)
				}
				if document.Markdown != want {
					t.Fatalf("%s Markdown = %q, want %q", action, document.Markdown, want)
				}
				if _, err := q.GetRun(ctx, retryID); err != nil {
					t.Fatalf("%s lost the suggestion's Run: %v", action, err)
				}
				if _, err := q.GetDocumentWritingRequest(ctx, generated.GetDocumentWritingRequestParams{
					CompanyID: f.CompanyA, RunID: retryID,
				}); err != nil {
					t.Fatalf("%s lost the captured writing request: %v", action, err)
				}
				threads, err := s.PageThreads(ctx, p, f.ProjectA, page.PageID)
				if err != nil {
					t.Fatal(err)
				}
				if len(threads) != 1 || threads[0].ID != thread.ID || len(threads[0].Comments) != 1 {
					t.Fatalf("%s threads = %+v, want retained discussion and one Agent reply", action, threads)
				}
				if string(threads[0].Anchor) != `{"type": "version"}` {
					t.Fatalf("%s anchor = %s, want page-level discussion", action, threads[0].Anchor)
				}
			}
			lookup := generated.GetDocumentAgentRunParams{CompanyID: f.CompanyA, RunID: retryID, ActorUserID: f.UserA}
			if _, err = q.GetDocumentAgentRun(ctx, lookup); err != nil {
				t.Fatalf("read document Run with Project access: %v", err)
			}
			dbtest.MustExec(
				t,
				ctx,
				pool,
				`DELETE FROM project_grants WHERE company_id=$1 AND project_id=$2 AND user_id=$3`,
				f.CompanyA,
				f.ProjectA,
				f.UserA,
			)
			if _, err = q.GetDocumentAgentRun(ctx, lookup); err != nil {
				t.Fatalf("revocation erased immutable document Run scope: %v", err)
			}
			if err = checkDocumentAgentPage(ctx, q, p, retryID, uuid.New()); !errors.Is(err, ErrForbidden) {
				t.Fatalf("revoked Run escaped its Page scope = %v", err)
			}
			err = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
				_, _, err := admitPageRun(ctx, q, p, retryID, true)
				return err
			})
			if !errors.Is(err, auth.ErrProjectPermissionRequired) {
				t.Fatalf("revoked document Run admission = %v, want permission required", err)
			}
		})
	}
}
