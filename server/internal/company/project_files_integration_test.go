//go:build integration

package company_test

import (
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"errors"
	"github.com/google/uuid"
	"testing"
)

func TestProjectFilesAtomicOwnershipAndRetention(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var user, tenant uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users(email,display_name) VALUES ('project-files@test.invalid','Owner') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO companies(name,slug,task_prefix) VALUES ('Files','files','FIL') RETURNING id`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO company_members(company_id,user_id,role) VALUES ($1,$2,'owner')`, tenant, user); err != nil {
		t.Fatal(err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := company.NewProjectService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.UserPrincipal{UserID: user, CompanyID: tenant, Role: auth.CompanyRoleOwner}
	var file uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO attachments(company_id,uploaded_by_user_id,request_key,filename,content_type,size_bytes,status,version) VALUES ($1,$2,gen_random_uuid(),'brief.txt','text/plain',10,'ready',2) RETURNING id`, tenant, user).Scan(&file); err != nil {
		t.Fatal(err)
	}
	files := &company.ProjectFilesInput{DocumentIDs: []uuid.UUID{file}}
	project, err := service.Create(ctx, principal, company.ProjectCreateInput{Key: "files", Title: "Files", Files: files})
	if err != nil {
		t.Fatal(err)
	}
	if len(project.Files.Documents) != 1 || project.Files.Documents[0].ID != file {
		t.Fatalf("files = %+v", project.Files)
	}

	var reader uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users(email,display_name) VALUES ('project-reader@test.invalid','Reader') RETURNING id`).Scan(&reader); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO company_members(company_id,user_id,role) VALUES ($1,$2,'member')`, tenant, reader); err != nil {
		t.Fatal(err)
	}
	q := generated.New(pool)
	// A Project file follows the Project's readers (ADR 0017): a member with
	// no grant on the restricted Project cannot open it, a reader can.
	if _, err := q.AuthorizeAttachmentDownload(ctx, generated.AuthorizeAttachmentDownloadParams{CompanyID: tenant, ActorUserID: reader, AttachmentID: file}); err == nil {
		t.Fatal("a member without project.read opened the Project document")
	}
	dbtest.GrantProject(t, ctx, pool, tenant, project.ID, reader, "project.read")
	if _, err := q.AuthorizeAttachmentDownload(ctx, generated.AuthorizeAttachmentDownloadParams{CompanyID: tenant, ActorUserID: reader, AttachmentID: file}); err != nil {
		t.Fatalf("Project reader cannot read Project document: %v", err)
	}
	var foreignCompany, foreignFile uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO companies(name,slug,task_prefix) VALUES ('Other','other-files','OTH') RETURNING id`).Scan(&foreignCompany); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO attachments(company_id,uploaded_by_user_id,request_key,filename,content_type,size_bytes,status,version) VALUES ($1,$2,gen_random_uuid(),'other.txt','text/plain',10,'ready',2) RETURNING id`, foreignCompany, user).Scan(&foreignFile); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReplaceFiles(ctx, principal, project.ID, company.ProjectFilesInput{DocumentIDs: []uuid.UUID{foreignFile}}, project.Revision); !errors.Is(err, company.ErrProjectInvalidInput) {
		t.Fatalf("foreign file accepted: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE attachments SET updated_at=clock_timestamp()-interval '2 days' WHERE id=$1`, file); err != nil {
		t.Fatal(err)
	}
	candidates, err := q.ClaimAbandonedReadyAttachmentCandidates(ctx, generated.ClaimAbandonedReadyAttachmentCandidatesParams{BatchSize: 100, AbandonedGraceMilliseconds: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate == file {
			t.Fatal("Project input claimed for cleanup")
		}
	}
	invalid := &company.ProjectFilesInput{DocumentIDs: []uuid.UUID{uuid.New()}}
	if _, err := service.Create(ctx, principal, company.ProjectCreateInput{Key: "invalid", Title: "Invalid", Files: invalid}); !errors.Is(err, company.ErrProjectInvalidInput) {
		t.Fatalf("invalid upload = %v", err)
	}
	items, err := service.List(ctx, principal, true)
	if err != nil || len(items) != 1 {
		t.Fatalf("atomic create = %v, %v", items, err)
	}
	updated, err := service.ReplaceFiles(ctx, principal, project.ID, company.ProjectFilesInput{DocumentIDs: []uuid.UUID{}}, project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	// A replace against the revision read before that one is stale.
	if _, err := service.ReplaceFiles(ctx, principal, project.ID, company.ProjectFilesInput{DocumentIDs: []uuid.UUID{file}}, project.Revision); !errors.Is(err, company.ErrProjectConflict) {
		t.Fatalf("stale files replace = %v", err)
	}

	if _, err := q.AuthorizeAttachmentDownload(ctx, generated.AuthorizeAttachmentDownloadParams{CompanyID: tenant, ActorUserID: reader, AttachmentID: file}); err == nil {
		t.Fatal("removed Project document still grants member access")
	}
	cleaned, err := q.TransitionClaimedAbandonedReadyAttachments(ctx, generated.TransitionClaimedAbandonedReadyAttachmentsParams{AttachmentIds: []uuid.UUID{file}, AbandonedGraceMilliseconds: 1000})
	if err != nil || len(cleaned) != 0 {
		t.Fatalf("retired input cleanup = %+v, %v", cleaned, err)
	}
	if len(updated.Files.Documents) != 0 {
		t.Fatalf("removed files = %+v", updated.Files)
	}
	var retained bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_files WHERE project_id=$1 AND attachment_id=$2 AND removed_at IS NOT NULL)`, project.ID, file).Scan(&retained); err != nil || !retained {
		t.Fatalf("retained = %v, %v", retained, err)
	}
	// An unchanged selection writes nothing: the revision stays where it is.
	again, err := service.ReplaceFiles(ctx, principal, project.ID, company.ProjectFilesInput{DocumentIDs: []uuid.UUID{}}, updated.Revision)
	if err != nil || again.Revision != updated.Revision || updated.Revision != project.Revision+1 {
		t.Fatalf("unchanged files = %+v (%v), after replace %d, created %d", again, err, updated.Revision, project.Revision)
	}
}
