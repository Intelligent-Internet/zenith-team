//go:build integration

package conversation

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestArtifactGroupAccessFollowsCurrentMembership(t *testing.T) {
	f := newDiscussionFixture(t)
	group, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{
			RequestKey:    uuid.New(),
			Name:          "Private files",
			MemberUserIDs: []uuid.UUID{f.b.UserID},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var outputID uuid.UUID
	err = f.pool.QueryRow(f.ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,'Private report','Private summary','sharing-test',$3, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`, f.seed.CompanyA, f.seed.RunA, f.a.UserID).
		Scan(&outputID)
	if err != nil {
		t.Fatal(err)
	}
	assertAccess := func(userID uuid.UUID, want int) {
		t.Helper()
		var got int
		if err := f.pool.QueryRow(f.ctx, `SELECT COALESCE((SELECT access_level FROM output_access WHERE output_id=$1 AND user_id=$2),0)`, outputID, userID).
			Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("access for %s = %d, want %d", userID, got, want)
		}
	}
	assertAccess(f.owner.UserID, 0)
	assertAccess(f.b.UserID, 0)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`INSERT INTO output_conversation_grants(company_id,output_id,conversation_id,granted_by_user_id) VALUES($1,$2,$3,$4)`,
		f.seed.CompanyA,
		outputID,
		group.Conversation.ID,
		f.a.UserID,
	)
	assertAccess(f.b.UserID, 1)
	meta, err := f.conversations.FetchChannelManagement(f.ctx, f.a, group.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta, err = f.conversations.SetChannelMember(
		f.ctx,
		f.a,
		group.Conversation.ID,
		ChannelMemberInput{UserID: f.c.UserID, Active: true, ExpectedRevision: meta.ConfigurationRevision},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertAccess(f.c.UserID, 1)
	_, err = f.conversations.SetChannelMember(
		f.ctx,
		f.a,
		group.Conversation.ID,
		ChannelMemberInput{UserID: f.b.UserID, Active: false, ExpectedRevision: meta.ConfigurationRevision},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertAccess(f.b.UserID, 0)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`INSERT INTO output_grants(company_id,output_id,user_id,permission,granted_by_user_id) VALUES($1,$2,$3,'view',$4)`,
		f.seed.CompanyA,
		outputID,
		f.b.UserID,
		f.a.UserID,
	)
	assertAccess(f.b.UserID, 1)
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE outputs SET visibility='company' WHERE id=$1`, outputID)
	// RunA works in the restricted ProjectA, so Company visibility reaches
	// only that Project's readers (ADR 0017).
	assertAccess(f.owner.UserID, 0)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`INSERT INTO project_grants(company_id,project_id,user_id,permission,granted_by_user_id)
		VALUES($1,$2,$3,'project.read',$4)`,
		f.seed.CompanyA,
		f.seed.ProjectA,
		f.owner.UserID,
		f.a.UserID,
	)
	assertAccess(f.owner.UserID, 1)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		f.seed.CompanyA,
		f.b.UserID,
	)
	assertAccess(f.b.UserID, 0)
}

