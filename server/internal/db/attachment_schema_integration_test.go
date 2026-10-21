//go:build integration

package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAttachmentSchemaEnforcesIdentityContentAndFiniteLifecycle(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	userID, companyID := seedAttachmentSchemaOwner(t, ctx, pool)
	attachmentID := dbtest.NewUUID(t)
	objectKey := "companies/" + companyID.String() + "/attachments/" + attachmentID.String()
	createdAt := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	expiresAt := createdAt.Add(time.Hour)
	if _, err := pool.Exec(ctx, `
		INSERT INTO attachments
			(id, company_id, uploaded_by_user_id, request_key,
			 filename, content_type, size_bytes, status, upload_lease_expires_at,
			 created_at, updated_at, version)
		VALUES ($1,$2,$3,$4,'report.txt','text/plain',0,
			'pending',$5,$6,$6,1)`,
		attachmentID, companyID, userID, dbtest.NewUUID(t), expiresAt, createdAt); err != nil {
		t.Fatalf("insert attachment without storage authority or checksum columns: %v", err)
	}
	// object_key is schema-derived: the generated column must produce the
	// canonical storage path without the client supplying it.
	var generatedObjectKey string
	if err := pool.QueryRow(
		ctx,
		`SELECT object_key FROM attachments WHERE id=$1`, attachmentID,
	).Scan(&generatedObjectKey); err != nil {
		t.Fatalf("read generated object_key: %v", err)
	}
	if generatedObjectKey != objectKey {
		t.Fatalf("generated object_key = %q, want %q", generatedObjectKey, objectKey)
	}

	// status is the one attachment rule still in the schema: a closed
	// vocabulary. The lifecycle it belongs to — which timestamps each status
	// requires, and which version value goes with it — is the attachment
	// service's, which can say why a transition was refused.
	assertStatementRejected(t, ctx, pool, "23514", `
		UPDATE attachments SET status='failed' WHERE id=$1`, attachmentID)

	completedAt := createdAt.Add(10 * time.Minute)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE attachments
		SET status='ready', upload_lease_expires_at=NULL,
			updated_at=$2, version=2
		WHERE id=$1`, attachmentID, completedAt)
	deletionRequestedAt := completedAt.Add(time.Minute)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE attachments
		SET status='deleting', updated_at=$2, version=3
		WHERE id=$1`, attachmentID, deletionRequestedAt)
	deletedAt := deletionRequestedAt.Add(time.Minute)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE attachments
		SET status='deleted', updated_at=$2, version=4
		WHERE id=$1`, attachmentID, deletedAt)
}

func TestAttachmentSchemaAllowsEachAttachmentToLinkToOnlyOneMessage(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	userID, companyID := seedAttachmentSchemaOwner(t, ctx, pool)
	agentID, conversationID := dbtest.NewUUID(t), dbtest.NewUUID(t)
	runtimeID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes (id,company_id,daemon_id,name,provider,status)
		VALUES ($1,$2,'daemon-attachment','Attachment Runtime','codex','online')`,
		runtimeID, companyID)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO agents (id,company_id,name,title,provider)
		VALUES ($1,$2,'CEO','CEO','codex')`, agentID, companyID)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversations
			(id,company_id,kind,agent_id,title,created_by_user_id)
		VALUES ($1,$2,'direct',$3,'Attachment chat',$4)`,
		conversationID, companyID, agentID, userID)
	// A user's turn is a conversation_item, and every item carries the Run that
	// consumed it. runs.id is a bigserial, so the sequence allocates it.
	var runID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs (company_id,agent_id,conversation_id,runtime_id, run_type)
		VALUES ($1,$2,$3,$4, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
		RETURNING id`, companyID, agentID, conversationID, runtimeID).Scan(&runID); err != nil {
		t.Fatalf("insert attachment conversation run: %v", err)
	}
	messageA, messageB := dbtest.NewUUID(t), dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversation_items
			(id,company_id,conversation_id,run_id,seq,type,author_kind,author_id,content)
		VALUES ($1,$3,$4,$6,1,'user_message','user',$5,'first'),
			($2,$3,$4,$6,2,'user_message','user',$5,'second')`,
		messageA, messageB, companyID, conversationID, userID, runID)
	attachmentID := dbtest.NewUUID(t)
	now := time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO attachments
			(id,company_id,uploaded_by_user_id,request_key,filename,
			 content_type,size_bytes,status,created_at,updated_at,version)
		VALUES ($1,$2,$3,$4,'linked.txt','text/plain',1,'ready',$5,$5,2)`,
		attachmentID, companyID, userID, dbtest.NewUUID(t),
		now); err != nil {
		t.Fatalf("insert linked attachment without storage authority or checksum columns: %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO message_attachments(company_id,message_id,attachment_id,position)
		VALUES ($1,$2,$3,0)`, companyID, messageA, attachmentID)
	assertStatementRejected(t, ctx, pool, "23505", `
		INSERT INTO message_attachments(company_id,message_id,attachment_id,position)
		VALUES ($1,$2,$3,0)`, companyID, messageB, attachmentID)
}

