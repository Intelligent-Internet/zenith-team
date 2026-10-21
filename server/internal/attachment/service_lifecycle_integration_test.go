//go:build integration

package attachment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestResolvedDownloadRequiresOutputAuthorization(t *testing.T) {
	pool, store := sharedAttachmentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, objects, _ := newAttachmentCleanupStack(t, store)
	attachmentID := dbtest.NewUUID(t)
	assetID := dbtest.NewUUID(t)
	assetVersionID := dbtest.NewUUID(t)
	outputID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO attachments (id, company_id, uploaded_by_user_id, request_key,
			filename, content_type, size_bytes, status)
		VALUES ($1, $2, $3, $4, 'private.txt', 'text/plain', 4, 'ready')`,
		attachmentID, fixture.CompanyA, fixture.UserA, dbtest.NewUUID(t))
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO assets (id, company_id, title, media_class, created_by_user_id)
		VALUES ($1, $2, 'Private file', 'file', $3)`, assetID, fixture.CompanyA, fixture.UserA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO asset_versions (id, company_id, asset_id, version_number,
			attachment_id, source_run_id, filename, content_type, size_bytes, integrity_status)
		VALUES ($1, $2, $3, 1, $4, $5, 'private.txt', 'text/plain', 4, 'verified')`,
		assetVersionID, fixture.CompanyA, assetID, attachmentID, fixture.RunA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO outputs (id, company_id, run_id, title, summary,
			presentation_fingerprint, owner_user_id, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1, $2, $3, 'Private file', 'Private', $4, $5, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$2 AND origin.id=$3`,
		outputID, fixture.CompanyA, fixture.RunA,
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", fixture.UserA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO output_items (company_id, output_id, ordinal, role, label, asset_version_id)
		VALUES ($1, $2, 0, 'primary', 'Private file', $3)`,
		fixture.CompanyA, outputID, assetVersionID)
	if _, err := service.DownloadResolved(ctx, ResolvedDownloadInput{
		ID: attachmentID, CompanyID: fixture.CompanyA,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unresolved Output download = %v, want ErrNotFound", err)
	}
	if _, err := service.ShareResolved(ctx, ResolvedShareInput{
		ID: attachmentID, CompanyID: fixture.CompanyA,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Output share = %v, want ErrNotFound", err)
	}
	input := ResolvedDownloadInput{
		ID: attachmentID, CompanyID: fixture.CompanyA, RequestedByUserID: fixture.UserA,
	}
	assertDownload := func(want error) {
		t.Helper()
		objects.openBody = io.NopCloser(strings.NewReader("file"))
		got, err := service.DownloadResolved(ctx, input)
		if !errors.Is(err, want) {
			t.Fatalf("resolved download for %s = %v, want %v", input.RequestedByUserID, err, want)
		}
		if err == nil {
			defer got.Body.Close()
			body, err := io.ReadAll(got.Body)
			if err != nil || string(body) != "file" {
				t.Fatalf("download body = %q, %v, want file", body, err)
			}
		}
	}
	assertDownload(nil)
	dbtest.MustExec(t, ctx, pool, `INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`, fixture.CompanyA, fixture.UserB)
	input.RequestedByUserID = fixture.UserB
	assertDownload(ErrNotFound)
	dbtest.MustExec(t, ctx, pool, `INSERT INTO output_grants
		(company_id, output_id, user_id, permission, granted_by_user_id)
		VALUES ($1, $2, $3, 'view', $4)`, fixture.CompanyA, outputID, fixture.UserB, fixture.UserA)
	assertDownload(nil)
	record, err := getAttachment(
		ctx,
		store.Queries(),
		attachmentLocator{ID: attachmentID, CompanyID: fixture.CompanyA},
	)
	if err != nil {
		t.Fatal(err)
	}
	input.KnownRevision = record.Revision
	if got, err := service.DownloadResolved(ctx, input); err != nil || !got.NotModified || got.Body != nil {
		t.Fatalf("authorized cached download = %#v, %v", got, err)
	}
	dbtest.MustExec(t, ctx, pool, `DELETE FROM output_grants
		WHERE company_id = $1 AND output_id = $2 AND user_id = $3`, fixture.CompanyA, outputID, fixture.UserB)
	assertDownload(ErrNotFound)
	input.RequestedByUserID = fixture.UserA
	dbtest.MustExec(t, ctx, pool, `UPDATE company_members SET status = 'revoked'
		WHERE company_id = $1 AND user_id = $2`, fixture.CompanyA, fixture.UserA)
	assertDownload(ErrNotFound)
}

func TestLinkedAttachmentDeletionUsesConversationVisibility(t *testing.T) {
	t.Run("task conversation reaches the company", func(t *testing.T) {
		pool, _, fixture, service, _, eventLog := newSharedAttachmentStack(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		input := attachmentInitiateInputFor(fixture, []byte("task linked attachment"))
		initiated, err := service.Initiate(ctx, input, bytes.NewReader([]byte("task linked attachment")))
		if err != nil {
			t.Fatalf("Initiate() error = %v", err)
		}
		attachmentID := initiated.Attachment.ID
		if _, err := service.Complete(ctx, completeInput(input, attachmentID)); err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
		linkReadyAttachmentForCleanup(t, ctx, pool, fixture, attachmentID, 71)
		if err := service.Delete(ctx, DeleteInput{
			ID: attachmentID, CompanyID: fixture.companyID, UploadedByUserID: fixture.ownerID,
		}); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		routing := onlyAttachmentDeletedEvent(t, eventLog.deletedSnapshot(), attachmentID).Payload
		// A task conversation is company-wide, and that is the whole fact the
		// frame carries: the consumer that delivers reads it and picks the
		// company route.
		if routing.CompanyID != fixture.companyID || routing.ConversationKind != "task" {
			t.Fatalf("task-linked attachment routing = %+v", routing)
		}
	})

	t.Run("direct conversation excludes another company member", func(t *testing.T) {
		pool, _, fixture, service, _, eventLog := newSharedAttachmentStack(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		payload := []byte("direct linked attachment")
		input := attachmentInitiateInputFor(fixture, payload)
		initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("Initiate() error = %v", err)
		}
		attachmentID := initiated.Attachment.ID
		if _, err := service.Complete(ctx, completeInput(input, attachmentID)); err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
		conversationID, messageID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
		mustAttachmentExec(t, ctx, pool, `
			INSERT INTO conversations(id,company_id,kind,agent_id,created_by_user_id,title)
			VALUES ($1,$2,'direct',$3,$4,'Private chat')`,
			conversationID, fixture.companyID, fixture.agentID, fixture.ownerID)
		mustAttachmentExec(t, ctx, pool, `
			INSERT INTO conversation_items(
				id,company_id,conversation_id,run_id,seq,type,author_kind,author_id,content
			)
			VALUES ($1,$2,$3,gen_random_uuid(),1,'user_message','user',$4,'Private attachment')`,
			messageID, fixture.companyID, conversationID, fixture.ownerID)
		mustAttachmentExec(t, ctx, pool, `
			INSERT INTO message_attachments(company_id,message_id,attachment_id,position)
			VALUES ($1,$2,$3,0)`, fixture.companyID, messageID, attachmentID)
		if err := service.Delete(ctx, DeleteInput{
			ID: attachmentID, CompanyID: fixture.companyID, UploadedByUserID: fixture.ownerID,
		}); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		routing := onlyAttachmentDeletedEvent(t, eventLog.deletedSnapshot(), attachmentID).Payload
		// A direct conversation belongs to whoever created it, so the frame
		// names that user and never the other company member.
		if routing.CompanyID != fixture.companyID || routing.ConversationKind != "direct" ||
			routing.ConversationCreatorUserID != fixture.ownerID ||
			routing.ConversationCreatorUserID == fixture.memberID {
			t.Fatalf("direct-linked attachment routing = %+v", routing)
		}
	})
}

func onlyAttachmentDeletedEvent(
	t *testing.T,
	recorded []deletedEvent,
	attachmentID uuid.UUID,
) deletedEvent {
	t.Helper()
	matched := attachmentDeletedEvents(recorded, attachmentID)
	if len(matched) != 1 {
		t.Fatalf("attachment.deleted events = %d, want 1", len(matched))
	}
	return matched[0]
}

func TestInitiateStoresCanonicalContentAndReturnsPendingProjection(t *testing.T) {
	pool, _, fixture, service, objects, _ := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("zenith-team attachment")
	input := attachmentInitiateInputFor(fixture, payload)

	result, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Initiate() error = %v", err)
	}
	if !result.Created || result.Attachment.Status != PublicStatusPending ||
		result.Attachment.Revision != 1 {
		t.Fatalf("Initiate() result = %#v", result)
	}
	if result.Attachment.ID == uuid.Nil || result.Attachment.ID == input.RequestKey {
		t.Fatalf("DB-generated Attachment id = %s, request key = %s", result.Attachment.ID, input.RequestKey)
	}

	wantKey := canonicalObjectKey(input.CompanyID, result.Attachment.ID)
	var companyID, uploadedByUserID uuid.UUID
	var objectKey, filename, contentType string
	var sizeBytes int64
	var createdAt, leaseExpiresAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT company_id,uploaded_by_user_id,object_key,filename,content_type,
		       size_bytes,created_at,upload_lease_expires_at
		FROM attachments WHERE id=$1`, result.Attachment.ID).
		Scan(&companyID, &uploadedByUserID, &objectKey, &filename, &contentType,
			&sizeBytes, &createdAt, &leaseExpiresAt); err != nil {
		t.Fatalf("read stored Attachment: %v", err)
	}
	if companyID != input.CompanyID || uploadedByUserID != input.UploadedByUserID ||
		objectKey != wantKey || filename != input.Filename ||
		contentType != input.ContentType || sizeBytes != input.SizeBytes ||
		leaseExpiresAt.Sub(createdAt) != defaultPendingTTL {
		t.Fatalf("stored Attachment row = %s/%s/%s/%d lease %s after %s",
			companyID, objectKey, filename, sizeBytes, leaseExpiresAt, createdAt)
	}
	stored := objects.mustObject(t, wantKey)
	if !bytes.Equal(stored.content, payload) || stored.contentType != "text/plain" {
		t.Fatalf("stored object = %#v", stored)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal(result) error = %v", err)
	}
	for _, forbidden := range []string{"objectKey", "storageDriver", "filesystem", "bucket", "credential"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("public result exposed %q: %s", forbidden, encoded)
		}
	}
}

