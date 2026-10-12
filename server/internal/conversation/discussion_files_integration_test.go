//go:build integration

package conversation

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestDiscussionAttachmentDownloadUsesCurrentParticipants(t *testing.T) {
	for _, kind := range []string{"channel", "human_direct"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := newDiscussionFixture(t)
			var conversationID uuid.UUID
			if kind == "channel" {
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
				conversationID = group.Conversation.ID
			} else {
				direct, err := f.conversations.CreateHumanDirect(
					f.ctx,
					f.a,
					CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
				)
				if err != nil {
					t.Fatal(err)
				}
				conversationID = direct.Conversation.ID
			}
			var fileID uuid.UUID
			if err := f.pool.QueryRow(f.ctx, `INSERT INTO attachments(company_id,uploaded_by_user_id,request_key,filename,content_type,size_bytes,status) VALUES($1,$2,gen_random_uuid(),'private.txt','text/plain',4,'ready') RETURNING id`, f.a.CompanyID, f.a.UserID).
				Scan(&fileID); err != nil {
				t.Fatal(err)
			}
			// A completed draft upload belongs only to its uploader, even before sending.
			assertDiscussionFileDownload(t, f, fileID, f.a.UserID, f.a.CompanyID, true)
			assertDiscussionFileDownload(t, f, fileID, f.b.UserID, f.a.CompanyID, false)
			assertDiscussionFileDownload(t, f, fileID, f.owner.UserID, f.a.CompanyID, false)
			sent, err := f.discussion.Send(
				f.ctx,
				f.a,
				conversationID,
				DiscussionSendInput{
					RequestKey:    uuid.New(),
					Body:          DiscussionBody{Version: 1},
					AttachmentIDs: []uuid.UUID{fileID},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			assertDiscussionFileDownload(t, f, fileID, f.a.UserID, f.a.CompanyID, true)
			assertDiscussionFileDownload(t, f, fileID, f.b.UserID, f.a.CompanyID, true)
			// Company roles never turn an outsider into a conversation participant.
			for _, role := range []string{"member", "admin"} {
				dbtest.MustExec(
					t,
					f.ctx,
					f.pool,
					`UPDATE company_members SET role=$3 WHERE company_id=$1 AND user_id=$2`,
					f.a.CompanyID,
					f.c.UserID,
					role,
				)
				assertDiscussionFileDownload(t, f, fileID, f.c.UserID, f.a.CompanyID, false)
			}
			assertDiscussionFileDownload(t, f, fileID, f.owner.UserID, f.a.CompanyID, false)
			assertDiscussionFileDownload(t, f, fileID, f.b.UserID, f.seed.CompanyB, false)
			if _, err := f.discussion.ListMessages(
				f.ctx,
				f.c,
				conversationID,
				DiscussionPageOptions{},
			); err == nil {
				t.Fatal("outsider can list file metadata")
			}
			// A message cannot reuse a previously linked file to expand its audience.
			other, err := f.conversations.CreateChannel(
				f.ctx,
				f.a,
				CreateChannelInput{
					RequestKey:    uuid.New(),
					Name:          "Other audience",
					MemberUserIDs: []uuid.UUID{f.c.UserID},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.discussion.Send(
				f.ctx,
				f.a,
				other.Conversation.ID,
				DiscussionSendInput{
					RequestKey:    uuid.New(),
					Body:          DiscussionBody{Version: 1},
					AttachmentIDs: []uuid.UUID{fileID},
				},
			); !errors.Is(
				err,
				ErrDiscussionInvalid,
			) {
				t.Fatalf("reuse linked attachment: %v", err)
			}
			assertDiscussionFileDownload(t, f, fileID, f.c.UserID, f.a.CompanyID, false)
			// A revoked Company membership overrides an otherwise active chat grant.
			dbtest.MustExec(
				t,
				f.ctx,
				f.pool,
				`UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
				f.a.CompanyID,
				f.b.UserID,
			)
			assertDiscussionFileDownload(t, f, fileID, f.b.UserID, f.a.CompanyID, false)
			if kind == "channel" {
				meta, err := f.conversations.FetchChannelManagement(f.ctx, f.a, conversationID)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.conversations.LeaveChannel(
					f.ctx,
					f.a,
					conversationID,
					meta.ConfigurationRevision,
				); err != nil {
					t.Fatal(err)
				}
				assertDiscussionFileDownload(t, f, fileID, f.a.UserID, f.a.CompanyID, false)
			} else {
				// Restore the peer so the direct conversation is writable for deletion.
				dbtest.MustExec(
					t,
					f.ctx,
					f.pool,
					`UPDATE company_members SET status='active' WHERE company_id=$1 AND user_id=$2`,
					f.a.CompanyID,
					f.b.UserID,
				)
				// Deleted messages cannot be downloaded through a saved attachment URL.
				if _, err := f.discussion.DeleteMessage(
					f.ctx,
					f.a,
					DiscussionMessageTarget{
						ConversationID: conversationID,
						MessageID:      sent.Message.ID,
					},
					sent.Message.Revision,
				); err != nil {
					t.Fatal(err)
				}
				assertDiscussionFileDownload(t, f, fileID, f.a.UserID, f.a.CompanyID, false)
			}
		})
	}
}

func assertDiscussionFileDownload(
	t *testing.T,
	f discussionFixture,
	fileID, userID, companyID uuid.UUID,
	allowed bool,
) {
	t.Helper()
	_, err := generated.New(f.pool).
		AuthorizeAttachmentDownload(f.ctx, generated.AuthorizeAttachmentDownloadParams{CompanyID: companyID, AttachmentID: fileID, ActorUserID: userID})
	if allowed && err != nil || !allowed && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("download allowed=%v: %v", allowed, err)
	}
}

func TestThreadReplyIncludesOnlyReadableOutputsFromItsPublishedRun(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	root := f.send(t, textSend("Thread root"))
	input := taggedSend(t, f.seed.AgentA)
	input.ThreadRootID = &root.ID
	trigger := f.send(t, input)
	invocation := f.invocation(t, trigger.ID)
	run := f.run(t, invocation.ID)
	insertOutput := func(runID uuid.UUID, title string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		err := f.pool.QueryRow(f.ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id,presentation_number,conversation_id, task_id, project_id, task_execution_generation, actor_agent_id)
   SELECT $1,$2,$3,'Summary',$3,$4,(SELECT COALESCE(max(presentation_number),0)+1 FROM outputs WHERE run_id=$2), origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.id=$2 RETURNING id`, f.a.CompanyID, runID, title, f.a.UserID).
			Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := insertOutput(run.ID, "First result")
	second := insertOutput(run.ID, "Second result")
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE outputs SET type='text' WHERE id=$1`, first)
	for ordinal, role := range []string{"supporting", "primary", "final"} {
		fileName := role + "-report.md"
		contentType := "text/markdown"
		if role == "final" {
			fileName = "cover.png"
			contentType = "image/png"
		}
		var versionID uuid.UUID
		err := f.pool.QueryRow(f.ctx, `WITH attachment AS (
		 INSERT INTO attachments(company_id,uploaded_by_user_id,request_key,filename,content_type,size_bytes,status)
		 VALUES($1,$2,gen_random_uuid(),$4,$5,24,'ready') RETURNING id
		), asset AS (
		 INSERT INTO assets(company_id,title,media_class,created_by_user_id) VALUES($1,'Report','file',$2) RETURNING id
		) INSERT INTO asset_versions(company_id,asset_id,version_number,attachment_id,source_run_id,filename,content_type,size_bytes,integrity_status)
		 SELECT $1,asset.id,1,attachment.id,$3,$4,$5,24,'verified' FROM asset,attachment RETURNING id`,
			f.a.CompanyID, f.a.UserID, run.ID, fileName, contentType).Scan(&versionID)
		if err != nil {
			t.Fatal(err)
		}
		dbtest.MustExec(
			t,
			f.ctx,
			f.pool,
			`INSERT INTO output_items(company_id,output_id,ordinal,role,label,asset_version_id) VALUES($1,$2,$3,$4,'Report',$5)`,
			f.a.CompanyID,
			first,
			ordinal,
			role,
			versionID,
		)
	}
	insertOutput(f.seed.RunA, "Unrelated result")
	f.finish(t, run, "completed", "Files are ready", true)
	// No explicit message links exist: this also covers already-published replies.
	list := func(principal auth.UserPrincipal) DiscussionMessage {
		t.Helper()
		page, err := f.discussion.ListThread(f.ctx, principal, DiscussionMessageTarget{
			ConversationID: f.channelID, MessageID: root.ID,
		}, DiscussionPageOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range page.Items {
			if message.Author.Type == "agent" {
				return message
			}
		}
		t.Fatal("published Agent reply missing")
		return DiscussionMessage{}
	}
	reply := list(f.a)
	if len(reply.Artifacts) != 2 {
		t.Fatalf("owner artifacts = %+v, want two outputs", reply.Artifacts)
	}
	seen := map[uuid.UUID]bool{}
	for _, artifact := range reply.Artifacts {
		seen[artifact.ID] = true
		if artifact.ID == first {
			if len(artifact.Files) != 2 || artifact.Files[0].FileName != "primary-report.md" ||
				artifact.Files[1].FileName != "cover.png" ||
				artifact.Files[1].ContentType != "image/png" ||
				artifact.Files[0].ContentType != "text/markdown" {
				t.Fatalf("ordered deliverables = %+v", artifact.Files)
			}
		}
		if artifact.ID == second && (artifact.Files == nil || len(artifact.Files) != 0) {
			t.Fatalf("empty deliverables = %+v", artifact.Files)
		}
		if artifact.ID == first &&
			(artifact.Type != "text" || artifact.FileCount != 2 || artifact.FileName != "cover.png" || artifact.ContentType != "image/png") {
			t.Fatalf("artifact metadata = %+v", artifact)
		}
		if artifact.ID == second &&
			(artifact.FileCount != 0 || artifact.FileName != "" || artifact.ContentType != "") {
			t.Fatalf("empty artifact metadata = %+v", artifact)
		}
	}
	if !seen[first] || !seen[second] {
		t.Fatalf("artifact IDs = %v", seen)
	}
	if got := list(f.b); len(got.Artifacts) != 0 {
		t.Fatalf("private outputs exposed to member: %+v", got.Artifacts)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE outputs SET visibility='company' WHERE id=$1`, first)
	if got := list(f.b); len(got.Artifacts) != 1 || got.Artifacts[0].ID != first {
		t.Fatalf("shared artifacts = %+v, want only %s", got.Artifacts, first)
	}
	// An explicit reference to the same output must not duplicate its card.
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`INSERT INTO discussion_artifacts(company_id,message_id,output_id) VALUES($1,$2,$3)`,
		f.a.CompanyID,
		reply.ID,
		first,
	)
	if got := list(f.a); len(got.Artifacts) != 2 {
		t.Fatalf("duplicate artifacts = %+v", got.Artifacts)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE outputs SET visibility='private' WHERE id=$1`, first)
	if got := list(f.b); len(got.Artifacts) != 0 {
		t.Fatalf("revoked artifacts = %+v", got.Artifacts)
	}
}