func TestAttachmentSchemaHasOnlyPurposeBuiltCleanupIndexes(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	rows, err := pool.Query(ctx, `
		SELECT indexrelid::regclass::text,
		       pg_get_indexdef(indexrelid),
		       pg_get_expr(indpred, indrelid)
		FROM pg_index
		WHERE indrelid='attachments'::regclass
		  AND indexrelid::regclass::text LIKE 'attachments_%cleanup_idx'
		ORDER BY indexrelid::regclass::text`)
	if err != nil {
		t.Fatalf("read Attachment cleanup indexes: %v", err)
	}
	defer rows.Close()
	type indexShape struct {
		definition string
		predicate  string
	}
	indexes := make(map[string]indexShape)
	for rows.Next() {
		var name, definition, predicate string
		if err := rows.Scan(&name, &definition, &predicate); err != nil {
			t.Fatalf("scan Attachment cleanup index: %v", err)
		}
		indexes[name] = indexShape{definition: definition, predicate: predicate}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate Attachment cleanup indexes: %v", err)
	}
	want := map[string]struct {
		columns   string
		predicate string
	}{
		"attachments_pending_expiry_cleanup_idx": {
			columns: "(upload_lease_expires_at, id)", predicate: "status = 'pending'",
		},
		"attachments_ready_abandonment_cleanup_idx": {
			columns: "(updated_at, id)", predicate: "status = 'ready'",
		},
		"attachments_deleting_retry_cleanup_idx": {
			columns:   "(updated_at, id)",
			predicate: "status = 'deleting'",
		},
	}
	if len(indexes) != len(want) {
		t.Fatalf("Attachment cleanup indexes = %#v, want %d purpose-built indexes", indexes, len(want))
	}
	for name, expected := range want {
		shape, exists := indexes[name]
		if !exists || !strings.Contains(shape.definition, expected.columns) ||
			!strings.Contains(shape.predicate, expected.predicate) {
			t.Fatalf("Attachment cleanup index %s = %#v, want columns %s predicate %s",
				name, shape, expected.columns, expected.predicate)
		}
	}
	var redundant bool
	if err := pool.QueryRow(ctx, `
		SELECT to_regclass('attachments_company_status_idx') IS NOT NULL`).Scan(&redundant); err != nil {
		t.Fatalf("check redundant Attachment status index: %v", err)
	}
	if redundant {
		t.Fatal("redundant broad Attachment status index remains")
	}
}

func seedAttachmentSchemaOwner(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	// Kept local to this schema suite so the attachment constraints remain
	// readable without the much larger collaboration fixture.
	userID, companyID := dbtest.NewUUID(t), dbtest.NewUUID(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO users(id,email,display_name) VALUES ($1,$2,'Attachment Owner')`,
		userID, userID.String()+"@attachments.zenith-team.test"); err != nil {
		t.Fatalf("seed attachment schema user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO companies(id,name,slug,task_prefix)
		VALUES ($1,'Attachment Company',$2,'ATT')`,
		companyID, "attachments-"+strings.ReplaceAll(companyID.String(), "-", "")); err != nil {
		t.Fatalf("seed attachment schema company: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO company_members(company_id,user_id,role) VALUES ($1,$2,'owner')`,
		companyID, userID); err != nil {
		t.Fatalf("seed attachment schema membership: %v", err)
	}
	return userID, companyID
}