func TestInitiateOperationDeadlineStaysInsideMinimumUploadLease(t *testing.T) {
	maximumUploadTimeout := attachmentUploadTimeout(100 << 20)
	if maximumUploadTimeout > minimumPendingTTL-3*time.Minute {
		t.Fatalf(
			"initiate timeout %s leaves less than the required three-minute lease margin",
			maximumUploadTimeout,
		)
	}
	_, _, fixture, service, objects, _ := newSharedAttachmentStack(t)
	// The caller's context carries no deadline, so any deadline the object
	// store observes is the operation deadline Initiate installed before it
	// touched either the database or storage — both run on that one context.
	ctx := context.Background()
	var storageDeadline time.Time
	var storageRemaining time.Duration
	objects.putCtx = func(putCtx context.Context) {
		var ok bool
		storageDeadline, ok = putCtx.Deadline()
		if !ok {
			t.Error("storage context has no operation deadline")
			return
		}
		storageRemaining = time.Until(storageDeadline)
	}
	payload := []byte("deadline propagation")
	if _, err := service.Initiate(
		ctx,
		attachmentInitiateInputFor(fixture, payload),
		bytes.NewReader(payload),
	); err != nil {
		t.Fatal(err)
	}
	wantTimeout := attachmentUploadTimeout(int64(len(payload)))
	if storageDeadline.IsZero() || storageRemaining <= 0 || storageRemaining > wantTimeout {
		t.Fatalf("storage deadline = %s, remaining %s", storageDeadline, storageRemaining)
	}
}

