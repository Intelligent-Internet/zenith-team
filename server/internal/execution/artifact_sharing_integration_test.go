//go:build integration

package execution

import (
	"errors"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestArtifactVisibilityRestrictedPreservesGrantsAndRevokesPublicLink(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewDeliverableService(DeliverableServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
	// The Artifact comes from a Task in the restricted ProjectA: Company
	// visibility reaches only that Project's readers (ADR 0017).
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO project_grants(company_id,project_id,user_id,permission,granted_by_user_id)
		VALUES($1,$2,$3,'project.read',$4)`,
		f.CompanyA,
		f.ProjectA,
		f.UserB,
		f.UserA,
	)
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,'Report','Summary','access-test',$3, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`, f.CompanyA, f.RunA, f.UserA).
		Scan(&id); err != nil {
		t.Fatal(err)
	}

	t.Run("revoking absent grants preserves privacy", func(t *testing.T) {
		access, err := service.RevokeArtifactConversation(
			ctx,
			p,
			id,
			ArtifactConversationInput{ConversationID: uuid.New(), ExpectedRevision: 1},
		)
		if err != nil || access.Visibility != "private" || access.Revision != 1 {
			t.Fatalf("absent group revoke = %+v, %v", access, err)
		}
		access, err = service.ChangeArtifactGrant(ctx, p, id, f.UserB, nil, 1)
		if err != nil || access.Visibility != "private" || access.Revision != 1 {
			t.Fatalf("absent person revoke = %+v, %v", access, err)
		}
	})
	access, err := service.SetArtifactVisibility(ctx, p, id,
		ArtifactVisibilityInput{Visibility: "public", ExpectedRevision: 1})
	if err != nil || access.PublicToken == nil {
		t.Fatalf("public sharing without Company policy or approval = %+v, %v", access, err)
	}
	access, err = service.SetArtifactVisibility(ctx, p, id,
		ArtifactVisibilityInput{Visibility: "company", ExpectedRevision: access.Revision})
	if err != nil {
		t.Fatal(err)
	}
	viewer := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: auth.CompanyRoleMember}
	if _, err = service.getArtifactAccess(ctx, viewer, id); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SetArtifactVisibility(
		ctx,
		viewer,
		id,
		ArtifactVisibilityInput{Visibility: "shared", ExpectedRevision: access.Revision},
	); !errors.Is(
		err,
		ErrDeliverableForbidden,
	) {
		t.Fatalf("viewer changed scope = %v", err)
	}
	edit := ArtifactPermissionEdit
	access, err = service.ChangeArtifactGrant(ctx, p, id, f.UserB, &edit, access.Revision)
	if err != nil {
		t.Fatal(err)
	}
	editorAccess, err := service.getArtifactAccess(ctx, viewer, id)
	if err != nil || editorAccess.Permission != ArtifactPermissionEdit {
		t.Fatalf("Editor permission = %+v, %v", editorAccess, err)
	}
	if _, err = service.SetArtifactVisibility(
		ctx,
		viewer,
		id,
		ArtifactVisibilityInput{Visibility: "public", ExpectedRevision: access.Revision},
	); !errors.Is(
		err,
		ErrDeliverableForbidden,
	) {
		t.Fatalf("Editor changed sharing = %v", err)
	}
	if _, err = service.ChangeArtifactGrant(
		ctx,
		viewer,
		id,
		uuid.New(),
		&edit,
		access.Revision,
	); !errors.Is(
		err,
		ErrDeliverableForbidden,
	) {
		t.Fatalf("Editor granted access = %v", err)
	}
	manage := ArtifactPermissionManage
	access, err = service.ChangeArtifactGrant(ctx, p, id, f.UserB, &manage, access.Revision)
	if err != nil {
		t.Fatal(err)
	}
	access, err = service.SetArtifactVisibility(
		ctx,
		viewer,
		id,
		ArtifactVisibilityInput{Visibility: "public", ExpectedRevision: access.Revision},
	)
	if err != nil {
		t.Fatal(err)
	}
	token := *access.PublicToken

	attachments := make([]uuid.UUID, 3)
	for ordinal, role := range []string{"final", "supporting", "primary"} {
		contentType := "text/plain"
		if ordinal == 2 {
			contentType = "application/vnd.zenith-studio.canvas+json"
		}
		var assetID, versionID uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO attachments(company_id,uploaded_by_user_id,request_key,filename,content_type,size_bytes,status) VALUES($1,$2,gen_random_uuid(),'file.txt',$3,4,'ready') RETURNING id`, f.CompanyA, f.UserA, contentType).
			Scan(&attachments[ordinal]); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO assets(company_id,title,media_class,created_by_user_id) VALUES($1,'File','file',$2) RETURNING id`, f.CompanyA, f.UserA).
			Scan(&assetID); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO asset_versions(company_id,asset_id,version_number,attachment_id,source_run_id,filename,content_type,size_bytes,integrity_status) VALUES($1,$2,1,$3,$4,'file.txt',$5,4,'verified') RETURNING id`, f.CompanyA, assetID, attachments[ordinal], f.RunA, contentType).
			Scan(&versionID); err != nil {
			t.Fatal(err)
		}
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`INSERT INTO output_items(company_id,output_id,ordinal,role,label,asset_version_id) VALUES($1,$2,$3,$4,'File',$5)`,
			f.CompanyA,
			id,
			ordinal,
			role,
			versionID,
		)
	}
	public, err := service.GetPublicArtifact(ctx, token)
	if err != nil || len(public.Items) != 1 || public.Items[0].Ordinal != 0 || public.Items[0].Role != "final" {
		t.Fatalf("public projection = %+v, %v", public, err)
	}
	companyID, attachmentID, err := service.ResolvePublicArtifactFile(ctx, token, 0)
	if err != nil || companyID != f.CompanyA || attachmentID != attachments[0] {
		t.Fatalf("public file = %s, %s, %v", companyID, attachmentID, err)
	}
	for _, ordinal := range []int{1, 2} {
		if _, _, err = service.ResolvePublicArtifactFile(
			ctx,
			token,
			ordinal,
		); !errors.Is(
			err,
			ErrDeliverableNotFound,
		) {
			t.Fatalf("private source %d exposed: %v", ordinal, err)
		}
	}
	if _, err = service.GetPublicArtifact(ctx, token); err != nil {
		t.Fatal(err)
	}

	// An app source bundle is not an anonymous final file, even if marked primary.
	dbtest.MustExec(t, ctx, pool, `UPDATE outputs SET type='app' WHERE id=$1`, id)
	dbtest.MustExec(t, ctx, pool, `UPDATE output_items SET role='supporting' WHERE output_id=$1 AND ordinal=2`, id)
	dbtest.MustExec(t, ctx, pool, `UPDATE output_items SET role='primary' WHERE output_id=$1 AND ordinal=0`, id)
	if _, _, err = service.ResolvePublicArtifactFile(ctx, token, 0); !errors.Is(err, ErrDeliverableNotFound) {
		t.Fatalf("app source exposed: %v", err)
	}
	public, err = service.GetPublicArtifact(ctx, token)
	if err != nil || len(public.Items) != 0 {
		t.Fatalf("app sources in public metadata = %+v, %v", public, err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE outputs SET type='file' WHERE id=$1`, id)
	view := ArtifactPermissionView
	access, err = service.ChangeArtifactGrant(ctx, p, id, f.UserB, &view, access.Revision)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO output_conversation_grants(company_id,output_id,conversation_id,granted_by_user_id) VALUES($1,$2,$3,$4)`,
		f.CompanyA,
		id,
		f.ConversationA,
		f.UserA,
	)
	if _, err = service.SetArtifactVisibility(ctx, p, id, ArtifactVisibilityInput{
		Visibility: "private", ExpectedRevision: access.Revision,
	}); !errors.Is(err, ErrDeliverableInvalidInput) {
		t.Fatalf("removed private mutation = %v, want invalid input", err)
	}
	access, err = service.SetArtifactVisibility(
		ctx,
		p,
		id,
		ArtifactVisibilityInput{Visibility: "shared", ExpectedRevision: access.Revision},
	)
	if err != nil {
		t.Fatal(err)
	}
	if access.Visibility != "shared" || len(access.Grants) != 1 || len(access.ConversationGrants) != 1 ||
		access.PublicToken != nil {
		t.Fatalf("restricted access = %+v", access)
	}
	if _, err = service.getArtifactAccess(ctx, viewer, id); err != nil {
		t.Fatalf("explicit viewer lost access = %v", err)
	}
	if _, _, err = service.ResolvePublicArtifactFile(ctx, token, 0); !errors.Is(err, ErrDeliverableNotFound) {
		t.Fatalf("revoked token downloaded bytes: %v", err)
	}
	if _, err = service.GetPublicArtifact(ctx, token); !errors.Is(err, ErrDeliverableNotFound) {
		t.Fatalf("revoked token = %v", err)
	}
	if _, err = service.SetArtifactVisibility(
		ctx,
		p,
		id,
		ArtifactVisibilityInput{Visibility: "company", ExpectedRevision: 1},
	); !errors.Is(
		err,
		ErrDeliverableConflict,
	) {
		t.Fatalf("stale mutation = %v", err)
	}
}

// A direct message has no title, so a member sees it named by its other
// participant, as the chat list names it.
func TestArtifactConversationGrantNamesDirectMessageByPeer(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewDeliverableService(DeliverableServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
	var id, dm uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,'Report','Summary','dm-title-test',$3, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`, f.CompanyA, f.RunA, f.UserA).
		Scan(&id); err != nil {
		t.Fatal(err)
	}
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `INSERT INTO conversations(company_id,kind,created_by_user_id) VALUES($1,'human_direct',$2) RETURNING id`, f.CompanyA, f.UserA).
			Scan(&dm); err != nil {
			t.Fatal(err)
		}
		dbtest.MustExecTx(
			t,
			ctx,
			tx,
			`INSERT INTO conversation_human_directs(company_id,conversation_id,user_low_id,user_high_id) VALUES($1,$2,LEAST($3::uuid,$4::uuid),GREATEST($3::uuid,$4::uuid))`,
			f.CompanyA,
			dm,
			f.UserA,
			f.UserB,
		)
		dbtest.MustExecTx(
			t,
			ctx,
			tx,
			`INSERT INTO conversation_members(company_id,conversation_id,user_id,source_type) VALUES($1,$2,$3,'human_direct'),($1,$2,$4,'human_direct')`,
			f.CompanyA,
			dm,
			f.UserA,
			f.UserB,
		)
	})
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO output_conversation_grants(company_id,output_id,conversation_id,granted_by_user_id) VALUES($1,$2,$3,$4)`,
		f.CompanyA,
		id,
		dm,
		f.UserA,
	)
	for _, tc := range []struct {
		user uuid.UUID
		want string
	}{{f.UserA, "Schema B"}, {f.UserB, "Schema A"}} {
		access, err := service.getArtifactAccess(
			ctx,
			auth.UserPrincipal{CompanyID: f.CompanyA, UserID: tc.user, Role: auth.CompanyRoleMember},
			id,
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(access.ConversationGrants) != 1 || access.ConversationGrants[0].Title != tc.want {
			t.Fatalf(
				"grants for %s = %+v, want the DM named %q",
				tc.user,
				access.ConversationGrants,
				tc.want,
			)
		}
	}
}
