//go:build integration

package attachment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestAttachmentInitiateIsAuthorizedIdempotentAndDatabaseClocked(t *testing.T) {
	pool, store := startAttachmentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAttachmentAuthority(t, ctx, pool)
	requestKey := uuid.Must(uuid.NewV7())
	create := validPostgresPendingCreate(requestKey, fixture.companyID, fixture.ownerID)

	created, err := initiatePendingForTest(ctx, store, create)
	if err != nil {
		t.Fatalf("initiatePendingAttachment() error = %v", err)
	}
	if !created.Created || created.Expiry != pendingAttachmentCurrent ||
		created.Record.ID == uuid.Nil || created.Record.ID == requestKey ||
		created.Record.RequestKey != requestKey ||
		created.Record.ObjectKey != canonicalObjectKey(fixture.companyID, created.Record.ID) ||
		created.Record.Status != StatusPending || created.Record.Revision != 1 ||
		created.Record.UploadLeaseExpiresAt == nil ||
		created.Record.UploadLeaseExpiresAt.Sub(created.Record.CreatedAt) != create.PendingTTL ||
		!created.Record.CreatedAt.Equal(created.Record.UpdatedAt) {
		t.Fatalf("initiatePendingAttachment() = %+v", created)
	}
	replayed, err := initiatePendingForTest(ctx, store, create)
	if err != nil || replayed.Created || replayed.Expiry != pendingAttachmentCurrent ||
		replayed.Record.UploadLeaseExpiresAt == nil || created.Record.UploadLeaseExpiresAt == nil ||
		!replayed.Record.UploadLeaseExpiresAt.After(*created.Record.UploadLeaseExpiresAt) ||
		!replayed.Record.UpdatedAt.Equal(created.Record.UpdatedAt) ||
		replayed.Record.Revision != created.Record.Revision {
		t.Fatalf("initiatePendingAttachment(replay) = %+v, %v", replayed, err)
	}
	createdResponse := attachmentDTOFromRecord(created.Record, PublicStatusPending)
	replayedResponse := attachmentDTOFromRecord(replayed.Record, PublicStatusPending)
	if !reflect.DeepEqual(createdResponse, replayedResponse) {
		t.Fatalf("public replay changed with internal lease: created=%+v replayed=%+v",
			createdResponse, replayedResponse)
	}
	conflicting := create
	conflicting.Filename = "different.txt"
	if _, err := initiatePendingForTest(ctx, store, conflicting); !errors.Is(err, ErrConflict) {
		t.Fatalf("initiatePendingAttachment(conflict) error = %v, want ErrConflict", err)
	}
	otherUploader := validPostgresPendingCreate(requestKey, fixture.companyID, fixture.memberID)
	scoped, err := initiatePendingForTest(ctx, store, otherUploader)
	if err != nil || !scoped.Created || scoped.Record.ID == created.Record.ID {
		t.Fatalf("initiatePendingAttachment(other uploader) = %+v, %v", scoped, err)
	}

	mustAttachmentExec(t, ctx, pool, `
		UPDATE company_members
		SET status='revoked', revoked_at=clock_timestamp(), revoked_by_user_id=$2,
			updated_at=clock_timestamp(), version=version+1
		WHERE company_id=$1 AND user_id=$2`, fixture.companyID, fixture.ownerID)
	unauthorized := validPostgresPendingCreate(uuid.Must(uuid.NewV7()), fixture.companyID, fixture.ownerID)
	if _, err := initiatePendingForTest(ctx, store, unauthorized); !errors.Is(err, ErrNotFound) {
		t.Fatalf("initiatePendingAttachment(revoked member) error = %v, want ErrNotFound", err)
	}
	var unauthorizedRows int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM attachments
		WHERE company_id=$1 AND uploaded_by_user_id=$2 AND request_key=$3`,
		unauthorized.CompanyID, unauthorized.UploadedByUserID, unauthorized.RequestKey).
		Scan(&unauthorizedRows); err != nil {
		t.Fatalf("count unauthorized Attachment: %v", err)
	}
	if unauthorizedRows != 0 {
		t.Fatalf("unauthorized Attachment rows = %d, want 0", unauthorizedRows)
	}
}

func TestAttachmentReplayRenewsUploadLeaseBeforeBlockingPutAndExcludesCleanup(t *testing.T) {
	pool, store := startAttachmentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAttachmentAuthority(t, ctx, pool)
	objects := newAttachmentObjectStorageFake()
	service, err := NewAttachmentService(ServiceOptions{
		Store: store, Bus: attachmentTestBus(t), Objects: objects,
		PendingTTL: minimumPendingTTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("lease protected upload")
	input := attachmentInitiateInputFor(fixture, payload)
	created, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil || !created.Created {
		t.Fatalf("initial Initiate() = %+v, %v", created, err)
	}
	attachmentID := created.Attachment.ID

	var oldLease time.Time
	if err := pool.QueryRow(ctx, `
		UPDATE attachments
		SET upload_lease_expires_at=clock_timestamp()+interval '1 second'
		WHERE id=$1
		RETURNING upload_lease_expires_at`, attachmentID).Scan(&oldLease); err != nil {
		t.Fatalf("shorten upload lease: %v", err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	objects.putCtx = func(context.Context) {
		close(started)
		<-release
	}
	type initiateOutcome struct {
		result InitiateResult
		err    error
	}
	outcome := make(chan initiateOutcome, 1)
	go func() {
		result, replayErr := service.Initiate(ctx, input, bytes.NewReader(payload))
		outcome <- initiateOutcome{result: result, err: replayErr}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatalf("replay did not reach blocking object store: %v", ctx.Err())
	}

	var renewedLease, unchangedUpdatedAt, databaseNow time.Time
	var unchangedVersion int64
	if err := pool.QueryRow(ctx, `
		SELECT upload_lease_expires_at,updated_at,version,clock_timestamp()
		FROM attachments WHERE id=$1`, attachmentID).
		Scan(&renewedLease, &unchangedUpdatedAt, &unchangedVersion, &databaseNow); err != nil {
		t.Fatalf("read renewed upload lease: %v", err)
	}
	if !renewedLease.After(oldLease) || renewedLease.Sub(databaseNow) < minimumPendingTTL-time.Second ||
		!unchangedUpdatedAt.Equal(created.Attachment.UpdatedAt) || unchangedVersion != 1 {
		t.Fatalf("renewed lease/state = %s updated=%s version=%d old=%s now=%s",
			renewedLease, unchangedUpdatedAt, unchangedVersion, oldLease, databaseNow)
	}
	for {
		var oldDeadlinePassed bool
		if err := pool.QueryRow(ctx, `SELECT clock_timestamp() >= $1`, oldLease).
			Scan(&oldDeadlinePassed); err != nil {
			t.Fatal(err)
		}
		if oldDeadlinePassed {
			break
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			t.Fatalf("waiting for old upload lease: %v", ctx.Err())
		}
	}
	transitioned, err := transitionExpiredPendingForCleanup(ctx, store.Queries(), 8)
	if err != nil || transitioned != 0 {
		t.Fatalf("cleanup at old lease while Put blocked = %d, %v", transitioned, err)
	}

	releaseOnce.Do(func() { close(release) })
	replayed := <-outcome
	if replayed.err != nil || replayed.result.Created ||
		!reflect.DeepEqual(replayed.result.Attachment, created.Attachment) {
		t.Fatalf("replayed Initiate() = %+v, %v", replayed.result, replayed.err)
	}
	completed, err := service.Complete(ctx, completeInput(input, attachmentID))
	if err != nil || !completed.Changed || completed.Attachment.Status != PublicStatusReady {
		t.Fatalf("Complete() = %+v, %v", completed, err)
	}
	if transitioned, err := transitionExpiredPendingForCleanup(
		ctx, store.Queries(), 8,
	); err != nil || transitioned != 0 {
		t.Fatalf("cleanup after completion = %d, %v", transitioned, err)
	}
	assertAttachmentDatabaseStatus(t, ctx, pool, attachmentID, StatusReady, 2)
}

func TestAttachmentCompletionIsAtomicReplaySafeAndRevocationSafe(t *testing.T) {
	pool, store := startAttachmentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAttachmentAuthority(t, ctx, pool)
	create := validPostgresPendingCreate(uuid.Must(uuid.NewV7()), fixture.companyID, fixture.ownerID)
	created, err := initiatePendingForTest(ctx, store, create)
	if err != nil {
		t.Fatalf("initiatePendingAttachment() error = %v", err)
	}
	completion := completionForRecord(created.Record)

	var event []events.Event
	completed, completedEvents, err := completePendingForTest(ctx, store, completion)
	if err != nil || completed.Outcome != attachmentCompletionApplied || completed.Record.Status != StatusReady ||
		completed.Record.Revision != 2 || completed.Record.UpdatedAt.IsZero() {
		t.Fatalf("completePendingAttachment() = %+v, %v", completed, err)
	}
	event = append(event, completedEvents...)
	replayed, replayedEvents, err := completePendingForTest(ctx, store, completion)
	if err != nil || replayed.Outcome != attachmentCompletionReplay ||
		!reflect.DeepEqual(replayed.Record, completed.Record) {
		t.Fatalf("completePendingAttachment(replay) = %+v, %v", replayed, err)
	}
	event = append(event, replayedEvents...)
	// The rolled-back attempt and the replay both stage nothing.
	completedLog := publishStagedAttachmentEvents(t, event)
	if count := attachmentCompletedEventCount(completedLog.completedSnapshot(), created.Record.ID); count != 1 {
		t.Fatalf("attachment.completed count = %d, want 1", count)
	}

	second := validPostgresPendingCreate(uuid.Must(uuid.NewV7()), fixture.companyID, fixture.ownerID)
	secondResult, err := initiatePendingForTest(ctx, store, second)
	if err != nil {
		t.Fatal(err)
	}
	mustAttachmentExec(t, ctx, pool, `
		UPDATE company_members
		SET status='revoked', revoked_at=clock_timestamp(), revoked_by_user_id=$2,
			updated_at=clock_timestamp(), version=version+1
		WHERE company_id=$1 AND user_id=$2`, fixture.companyID, fixture.ownerID)
	if _, err := authorizeAttachmentCompletion(ctx, store.Queries(), attachmentCompletionAuthorization{
		Locator: attachmentLocator{
			ID:        secondResult.Record.ID,
			CompanyID: second.CompanyID,
		},
		RequestedByUserID: fixture.ownerID,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("authorizeAttachmentCompletion(revoked) error = %v, want ErrNotFound", err)
	}
	if _, _, err := completePendingForTest(
		ctx,
		store,
		completionForRecord(secondResult.Record),
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("completePendingAttachment(revoked) error = %v, want ErrNotFound", err)
	}
	assertAttachmentDatabaseStatus(t, ctx, pool, secondResult.Record.ID, StatusPending, 1)
}

func TestAttachmentServiceCleansIntegrityAndExpiredUploadsWithDurableEvents(t *testing.T) {
	pool, store := startAttachmentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAttachmentAuthority(t, ctx, pool)
	objects := newAttachmentObjectStorageFake()
	bus, eventLog := createdAttachmentBus(t)
	service, err := NewAttachmentService(ServiceOptions{
		Store: store, Bus: bus, Objects: objects,
		PendingTTL: defaultPendingTTL,
	})
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte("integrity payload")
	integrityInput := attachmentInitiateInputFor(fixture, payload)
	integrityInput.SizeBytes++
	if _, err := service.Initiate(
		ctx,
		integrityInput,
		bytes.NewReader(payload),
	); !errors.Is(
		err,
		ErrIntegrityMismatch,
	) {
		t.Fatalf("Initiate(integrity mismatch) error = %v", err)
	}
	integrityAttachmentID := attachmentIDForRequest(t, ctx, pool, integrityInput)
	assertAttachmentDatabaseStatus(t, ctx, pool, integrityAttachmentID, StatusDeleted, 3)
	assertAttachmentDeletedEvent(
		t, eventLog.deletedSnapshot(), integrityAttachmentID,
		events.SystemActor(), "user", fixture.ownerID, 3,
	)

	expiredPayload := []byte("expired payload")
	expiredInput := attachmentInitiateInputFor(fixture, expiredPayload)
	expiring, err := service.Initiate(ctx, expiredInput, bytes.NewReader(expiredPayload))
	if err != nil {
		t.Fatalf("Initiate(expiring) error = %v", err)
	}
	expiredAttachmentID := expiring.Attachment.ID
	expireAttachmentForCleanup(t, ctx, pool, expiredAttachmentID)
	if _, err := service.Initiate(
		ctx,
		expiredInput,
		bytes.NewReader(expiredPayload),
	); !errors.Is(
		err,
		ErrUploadExpired,
	) {
		t.Fatalf("Initiate(expired replay) error = %v", err)
	}
	assertAttachmentDatabaseStatus(t, ctx, pool, expiredAttachmentID, StatusDeleted, 3)
	assertAttachmentDeletedEvent(
		t, eventLog.deletedSnapshot(), expiredAttachmentID,
		events.SystemActor(), "user", fixture.ownerID, 3,
	)
}

func TestAttachmentDownloadAuthorizationFollowsLinkVisibility(t *testing.T) {
	pool, store := startAttachmentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAttachmentAuthority(t, ctx, pool)

	unlinked, unlinkedEvents := createReadyAttachment(t, ctx, store, fixture.companyID, fixture.ownerID)
	assertAttachmentCompletedEvent(
		t,
		publishStagedAttachmentEvents(t, unlinkedEvents).completedSnapshot(),
		unlinked.ID,
		fixture.ownerID,
	)
	assertAttachmentDownloadAuthorization(t, ctx, store, unlinked, fixture.ownerID, true)
	assertAttachmentDownloadAuthorization(t, ctx, store, unlinked, fixture.memberID, false)

	taskLinked, _ := createReadyAttachment(t, ctx, store, fixture.companyID, fixture.ownerID)
	taskMessageID := seedAttachmentCleanupMessage(t, ctx, pool, fixture, 50)
	mustAttachmentExec(t, ctx, pool, `
		INSERT INTO message_attachments(company_id,message_id,attachment_id,position)
		VALUES ($1,$2,$3,0)`, fixture.companyID, taskMessageID, taskLinked.ID)
	// A Task chat's file follows the chat's Project (ADR 0017): a member
	// without a grant on the restricted Project cannot open it; a reader can.
	assertAttachmentDownloadAuthorization(t, ctx, store, taskLinked, fixture.memberID, false)
	var projectID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM projects WHERE company_id=$1 AND key='work'`,
		fixture.companyID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	dbtest.GrantProject(t, ctx, pool, fixture.companyID, projectID, fixture.memberID)
	assertAttachmentDownloadAuthorization(t, ctx, store, taskLinked, fixture.memberID, true)

	directLinked, _ := createReadyAttachment(t, ctx, store, fixture.companyID, fixture.ownerID)
	conversationID, messageID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	mustAttachmentExec(t, ctx, pool, `
		INSERT INTO conversations(id,company_id,kind,agent_id,created_by_user_id,title)
		VALUES ($1,$2,'direct',$3,$4,'Private chat')`,
		conversationID, fixture.companyID, fixture.agentID, fixture.ownerID)
	mustAttachmentExec(t, ctx, pool, `
		INSERT INTO conversation_items(
			id,company_id,conversation_id,run_id,seq,type,author_kind,author_id,content
		)
		VALUES ($1,$2,$3,gen_random_uuid(),1,'user_message','user',$4,'Direct attachment')`,
		messageID, fixture.companyID, conversationID, fixture.ownerID)
	mustAttachmentExec(t, ctx, pool, `
		INSERT INTO message_attachments(company_id,message_id,attachment_id,position)
		VALUES ($1,$2,$3,0)`, fixture.companyID, messageID, directLinked.ID)
	assertAttachmentDownloadAuthorization(t, ctx, store, directLinked, fixture.ownerID, true)
	assertAttachmentDownloadAuthorization(t, ctx, store, directLinked, fixture.memberID, false)
}