func TestInitiateCannotReturnSuccessAfterOperationContextExpires(t *testing.T) {
	_, _, fixture, service, objects, _ := newSharedAttachmentStack(t)
	objects.putCtx = func(putCtx context.Context) { <-putCtx.Done() }
	payload := []byte("timed out upload")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := service.Initiate(ctx, attachmentInitiateInputFor(fixture, payload), bytes.NewReader(payload))
	if !errors.Is(err, context.DeadlineExceeded) || result != (InitiateResult{}) {
		t.Fatalf("Initiate() = %#v, %v; want deadline failure", result, err)
	}
}

func TestInitiateReplaysPendingUploadWithoutReplacingCanonicalObject(t *testing.T) {
	_, _, fixture, service, objects, _ := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("first immutable payload")
	input := attachmentInitiateInputFor(fixture, payload)
	first, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil || !first.Created {
		t.Fatalf("first Initiate() = %#v, %v", first, err)
	}

	replay, err := service.Initiate(ctx, input, readerFunc(func([]byte) (int, error) {
		t.Error("replay source was read after create-only storage found the object")
		return 0, io.EOF
	}))
	if err != nil {
		t.Fatalf("replayed Initiate() error = %v", err)
	}
	if replay.Created || !reflect.DeepEqual(replay.Attachment, first.Attachment) {
		t.Fatalf("replayed Initiate() = %#v, want original projection", replay)
	}
	if got := objects.mustObject(
		t,
		canonicalObjectKey(input.CompanyID, first.Attachment.ID),
	).content; !bytes.Equal(
		got,
		payload,
	) {
		t.Fatalf("object changed on replay: %q", got)
	}
}

func TestInitiateReturnsReadyReplayWithoutAccessingObjectStorage(t *testing.T) {
	_, _, fixture, service, objects, _ := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("already complete")
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("initial Initiate() error = %v", err)
	}
	if _, err := service.Complete(ctx, completeInput(input, initiated.Attachment.ID)); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	objectCalls := objects.callCount()

	result, err := service.Initiate(ctx, input, readerFunc(func([]byte) (int, error) {
		t.Error("ready replay source was read")
		return 0, io.EOF
	}))
	if err != nil {
		t.Fatalf("ready replay Initiate() error = %v", err)
	}
	if result.Created || result.Attachment.Status != PublicStatusReady || objects.callCount() != objectCalls {
		t.Fatalf("ready replay = %#v, object calls %d -> %d", result, objectCalls, objects.callCount())
	}
}

func TestInitiateReplayUsesDatabaseClockForPendingExpiry(t *testing.T) {
	pool, _, fixture, service, objects, eventLog := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("expire by the durable clock")
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("initial Initiate() error = %v", err)
	}

	// Only the database clock decides. An API host clock may be arbitrarily
	// skewed and must not decide whether authoritative PostgreSQL state expired.
	expireAttachmentForCleanup(t, ctx, pool, initiated.Attachment.ID)
	_, err = service.Initiate(ctx, input, readerFunc(func([]byte) (int, error) {
		t.Error("expired replay read the upload source")
		return 0, io.EOF
	}))
	if !errors.Is(err, ErrUploadExpired) {
		t.Fatalf("replayed Initiate(expired by database clock) error = %v, want upload expired", err)
	}
	assertAttachmentCleanupCompleted(
		t, ctx, pool, objects, eventLog, initiated.Attachment.ID, input.CompanyID, 3,
	)
}

func TestInitiateRejectsConflictingRequestKeyBeforeReadingContent(t *testing.T) {
	_, _, fixture, service, _, _ := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("first")
	input := attachmentInitiateInputFor(fixture, payload)
	if _, err := service.Initiate(ctx, input, bytes.NewReader(payload)); err != nil {
		t.Fatalf("initial Initiate() error = %v", err)
	}
	input.Filename = "conflicting.txt"
	_, err := service.Initiate(ctx, input, readerFunc(func([]byte) (int, error) {
		t.Error("conflicting replay source was read")
		return 0, io.EOF
	}))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting Initiate() error = %v, want conflict", err)
	}
}

func TestCompleteTransitionsPendingAttachmentAndReadyReplayIsNaturalNoOp(t *testing.T) {
	_, _, fixture, service, objects, _ := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("complete this attachment")
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Initiate() error = %v", err)
	}

	completed, err := service.Complete(ctx, completeInput(input, initiated.Attachment.ID))
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if !completed.Changed || completed.Attachment.Status != PublicStatusReady ||
		!completed.Attachment.UpdatedAt.After(initiated.Attachment.UpdatedAt) ||
		completed.Attachment.Revision != initiated.Attachment.Revision+1 {
		t.Fatalf("Complete() = %#v", completed)
	}
	objectCalls := objects.callCount()
	objects.openErr = errors.New("provider credential must remain hidden")
	replayed, err := service.Complete(ctx, completeInput(input, initiated.Attachment.ID))
	if err != nil {
		t.Fatalf("replayed Complete() error = %v", err)
	}
	if replayed.Changed || !reflect.DeepEqual(replayed.Attachment, completed.Attachment) ||
		objects.callCount() != objectCalls {
		t.Fatalf("replayed Complete() = %#v, object calls %d -> %d", replayed, objectCalls, objects.callCount())
	}
}

func TestCompleteScopesCreatorAndRequiresUploadedObject(t *testing.T) {
	_, _, fixture, service, objects, _ := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	payload := []byte("pending")

	t.Run("wrong creator", func(t *testing.T) {
		input := attachmentInitiateInputFor(fixture, payload)
		initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("Initiate() error = %v", err)
		}
		calls := objects.callCount()
		completion := completeInput(input, initiated.Attachment.ID)
		completion.UploadedByUserID = fixture.memberID
		if _, err := service.Complete(ctx, completion); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Complete(wrong creator) error = %v, want not found", err)
		}
		if objects.callCount() != calls {
			t.Fatal("object store was accessed before creator provenance matched")
		}
	})

	t.Run("missing object", func(t *testing.T) {
		input := attachmentInitiateInputFor(fixture, payload)
		initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("Initiate() error = %v", err)
		}
		attachmentID := initiated.Attachment.ID
		objects.removeObject(canonicalObjectKey(input.CompanyID, attachmentID))
		if _, err := service.Complete(
			ctx,
			completeInput(input, attachmentID),
		); !errors.Is(
			err,
			ErrUploadIncomplete,
		) {
			t.Fatalf("Complete(missing object) error = %v, want upload incomplete", err)
		}
	})

	t.Run("invalid object-store reader", func(t *testing.T) {
		input := attachmentInitiateInputFor(fixture, payload)
		initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("Initiate() error = %v", err)
		}
		objects.openNil = true
		defer func() { objects.openNil = false }()
		if _, err := service.Complete(
			ctx, completeInput(input, initiated.Attachment.ID),
		); !errors.Is(err, ErrStorageUnavailable) {
			t.Fatalf("Complete(nil object reader) error = %v, want storage unavailable", err)
		}
	})
}

