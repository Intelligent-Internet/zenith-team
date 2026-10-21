package attachment

// Helpers shared by this package's unit tests and its `//go:build integration`
// suites. They live in an untagged file on purpose: declared inside a tagged
// file, the untagged build of this package would fail with `undefined:`.

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
)

type attachmentAuthorityFixture struct {
	ownerID   uuid.UUID
	memberID  uuid.UUID
	companyID uuid.UUID
	runtimeID uuid.UUID
	agentID   uuid.UUID
}

func seedAttachmentAuthority(t *testing.T, ctx context.Context, pool *pgxpool.Pool) attachmentAuthorityFixture {
	t.Helper()
	fixture := attachmentAuthorityFixture{
		ownerID: uuid.Must(uuid.NewV7()), memberID: uuid.Must(uuid.NewV7()),
		companyID: uuid.Must(uuid.NewV7()), runtimeID: uuid.Must(uuid.NewV7()),
		agentID: uuid.Must(uuid.NewV7()),
	}
	mustAttachmentExec(t, ctx, pool, `
		INSERT INTO users(id,email,display_name) VALUES
			($1,$3,'Attachment Owner'),($2,$4,'Attachment Member')`,
		fixture.ownerID, fixture.memberID,
		fixture.ownerID.String()+"@attachments.zenith-team.test",
		fixture.memberID.String()+"@attachments.zenith-team.test")
	mustAttachmentExec(t, ctx, pool, `
		INSERT INTO companies(id,name,slug,task_prefix)
		VALUES ($1,'Attachment Company',$2,'ATT')`,
		fixture.companyID, "attachment-"+fixture.companyID.String())
	mustAttachmentExec(t, ctx, pool, `
		INSERT INTO runtimes(id,company_id,daemon_id,name,provider,status,owner_user_id)
		VALUES ($1,$2,$3,'Attachment Runtime','codex','online',$4)`,
		fixture.runtimeID, fixture.companyID,
		"daemon-"+fixture.companyID.String(), fixture.ownerID)
	mustAttachmentExec(t, ctx, pool, `
		INSERT INTO company_members(company_id,user_id,role) VALUES
			($1,$2,'owner'),($1,$3,'member')`,
		fixture.companyID, fixture.ownerID, fixture.memberID)
	mustAttachmentExec(t, ctx, pool, `
				INSERT INTO agents(id,company_id,name,title,provider)
		VALUES ($1,$2,'Attachment Agent','Operations','codex')`,
		fixture.agentID, fixture.companyID)
	mustAttachmentExec(t, ctx, pool, `
		INSERT INTO agent_runtime_bindings(company_id,agent_id,user_id,runtime_id,provider)
		VALUES ($1,$2,$3,$4,'codex')`,
		fixture.companyID, fixture.agentID, fixture.ownerID, fixture.runtimeID)
	return fixture
}

func mustAttachmentExec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, statement string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, statement, args...); err != nil {
		t.Fatalf("execute Attachment test statement: %v", err)
	}
}

func newAttachmentCleanupStack(
	t *testing.T,
	store *db.Store,
) (*DefaultAttachmentService, *attachmentObjectStorageFake, *attachmentEventLog) {
	t.Helper()
	objects := newAttachmentObjectStorageFake()
	bus, eventLog := createdAttachmentBus(t)
	service, err := NewAttachmentService(ServiceOptions{Store: store, Bus: bus, Objects: objects})
	if err != nil {
		t.Fatalf("NewAttachmentService() error = %v", err)
	}
	return service, objects, eventLog
}
