//go:build integration

package attachment

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/infra/storage"
)

func TestAttachmentCleanupWorkerDeletesExpiredPendingUpload(t *testing.T) {
	pool, store := startAttachmentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAttachmentAuthority(t, ctx, pool)
	service, objects, eventLog := newAttachmentCleanupStack(t, store)
	worker, err := NewCleanupWorker(store, service, CleanupWorkerOptions{
		BatchSize: 8,
	})
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte("abandoned pending upload")
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Initiate() error = %v", err)
	}
	attachmentID := initiated.Attachment.ID
	expireAttachmentForCleanup(t, ctx, pool, attachmentID)
	completed, err := worker.CleanupOnce(ctx)
	if err != nil || completed != 1 {
		t.Fatalf("CleanupOnce() = %d, %v", completed, err)
	}
	assertAttachmentDatabaseStatus(t, ctx, pool, attachmentID, StatusDeleted, 3)
	assertAttachmentDeletedEvent(
		t, eventLog.deletedSnapshot(), attachmentID, events.SystemActor(), "user", fixture.ownerID, 3,
	)
	reader, err := objects.GetReader(ctx, canonicalObjectKey(input.CompanyID, attachmentID))
	closeAttachmentReaderBestEffort(reader)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetReader(cleaned object) error = %v, want ErrNotFound", err)
	}
}