func TestConcurrentCompleteHasOneTransitionAndOneNaturalReplay(t *testing.T) {
	_, _, fixture, service, _, _ := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("complete concurrently")
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Initiate() error = %v", err)
	}
	attachmentID := initiated.Attachment.ID
	results := make([]CompleteResult, 2)
	errorsByIndex := make([]error, len(results))
	start := make(chan struct{})
	var agents sync.WaitGroup
	agents.Add(len(results))
	for index := range results {
		go func() {
			defer agents.Done()
			<-start
			results[index], errorsByIndex[index] = service.Complete(ctx, completeInput(input, attachmentID))
		}()
	}
	close(start)
	agents.Wait()
	changed := 0
	for index, err := range errorsByIndex {
		if err != nil || results[index].Attachment.Status != PublicStatusReady {
			t.Fatalf("Complete[%d] = %#v, %v", index, results[index], err)
		}
		if results[index].Changed {
			changed++
		}
	}
	if changed != 1 {
		t.Fatalf("changed results = %d, want 1", changed)
	}
}

func TestSizeMismatchPersistsDeletionIntentBeforeObjectCleanup(t *testing.T) {
	pool, _, fixture, service, objects, eventLog := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	t.Run("initiate stream", func(t *testing.T) {
		declared := []byte("declared bytes")
		input := attachmentInitiateInputFor(fixture, declared)
		if _, err := service.Initiate(
			ctx,
			input,
			bytes.NewReader([]byte("short")),
		); !errors.Is(
			err,
			ErrIntegrityMismatch,
		) {
			t.Fatalf("Initiate(mismatch) error = %v, want integrity mismatch", err)
		}
		attachmentID := attachmentIDForRequest(t, ctx, pool, input)
		assertAttachmentCleanupCompleted(t, ctx, pool, objects, eventLog, attachmentID, input.CompanyID, 3)
	})

	t.Run("existing object on pending replay", func(t *testing.T) {
		payload := []byte("declared bytes")
		input := attachmentInitiateInputFor(fixture, payload)
		initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("initial Initiate() error = %v", err)
		}
		attachmentID := initiated.Attachment.ID
		objects.replaceObject(canonicalObjectKey(input.CompanyID, attachmentID), []byte("short"))
		if _, err := service.Initiate(ctx, input, readerFunc(func([]byte) (int, error) {
			t.Error("pending replay source was read after the object already existed")
			return 0, io.EOF
		})); !errors.Is(err, ErrIntegrityMismatch) {
			t.Fatalf("replayed Initiate(mismatch) error = %v, want integrity mismatch", err)
		}
		assertAttachmentCleanupCompleted(t, ctx, pool, objects, eventLog, attachmentID, input.CompanyID, 3)
	})

	t.Run("complete verification", func(t *testing.T) {
		payload := []byte("declared bytes")
		input := attachmentInitiateInputFor(fixture, payload)
		initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("Initiate() error = %v", err)
		}
		attachmentID := initiated.Attachment.ID
		objects.replaceObject(canonicalObjectKey(input.CompanyID, attachmentID), []byte("short"))
		if _, err := service.Complete(
			ctx,
			completeInput(input, attachmentID),
		); !errors.Is(
			err,
			ErrIntegrityMismatch,
		) {
			t.Fatalf("Complete(mismatch) error = %v, want integrity mismatch", err)
		}
		assertAttachmentCleanupCompleted(t, ctx, pool, objects, eventLog, attachmentID, input.CompanyID, 3)
	})
}

func TestCompleteExpiredPendingUploadCreatesCleanupIntent(t *testing.T) {
	pool, _, fixture, service, objects, eventLog := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("expires")
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Initiate() error = %v", err)
	}
	attachmentID := initiated.Attachment.ID
	expireAttachmentForCleanup(t, ctx, pool, attachmentID)
	if _, err := service.Complete(ctx, completeInput(input, attachmentID)); !errors.Is(err, ErrUploadExpired) {
		t.Fatalf("Complete(expired) error = %v, want upload expired", err)
	}
	assertAttachmentCleanupCompleted(t, ctx, pool, objects, eventLog, attachmentID, input.CompanyID, 3)
}

func TestCompleteUsesDatabaseClockForPendingExpiry(t *testing.T) {
	pool, _, fixture, service, objects, eventLog := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("database-clock expiry")
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Initiate() error = %v", err)
	}

	// The row is barely past its durable lease but nowhere near
	// created_at + PendingTTL, so a host-side TTL computation would call it live.
	// Only the database clock expires it; the service owns no wall clock.
	mustAttachmentExec(t, ctx, pool, `
		WITH completion_clock AS MATERIALIZED (SELECT clock_timestamp() AS now_at)
		UPDATE attachments
		SET created_at=completion_clock.now_at-interval '1 second',
			updated_at=completion_clock.now_at-interval '1 second',
			upload_lease_expires_at=completion_clock.now_at-interval '1 millisecond'
		FROM completion_clock
		WHERE id=$1`, initiated.Attachment.ID)
	if _, err := service.Complete(
		ctx, completeInput(input, initiated.Attachment.ID),
	); !errors.Is(err, ErrUploadExpired) {
		t.Fatalf("Complete(expired by database clock) error = %v, want upload expired", err)
	}
	assertAttachmentCleanupCompleted(
		t, ctx, pool, objects, eventLog, initiated.Attachment.ID, input.CompanyID, 3,
	)
}

