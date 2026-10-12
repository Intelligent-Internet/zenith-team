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

// conversation_readers (migration 000086) restates, per chat kind, who reads
// a chat. Each kind already had its own rule before the view existed; this
// fixture pins the view to them, person by person, so the files that now
// follow the view open to exactly the people who read the chat:
//   - channel and human_direct: ListDiscussionAccess (content access);
//   - channel_agent: the live-caller arm of LockHumanInputOriginConversation.
//     Its creator arm is deliberately not in the view (ADR 0017): the member
//     who first mentioned the Agent does not read later callers' runs;
//   - direct and schedule: the creator, while an active member with an
//     enabled account.
func TestConversationReadersMatchEachKindsOwnRule(t *testing.T) {
	t.Parallel()
	f := newDiscussionFixture(t)
	q := generated.New(f.pool)
	companyID := f.a.CompanyID

	var disabled, revoked uuid.UUID
	for _, person := range []*uuid.UUID{&disabled, &revoked} {
		*person = dbtest.NewUUID(t)
		dbtest.MustExec(t, f.ctx, f.pool, `INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Reader')`,
			*person, person.String()+"@readers.example.test")
		dbtest.MustExec(t, f.ctx, f.pool, `INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
			companyID, *person)
	}
	people := []uuid.UUID{f.a.UserID, f.b.UserID, f.c.UserID, f.owner.UserID, disabled, revoked}

	channel, err := f.conversations.CreateChannel(f.ctx, f.a, CreateChannelInput{
		RequestKey: uuid.New(), Name: "Readers",
		MemberUserIDs: []uuid.UUID{f.b.UserID, f.c.UserID, disabled, revoked},
	})
	if err != nil {
		t.Fatal(err)
	}
	management, err := f.conversations.FetchChannelManagement(f.ctx, f.a, channel.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.conversations.LeaveChannel(f.ctx, f.c, channel.Conversation.ID, management.ConfigurationRevision); err != nil {
		t.Fatal(err)
	}
	pair, err := f.conversations.CreateHumanDirect(f.ctx, f.a, CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}})
	if err != nil {
		t.Fatal(err)
	}
	group, err := f.conversations.CreateHumanDirect(f.ctx, f.b, CreateHumanDirectInput{
		PeerUserIDs: []uuid.UUID{f.c.UserID, revoked},
	})
	if err != nil {
		t.Fatal(err)
	}

	// A channel Agent's execution chat: b's invocation waits for input, c's
	// finished. a created the chat by mentioning the Agent first.
	var agentChat uuid.UUID
	dbtest.WithinSeedTx(t, f.ctx, f.pool, func(tx pgx.Tx) {
		messages := make([]uuid.UUID, 2)
		for i := range messages {
			if err := tx.QueryRow(f.ctx, `
				INSERT INTO conversation_items
					(company_id, conversation_id, seq, type, author_kind, author_id,
					 content, body, request_key, send_fingerprint)
				VALUES ($1, $2, 100 + $3::bigint, 'user_message', 'user', $4, 'Agent, look',
					'{"markdown":"Agent, look"}'::jsonb, gen_random_uuid(), decode(repeat('11', 32), 'hex'))
				RETURNING id`, companyID, channel.Conversation.ID, i, f.a.UserID).Scan(&messages[i]); err != nil {
				t.Fatalf("seed channel message: %v", err)
			}
		}
		if err := tx.QueryRow(f.ctx, `
			INSERT INTO conversations (company_id, kind, channel_id, agent_id, created_by_user_id, title)
			VALUES ($1, 'channel_agent', $2, $3, $4, 'Agent A') RETURNING id`,
			companyID, channel.Conversation.ID, f.seed.AgentA, f.a.UserID).Scan(&agentChat); err != nil {
			t.Fatalf("seed channel-Agent chat: %v", err)
		}
		for i, invocation := range []struct {
			caller uuid.UUID
			status string
		}{{f.b.UserID, "waiting_input"}, {f.c.UserID, "completed"}} {
			dbtest.MustExecTx(t, f.ctx, tx, `
				INSERT INTO channel_invocations
					(company_id, channel_id, conversation_id, agent_id, trigger_message_id,
					 thread_root_id, caller_user_id, agent_generation, member_generation,
					 channel_generation, trigger_content, trigger_revision, status)
				VALUES ($1, $2, $3, $4, $5, $5, $6, 1, 1, 1, 'Agent, look', 1, $7)`,
				companyID, channel.Conversation.ID, agentChat, f.seed.AgentA, messages[i],
				invocation.caller, invocation.status)
		}
	})

	// A Schedule chat created by b.
	schedule, scheduleChat := uuid.New(), uuid.New()
	dbtest.WithinSeedTx(t, f.ctx, f.pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, f.ctx, tx, `
			INSERT INTO schedules (id, company_id, conversation_id, creator_user_id, request_key,
				agent_id, reviewed_provider, name, instructions, trigger_type, run_at)
			VALUES ($1, $2, $3, $4, gen_random_uuid(), $5, 'codex', 'Digest', 'Digest', 'once', now())`,
			schedule, companyID, scheduleChat, f.b.UserID, f.seed.AgentA)
		dbtest.MustExecTx(t, f.ctx, tx, `
			INSERT INTO conversations (id, company_id, kind, schedule_id, agent_id, created_by_user_id, title)
			VALUES ($1, $2, 'schedule', $3, $4, $5, 'Digest')`,
			scheduleChat, companyID, schedule, f.seed.AgentA, f.b.UserID)
	})

	// Now lose two people the ordinary ways.
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE users SET disabled_at = now() WHERE id = $1`, disabled)
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE company_members SET status = 'revoked', revoked_at = now()
		WHERE company_id = $1 AND user_id = $2`, companyID, revoked)

	reads := func(conversationID, userID uuid.UUID) bool {
		t.Helper()
		var found bool
		if err := f.pool.QueryRow(f.ctx, `SELECT EXISTS (SELECT 1 FROM conversation_readers
			WHERE company_id = $1 AND conversation_id = $2 AND user_id = $3)`,
			companyID, conversationID, userID).Scan(&found); err != nil {
			t.Fatal(err)
		}
		return found
	}
	discussionAccess := func(conversationID, userID uuid.UUID) bool {
		t.Helper()
		rows, err := q.ListDiscussionAccess(f.ctx, generated.ListDiscussionAccessParams{
			ManageRoles: auth.RolesWith(auth.PermissionChannelsManage), ActorUserID: userID, CompanyID: companyID,
			ConversationIds: []uuid.UUID{conversationID}, IncludeArchived: true, IncludeHidden: true, PageLimit: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		return len(rows) == 1 && rows[0].ContentAccess
	}
	liveCaller := func(conversationID, userID uuid.UUID) bool {
		t.Helper()
		tx, err := f.pool.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(f.ctx) }()
		_, err = generated.New(tx).LockHumanInputOriginConversation(f.ctx, generated.LockHumanInputOriginConversationParams{
			ConversationID: conversationID, CompanyID: companyID, ConversationKind: "channel_agent",
			AgentID: f.seed.AgentA, OriginUserID: uuid.NullUUID{UUID: userID, Valid: true},
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		return err == nil
	}
	active := map[uuid.UUID]bool{f.a.UserID: true, f.b.UserID: true, f.c.UserID: true, f.owner.UserID: true}

	checked := 0
	for _, person := range people {
		for _, conversationID := range []uuid.UUID{channel.Conversation.ID, pair.Conversation.ID, group.Conversation.ID} {
			if got, want := reads(conversationID, person), discussionAccess(conversationID, person); got != want {
				t.Errorf("discussion %s, person %s: conversation_readers = %t, ListDiscussionAccess = %t",
					conversationID, person, got, want)
			}
			checked++
		}
		// The creator arm of the lock is the one deliberate difference.
		want := active[person] && liveCaller(agentChat, person) && person != f.a.UserID
		if got := reads(agentChat, person); got != want {
			t.Errorf("channel-Agent chat, person %s: conversation_readers = %t, want %t", person, got, want)
		}
		for chat, creator := range map[uuid.UUID]uuid.UUID{f.seed.DirectConversationA: f.a.UserID, scheduleChat: f.b.UserID} {
			if got, want := reads(chat, person), active[person] && person == creator; got != want {
				t.Errorf("chat %s, person %s: conversation_readers = %t, want the creator rule %t", chat, person, got, want)
			}
		}
	}
	// The fixture must exercise both answers, or the comparison proves nothing.
	if !reads(channel.Conversation.ID, f.b.UserID) || reads(channel.Conversation.ID, f.c.UserID) ||
		reads(channel.Conversation.ID, revoked) || reads(channel.Conversation.ID, disabled) ||
		!reads(agentChat, f.b.UserID) || !liveCaller(agentChat, f.a.UserID) || reads(agentChat, f.a.UserID) ||
		!reads(group.Conversation.ID, f.c.UserID) || reads(group.Conversation.ID, revoked) ||
		!reads(scheduleChat, f.b.UserID) || checked != 18 {
		t.Fatal("the fixture no longer separates readers from non-readers")
	}
}

// A Schedule chat's files open to the Schedule's creator: Schedule chats now
// follow the same creator rule as direct chats (attachment_access through
// conversation_readers). Before ADR 0017 the download query named direct chats
// only, so a file in a Schedule chat opened to nobody but its uploader.
func TestScheduleChatFilesOpenToItsCreator(t *testing.T) {
	t.Parallel()
	f := newDiscussionFixture(t)
	companyID := f.a.CompanyID
	schedule, scheduleChat, message := uuid.New(), uuid.New(), uuid.New()
	dbtest.WithinSeedTx(t, f.ctx, f.pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, f.ctx, tx, `
			INSERT INTO schedules (id, company_id, conversation_id, creator_user_id, request_key,
				agent_id, reviewed_provider, name, instructions, trigger_type, run_at)
			VALUES ($1, $2, $3, $4, gen_random_uuid(), $5, 'codex', 'Digest', 'Digest', 'once', now())`,
			schedule, companyID, scheduleChat, f.b.UserID, f.seed.AgentA)
		dbtest.MustExecTx(t, f.ctx, tx, `
			INSERT INTO conversations (id, company_id, kind, schedule_id, agent_id, created_by_user_id, title)
			VALUES ($1, $2, 'schedule', $3, $4, $5, 'Digest')`,
			scheduleChat, companyID, schedule, f.seed.AgentA, f.b.UserID)
	})
	dbtest.MustExec(t, f.ctx, f.pool, `
		INSERT INTO conversation_items (id, company_id, conversation_id, run_id, seq, type, author_kind, author_id, content)
		VALUES ($1, $2, $3, gen_random_uuid(), 1, 'user_message', 'user', $4, 'Digest these')`,
		message, companyID, scheduleChat, f.b.UserID)
	// Someone else's upload on the creator's Schedule message, so only the
	// chat rule (not the uploader rule) can open it to the creator.
	var file uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO attachments
		(company_id, uploaded_by_user_id, request_key, filename, content_type, size_bytes, status)
		VALUES ($1, $2, gen_random_uuid(), 'digest.txt', 'text/plain', 4, 'ready') RETURNING id`,
		companyID, f.c.UserID).Scan(&file); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `INSERT INTO message_attachments (company_id, message_id, attachment_id, position)
		VALUES ($1, $2, $3, 0)`, companyID, message, file)
	for person, want := range map[uuid.UUID]bool{f.b.UserID: true, f.c.UserID: false, f.a.UserID: false} {
		_, err := generated.New(f.pool).AuthorizeAttachmentDownload(f.ctx, generated.AuthorizeAttachmentDownloadParams{
			CompanyID: companyID, AttachmentID: file, ActorUserID: person,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		if got := err == nil; got != want {
			t.Errorf("Schedule chat file for %s: opens = %t, want %t", person, got, want)
		}
	}
}