func TestDiscussionFilesArePrivateAndFollowGroupMembership(t *testing.T) {
	f := newDiscussionFixture(t)
	group, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{RequestKey: uuid.New(), Name: "Files", MemberUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var attachmentID uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO attachments(company_id,uploaded_by_user_id,request_key,filename,content_type,size_bytes,status) VALUES($1,$2,gen_random_uuid(),'report.pdf','application/pdf',4,'ready') RETURNING id`, f.seed.CompanyA, f.a.UserID).
		Scan(&attachmentID); err != nil {
		t.Fatal(err)
	}
	input := DiscussionSendInput{
		RequestKey:    uuid.New(),
		Body:          DiscussionBody{Version: 1},
		AttachmentIDs: []uuid.UUID{attachmentID},
	}
	sent, err := f.discussion.Send(f.ctx, f.a, group.Conversation.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(sent.Message.Attachments) != 1 {
		t.Fatal("sent attachment missing")
	}
	replay, err := f.discussion.Send(f.ctx, f.a, group.Conversation.ID, input)
	if err != nil || !replay.Replayed {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	page, err := f.discussion.ListMessages(f.ctx, f.b, group.Conversation.ID, DiscussionPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items[len(page.Items)-1].Attachments) != 1 {
		t.Fatal("recipient attachment missing")
	}

	// A page must keep each message's files and preserve the submitted ordering.
	extraIDs := make([]uuid.UUID, 2)
	for i := range extraIDs {
		if err := f.pool.QueryRow(f.ctx, `INSERT INTO attachments(company_id,uploaded_by_user_id,request_key,filename,content_type,size_bytes,status) VALUES($1,$2,gen_random_uuid(),'extra.pdf','application/pdf',4,'ready') RETURNING id`, f.seed.CompanyA, f.a.UserID).
			Scan(&extraIDs[i]); err != nil {
			t.Fatal(err)
		}
	}
	second, err := f.discussion.Send(
		f.ctx,
		f.a,
		group.Conversation.ID,
		DiscussionSendInput{RequestKey: uuid.New(), Body: DiscussionBody{Version: 1}, AttachmentIDs: extraIDs},
	)
	if err != nil {
		t.Fatal(err)
	}
	page, err = f.discussion.ListMessages(f.ctx, f.b, group.Conversation.ID, DiscussionPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	foundMessages := 0
	for _, message := range page.Items {
		switch message.ID {
		case sent.Message.ID:
			foundMessages++
			if len(message.Attachments) != 1 || message.Attachments[0].ID != attachmentID {
				t.Fatalf("first message files = %+v", message.Attachments)
			}
		case second.Message.ID:
			foundMessages++
			if len(message.Attachments) != 2 || message.Attachments[0].ID != extraIDs[0] ||
				message.Attachments[1].ID != extraIDs[1] {
				t.Fatalf("second message files = %+v", message.Attachments)
			}
		}
	}
	if foundMessages != 2 {
		t.Fatalf("messages found = %d, want 2", foundMessages)
	}
	meta, err := f.conversations.FetchChannelManagement(f.ctx, f.a, group.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.conversations.SetChannelMember(
		f.ctx,
		f.a,
		group.Conversation.ID,
		ChannelMemberInput{UserID: f.c.UserID, Active: true, ExpectedRevision: meta.ConfigurationRevision},
	); err != nil {
		t.Fatal(err)
	}
	page, err = f.discussion.ListMessages(f.ctx, f.c, group.Conversation.ID, DiscussionPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range page.Items {
		found = found || len(item.Attachments) == 1
	}
	if !found {
		t.Fatal("new member cannot see old file")
	}
	q := generated.New(f.pool)
	assertDownload := func(userID uuid.UUID, allowed bool) {
		t.Helper()
		_, err := q.AuthorizeAttachmentDownload(
			f.ctx,
			generated.AuthorizeAttachmentDownloadParams{
				CompanyID:    f.seed.CompanyA,
				AttachmentID: attachmentID,
				ActorUserID:  userID,
			},
		)
		if allowed && err != nil || !allowed && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("download for %s allowed=%v: %v", userID, allowed, err)
		}
	}
	assertDownload(f.c.UserID, true)
	assertDownload(f.owner.UserID, false)
	meta, err = f.conversations.FetchChannelManagement(f.ctx, f.a, group.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.conversations.SetChannelMember(
		f.ctx,
		f.a,
		group.Conversation.ID,
		ChannelMemberInput{UserID: f.c.UserID, Active: false, ExpectedRevision: meta.ConfigurationRevision},
	); err != nil {
		t.Fatal(err)
	}
	assertDownload(f.c.UserID, false)
	if _, err = f.discussion.ListMessages(f.ctx, f.c, group.Conversation.ID, DiscussionPageOptions{}); err == nil {
		t.Fatal("removed member sees file metadata")
	}
}

func TestSharingArtifactAndMessageIsAtomicAndRequiresManage(t *testing.T) {
	f := newDiscussionFixture(t)
	group, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{RequestKey: uuid.New(), Name: "Artifacts", MemberUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var outputID uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,'Private report','Summary','share-message',$3, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`, f.seed.CompanyA, f.seed.RunA, f.a.UserID).
		Scan(&outputID); err != nil {
		t.Fatal(err)
	}
	input := DiscussionSendInput{
		RequestKey:  uuid.New(),
		Body:        DiscussionBody{Version: 1},
		ArtifactIDs: []uuid.UUID{outputID, uuid.New()},
	}
	if _, err = f.discussion.Send(f.ctx, f.a, group.Conversation.ID, input); err == nil {
		t.Fatal("invalid artifact accepted")
	}
	var grants int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM output_conversation_grants WHERE output_id=$1`, outputID).
		Scan(&grants); err != nil ||
		grants != 0 {
		t.Fatalf("partially committed grants = %d, %v", grants, err)
	}
	input.RequestKey = uuid.New()
	input.ArtifactIDs = []uuid.UUID{outputID}
	sent, err := f.discussion.Send(f.ctx, f.a, group.Conversation.ID, input)
	if err != nil || len(sent.Message.Artifacts) != 1 {
		t.Fatalf("share = %+v, %v", sent, err)
	}
	var revision int64
	if err := f.pool.QueryRow(f.ctx, `SELECT access_version FROM outputs WHERE id=$1`, outputID).
		Scan(&revision); err != nil {
		t.Fatal(err)
	}
	replay, err := f.discussion.Send(f.ctx, f.a, group.Conversation.ID, input)
	if err != nil || !replay.Replayed {
		t.Fatalf("share replay = %+v, %v", replay, err)
	}
	var after int64
	if err := f.pool.QueryRow(f.ctx, `SELECT access_version FROM outputs WHERE id=$1`, outputID).
		Scan(&after); err != nil ||
		after != revision {
		t.Fatalf("replay changed grant = %d, %v", after, err)
	}
	input.RequestKey = uuid.New()
	if _, err = f.discussion.Send(f.ctx, f.b, group.Conversation.ID, input); err == nil {
		t.Fatal("viewer reshared artifact")
	}
	dbtest.MustExec(t, f.ctx, f.pool, `DELETE FROM output_conversation_grants WHERE output_id=$1`, outputID)
	page, err := f.discussion.ListMessages(f.ctx, f.b, group.Conversation.ID, DiscussionPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if len(item.Artifacts) > 0 {
			t.Fatal("revoked artifact metadata leaked through old message")
		}
	}
}