func TestDeleteRetainsIntentAcrossStorageFailureAndCleanupRetriesNaturally(t *testing.T) {
	pool, _, fixture, service, objects, eventLog := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("delete safely")
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Initiate() error = %v", err)
	}
	attachmentID := initiated.Attachment.ID
	if _, err := service.Complete(ctx, completeInput(input, attachmentID)); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	const secret = "s3 bucket credential detail"
	objects.setDeleteErr(errors.New(secret))
	if err := service.Delete(ctx, DeleteInput{
		ID: attachmentID, CompanyID: input.CompanyID, UploadedByUserID: input.UploadedByUserID,
	}); !errors.Is(err, ErrStorageUnavailable) || strings.Contains(err.Error(), secret) {
		t.Fatalf("Delete() error = %v", err)
	}
	assertAttachmentDatabaseStatus(t, ctx, pool, attachmentID, StatusDeleting, 3)
	if !objects.hasObject(canonicalObjectKey(input.CompanyID, attachmentID)) {
		t.Fatal("object was removed even though the deletion intent could not clean up")
	}
	if count := attachmentDeletionEventCount(eventLog.deletedSnapshot(), attachmentID); count != 0 {
		t.Fatalf("attachment.deleted count before object deletion = %d, want 0", count)
	}

	objects.setDeleteErr(nil)
	if err := service.Cleanup(ctx, CleanupInput{ID: attachmentID, CompanyID: input.CompanyID}); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	assertAttachmentCleanupCompleted(t, ctx, pool, objects, eventLog, attachmentID, input.CompanyID, 4)
	if err := service.Cleanup(ctx, CleanupInput{ID: attachmentID, CompanyID: input.CompanyID}); err != nil {
		t.Fatalf("replayed Cleanup() error = %v", err)
	}
}

func TestDeleteRejectsAssetBackedAttachmentAndRetainsObject(t *testing.T) {
	pool, _, fixture, service, objects, _ := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("published output")
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Initiate() error = %v", err)
	}
	attachmentID := initiated.Attachment.ID
	if _, err := service.Complete(ctx, completeInput(input, attachmentID)); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	linkReadyAttachmentToAssetVersionForCleanup(t, ctx, pool, fixture, attachmentID)

	err = service.Delete(ctx, DeleteInput{
		ID: attachmentID, CompanyID: input.CompanyID, UploadedByUserID: input.UploadedByUserID,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Delete(asset-backed) error = %v, want ErrConflict", err)
	}
	assertAttachmentDatabaseStatus(t, ctx, pool, attachmentID, StatusReady, 2)
	objects.mustObject(t, canonicalObjectKey(input.CompanyID, attachmentID))
}

func TestDeleteRetainsTerminalIdentityAndRejectsInitiateResurrection(t *testing.T) {
	pool, _, fixture, service, objects, eventLog := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("retain terminal attachment identity")
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Initiate() error = %v", err)
	}
	attachmentID := initiated.Attachment.ID
	ready, err := service.Complete(ctx, completeInput(input, attachmentID))
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if err := service.Delete(ctx, DeleteInput{
		ID: attachmentID, CompanyID: input.CompanyID, UploadedByUserID: input.UploadedByUserID,
	}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	assertAttachmentCleanupCompleted(t, ctx, pool, objects, eventLog, attachmentID, input.CompanyID, 4)

	var filename, contentType string
	var sizeBytes int64
	if err := pool.QueryRow(ctx, `
		SELECT filename,content_type,size_bytes FROM attachments WHERE id=$1`, attachmentID).
		Scan(&filename, &contentType, &sizeBytes); err != nil {
		t.Fatalf("read terminal Attachment identity: %v", err)
	}
	if filename != ready.Attachment.Filename || contentType != ready.Attachment.ContentType ||
		sizeBytes != ready.Attachment.SizeBytes {
		t.Fatalf("terminal identity = %s/%s/%d", filename, contentType, sizeBytes)
	}

	objectCalls := objects.callCount()
	if _, err := service.Initiate(ctx, input, readerFunc(func([]byte) (int, error) {
		t.Error("deleted attachment replay read the upload source")
		return 0, io.EOF
	})); !errors.Is(err, ErrConflict) {
		t.Fatalf("Initiate(deleted request key) error = %v, want conflict", err)
	}
	conflicting := input
	conflicting.Filename = "different.txt"
	if _, err := service.Initiate(ctx, conflicting, readerFunc(func([]byte) (int, error) {
		t.Error("conflicting deleted attachment replay read the upload source")
		return 0, io.EOF
	})); !errors.Is(err, ErrConflict) {
		t.Fatalf("Initiate(conflicting deleted request key) error = %v, want conflict", err)
	}
	if err := service.Delete(ctx, DeleteInput{
		ID: attachmentID, CompanyID: input.CompanyID, UploadedByUserID: input.UploadedByUserID,
	}); err != nil {
		t.Fatalf("Delete(deleted replay) error = %v", err)
	}
	if err := service.Cleanup(ctx, CleanupInput{ID: attachmentID, CompanyID: input.CompanyID}); err != nil {
		t.Fatalf("Cleanup(deleted replay) error = %v", err)
	}
	if objects.callCount() != objectCalls {
		t.Fatalf("deleted replay accessed storage: calls %d -> %d", objectCalls, objects.callCount())
	}
	assertAttachmentDatabaseStatus(t, ctx, pool, attachmentID, StatusDeleted, 4)
}

func TestExactDeleteConvergesAfterConcurrentDeleteAlreadyReachedTerminal(t *testing.T) {
	pool, _, fixture, service, objects, eventLog := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("concurrent exact delete")
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Initiate() error = %v", err)
	}
	attachmentID := initiated.Attachment.ID
	if _, err := service.Complete(ctx, completeInput(input, attachmentID)); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	deleteErrors := make([]error, 2)
	start := make(chan struct{})
	var agents sync.WaitGroup
	agents.Add(len(deleteErrors))
	for index := range deleteErrors {
		go func() {
			defer agents.Done()
			<-start
			deleteErrors[index] = service.Delete(ctx, DeleteInput{
				ID: attachmentID, CompanyID: input.CompanyID, UploadedByUserID: input.UploadedByUserID,
			})
		}()
	}
	close(start)
	agents.Wait()
	for index, err := range deleteErrors {
		if err != nil {
			t.Fatalf("concurrent Delete[%d] error = %v", index, err)
		}
	}
	assertAttachmentCleanupCompleted(t, ctx, pool, objects, eventLog, attachmentID, input.CompanyID, 4)
}

