//go:build integration

package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// A file a member attaches in a Task's chat is one of the Task's inputs: the
// Run the message starts reads it, and so does every later Run of the Task.
func TestTaskChatFilesReachThisAndLaterTaskRuns(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	service := newTestConversationService(t, store, bus)
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	target := MessageTarget{Kind: TargetTask, ID: fixture.TaskA}
	file := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO attachments (
			id, company_id, uploaded_by_user_id, request_key, filename,
			content_type, size_bytes, status, version
		)
		VALUES ($1, $2, $3, $4, 'ledger.csv', 'text/csv', 5, 'ready', 2)`,
		file, fixture.CompanyA, fixture.UserA, dbtest.NewUUID(t))
	frozen := func(t *testing.T, runID uuid.UUID) []string {
		t.Helper()
		run, err := store.Queries().GetRun(ctx, runID)
		if err != nil {
			t.Fatalf("read Run: %v", err)
		}
		var document struct {
			TaskAttachments []struct {
				ID string `json:"id"`
			} `json:"taskAttachments"`
		}
		if err := json.Unmarshal(run.Context, &document); err != nil {
			t.Fatalf("decode run context: %v", err)
		}
		ids := []string{}
		for _, attached := range document.TaskAttachments {
			ids = append(ids, attached.ID)
		}
		return ids
	}

	first, err := service.Send(ctx, principal, target, SendInput{
		RequestKey: dbtest.NewUUID(t), Content: "Reconcile this ledger.",
		AttachmentIDs: []uuid.UUID{file},
	})
	if err != nil || first.RunID == nil {
		t.Fatalf("send with a file = %+v, %v", first, err)
	}
	if got := frozen(t, *first.RunID); !slices.Equal(got, []string{file.String()}) {
		t.Fatalf("the message's Run froze %v, want the file it carries", got)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status = 'completed' WHERE id = $1`, *first.RunID)

	second, err := service.Send(ctx, principal, target, SendInput{
		RequestKey: dbtest.NewUUID(t), Content: "Now summarize the differences.",
	})
	if err != nil || second.RunID == nil {
		t.Fatalf("send without a file = %+v, %v", second, err)
	}
	if got := frozen(t, *second.RunID); !slices.Equal(got, []string{file.String()}) {
		t.Fatalf("the next Run froze %v, want the Task's earlier file", got)
	}
}

// A colleague's Task message waits for the assignee (ADR 0014), and so do its
// files: the assignee's next Run carries the message into its transcript and
// reads the files with it, before the message is written. Later Runs read
// them from the written message like any other Task input.
func TestQueuedColleagueTaskFilesReachTheAssigneesNextRun(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	// A colleague's queued message nudges the assignee with the Task's next
	// step, which the Task domain answers in production.
	runs := testTaskMessageRuns().(TaskMessageRunPreparerFuncs)
	runs.NextStep = func(context.Context, *generated.Queries, generated.Task) (string, error) {
		return "Reply in its chat to run it.", nil
	}
	service, err := NewConversationService(ServiceOptions{Store: store, Bus: bus, TaskMessageRuns: runs})
	if err != nil {
		t.Fatalf("construct Conversation service: %v", err)
	}
	colleague := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, 'files-colleague@example.com', 'Colleague')`,
		colleague)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		fixture.CompanyA, colleague)
	file := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO attachments (
			id, company_id, uploaded_by_user_id, request_key, filename,
			content_type, size_bytes, status, version
		)
		VALUES ($1, $2, $3, $4, 'brief.pdf', 'application/pdf', 5, 'ready', 2)`,
		file, fixture.CompanyA, colleague, dbtest.NewUUID(t))
	target := MessageTarget{Kind: TargetTask, ID: fixture.TaskA}
	frozen := func(t *testing.T, runID uuid.UUID) []string {
		t.Helper()
		run, err := store.Queries().GetRun(ctx, runID)
		if err != nil {
			t.Fatalf("read Run: %v", err)
		}
		var document struct {
			TaskAttachments []struct {
				ID string `json:"id"`
			} `json:"taskAttachments"`
		}
		if err := json.Unmarshal(run.Context, &document); err != nil {
			t.Fatalf("decode run context: %v", err)
		}
		ids := []string{}
		for _, attached := range document.TaskAttachments {
			ids = append(ids, attached.ID)
		}
		return ids
	}

	// Task chats follow their Project (ADR 0017): a plain Company member who
	// does not read Project A cannot post in it.
	if _, err := service.Send(ctx, auth.UserPrincipal{UserID: colleague}, target, SendInput{
		RequestKey: dbtest.NewUUID(t), Content: "Use this brief.", AttachmentIDs: []uuid.UUID{file},
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-reader's send error = %v; want ErrNotFound", err)
	}
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, colleague, "project.tasks.write")

	// Task A belongs to UserA, so the colleague's message and file wait.
	queued, err := service.Send(ctx, auth.UserPrincipal{UserID: colleague}, target, SendInput{
		RequestKey: dbtest.NewUUID(t), Content: "Use this brief.", AttachmentIDs: []uuid.UUID{file},
	})
	if err != nil || queued.Disposition != SendDispositionQueued || queued.QueueItem == nil || queued.RunID != nil {
		t.Fatalf("colleague's send = %+v, %v; want a queued item", queued, err)
	}

	assignee := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	first, err := service.Send(ctx, assignee, target, SendInput{
		RequestKey: dbtest.NewUUID(t), Content: "Go ahead.",
	})
	if err != nil || first.RunID == nil {
		t.Fatalf("assignee's send = %+v, %v", first, err)
	}
	if got := frozen(t, *first.RunID); !slices.Equal(got, []string{file.String()}) {
		t.Fatalf("the Run carrying the colleague's message froze %v, want its file", got)
	}
	var linked bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM message_attachments
			WHERE message_id = $1 AND attachment_id = $2
		)`, queued.QueueItem.MessageID, file).Scan(&linked); err != nil || !linked {
		t.Fatalf("materialized colleague message links the file = %v (%v)", linked, err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status = 'completed' WHERE id = $1`, *first.RunID)

	second, err := service.Send(ctx, assignee, target, SendInput{
		RequestKey: dbtest.NewUUID(t), Content: "Now shorten it.",
	})
	if err != nil || second.RunID == nil {
		t.Fatalf("assignee's second send = %+v, %v", second, err)
	}
	if got := frozen(t, *second.RunID); !slices.Equal(got, []string{file.String()}) {
		t.Fatalf("the next Run froze %v, want the colleague's file", got)
	}
}