func TestAttachmentCleanupWorkerRetriesDurableDeletingRow(t *testing.T) {
	pool, store := startAttachmentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAttachmentAuthority(t, ctx, pool)
	service, objects, _ := newAttachmentCleanupStack(t, store)
	payload := []byte("retry deleting upload")
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Initiate() error = %v", err)
	}
	attachmentID := initiated.Attachment.ID
	expireAttachmentForCleanup(t, ctx, pool, attachmentID)

	objects.setDeleteErr(errors.New("private storage failure"))
	worker, err := NewCleanupWorker(store, service, CleanupWorkerOptions{
		BatchSize: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed, err := worker.CleanupOnce(ctx); completed != 0 || !errors.Is(err, errCleanupObjectDelete) {
		t.Fatalf("first CleanupOnce() = %d, %v", completed, err)
	}
	assertAttachmentDatabaseStatus(t, ctx, pool, attachmentID, StatusDeleting, 2)

	objects.setDeleteErr(nil)
	if completed, err := worker.CleanupOnce(ctx); completed != 1 || err != nil {
		t.Fatalf("second CleanupOnce() = %d, %v", completed, err)
	}
	assertAttachmentDatabaseStatus(t, ctx, pool, attachmentID, StatusDeleted, 3)
}

func TestAttachmentCleanupWorkerDeletesOnlyOldUnlinkedReadyUploads(t *testing.T) {
	pool, store := startAttachmentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAttachmentAuthority(t, ctx, pool)
	service, objects, eventLog := newAttachmentCleanupStack(t, store)
	worker, err := NewCleanupWorker(store, service, CleanupWorkerOptions{
		BatchSize: 2, AbandonedReadyGrace: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	oldLinked := uploadReadyAttachmentForCleanup(t, ctx, store, service, fixture, []byte("old linked"))
	oldAsset := uploadReadyAttachmentForCleanup(t, ctx, store, service, fixture, []byte("old asset"))
	oldOrphan := uploadReadyAttachmentForCleanup(t, ctx, store, service, fixture, []byte("old orphan"))
	youngOrphan := uploadReadyAttachmentForCleanup(t, ctx, store, service, fixture, []byte("young orphan"))
	for _, id := range []uuid.UUID{oldLinked.ID, oldAsset.ID, oldOrphan.ID} {
		ageReadyAttachmentForCleanup(t, ctx, pool, id, 48*time.Hour)
	}
	linkReadyAttachmentForCleanup(t, ctx, pool, fixture, oldLinked.ID, 1)
	linkReadyAttachmentToAssetVersionForCleanup(t, ctx, pool, fixture, oldAsset.ID)

	completed, err := worker.CleanupOnce(ctx)
	if err != nil || completed != 1 {
		t.Fatalf("CleanupOnce() = %d, %v", completed, err)
	}
	assertAttachmentDatabaseStatus(t, ctx, pool, oldOrphan.ID, StatusDeleted, 4)
	assertAttachmentDeletedEvent(
		t, eventLog.deletedSnapshot(), oldOrphan.ID, events.SystemActor(), "user", fixture.ownerID, 4,
	)
	for _, retained := range []attachmentRecord{oldLinked, oldAsset, youngOrphan} {
		assertAttachmentDatabaseStatus(t, ctx, pool, retained.ID, StatusReady, 2)
		objects.mustObject(t, retained.ObjectKey)
	}
	if objects.hasObject(oldOrphan.ObjectKey) {
		t.Fatal("abandoned ready object remains after terminal cleanup")
	}
}

func TestAttachmentCleanupWorkerPreparesBothPendingAndReadyCandidatesBeforeClaim(t *testing.T) {
	pool, store := startAttachmentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAttachmentAuthority(t, ctx, pool)
	service, _, _ := newAttachmentCleanupStack(t, store)

	expiredPayload := []byte("expired pending")
	expired := attachmentInitiateInputFor(fixture, expiredPayload)
	initiated, err := service.Initiate(ctx, expired, bytes.NewReader(expiredPayload))
	if err != nil {
		t.Fatalf("Initiate(expired) error = %v", err)
	}
	expiredID := initiated.Attachment.ID
	expireAttachmentForCleanup(t, ctx, pool, expiredID)
	abandoned := uploadReadyAttachmentForCleanup(t, ctx, store, service, fixture, []byte("abandoned ready"))
	ageReadyAttachmentForCleanup(t, ctx, pool, abandoned.ID, 48*time.Hour)

	offered := make([]uuid.UUID, 0, 2)
	processor := &cleanupProcessorStub{cleanup: func(_ context.Context, input CleanupInput) error {
		offered = append(offered, input.ID)
		return nil
	}}
	worker, err := NewCleanupWorker(store, processor, CleanupWorkerOptions{
		BatchSize: 2, AbandonedReadyGrace: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := worker.CleanupOnce(ctx)
	if err != nil || completed != 2 {
		t.Fatalf("CleanupOnce() = %d, %v", completed, err)
	}
	if len(offered) != 2 {
		t.Fatalf("cleanup candidates = %v, want both prepared rows", offered)
	}
	assertAttachmentDatabaseStatus(t, ctx, pool, expiredID, StatusDeleting, 2)
	assertAttachmentDatabaseStatus(t, ctx, pool, abandoned.ID, StatusDeleting, 3)
}

func uploadReadyAttachmentForCleanup(
	t *testing.T,
	ctx context.Context,
	store *db.Store,
	service *DefaultAttachmentService,
	fixture attachmentAuthorityFixture,
	payload []byte,
) attachmentRecord {
	t.Helper()
	input := attachmentInitiateInputFor(fixture, payload)
	initiated, err := service.Initiate(ctx, input, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Initiate() error = %v", err)
	}
	attachmentID := initiated.Attachment.ID
	if _, err := service.Complete(ctx, CompleteInput{
		ID: attachmentID, CompanyID: input.CompanyID, UploadedByUserID: input.UploadedByUserID,
	}); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	record, err := getAttachment(
		ctx, store.Queries(), attachmentLocator{ID: attachmentID, CompanyID: input.CompanyID},
	)
	if err != nil {
		t.Fatalf("get ready Attachment: %v", err)
	}
	return record
}

func ageReadyAttachmentForCleanup(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	attachmentID uuid.UUID,
	age time.Duration,
) {
	t.Helper()
	mustAttachmentExec(t, ctx, pool, `
		WITH cleanup_clock AS MATERIALIZED (SELECT clock_timestamp() AS now_at)
		UPDATE attachments
		SET created_at=cleanup_clock.now_at-$2::bigint*interval '1 millisecond'-interval '1 hour',
			updated_at=cleanup_clock.now_at-$2::bigint*interval '1 millisecond'
		FROM cleanup_clock
		WHERE id=$1 AND status='ready'`, attachmentID, int64(age/time.Millisecond))
}

func seedAttachmentCleanupMessage(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture attachmentAuthorityFixture,
	suffix int,
) uuid.UUID {
	t.Helper()
	conversationID, taskID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	messageID := uuid.Must(uuid.NewV7())
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin attachment conversation fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO projects(company_id,key,title) VALUES ($1,'work','Work') ON CONFLICT (company_id,key) DO NOTHING`,
		fixture.companyID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO conversations(id,company_id,kind,task_id,title,project_id)
		VALUES ($1,$2,'task',$3,$4,(SELECT id FROM projects WHERE company_id=$2 AND key='work'))`,
		conversationID, fixture.companyID, taskID, fmt.Sprintf("Cleanup %d", suffix)); err != nil {
		t.Fatalf("insert attachment fixture conversation: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tasks(project_id,
			id,company_id,conversation_id,number,key,title,
						assignee_user_id,creator_user_id,
			milestone_type
		)
		VALUES ((SELECT id FROM projects WHERE company_id=$2 AND key='work'), $1,$2,$3,$4::bigint,$5,$6,$7,$7,'operations')`,
		taskID, fixture.companyID, conversationID, int64(suffix),
		fmt.Sprintf("ATT-%d", suffix), fmt.Sprintf("Cleanup %d", suffix), fixture.ownerID,
	); err != nil {
		t.Fatalf("insert attachment fixture task: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit attachment conversation fixture: %v", err)
	}
	mustAttachmentExec(t, ctx, pool, `
		INSERT INTO conversation_items(
			id,company_id,conversation_id,run_id,seq,type,author_kind,author_id,content
		)
		VALUES ($1,$2,$3,gen_random_uuid(),1,'user_message','user',$4,$5)`,
		messageID, fixture.companyID, conversationID, fixture.ownerID,
		fmt.Sprintf("Attachment %d", suffix))
	return messageID
}

func linkReadyAttachmentForCleanup(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture attachmentAuthorityFixture,
	attachmentID uuid.UUID,
	suffix int,
) {
	t.Helper()
	messageID := seedAttachmentCleanupMessage(t, ctx, pool, fixture, suffix)
	mustAttachmentExec(t, ctx, pool, `
		INSERT INTO message_attachments(company_id,message_id,attachment_id,position)
		VALUES ($1,$2,$3,0)`, fixture.companyID, messageID, attachmentID)
}

func linkReadyAttachmentToAssetVersionForCleanup(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture attachmentAuthorityFixture,
	attachmentID uuid.UUID,
) {
	t.Helper()
	conversationID := uuid.Must(uuid.NewV7())
	mustAttachmentExec(t, ctx, pool, `
		INSERT INTO conversations(id,company_id,kind,agent_id,created_by_user_id,title)
		VALUES ($1,$2,'direct',$3,$4,'Asset-backed output')`,
		conversationID, fixture.companyID, fixture.agentID, fixture.ownerID)
	var runID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		INSERT INTO runs(company_id,agent_id,conversation_id,runtime_id,status, run_type)
		VALUES ($1,$2,$3,$4,'completed', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
		RETURNING id`,
		fixture.companyID, fixture.agentID, conversationID, fixture.runtimeID,
	).Scan(&runID); err != nil {
		t.Fatalf("insert Asset source Run: %v", err)
	}
	assetID := uuid.Must(uuid.NewV7())
	mustAttachmentExec(t, ctx, pool, `
		INSERT INTO assets(id,company_id,title,media_class,created_by_user_id)
		VALUES ($1,$2,'Output asset','file',$3)`,
		assetID, fixture.companyID, fixture.ownerID)
	mustAttachmentExec(t, ctx, pool, `
		INSERT INTO asset_versions(
			company_id,asset_id,version_number,attachment_id,source_run_id,
			filename,content_type,size_bytes,integrity_status
		)
		SELECT company_id,$2,1,id,$3,filename,content_type,size_bytes,'verified'
		FROM attachments
		WHERE id=$1`, attachmentID, assetID, runID)
}

func expireAttachmentForCleanup(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	attachmentID uuid.UUID,
) {
	t.Helper()
	mustAttachmentExec(t, ctx, pool, `
		WITH cleanup_clock AS MATERIALIZED (SELECT clock_timestamp() AS now_at)
		UPDATE attachments
		SET created_at=cleanup_clock.now_at-interval '2 hours',
			updated_at=cleanup_clock.now_at-interval '2 hours',
			upload_lease_expires_at=cleanup_clock.now_at-interval '1 hour'
		FROM cleanup_clock
		WHERE id=$1`, attachmentID)
}

func attachmentDeletionEventCount(
	recorded []deletedEvent,
	attachmentID uuid.UUID,
) int {
	return len(attachmentDeletedEvents(recorded, attachmentID))
}