func TestDeleteConvergesWhenCompletionWinsAfterDeleteRead(t *testing.T) {
	pool, store, fixture, service, objects, eventLog := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("completion serializes before deletion")
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Initiate() error = %v", err)
	}
	attachmentID := initiated.Attachment.ID
	stalePending, err := getAttachment(ctx, store.Queries(), attachmentLocator{
		ID: attachmentID, CompanyID: input.CompanyID,
	})
	if err != nil || stalePending.Status != StatusPending {
		t.Fatalf("getAttachment(pending) = %+v, %v", stalePending, err)
	}

	// A completion serializes after Delete read the pending row, so the deletion
	// intent lands on the resulting ready row. That is the one legal
	// cross-timeline path, and it must converge rather than be rejected.
	if _, err := service.Complete(ctx, completeInput(input, attachmentID)); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if err := service.requestDeletionAndCleanup(ctx, stalePending, attachmentDeletionRequested); err != nil {
		t.Fatalf("delete after concurrent completion error = %v", err)
	}
	assertAttachmentCleanupCompleted(t, ctx, pool, objects, eventLog, attachmentID, input.CompanyID, 4)
}

func TestCompleteDoesNotResurrectWhenDeleteSerializesFirst(t *testing.T) {
	pool, _, fixture, service, objects, eventLog := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("deletion serializes before completion")
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Initiate() error = %v", err)
	}
	attachmentID := initiated.Attachment.ID
	if err := service.Delete(ctx, DeleteInput{
		ID: attachmentID, CompanyID: input.CompanyID, UploadedByUserID: input.UploadedByUserID,
	}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	assertAttachmentCleanupCompleted(t, ctx, pool, objects, eventLog, attachmentID, input.CompanyID, 3)

	if _, err := service.Complete(ctx, completeInput(input, attachmentID)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Complete(after deletion won) error = %v, want not found", err)
	}
	assertAttachmentDatabaseStatus(t, ctx, pool, attachmentID, StatusDeleted, 3)
	if objects.hasObject(canonicalObjectKey(input.CompanyID, attachmentID)) {
		t.Fatal("a losing completion resurrected the terminal object")
	}
}

func TestConcurrentCleanupFinalizesDeletedTerminalVersionOnce(t *testing.T) {
	pool, store, fixture, service, objects, eventLog := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("concurrent terminal cleanup")
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Initiate() error = %v", err)
	}
	attachmentID := initiated.Attachment.ID
	if _, err := service.Complete(ctx, completeInput(input, attachmentID)); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	actor := input.UploadedByUserID
	deleting, err := requestDeletionForTest(ctx, store, attachmentDeletionRequest{
		Locator:          attachmentLocator{ID: attachmentID, CompanyID: input.CompanyID},
		UploadedByUserID: &actor,
		ObjectKey:        canonicalObjectKey(input.CompanyID, attachmentID),
		Reason:           attachmentDeletionRequested,
	})
	if err != nil || deleting.Status != StatusDeleting || deleting.Revision != 3 {
		t.Fatalf("requestAttachmentDeletion() = %+v, %v", deleting, err)
	}

	errorsByIndex := make([]error, 2)
	start := make(chan struct{})
	var agents sync.WaitGroup
	agents.Add(len(errorsByIndex))
	for index := range errorsByIndex {
		go func() {
			defer agents.Done()
			<-start
			errorsByIndex[index] = service.Cleanup(
				ctx,
				CleanupInput{ID: attachmentID, CompanyID: input.CompanyID},
			)
		}()
	}
	close(start)
	agents.Wait()
	for index, err := range errorsByIndex {
		if err != nil {
			t.Fatalf("Cleanup[%d] error = %v", index, err)
		}
	}
	// Exactly one attachment.deleted row proves exactly one finalization ran.
	assertAttachmentCleanupCompleted(t, ctx, pool, objects, eventLog, attachmentID, input.CompanyID, 4)
}

func TestDownloadReturnsReadyContentWithoutStorageMetadata(t *testing.T) {
	_, _, fixture, service, _, _ := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("download body")
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Initiate() error = %v", err)
	}
	attachmentID := initiated.Attachment.ID
	if _, err := service.Complete(ctx, completeInput(input, attachmentID)); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	download, err := service.Download(ctx, DownloadInput{
		ID: attachmentID, CompanyID: input.CompanyID, RequestedByUserID: input.UploadedByUserID,
	})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	content, readErr := io.ReadAll(download.Body)
	closeErr := download.Body.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(content, payload) ||
		download.Attachment.Status != PublicStatusReady {
		t.Fatalf(
			"download content/read/close/result = %q, %v, %v, %#v",
			content,
			readErr,
			closeErr,
			download.Attachment,
		)
	}
	encoded, err := json.Marshal(download.Attachment)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"objectKey", "storageDriver", "storageBinding", "storageGeneration"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("download projection exposed %q: %s", forbidden, encoded)
		}
	}
}

