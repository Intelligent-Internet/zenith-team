//go:build integration

package execution

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	internaldb "boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	db "boardly.dev/boardly/server/pkg/db/generated"
)

// A Task Run answers into its Task chat, which only the Task's Project
// readers see, so it reads files only inside that Project (ADR 0017). A file
// of another Project that reaches the chat (on a message, as a file a create
// is about to link, or on a queued prompt) is neither frozen into the Run nor
// downloadable by it, even when the Run's authorizer reads that Project too.
func TestTaskRunFilesStayInsideTheTasksProject(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := internaldb.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	company, owner := f.CompanyA, f.UserA
	queries := db.New(pool)

	var secret uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (company_id, key, title) VALUES ($1, 'secret', 'Secret')
		RETURNING id`, company).Scan(&secret); err != nil {
		t.Fatal(err)
	}
	// The authorizer reads both Projects, so only the Run's audience can
	// keep the secret file out.
	dbtest.GrantProject(t, ctx, pool, company, secret, owner, "project.read")
	dbtest.GrantProject(t, ctx, pool, company, f.ProjectA, owner, "project.read")
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET execution_authorized_by_user_id = $2 WHERE id = $1`, f.RunA, owner)

	upload := func(name string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO attachments
			(company_id, uploaded_by_user_id, request_key, filename, content_type, size_bytes, status)
			VALUES ($1, $2, gen_random_uuid(), $3, 'text/plain', 4, 'ready') RETURNING id`,
			company, owner, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	holdInProject := func(project, file uuid.UUID) {
		t.Helper()
		dbtest.MustExec(t, ctx, pool, `INSERT INTO project_files (company_id, project_id, attachment_id, file_type)
			VALUES ($1, $2, $3, 'document')`, company, project, file)
	}
	position := 0
	linkInTaskChat := func(file uuid.UUID) {
		t.Helper()
		dbtest.MustExec(t, ctx, pool, `INSERT INTO message_attachments (company_id, message_id, attachment_id, position)
			VALUES ($1, $2, $3, $4)`, company, f.MessageA, file, position)
		position++
	}

	plain := upload("plain.txt")
	linkInTaskChat(plain)
	shared := upload("shared.txt") // a file of both Projects
	holdInProject(f.ProjectA, shared)
	holdInProject(secret, shared)
	linkInTaskChat(shared)
	secretOnMessage := upload("secret-plan.txt")
	holdInProject(secret, secretOnMessage)
	linkInTaskChat(secretOnMessage)
	plainTrigger := upload("trigger.txt")
	secretTrigger := upload("secret-trigger.txt")
	holdInProject(secret, secretTrigger)
	secretQueued := upload("secret-queued.txt")
	holdInProject(secret, secretQueued)
	var queueItem uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO conversation_run_queue_items
		(company_id, conversation_id, initiated_by_user_id, request_key, content, "position")
		VALUES ($1, $2, $3, gen_random_uuid(), 'queued', 1) RETURNING id`,
		company, f.ConversationA, owner).Scan(&queueItem); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `INSERT INTO conversation_run_queue_attachments
		(company_id, queue_item_id, attachment_id, "position") VALUES ($1, $2, $3, 0)`,
		company, queueItem, secretQueued)

	rows, err := queries.ListTaskRunInputAttachments(ctx, db.ListTaskRunInputAttachmentsParams{
		TaskID: f.TaskA, CompanyID: company, AuthorizedByUserID: owner,
		TriggerAttachmentIds: []uuid.UUID{plainTrigger, secretTrigger},
		CarriedQueueItemIds:  []uuid.UUID{queueItem}, RowLimit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		frozen = append(frozen, row.ID)
	}
	for _, want := range []uuid.UUID{plain, shared, plainTrigger} {
		if !slices.Contains(frozen, want) {
			t.Errorf("frozen Task files %v miss %v", frozen, want)
		}
	}
	for _, refused := range []uuid.UUID{secretOnMessage, secretTrigger, secretQueued} {
		if slices.Contains(frozen, refused) {
			t.Errorf("frozen Task files %v include another Project's file %v", frozen, refused)
		}
	}

	download := func(file uuid.UUID, projectDocument bool) error {
		t.Helper()
		_, err := queries.AuthorizeRunAttachmentDownload(ctx, db.AuthorizeRunAttachmentDownloadParams{
			CompanyID: company, AttachmentID: file, RunID: f.RunA, AuthorizerUserID: owner,
			FrozenProjectDocument: projectDocument,
		})
		return err
	}
	for _, allowed := range []uuid.UUID{plain, shared} {
		if err := download(allowed, false); err != nil {
			t.Errorf("Task Run download of %v = %v, want allowed", allowed, err)
		}
	}
	for _, refused := range []uuid.UUID{secretOnMessage, secretTrigger, secretQueued} {
		for _, projectDocument := range []bool{false, true} {
			if err := download(refused, projectDocument); !errors.Is(err, pgx.ErrNoRows) {
				t.Errorf("Task Run download of another Project's file %v (document %v) = %v, want no rows",
					refused, projectDocument, err)
			}
		}
	}
	// A retired file of the Task's own Project stays open to a Run that froze
	// it as a Project document, and only to such a Run.
	dbtest.MustExec(t, ctx, pool, `UPDATE project_files SET removed_at = now()
		WHERE company_id = $1 AND attachment_id = $2`, company, shared)
	if err := download(shared, true); err != nil {
		t.Errorf("Task Run download of its own retired Project document = %v, want allowed", err)
	}
}
