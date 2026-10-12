//go:build integration

package company

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestCompanyAvatarOwnershipRevisionAccessAndRetention(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, eventLog := recordedCompanyBus(t)
	service, err := NewCompanyService(ServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA}
	if _, err := pool.Exec(ctx, `INSERT INTO company_members(company_id,user_id,role) VALUES ($1,$2,'member')`, fixture.CompanyA, fixture.UserB); err != nil {
		t.Fatal(err)
	}
	seedUpload := func(companyID, userID uuid.UUID, contentType string, size int64, status string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		err := pool.QueryRow(ctx, `INSERT INTO attachments(company_id,uploaded_by_user_id,request_key,filename,content_type,size_bytes,status,version,upload_lease_expires_at)
			VALUES ($1,$2,gen_random_uuid(),'avatar',$3,$4,$5,CASE WHEN $5='pending' THEN 1 ELSE 2 END,
			CASE WHEN $5='pending' THEN clock_timestamp()+interval '1 hour' END) RETURNING id`, companyID, userID, contentType, size, status).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	heldUpload := func(hold string, holder uuid.UUID) uuid.UUID {
		t.Helper()
		id := seedUpload(fixture.CompanyA, fixture.UserA, "image/png", 10, "ready")
		if _, err := pool.Exec(ctx, hold, fixture.CompanyA, holder, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	avatar := seedUpload(fixture.CompanyA, fixture.UserA, "image/png", 1024, "ready")
	input := UpdateProfileInput{ExpectedRevision: 1, Name: "Company avatar", AvatarAttachmentID: &uuid.NullUUID{UUID: avatar, Valid: true}}
	updated, err := service.UpdateProfile(ctx, principal, input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.AvatarAttachmentID == nil || *updated.AvatarAttachmentID != avatar || updated.Revision != 2 {
		t.Fatalf("avatar update = %+v", updated)
	}
	if payload := payloadOf[Response](t, eventLog.snapshot()[0]); payload.AvatarAttachmentID == nil || *payload.AvatarAttachmentID != avatar {
		t.Fatalf("event avatar = %v, want %v", payload.AvatarAttachmentID, avatar)
	}
	q := generated.New(pool)
	read := generated.AuthorizeAttachmentDownloadParams{CompanyID: fixture.CompanyA, ActorUserID: fixture.UserB, AttachmentID: avatar}
	if _, err := q.AuthorizeAttachmentDownload(ctx, read); err != nil {
		t.Fatalf("member cannot read avatar: %v", err)
	}
	// Linked into a Task chat after it became the avatar, the file follows
	// that chat's readers: UserB does not read Project A, so it closes.
	linked, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := linked.Exec(ctx, `INSERT INTO message_attachments (company_id, message_id, attachment_id, position)
		VALUES ($1, $2, $3, 0)`, fixture.CompanyA, fixture.MessageA, avatar); err != nil {
		t.Fatal(err)
	}
	if _, err := generated.New(linked).AuthorizeAttachmentDownload(ctx, read); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("avatar linked into a Task chat, non-reader access = %v, want no rows", err)
	}
	if err := linked.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	input.ExpectedRevision = 2
	input.AvatarAttachmentID = nil
	unchanged, err := service.UpdateProfile(ctx, principal, input)
	if err != nil || unchanged.Revision != 2 || unchanged.AvatarAttachmentID == nil || *unchanged.AvatarAttachmentID != avatar {
		t.Fatalf("omitted avatar = %+v, %v", unchanged, err)
	}
	for _, test := range []struct {
		name string
		id   uuid.UUID
	}{
		{"foreign company", seedUpload(fixture.CompanyB, fixture.UserA, "image/png", 10, "ready")},
		{"another uploader", seedUpload(fixture.CompanyA, fixture.UserB, "image/png", 10, "ready")},
		{"pending", seedUpload(fixture.CompanyA, fixture.UserA, "image/png", 10, "pending")},
		{"non image", seedUpload(fixture.CompanyA, fixture.UserA, "text/plain", 10, "ready")},
		{"SVG", seedUpload(fixture.CompanyA, fixture.UserA, "image/svg+xml", 10, "ready")},
		{"too large", seedUpload(fixture.CompanyA, fixture.UserA, "image/png", 5<<20+1, "ready")},
		// Every member opens the avatar, so a file a Project or a Task chat
		// holds must not become it (ADR 0017).
		{"Project file", heldUpload(`INSERT INTO project_files (company_id, project_id, attachment_id, file_type)
			VALUES ($1, $2, $3, 'document')`, fixture.ProjectA)},
		{"Task chat file", heldUpload(`INSERT INTO message_attachments (company_id, message_id, attachment_id, position)
			VALUES ($1, $2, $3, 0)`, fixture.MessageA)},
	} {
		t.Run(test.name, func(t *testing.T) {
			input.AvatarAttachmentID = &uuid.NullUUID{UUID: test.id, Valid: true}
			if _, err := service.UpdateProfile(ctx, principal, input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("invalid avatar error = %v", err)
			}
		})
	}
	if _, err := pool.Exec(ctx, `UPDATE attachments SET updated_at=clock_timestamp()-interval '2 days' WHERE id=$1`, avatar); err != nil {
		t.Fatal(err)
	}
	candidates, err := q.ClaimAbandonedReadyAttachmentCandidates(ctx, generated.ClaimAbandonedReadyAttachmentCandidatesParams{BatchSize: 100, AbandonedGraceMilliseconds: 1000})
	if err != nil || slices.Contains(candidates, avatar) {
		t.Fatalf("avatar cleanup candidates = %v, %v", candidates, err)
	}
	cleaned, err := q.TransitionClaimedAbandonedReadyAttachments(ctx, generated.TransitionClaimedAbandonedReadyAttachmentsParams{AttachmentIds: []uuid.UUID{avatar}, AbandonedGraceMilliseconds: 1000})
	if err != nil || len(cleaned) != 0 {
		t.Fatalf("avatar cleanup transition = %v, %v", cleaned, err)
	}
	input.AvatarAttachmentID = &uuid.NullUUID{}
	input.ExpectedRevision = 1
	_, err = service.UpdateProfile(ctx, principal, input)
	var conflict *ProfileRevisionConflictError
	if !errors.As(err, &conflict) || conflict.Current.AvatarAttachmentID == nil || *conflict.Current.AvatarAttachmentID != avatar {
		t.Fatalf("avatar conflict = %v", err)
	}
	input.ExpectedRevision = 2
	reader := auth.UserPrincipal{UserID: fixture.UserB, CompanyID: fixture.CompanyA}
	if _, err := service.UpdateProfile(ctx, reader, input); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member update = %v", err)
	}
	removed, err := service.UpdateProfile(ctx, principal, input)
	if err != nil || removed.AvatarAttachmentID != nil || removed.Revision != 3 {
		t.Fatalf("avatar removal = %+v, %v", removed, err)
	}
	if _, err := q.AuthorizeAttachmentDownload(ctx, read); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("removed avatar access = %v", err)
	}
	cleaned, err = q.TransitionClaimedAbandonedReadyAttachments(ctx, generated.TransitionClaimedAbandonedReadyAttachmentsParams{AttachmentIds: []uuid.UUID{avatar}, AbandonedGraceMilliseconds: 1000})
	if err != nil || len(cleaned) != 1 {
		t.Fatalf("removed avatar cleanup = %v, %v", cleaned, err)
	}
	if len(eventLog.snapshot()) != 2 {
		t.Fatalf("events = %d, want 2", len(eventLog.snapshot()))
	}
}