func completionForRecord(record attachmentRecord) attachmentCompletion {
	return attachmentCompletion{
		Locator:          attachmentLocator{ID: record.ID, CompanyID: record.CompanyID},
		UploadedByUserID: record.UploadedByUserID, ObjectKey: record.ObjectKey,
		SizeBytes: record.SizeBytes, ExpectedVersion: record.Revision,
	}
}

func createReadyAttachment(
	t *testing.T,
	ctx context.Context,
	store *db.Store,
	companyID uuid.UUID,
	userID uuid.UUID,
) (attachmentRecord, []events.Event) {
	t.Helper()
	create := validPostgresPendingCreate(uuid.Must(uuid.NewV7()), companyID, userID)
	pending, err := initiatePendingForTest(ctx, store, create)
	if err != nil {
		t.Fatalf("initiatePendingAttachment() error = %v", err)
	}
	completed, event, err := completePendingForTest(ctx, store, completionForRecord(pending.Record))
	if err != nil || completed.Outcome != attachmentCompletionApplied {
		t.Fatalf("completePendingAttachment() = %+v, %v", completed, err)
	}
	return completed.Record, event
}

func assertAttachmentDatabaseStatus(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	attachmentID uuid.UUID,
	wantStatus Status,
	wantVersion int64,
) {
	t.Helper()
	var status string
	var version int64
	if err := pool.QueryRow(ctx, `SELECT status,version FROM attachments WHERE id=$1`, attachmentID).
		Scan(&status, &version); err != nil {
		t.Fatalf("read Attachment state: %v", err)
	}
	if Status(status) != wantStatus || version != wantVersion {
		t.Fatalf("Attachment state = %s/v%d, want %s/v%d", status, version, wantStatus, wantVersion)
	}
}

