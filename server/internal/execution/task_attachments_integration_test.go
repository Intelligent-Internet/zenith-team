//go:build integration

package execution

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// A Task Run reads the files a member attached to the Task: the ones already
// on their messages in the Task Conversation and the ones the triggering write
// is about to link. The kernel freezes them from rows, so a caller can neither
// forge the list nor slip in another tenant's, another member's or an
// unfinished file.
func TestEnqueueFreezesTaskAttachmentsIntoRunContext(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)

	base := time.Date(2026, time.September, 25, 9, 0, 0, 0, time.UTC)
	seed := func(companyID, userID uuid.UUID, filename, status string, version int, offset time.Duration) uuid.UUID {
		t.Helper()
		id := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO attachments (
				id, company_id, uploaded_by_user_id, request_key, filename,
				content_type, size_bytes, status, version, created_at, updated_at
			)
			VALUES ($1, $2, $3, $4, $5, 'application/pdf', 42, $6, $7, $8, $8)`,
			id, companyID, userID, dbtest.NewUUID(t), filename, status, version, base.Add(offset))
		return id
	}
	onTaskMessage := seed(fixture.CompanyA, fixture.UserA, "brief.pdf", "ready", 2, 0)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO message_attachments (company_id, message_id, attachment_id, "position")
		VALUES ($1, $2, $3, 0)`, fixture.CompanyA, fixture.MessageA, onTaskMessage)
	triggered := seed(fixture.CompanyA, fixture.UserA, "invoice.pdf", "ready", 2, time.Minute)
	unfinished := seed(fixture.CompanyA, fixture.UserA, "partial.pdf", "pending", 1, 2*time.Minute)
	foreign := seed(fixture.CompanyB, fixture.UserB, "foreign.pdf", "ready", 2, 3*time.Minute)
	// Linking takes only the sender's own uploads, so the kernel does not
	// freeze a file another member of the Company uploaded.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		fixture.CompanyA, fixture.UserB)
	others := seed(fixture.CompanyA, fixture.UserB, "payroll.pdf", "ready", 2, 4*time.Minute)

	frozenIDs := func(t *testing.T, run generated.Run) ([]string, bool) {
		t.Helper()
		var document struct {
			TaskAttachments *[]struct {
				ID       string `json:"id"`
				Filename string `json:"filename"`
			} `json:"taskAttachments"`
		}
		if err := json.Unmarshal(run.Context, &document); err != nil {
			t.Fatalf("decode run context %s: %v", run.Context, err)
		}
		if document.TaskAttachments == nil {
			return nil, false
		}
		ids := make([]string, 0, len(*document.TaskAttachments))
		for _, file := range *document.TaskAttachments {
			ids = append(ids, file.ID)
		}
		return ids, true
	}
	var pending []events.Event

	t.Run("a Task Run freezes the Task's files and the trigger's, oldest first", func(t *testing.T) {
		run, err := EnqueueRunInTx(ctx, queries, &pending, RunTrigger{
			CompanyID: fixture.CompanyA, ConversationID: fixture.ConversationA,
			AgentID: fixture.AgentA, TaskID: util.NullUUIDFromPtr(&fixture.TaskA),
			TriggerPrompt:        "Use the invoice too.",
			TriggerAttachmentIDs: []uuid.UUID{triggered, unfinished, foreign, others},
			Context:              []byte(`{"taskAttachments":[{"id":"forged","filename":"forged.pdf"}]}`),
			Source:               RunSourceMessage, AuthorizedByUserID: fixture.UserA,
		})
		if err != nil {
			t.Fatalf("enqueue Task Run: %v", err)
		}
		got, ok := frozenIDs(t, run)
		want := []string{onTaskMessage.String(), triggered.String()}
		if !ok || !slices.Equal(got, want) {
			t.Fatalf("frozen taskAttachments = %v (present=%v), want %v", got, ok, want)
		}
		dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status = 'completed' WHERE id = $1`, run.ID)
	})

	t.Run("a Run outside a Task freezes no Task files", func(t *testing.T) {
		run, err := EnqueueRunInTx(ctx, queries, &pending, RunTrigger{
			CompanyID: fixture.CompanyA, ConversationID: fixture.DirectConversationA,
			AgentID: fixture.AgentA, TriggerPrompt: "Read this.",
			TriggerAttachmentIDs: []uuid.UUID{triggered},
			Context:              []byte(`{"taskAttachments":[{"id":"forged","filename":"forged.pdf"}]}`),
			Source:               RunSourceMessage, AuthorizedByUserID: fixture.UserA,
		})
		if err != nil {
			t.Fatalf("enqueue direct Run: %v", err)
		}
		if got, ok := frozenIDs(t, run); ok {
			t.Fatalf("direct Run froze taskAttachments %v, want none", got)
		}
	})
}
