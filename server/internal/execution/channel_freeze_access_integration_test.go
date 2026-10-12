//go:build integration

package execution

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
	db "boardly.dev/boardly/server/pkg/db/generated"
)

// A tagged Run's proof names the channel it answers into. The field marks a
// tagged Run and bounds nothing (D31); a Run in any other chat names none.
func TestRunOriginNamesTheChannelATaggedRunAnswersInto(t *testing.T) {
	h := newRunHarness(t)
	ctx := context.Background()
	runID := seedQueuedChannelTagRun(t, h)
	const token = "brt_00000000000000000000000000000000000000000000000000000000000000aa"
	digest := sha256.Sum256([]byte(token))
	dbtest.MustExec(t, ctx, h.pool, `UPDATE runs SET status = 'running' WHERE id = $1`, runID)
	if _, err := h.queries.CreateRunToken(ctx, db.CreateRunTokenParams{
		RunID: runID, TokenHash: digest[:],
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	var channelID uuid.UUID
	if err := h.pool.QueryRow(ctx, `
		SELECT invocation.channel_id FROM runs JOIN channel_invocations AS invocation ON invocation.id = runs.invocation_id
		WHERE runs.id = $1`, runID).Scan(&channelID); err != nil {
		t.Fatal(err)
	}
	origin, err := h.svc.ResolveRunOrigin(ctx, token)
	if err != nil {
		t.Fatalf("ResolveRunOrigin() = %v", err)
	}
	if origin.InvocationChannelID == nil || *origin.InvocationChannelID != channelID {
		t.Fatalf("tagged Run channel = %v, want the channel %s", origin.InvocationChannelID, channelID)
	}
}

// A channel Run uses its caller's access and nothing else narrows it (D31):
// it freezes the files its caller may open. A plain chat upload is frozen, a
// Project file is frozen while the caller reads its Project though a channel
// member cannot, and stays out once the caller cannot.
func TestChannelRunFreezesTheFilesItsCallerMayOpen(t *testing.T) {
	h := newRunHarness(t)
	ctx := context.Background()
	f := h.fixture
	colleague := dbtest.NewUUID(t)
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, 'freeze-colleague@example.com', 'Colleague')`,
		colleague,
	)
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		f.CompanyA,
		colleague,
	)
	channelID := dbtest.SeedDiscussionChannel(t, ctx, h.pool, f.CompanyA, f.UserA, []uuid.UUID{f.AgentA})
	dbtest.AddDiscussionMember(t, ctx, h.pool, f.CompanyA, channelID, colleague)
	var messageID uuid.UUID
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO conversation_items
			(company_id, conversation_id, seq, type, author_kind, author_id,
			 content, body, request_key, send_fingerprint)
		VALUES ($1, $2, 1, 'user_message', 'user', $3, '@Agent read these',
			'{"markdown":"@Agent read these"}'::jsonb, gen_random_uuid(),
			decode(repeat('11', 32), 'hex'))
		RETURNING id`, f.CompanyA, channelID, f.UserA).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	upload := func(name string, position int) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := h.pool.QueryRow(ctx, `
			INSERT INTO attachments (company_id, uploaded_by_user_id, request_key, filename, content_type, size_bytes, status)
			VALUES ($1, $2, gen_random_uuid(), $3, 'text/plain', 1, 'ready') RETURNING id`,
			f.CompanyA, f.UserA, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		dbtest.MustExec(t, ctx, h.pool, `
			INSERT INTO message_attachments (company_id, message_id, attachment_id, position) VALUES ($1, $2, $3, $4)`,
			f.CompanyA, messageID, id, position)
		return id
	}
	plain := upload("notes.txt", 0)
	projectFile := upload("roadmap.pdf", 1)
	dbtest.MustExec(t, ctx, h.pool, `
		INSERT INTO project_files (company_id, project_id, attachment_id, file_type) VALUES ($1, $2, $3, 'document')`,
		f.CompanyA, f.ProjectA, projectFile)
	conversation, err := h.queries.EnsureChannelAgentConversation(ctx, db.EnsureChannelAgentConversationParams{
		ChannelID: channelID, AgentID: f.AgentA, CompanyID: f.CompanyA,
	})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := h.queries.InsertChannelInvocation(ctx, db.InsertChannelInvocationParams{
		ConversationID: conversation.ID, AgentID: f.AgentA, CallerUserID: f.UserA,
		MessageID: messageID, ChannelID: channelID, CompanyID: f.CompanyA,
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen := func() []uuid.UUID {
		t.Helper()
		document := map[string]json.RawMessage{}
		if err := freezeChannelInvocation(ctx, h.queries, RunTrigger{
			CompanyID: f.CompanyA, ConversationID: conversation.ID, AgentID: f.AgentA,
			InvocationID: uuid.NullUUID{UUID: invocation.ID, Valid: true},
		}, document); err != nil {
			t.Fatal(err)
		}
		var files []runAttachedFileBrief
		if err := json.Unmarshal(document["channelAttachments"], &files); err != nil {
			t.Fatal(err)
		}
		ids := make([]uuid.UUID, len(files))
		for i, file := range files {
			ids[i] = uuid.MustParse(file.ID)
		}
		return ids
	}

	if got := frozen(); !slices.Equal(got, []uuid.UUID{plain, projectFile}) {
		t.Fatalf("frozen with a member outside the Project = %v, want both files", got)
	}
	dbtest.MustExec(t, ctx, h.pool, `DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2`,
		f.ProjectA, f.UserA)
	if got := frozen(); !slices.Equal(got, []uuid.UUID{plain}) {
		t.Fatalf("frozen when the caller cannot read the Project = %v, want only the chat upload", got)
	}

	// The daemon's download follows the caller's access too: a Project file
	// is refused while the caller cannot read its Project, and opens once
	// they can, though a channel member cannot read it, and still after
	// someone else who cannot read it joins.
	var runID uuid.UUID
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO runs
			(company_id, agent_id, conversation_id, runtime_id, run_type, status,
			 invocation_id, execution_authorized_by_user_id, context)
		VALUES ($1, $2, $3, $4, 'channel_agent', 'running', $5, $6, '{}'::jsonb)
		RETURNING id`, f.CompanyA, f.AgentA, conversation.ID, f.RuntimeA, invocation.ID, f.UserA,
	).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	downloads := func(attachmentID uuid.UUID) bool {
		t.Helper()
		_, err := h.queries.AuthorizeRunAttachmentDownload(ctx, db.AuthorizeRunAttachmentDownloadParams{
			AttachmentID: attachmentID, CompanyID: f.CompanyA, RunID: runID, AuthorizerUserID: f.UserA,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		return err == nil
	}
	if downloads(projectFile) {
		t.Fatal("the Run downloads a Project file its caller cannot read")
	}
	dbtest.GrantProject(t, ctx, h.pool, f.CompanyA, f.ProjectA, f.UserA, "project.read")
	if !downloads(projectFile) || !downloads(plain) {
		t.Fatal("the Run cannot download files its caller reads while a channel member cannot")
	}
	newcomer := dbtest.NewUUID(t)
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, 'freeze-newcomer@example.com', 'Newcomer')`,
		newcomer,
	)
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		f.CompanyA,
		newcomer,
	)
	dbtest.AddDiscussionMember(t, ctx, h.pool, f.CompanyA, channelID, newcomer)
	if !downloads(projectFile) || !downloads(plain) {
		t.Fatal("the Run lost a file its caller reads when someone joined the channel")
	}
}