func attachmentDeletedEvents(
	recorded []deletedEvent,
	attachmentID uuid.UUID,
) []deletedEvent {
	var matched []deletedEvent
	for _, event := range recorded {
		if event.Payload.AttachmentID == attachmentID {
			matched = append(matched, event)
		}
	}
	return matched
}

func assertAttachmentDeletedEvent(
	t *testing.T,
	recorded []deletedEvent,
	attachmentID uuid.UUID,
	wantActor events.Actor,
	wantScopeKind string,
	wantScopeID uuid.UUID,
	wantVersion int64,
) {
	t.Helper()
	matched := attachmentDeletedEvents(recorded, attachmentID)
	if len(matched) != 1 {
		t.Fatalf("attachment.deleted events for %s = %d, want 1", attachmentID, len(matched))
	}
	event := matched[0]
	payload := event.Payload
	if event.Name != "attachment.deleted" || event.Actor != wantActor ||
		payload.AttachmentID != attachmentID || payload.Revision != wantVersion ||
		payload.CompanyID == uuid.Nil {
		t.Fatalf("attachment.deleted = %#v payload %#v", event, payload)
	}
	// Delivery is the consumer's decision now, so what the payload has to
	// carry is the fact the consumer branches on.
	switch wantScopeKind {
	case "user":
		if payload.ConversationID != nil || payload.MessageID != nil ||
			payload.ConversationKind != "" || payload.UploadedByUserID != wantScopeID {
			t.Fatalf("attachment.deleted unlinked route = %#v payload %#v", event, payload)
		}
	case "conversation":
		routable := payload.ConversationKind == "task" ||
			payload.ConversationCreatorUserID != uuid.Nil
		if payload.ConversationID == nil || *payload.ConversationID != wantScopeID ||
			payload.MessageID == nil || !routable {
			t.Fatalf("attachment.deleted linked route = %#v payload %#v", event, payload)
		}
	default:
		t.Fatalf("unknown attachment.deleted scope kind %q", wantScopeKind)
	}
	assertAttachmentRoutingStaysOffTheWire(t, payload)
}

