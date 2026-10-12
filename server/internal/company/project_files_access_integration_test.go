//go:build integration

package company

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func (w projectAccessWorld) upload(t *testing.T, ctx context.Context, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := w.pool.QueryRow(ctx, `INSERT INTO attachments
		(company_id, uploaded_by_user_id, request_key, filename, content_type, size_bytes, status)
		VALUES ($1, $2, gen_random_uuid(), $3, 'text/plain', 4, 'ready') RETURNING id`,
		w.f.CompanyA, w.owner.UserID, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (w projectAccessWorld) replaceFiles(t *testing.T, ctx context.Context, documents ...uuid.UUID) {
	t.Helper()
	current, err := w.projects.Get(ctx, w.owner, w.f.ProjectA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.projects.ReplaceFiles(ctx, w.owner, w.f.ProjectA,
		ProjectFilesInput{DocumentIDs: append([]uuid.UUID{}, documents...)}, current.Revision); err != nil {
		t.Fatal(err)
	}
}

// insertRun adds a Run on the fixture's Task chat that the person authorized,
// optionally a retry of an earlier one.
func (w projectAccessWorld) insertRun(t *testing.T, ctx context.Context, authorizer uuid.UUID, parent *uuid.UUID) uuid.UUID {
	t.Helper()
	var runID uuid.UUID
	if err := w.pool.QueryRow(ctx, `
		INSERT INTO runs (company_id, agent_id, conversation_id, runtime_id, execution_authorized_by_user_id,
		                  status, run_type, parent_run_id, attempt)
		VALUES ($1, $2, $3, $4, $5, 'dispatched', 'task', $6, CASE WHEN $6::uuid IS NULL THEN 1 ELSE 2 END)
		RETURNING id`,
		w.f.CompanyA, w.f.AgentA, w.f.ConversationA, w.f.RuntimeA, authorizer, parent,
	).Scan(&runID); err != nil {
		t.Fatalf("insert Run: %v", err)
	}
	return runID
}

// runDownloads asks whether the Run may download the file. frozen says the
// Run's context lists it among its frozen Project documents.
func (w projectAccessWorld) runDownloads(
	t *testing.T, ctx context.Context, runID, authorizer, file uuid.UUID, frozen bool,
) bool {
	t.Helper()
	_, err := generated.New(w.pool).AuthorizeRunAttachmentDownload(ctx, generated.AuthorizeRunAttachmentDownloadParams{
		AttachmentID: file, CompanyID: w.f.CompanyA, RunID: runID, AuthorizerUserID: authorizer,
		FrozenProjectDocument: frozen,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	return err == nil
}

// Replacing a Project's files while a Run is dispatched (ADR 0017): the Run
// froze the document, and its authorizer still reads the Project, so the Run
// (and a retry of it, which inherits the frozen context) still downloads the
// document now retired. Someone who cannot read the Project cannot, and a Run
// whose context does not list it as a frozen document (one created after the
// document left) cannot either. A person's own download keeps the
// active-file rule.
func TestRunDownloadsFollowTheAuthorizersProjectAccess(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	if _, err := w.projects.SetGrants(ctx, w.owner, w.f.ProjectA, ProjectGrantInput{
		UserID: w.member.UserID, Permissions: []string{"project.read"},
		ExpectedRevision: w.accessRevision(t, ctx, w.f.ProjectA),
	}); err != nil {
		t.Fatal(err)
	}
	doc := w.upload(t, ctx, "brief.txt")
	w.replaceFiles(t, ctx, doc)
	memberRun := w.insertRun(t, ctx, w.member.UserID, nil)
	outsiderRun := w.insertRun(t, ctx, w.admin.UserID, nil)
	if !w.runDownloads(t, ctx, memberRun, w.member.UserID, doc, true) ||
		w.runDownloads(t, ctx, outsiderRun, w.admin.UserID, doc, true) {
		t.Fatal("an active Project document does not follow the authorizer's Project access")
	}

	w.replaceFiles(t, ctx)
	retry := w.insertRun(t, ctx, w.member.UserID, &memberRun)
	later := w.insertRun(t, ctx, w.member.UserID, nil)
	for _, check := range []struct {
		name             string
		run, authorizer  uuid.UUID
		frozen           bool
		wantDownloadable bool
	}{
		{"the dispatched Run", memberRun, w.member.UserID, true, true},
		{"its retry", retry, w.member.UserID, true, true},
		{"a Run created after the document left", later, w.member.UserID, false, false},
		{"the Run naming it outside its frozen documents", memberRun, w.member.UserID, false, false},
		{"an authorizer without project.read", outsiderRun, w.admin.UserID, true, false},
		{"a person who did not authorize the Run", memberRun, w.owner.UserID, true, false},
	} {
		if got := w.runDownloads(t, ctx, check.run, check.authorizer, doc, check.frozen); got != check.wantDownloadable {
			t.Errorf("%s: downloads the retired document = %t, want %t", check.name, got, check.wantDownloadable)
		}
	}
	if _, err := generated.New(w.pool).AuthorizeAttachmentDownload(ctx, generated.AuthorizeAttachmentDownloadParams{
		CompanyID: w.f.CompanyA, AttachmentID: doc, ActorUserID: w.member.UserID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("a person's own download of a retired Project document = %v, want refused", err)
	}
	// Losing the Project closes the retired document to the Run as well.
	if _, err := w.projects.RemoveGrants(ctx, w.owner, w.f.ProjectA, w.member.UserID,
		w.accessRevision(t, ctx, w.f.ProjectA)); err != nil {
		t.Fatal(err)
	}
	if w.runDownloads(t, ctx, memberRun, w.member.UserID, doc, true) {
		t.Error("a Run downloads a Project document after its authorizer lost the Project")
	}
}

// A Company reference document opens to every active member's Run, so it may
// only ever be a file that has no other rule (ADR 0017: managing the Company
// is not reading its content). Registration takes only the registrant's own
// upload that no chat, queue or Project holds; a reference row that names a
// chat file or a Project file (one written before that fence, or a file shared
// after it was registered) follows that file's own rule for the Run and is
// left out of the list every Run freezes.
func TestCompanyReferencesOpenOnlyFilesWithNoOtherRule(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	store, err := db.NewStore(w.pool)
	if err != nil {
		t.Fatal(err)
	}
	references, err := NewReferenceService(store)
	if err != nil {
		t.Fatal(err)
	}
	// postFile puts the owner's upload on a new message of a Run chat.
	postFile := func(conversationID uuid.UUID, runType, name string) uuid.UUID {
		t.Helper()
		file := w.upload(t, ctx, name)
		var message uuid.UUID
		if err := w.pool.QueryRow(ctx, `
			WITH run AS (
				INSERT INTO runs (company_id, agent_id, conversation_id, runtime_id, execution_authorized_by_user_id,
				                  status, run_type)
				VALUES ($1, $4, $2, $5, $3, 'completed', $6)
				RETURNING id
			)
			INSERT INTO conversation_items (company_id, conversation_id, run_id, seq, type, author_kind, author_id, content)
			SELECT $1, $2, run.id, 900 + (SELECT count(*) FROM conversation_items WHERE conversation_id = $2),
				'user_message', 'user', $3, 'File'
			FROM run
			RETURNING id`, w.f.CompanyA, conversationID, w.owner.UserID, w.f.AgentA, w.f.RuntimeA, runType,
		).Scan(&message); err != nil {
			t.Fatal(err)
		}
		dbtest.MustExec(t, ctx, w.pool, `INSERT INTO message_attachments (company_id, message_id, attachment_id, position)
			VALUES ($1, $2, $3, 0)`, w.f.CompanyA, message, file)
		return file
	}
	directFile := postFile(w.f.DirectConversationA, "direct_chat", "owner-private.txt")
	taskChatFile := postFile(w.f.ConversationA, "task", "restricted-task.txt")
	adminRun := w.insertRun(t, ctx, w.admin.UserID, nil)
	for _, file := range []uuid.UUID{directFile, taskChatFile} {
		if _, err := generated.New(w.pool).AuthorizeAttachmentDownload(ctx, generated.AuthorizeAttachmentDownloadParams{
			CompanyID: w.f.CompanyA, AttachmentID: file, ActorUserID: w.admin.UserID,
		}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("the admin opens a file of a chat they do not read: %v", err)
		}
	}

	// Registration: an admin cannot register someone else's chat file, and
	// the uploader cannot register a file already shared in a chat.
	for _, check := range []struct {
		name   string
		person auth.UserPrincipal
		file   uuid.UUID
	}{
		{"an admin, another member's direct chat file", w.admin, directFile},
		{"an admin, a Task chat file", w.admin, taskChatFile},
		{"the uploader, a file already in a chat", w.owner, directFile},
	} {
		if _, err := references.AddReferenceDocuments(ctx, check.person, []uuid.UUID{check.file}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: register = %v, want ErrInvalidInput", check.name, err)
		}
	}
	projectFile := w.upload(t, ctx, "project-brief.txt")
	w.replaceFiles(t, ctx, projectFile)
	if _, err := references.AddReferenceDocuments(ctx, w.owner, []uuid.UUID{projectFile}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("the uploader registers an active Project file = %v, want ErrInvalidInput", err)
	}
	w.replaceFiles(t, ctx)
	if _, err := references.AddReferenceDocuments(ctx, w.owner, []uuid.UUID{projectFile}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("the uploader registers a retired Project file = %v, want ErrInvalidInput", err)
	}
	overview := w.upload(t, ctx, "company-overview.txt")
	if _, err := references.AddReferenceDocuments(ctx, w.owner, []uuid.UUID{overview}); err != nil {
		t.Fatalf("the uploader registers their own unshared upload: %v", err)
	}
	if !w.runDownloads(t, ctx, adminRun, w.admin.UserID, overview, false) {
		t.Error("a Company reference document is closed to an active member's Run")
	}

	// Reference rows that name files with another rule, as a row written
	// before the registration fence would.
	for _, file := range []uuid.UUID{directFile, taskChatFile, projectFile} {
		dbtest.MustExec(t, ctx, w.pool, `INSERT INTO company_reference_documents (company_id, attachment_id)
			VALUES ($1, $2)`, w.f.CompanyA, file)
	}
	for name, file := range map[string]uuid.UUID{
		"another member's direct chat file": directFile,
		"a Task chat file":                  taskChatFile,
		"a retired Project file":            projectFile,
	} {
		if w.runDownloads(t, ctx, adminRun, w.admin.UserID, file, false) {
			t.Errorf("the admin's Run downloads %s through the reference rule", name)
		}
	}
	frozen, err := generated.New(w.pool).ListCompanyReferenceDocuments(ctx, w.f.CompanyA)
	if err != nil {
		t.Fatal(err)
	}
	if len(frozen) != 1 || frozen[0].ID != overview {
		t.Errorf("Company references every Run freezes = %+v, want only the unshared upload", frozen)
	}
	// A registered reference later made a Project file follows the Project.
	w.replaceFiles(t, ctx, overview)
	if w.runDownloads(t, ctx, adminRun, w.admin.UserID, overview, false) {
		t.Error("a Company reference document that is an active Project file opens to a non-reader's Run")
	}
}

// A file on a chat message changes who sees it there when it joins or leaves
// a Project, so a files replace tells that chat to refetch the message: one
// opaque message.updated for each such message, naming no file.
func TestProjectFilesReplaceTellsChatsToRefetchTheirMessages(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	channel := dbtest.SeedDiscussionChannel(t, ctx, w.pool, w.f.CompanyA, w.owner.UserID, nil)
	var message uuid.UUID
	if err := w.pool.QueryRow(ctx, `
		INSERT INTO conversation_items
			(company_id, conversation_id, seq, type, author_kind, author_id, content, body, request_key, send_fingerprint)
		VALUES ($1, $2, 1, 'user_message', 'user', $3, 'Plan', '{"markdown":"Plan"}'::jsonb, gen_random_uuid(),
			decode(repeat('11', 32), 'hex'))
		RETURNING id`, w.f.CompanyA, channel, w.owner.UserID).Scan(&message); err != nil {
		t.Fatal(err)
	}
	shared := w.upload(t, ctx, "shared-plan.txt")
	unshared := w.upload(t, ctx, "unshared.txt")
	dbtest.MustExec(t, ctx, w.pool, `INSERT INTO message_attachments (company_id, message_id, attachment_id, position)
		VALUES ($1, $2, $3, 0)`, w.f.CompanyA, message, shared)

	w.replaceFiles(t, ctx, shared, unshared) // registers the shared file
	w.replaceFiles(t, ctx, shared)           // keeps it: its message is unchanged
	w.replaceFiles(t, ctx, unshared)         // retires it
	updates := w.messages.named(conversation.EventMessageUpdated)
	if len(updates) != 2 {
		t.Fatalf("message.updated frames = %d, want one for the register and one for the retire, none for the keep",
			len(updates))
	}
	for _, update := range updates {
		payload := payloadOf[conversation.MessageUpdated](t, update)
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		want := `{"conversationId":"` + channel.String() + `","messageId":"` + message.String() + `"}`
		if string(encoded) != want || payload.ConversationKind != "channel" || update.ConversationID != channel {
			t.Fatalf("message.updated = %s (kind %q), want %s", encoded, payload.ConversationKind, want)
		}
	}
}

// A queued prompt's files show as they will once the prompt is carried into
// a message (ADR 0017): a plain upload to everyone who reads the chat, an
// active Project file only to the readers of its Projects.
func TestQueuedFilesFollowTheProjectFileRule(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	projectFile := w.upload(t, ctx, "project-brief.txt")
	plain := w.upload(t, ctx, "notes.txt")
	w.replaceFiles(t, ctx, projectFile)
	var item uuid.UUID
	if err := w.pool.QueryRow(ctx, `
		INSERT INTO conversation_run_queue_items
			(company_id, conversation_id, initiated_by_user_id, request_key, content, "position")
		VALUES ($1, $2, $3, gen_random_uuid(), 'Next', 1)
		RETURNING id`, w.f.CompanyA, w.f.ConversationA, w.owner.UserID).Scan(&item); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, w.pool, `
		INSERT INTO conversation_run_queue_attachments (company_id, queue_item_id, attachment_id, "position")
		VALUES ($1, $2, $3, 0), ($1, $2, $4, 1)`, w.f.CompanyA, item, projectFile, plain)
	queued := func(viewer uuid.UUID) []uuid.UUID {
		t.Helper()
		rows, err := generated.New(w.pool).ListRunQueueItemAttachments(ctx, generated.ListRunQueueItemAttachmentsParams{
			QueueItemIds: []uuid.UUID{item}, ViewerUserID: viewer,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]uuid.UUID, len(rows))
		for index, row := range rows {
			ids[index] = row.ID
		}
		return ids
	}
	if got := queued(w.owner.UserID); len(got) != 2 {
		t.Errorf("a reader of the Project sees queued files %v, want both", got)
	}
	if got := queued(w.admin.UserID); len(got) != 1 || got[0] != plain {
		t.Errorf("a non-reader of the Project sees queued files %v, want only the plain upload %s", got, plain)
	}
}