func TestResolvedDownloadRevalidationAvoidsObjectStorage(t *testing.T) {
	pool, _, fixture, service, objects, _ := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("cached file")
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	completed, err := service.Complete(ctx, completeInput(input, initiated.Attachment.ID))
	if err != nil {
		t.Fatal(err)
	}
	opened := false
	objects.openHook = func() { opened = true }
	downloadInput := ResolvedDownloadInput{
		ID: initiated.Attachment.ID, CompanyID: input.CompanyID, OutputAccessResolved: true,
		KnownRevision: completed.Attachment.Revision,
	}
	result, err := service.DownloadResolved(ctx, downloadInput)
	if err != nil || !result.NotModified || result.Body != nil || opened {
		t.Fatalf("revalidation error=%v unchanged=%v opened=%v", err, result.NotModified, opened)
	}
	downloadInput.KnownRevision--
	result, err = service.DownloadResolved(ctx, downloadInput)
	if err != nil || result.NotModified || !opened {
		t.Fatalf("stale revision must download: %v", err)
	}
	defer result.Body.Close()
	body, err := io.ReadAll(result.Body)
	if err != nil || !bytes.Equal(body, payload) {
		t.Fatal("stale revision did not download the file")
	}
	downloadInput.KnownRevision = completed.Attachment.Revision
	downloadInput.CompanyID = dbtest.NewUUID(t)
	if _, err := service.DownloadResolved(ctx, downloadInput); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-company cache: %v", err)
	}
	downloadInput.CompanyID = input.CompanyID
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE attachments SET status = 'deleting' WHERE id = $1`,
		initiated.Attachment.ID,
	)
	if _, err := service.DownloadResolved(ctx, downloadInput); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted file cache: %v", err)
	}
}

func TestDownloadMapsReaderErrorsWithoutProviderOrFilesystemDetails(t *testing.T) {
	_, _, fixture, service, objects, _ := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("download")
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Initiate() error = %v", err)
	}
	attachmentID := initiated.Attachment.ID
	if _, err := service.Complete(ctx, completeInput(input, attachmentID)); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	const secret = "/private/bucket/access-key"
	objects.openBody = &failingReadCloser{readErr: errors.New(secret), closeErr: errors.New(secret)}
	defer func() { objects.openBody = nil }()
	download, err := service.Download(ctx, DownloadInput{
		ID: attachmentID, CompanyID: input.CompanyID, RequestedByUserID: input.UploadedByUserID,
	})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if _, err := download.Body.Read(
		make([]byte, 1),
	); !errors.Is(err, ErrStorageUnavailable) ||
		strings.Contains(err.Error(), secret) {
		t.Fatalf("download Read() error = %v", err)
	}
	if err := download.Body.Close(); !errors.Is(err, ErrStorageUnavailable) ||
		strings.Contains(err.Error(), secret) {
		t.Fatalf("download Close() error = %v", err)
	}
}

func TestAttachmentServiceMapsRawRepositoryAndStorageErrorsToClosedErrors(t *testing.T) {
	pool, store, fixture, service, objects, eventLog := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	const secret = "postgres path bucket access key raw error"

	t.Run("repository read", func(t *testing.T) {
		input := attachmentInitiateInputFor(fixture, []byte("safe payload"))
		initiated, err := service.Initiate(ctx, input, bytes.NewReader([]byte("safe payload")))
		if err != nil {
			t.Fatalf("Initiate() error = %v", err)
		}
		brokenService := newClosedPoolAttachmentService(t, ctx, pool, newAttachmentObjectStorageFake())
		_, err = brokenService.Complete(ctx, completeInput(input, initiated.Attachment.ID))
		assertClosedAttachmentError(t, err, ErrUnavailable, "closed")
		if err.Error() != ErrUnavailable.Error() {
			t.Fatalf("closed repository error = %q, want exactly %q", err, ErrUnavailable)
		}
	})

	t.Run("object create", func(t *testing.T) {
		input := attachmentInitiateInputFor(fixture, []byte("safe payload"))
		objects.putErr = errors.New(secret)
		defer func() { objects.putErr = nil }()
		_, err := service.Initiate(ctx, input, bytes.NewReader([]byte("safe payload")))
		assertClosedAttachmentError(t, err, ErrStorageUnavailable, secret)
	})

	t.Run("object open", func(t *testing.T) {
		payload := []byte("safe payload")
		input := attachmentInitiateInputFor(fixture, payload)
		initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("Initiate() error = %v", err)
		}
		attachmentID := initiated.Attachment.ID
		if _, err := service.Complete(ctx, completeInput(input, attachmentID)); err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
		objects.openErr = errors.New(secret)
		defer func() { objects.openErr = nil }()
		_, err = service.Download(ctx, DownloadInput{
			ID: attachmentID, CompanyID: input.CompanyID, RequestedByUserID: input.UploadedByUserID,
		})
		assertClosedAttachmentError(t, err, ErrStorageUnavailable, secret)
	})

	t.Run("repository finalization", func(t *testing.T) {
		payload := []byte("safe payload")
		input := attachmentInitiateInputFor(fixture, payload)
		initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("Initiate() error = %v", err)
		}
		attachmentID := initiated.Attachment.ID
		if _, err := service.Complete(ctx, completeInput(input, attachmentID)); err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
		actor := input.UploadedByUserID
		deleting, err := requestDeletionForTest(ctx, store, attachmentDeletionRequest{
			Locator:          attachmentLocator{ID: attachmentID, CompanyID: input.CompanyID},
			UploadedByUserID: &actor,
			ObjectKey:        canonicalObjectKey(input.CompanyID, attachmentID),
			Reason:           attachmentDeletionRequested,
		})
		if err != nil || deleting.Status != StatusDeleting || deleting.Revision != 3 {
			t.Fatalf("requestAttachmentDeletion() = %+v, %v", deleting, err)
		}

		// The object is already gone when the finalization transaction fails, so
		// the raw driver error must surface closed AND the transaction must have
		// committed nothing: the intent stays durable at its own version, no
		// terminal event escapes, and a later cleanup still converges.
		brokenService := newClosedPoolAttachmentService(t, ctx, pool, objects)
		err = brokenService.cleanupDeleting(ctx, deleting)
		assertClosedAttachmentError(t, err, ErrUnavailable, "closed")
		if objects.hasObject(canonicalObjectKey(input.CompanyID, attachmentID)) {
			t.Fatal("finalization ran before the object was removed")
		}
		assertAttachmentDatabaseStatus(t, ctx, pool, attachmentID, StatusDeleting, 3)
		if count := attachmentDeletionEventCount(eventLog.deletedSnapshot(), attachmentID); count != 0 {
			t.Fatalf("attachment.deleted count after a rolled-back finalization = %d, want 0", count)
		}

		if err := service.Cleanup(ctx, CleanupInput{ID: attachmentID, CompanyID: input.CompanyID}); err != nil {
			t.Fatalf("Cleanup(after failed finalization) error = %v", err)
		}
		assertAttachmentCleanupCompleted(t, ctx, pool, objects, eventLog, attachmentID, input.CompanyID, 4)
	})
}

func TestAttachmentOpenErrorClosesPartialReaderWithoutLeakingStorageDetails(t *testing.T) {
	_, _, fixture, service, objects, _ := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	const secret = "/private/bucket/access-key/open-error"
	payload := []byte("safe attachment payload")

	t.Run("complete verification", func(t *testing.T) {
		input := attachmentInitiateInputFor(fixture, payload)
		initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("Initiate() error = %v", err)
		}
		partial := &failingReadCloser{closeErr: errors.New(secret)}
		objects.openBody, objects.openErr = partial, errors.New(secret)
		defer func() { objects.openBody, objects.openErr = nil, nil }()
		_, err = service.Complete(ctx, completeInput(input, initiated.Attachment.ID))
		assertClosedAttachmentError(t, err, ErrStorageUnavailable, secret)
		if !partial.closed {
			t.Fatal("Complete() did not close the reader returned with an Open error")
		}
	})

	t.Run("download", func(t *testing.T) {
		input := attachmentInitiateInputFor(fixture, payload)
		initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("Initiate() error = %v", err)
		}
		attachmentID := initiated.Attachment.ID
		if _, err := service.Complete(ctx, completeInput(input, attachmentID)); err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
		partial := &failingReadCloser{closeErr: errors.New(secret)}
		objects.openBody, objects.openErr = partial, errors.New(secret)
		defer func() { objects.openBody, objects.openErr = nil, nil }()
		_, err = service.Download(ctx, DownloadInput{
			ID: attachmentID, CompanyID: input.CompanyID, RequestedByUserID: input.UploadedByUserID,
		})
		assertClosedAttachmentError(t, err, ErrStorageUnavailable, secret)
		if !partial.closed {
			t.Fatal("Download() did not close the reader returned with an Open error")
		}
	})
}

// newClosedPoolAttachmentService returns a DefaultAttachmentService whose pool
// is already closed, so every query fails with a raw driver error the closed
// public sentinels must swallow.
func newClosedPoolAttachmentService(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	objects ObjectStorage,
) *DefaultAttachmentService {
	t.Helper()
	brokenPool, err := db.NewPool(ctx, db.PoolConfig{
		URL: pool.Config().ConnString(), MaxConns: 2, MinConns: 1,
		MaxConnLifetime: time.Hour, MaxConnIdleTime: 5 * time.Minute,
		HealthCheckPeriod: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	brokenStore, err := db.NewStore(brokenPool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	brokenPool.Close()
	service, err := NewAttachmentService(ServiceOptions{
		Store:   brokenStore,
		Bus:     attachmentTestBus(t),
		Objects: objects,
	})
	if err != nil {
		t.Fatalf("NewAttachmentService() error = %v", err)
	}
	return service
}

func completeInput(input InitiateInput, attachmentID uuid.UUID) CompleteInput {
	return CompleteInput{
		ID: attachmentID, CompanyID: input.CompanyID, UploadedByUserID: input.UploadedByUserID,
	}
}

func attachmentIDForRequest(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	input InitiateInput,
) uuid.UUID {
	t.Helper()
	var attachmentID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		SELECT id
		FROM attachments
		WHERE company_id=$1 AND uploaded_by_user_id=$2 AND request_key=$3`,
		input.CompanyID, input.UploadedByUserID, input.RequestKey,
	).Scan(&attachmentID); err != nil {
		t.Fatalf("read Attachment id by request key: %v", err)
	}
	return attachmentID
}