// assertAttachmentRoutingStaysOffTheWire pins the boundary the json:"-" tags
// draw: the bridge reads the routing facts off the Go value, and no client
// ever sees them.
func assertAttachmentRoutingStaysOffTheWire(
	t *testing.T,
	payload Removal,
) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode attachment.deleted payload: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("decode attachment.deleted payload: %v", err)
	}
	for _, routing := range []string{
		"companyId", "uploadedByUserId", "conversationKind", "conversationCreatorUserId",
	} {
		if _, onTheWire := fields[routing]; onTheWire {
			t.Fatalf("attachment.deleted wire payload carries routing field %q: %s", routing, encoded)
		}
	}
}

func attachmentCompletedEvents(
	recorded []completedEvent,
	attachmentID uuid.UUID,
) []completedEvent {
	var matched []completedEvent
	for _, event := range recorded {
		if event.Payload.ID == attachmentID {
			matched = append(matched, event)
		}
	}
	return matched
}

func assertAttachmentCompletedEvent(
	t *testing.T,
	recorded []completedEvent,
	attachmentID uuid.UUID,
	uploaderID uuid.UUID,
) {
	t.Helper()
	matched := attachmentCompletedEvents(recorded, attachmentID)
	if len(matched) != 1 {
		t.Fatalf("attachment.completed events for %s = %d, want 1", attachmentID, len(matched))
	}
	event := matched[0]
	payload := event.Payload
	// The projection already names both routing facts, on the wire.
	if event.Name != "attachment.completed" || payload.UploadedByUserID != uploaderID ||
		payload.CompanyID == uuid.Nil {
		t.Fatalf("attachment.completed = %#v payload %#v", event, payload)
	}
}