func TestChannelArtifactReferencesFollowTheCallersAccess(t *testing.T) {
	h := newRunHarness(t)
	ctx := t.Context()
	f := h.fixture
	runID := seedQueuedChannelTagRun(t, h)
	run, err := h.queries.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := h.queries.GetChannelInvocation(
		ctx,
		db.GetChannelInvocationParams{InvocationID: run.InvocationID.UUID, CompanyID: f.CompanyA},
	)
	if err != nil {
		t.Fatal(err)
	}
	var outputID uuid.UUID
	if err := h.pool.QueryRow(ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,'Private website','Hidden summary','channel-reference',$3, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`, f.CompanyA, f.RunA, f.UserA).
		Scan(&outputID); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`INSERT INTO discussion_artifacts(company_id,message_id,output_id) VALUES($1,$2,$3)`,
		f.CompanyA,
		invocation.TriggerMessageID,
		outputID,
	)
	freeze := func() []uuid.UUID {
		t.Helper()
		doc := map[string]json.RawMessage{}
		if err := freezeChannelInvocation(
			ctx,
			h.queries,
			RunTrigger{
				CompanyID:      f.CompanyA,
				ConversationID: run.ConversationID,
				AgentID:        run.AgentID,
				InvocationID:   run.InvocationID,
			},
			doc,
		); err != nil {
			t.Fatal(err)
		}
		var ids []uuid.UUID
		if err := json.Unmarshal(doc["channelArtifactIds"], &ids); err != nil {
			t.Fatal(err)
		}
		return ids
	}
	if got := freeze(); !slices.Equal(got, []uuid.UUID{outputID}) {
		t.Fatalf("the caller's own Artifact = %v", got)
	}
	// The Run uses its caller's access and no chat audience narrows it
	// (D31): a member who cannot read the Artifact joining changes nothing.
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
	dbtest.AddDiscussionMember(t, ctx, h.pool, f.CompanyA, invocation.ChannelID, f.UserB)
	if got := freeze(); !slices.Equal(got, []uuid.UUID{outputID}) {
		t.Fatalf("a member joining hid the caller's Artifact = %v", got)
	}
	// An Artifact the caller cannot read stays out until it is shared with
	// them, and leaves again when the share is revoked.
	var hidden uuid.UUID
	if err := h.pool.QueryRow(ctx, `INSERT INTO outputs(company_id,run_id,presentation_number,title,summary,presentation_fingerprint,owner_user_id,conversation_id,task_id,project_id,task_execution_generation,actor_agent_id) SELECT $1,$2,2,'Bob only','Hidden summary','channel-reference-hidden',$3,origin.conversation_id,origin.task_id,conversation.project_id,origin.task_execution_generation,origin.agent_id FROM runs origin JOIN conversations conversation ON conversation.id=origin.conversation_id WHERE origin.id=$2 RETURNING id`, f.CompanyA, f.RunA, f.UserB).
		Scan(&hidden); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`INSERT INTO discussion_artifacts(company_id,message_id,output_id) VALUES($1,$2,$3)`,
		f.CompanyA,
		invocation.TriggerMessageID,
		hidden,
	)
	if got := freeze(); !slices.Equal(got, []uuid.UUID{outputID}) {
		t.Fatalf("an Artifact the caller cannot read was frozen = %v", got)
	}
	dbtest.MustExec(
		t,
		ctx,
		h.pool,
		`INSERT INTO output_grants(company_id,output_id,user_id,permission,granted_by_user_id) VALUES($1,$2,$3,'view',$4)`,
		f.CompanyA,
		hidden,
		f.UserA,
		f.UserB,
	)
	if got := freeze(); len(got) != 2 || !slices.Contains(got, outputID) || !slices.Contains(got, hidden) {
		t.Fatalf("shared with the caller = %v", got)
	}
	dbtest.MustExec(t, ctx, h.pool, `DELETE FROM output_grants WHERE output_id=$1`, hidden)
	if got := freeze(); !slices.Equal(got, []uuid.UUID{outputID}) {
		t.Fatalf("revoked share = %v", got)
	}
}