// assertAttachmentCleanupCompleted pins the terminal shape of every cleanup
// path: the durable row reaches deleted at wantVersion, the object is gone, and
// exactly one attachment.deleted event was enqueued.
func assertAttachmentCleanupCompleted(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	objects *attachmentObjectStorageFake,
	eventLog *attachmentEventLog,
	attachmentID uuid.UUID,
	companyID uuid.UUID,
	wantVersion int64,
) {
	t.Helper()
	assertAttachmentDatabaseStatus(t, ctx, pool, attachmentID, StatusDeleted, wantVersion)
	if objects.hasObject(canonicalObjectKey(companyID, attachmentID)) {
		t.Fatal("object remains after terminal cleanup")
	}
	if count := attachmentDeletionEventCount(eventLog.deletedSnapshot(), attachmentID); count != 1 {
		t.Fatalf("attachment.deleted count = %d, want 1", count)
	}
}

func assertClosedAttachmentError(t *testing.T, got, want error, secret string) {
	t.Helper()
	if !errors.Is(got, want) || got == nil || strings.Contains(got.Error(), secret) {
		t.Fatalf("error = %v, want closed %v", got, want)
	}
}

func TestShareResolvedSignsReadyAttachmentForOneHour(t *testing.T) {
	_, _, fixture, service, objects, _ := newSharedAttachmentStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := []byte("shareable artifact")
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Initiate() error = %v", err)
	}
	attachmentID := initiated.Attachment.ID
	if _, err := service.Complete(ctx, completeInput(input, attachmentID)); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	startedAt := time.Now().UTC()
	share, err := service.ShareResolved(ctx, ResolvedShareInput{
		ID: attachmentID, CompanyID: input.CompanyID,
	})
	finishedAt := time.Now().UTC()
	if err != nil {
		t.Fatalf("ShareResolved() error = %v", err)
	}
	if share.URL != "https://objects.test/signed" ||
		share.ExpiresAt.Before(startedAt.Add(time.Hour)) ||
		share.ExpiresAt.After(finishedAt.Add(time.Hour)) {
		t.Fatalf("ShareResolved() = %+v", share)
	}
	wantKey := canonicalObjectKey(input.CompanyID, attachmentID)
	if key, ttl := objects.presignCall(); key != wantKey || ttl != time.Hour {
		t.Fatalf("PresignGet() = %q / %s, want %q / %s", key, ttl, wantKey, time.Hour)
	}

	_, err = service.ShareResolved(ctx, ResolvedShareInput{
		ID: attachmentID, CompanyID: uuid.Must(uuid.NewV7()),
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-company ShareResolved() error = %v, want %v", err, ErrNotFound)
	}
}