func attachmentCompletedEventCount(
	recorded []completedEvent,
	attachmentID uuid.UUID,
) int {
	return len(attachmentCompletedEvents(recorded, attachmentID))
}

func assertAttachmentDownloadAuthorization(
	t *testing.T,
	ctx context.Context,
	store *db.Store,
	record attachmentRecord,
	userID uuid.UUID,
	wantAllowed bool,
) {
	t.Helper()
	got, err := authorizeAttachmentDownload(ctx, store.Queries(), attachmentDownloadAuthorization{
		Locator: attachmentLocator{ID: record.ID, CompanyID: record.CompanyID}, RequestedByUserID: userID,
	})
	if wantAllowed {
		if err != nil || got.ID != record.ID || got.Status != StatusReady {
			t.Fatalf("authorizeAttachmentDownload(%s) = %+v, %v", userID, got, err)
		}
		return
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("authorizeAttachmentDownload(%s) error = %v, want ErrNotFound", userID, err)
	}
}

func validPostgresPendingCreate(requestKey, companyID, userID uuid.UUID) pendingAttachmentCreate {
	return pendingAttachmentCreate{
		RequestKey: requestKey, CompanyID: companyID, UploadedByUserID: userID,
		Filename: "attachment.txt", ContentType: "text/plain", SizeBytes: 3,
		PendingTTL: defaultPendingTTL,
	}
}

func startAttachmentPostgres(t *testing.T) (*pgxpool.Pool, *db.Store) {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return pool, store
}
