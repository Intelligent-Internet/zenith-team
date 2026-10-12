//go:build integration

package conversation

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestDiscussionReactionPeopleByteBound(t *testing.T) {
	t.Parallel()
	f := newDiscussionFixture(t)
	dbtest.MustExec(t, f.ctx, f.pool, `WITH people AS (
 INSERT INTO users(email,display_name,avatar_url)
 SELECT 'reaction-'||n||'@example.test','Member '||n,'https://example.test/?'||repeat('&',2000)
 FROM generate_series(1,100) n RETURNING id
 ) INSERT INTO company_members(company_id,user_id,role) SELECT $1,id,'member' FROM people`, f.a.CompanyID)
	err := f.store.WithinTx(f.ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := q.LockCompanyForMembershipMutation(f.ctx, f.a.CompanyID); err != nil {
			return err
		}
		var pending []events.Event
		return JoinGeneralChannelInTx(f.ctx, q, &pending, GeneralChannelMembership{
			CompanyID: f.a.CompanyID,
			UserID:    f.a.UserID,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	general, err := f.store.Queries().GetGeneralChannelForUpdate(f.ctx, f.a.CompanyID)
	if err != nil {
		t.Fatal(err)
	}
	root, err := f.discussion.Send(f.ctx, f.a, general.ID, textSend("Reaction people"))
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`INSERT INTO conversation_message_reactions(company_id,conversation_id,message_id,user_id,emoji)
 SELECT $1,$2,$3,u.id,'👍' FROM users u JOIN company_members m ON m.user_id=u.id
 WHERE m.company_id=$1 AND u.email LIKE 'reaction-%'`,
		f.a.CompanyID,
		general.ID,
		root.Message.ID,
	)
	target := DiscussionMessageTarget{ConversationID: general.ID, MessageID: root.Message.ID}
	seen := make(map[uuid.UUID]bool)
	options := DiscussionPageOptions{}
	for {
		page, err := f.discussion.ListReactions(f.ctx, f.a, target, options)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > maxDiscussionPageBytes {
			t.Fatalf("reaction page bytes = %d, want <= %d", len(encoded), maxDiscussionPageBytes)
		}
		if len(page.Items) != 1 || page.Items[0].Count != 100 || len(page.Items[0].Users) == 0 {
			t.Fatalf("reaction page = %+v", page)
		}
		for _, person := range page.Items[0].Users {
			if person.ID == nil || seen[*person.ID] {
				t.Fatalf("missing or duplicate person ID: %v", person.ID)
			}
			seen[*person.ID] = true
		}
		if page.NextCursor == nil {
			break
		}
		options.Cursor = *page.NextCursor
	}
	if len(seen) != 100 {
		t.Fatalf("reaction people = %d, want 100", len(seen))
	}
}

func TestDiscussionCollaborationSchema(t *testing.T) {
	t.Parallel()
	f := newDiscussionFixture(t)
	channel, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{RequestKey: uuid.New(), Name: "Integrity"},
	)
	if err != nil {
		t.Fatal(err)
	}
	id := channel.Conversation.ID
	root, err := f.discussion.Send(f.ctx, f.a, id, textSend("Root"))
	if err != nil {
		t.Fatal(err)
	}
	input := textSend("Reply")
	input.ThreadRootID = &root.Message.ID
	reply, err := f.discussion.Send(f.ctx, f.a, id, input)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.discussion.DeleteMessage(
		f.ctx,
		f.a,
		DiscussionMessageTarget{ConversationID: id, MessageID: root.Message.ID},
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, sql string
		target    uuid.UUID
	}{
		{"pin deleted", `INSERT INTO conversation_message_pins(company_id,conversation_id,message_id,pinned_by_user_id) VALUES($1,$2,$3,$4)`, root.Message.ID},
		{"react deleted", `INSERT INTO conversation_message_reactions(company_id,conversation_id,message_id,user_id,emoji) VALUES($1,$2,$3,$4,'👍')`, root.Message.ID},
		{"follow reply", `INSERT INTO conversation_thread_states(company_id,conversation_id,root_id,user_id,grant_generation,follow) SELECT $1,$2,$3,$4,generation,'following' FROM conversation_members WHERE conversation_id=$2 AND user_id=$4`, reply.Message.ID},
		{"stale follow grant", `INSERT INTO conversation_thread_states(company_id,conversation_id,root_id,user_id,grant_generation,follow) SELECT $1,$2,$3,$4,generation+1,'following' FROM conversation_members WHERE conversation_id=$2 AND user_id=$4 ON CONFLICT(root_id,user_id) DO UPDATE SET grant_generation=EXCLUDED.grant_generation`, root.Message.ID},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := f.pool.Exec(f.ctx, test.sql, f.a.CompanyID, id, test.target, f.a.UserID)
			var postgresError *pgconn.PgError
			if !errors.As(err, &postgresError) || postgresError.Code != "23514" {
				t.Fatalf("invalid collaboration row = %v, want check violation", err)
			}
		})
	}
}

func TestDiscussionOwnerFollowAndArchivedCollaboration(t *testing.T) {
	t.Parallel()
	f := newDiscussionFixture(t)
	channel, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{RequestKey: uuid.New(), Name: "Shared", MemberUserIDs: []uuid.UUID{f.b.UserID, f.owner.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	id := channel.Conversation.ID
	root, err := f.discussion.Send(f.ctx, f.b, id, textSend("Root"))
	if err != nil {
		t.Fatal(err)
	}
	target := DiscussionMessageTarget{ConversationID: id, MessageID: root.Message.ID}
	for _, actor := range []auth.UserPrincipal{f.a, f.b, f.owner} {
		if _, err := f.discussion.SetReaction(
			f.ctx,
			actor,
			target,
			DiscussionReactionInput{Emoji: "❤", Active: true},
		); err != nil {
			t.Fatal(err)
		}
	}
	people, err := f.discussion.ListReactions(f.ctx, f.b, target, DiscussionPageOptions{Limit: 1})
	if err != nil || len(people.Items) != 1 || people.Items[0].Emoji != "❤️" || people.Items[0].Count != 3 ||
		len(people.Items[0].Users) != 1 ||
		people.NextCursor == nil {
		t.Fatalf("bounded people = %+v, %v", people, err)
	}
	seen := map[uuid.UUID]bool{*people.Items[0].Users[0].ID: true}
	for people.NextCursor != nil {
		people, err = f.discussion.ListReactions(
			f.ctx,
			f.b,
			target,
			DiscussionPageOptions{Limit: 1, Cursor: *people.NextCursor},
		)
		if err != nil {
			t.Fatal(err)
		}
		for _, reaction := range people.Items {
			for _, person := range reaction.Users {
				if seen[*person.ID] {
					t.Fatal("duplicate reaction person")
				}
				seen[*person.ID] = true
			}
		}
	}
	if len(seen) != 3 {
		t.Fatalf("reaction people = %d", len(seen))
	}
	ownerState, err := f.discussion.UpdateThreadState(
		f.ctx,
		f.owner,
		target,
		DiscussionThreadStateInput{Follow: "following"},
	)
	if err != nil || ownerState.Revision != 1 || ownerState.GrantGeneration == nil {
		t.Fatalf("owner follows = %+v, %v", ownerState, err)
	}
	meta, err := f.conversations.FetchChannelManagement(f.ctx, f.a, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.conversations.SetChannelArchived(
		f.ctx,
		f.a,
		id,
		ChannelArchiveInput{ExpectedRevision: meta.ConfigurationRevision, Archived: true},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := f.discussion.UpdateThreadState(
		f.ctx,
		f.owner,
		target,
		DiscussionThreadStateInput{ExpectedRevision: ownerState.Revision, Follow: "unfollowed"},
	); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func() error{
		func() error { _, err := f.discussion.SetMessagePin(f.ctx, f.a, target, true); return err },
		func() error {
			_, err := f.discussion.SetReaction(f.ctx, f.a, target, DiscussionReactionInput{Emoji: "👍", Active: true})
			return err
		},
		func() error {
			_, err := f.discussion.EditMessage(f.ctx, f.b, target, DiscussionEditInput{ExpectedRevision: 1, Body: textSend("edit").Body})
			return err
		},
		func() error { _, err := f.discussion.DeleteMessage(f.ctx, f.b, target, 1); return err },
	} {
		if err := mutate(); !errors.Is(err, ErrDiscussionReadOnly) {
			t.Fatalf("archived mutation = %v", err)
		}
	}
	if _, err := f.discussion.ListThread(f.ctx, f.b, target, DiscussionPageOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestDiscussionThreadsMutationsAndReplay(t *testing.T) {
	t.Parallel()
	f := newDiscussionFixture(t)
	dm, err := f.conversations.CreateHumanDirect(f.ctx, f.a, CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}})
	if err != nil {
		t.Fatal(err)
	}
	id := dm.Conversation.ID
	input := textSend("Original root")
	root, err := f.discussion.Send(f.ctx, f.a, id, input)
	if err != nil {
		t.Fatal(err)
	}
	target := DiscussionMessageTarget{ConversationID: id, MessageID: root.Message.ID}
	replyInput := textSend("First reply")
	replyInput.ThreadRootID = &root.Message.ID
	reply, err := f.discussion.Send(f.ctx, f.b, id, replyInput)
	if err != nil {
		t.Fatal(err)
	}
	nestedInput := textSend("Reply to reply")
	nestedInput.ThreadRootID = &reply.Message.ID
	nested, err := f.discussion.Send(f.ctx, f.a, id, nestedInput)
	if err != nil {
		t.Fatal(err)
	}
	if nested.Message.ThreadRootID == nil || *nested.Message.ThreadRootID != root.Message.ID {
		t.Fatalf("thread root = %v, want %s", nested.Message.ThreadRootID, root.Message.ID)
	}
	page, err := f.discussion.ListMessages(f.ctx, f.b, id, DiscussionPageOptions{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ThreadSummary.ReplyCount != 2 {
		t.Fatalf("root page = %+v, %v", page, err)
	}
	thread, err := f.discussion.ListThread(f.ctx, f.b, target, DiscussionPageOptions{Limit: 1})
	if err != nil || len(thread.Items) != 1 || thread.Items[0].ID != nested.Message.ID ||
		thread.OlderCursor == nil ||
		thread.State.Follow != "auto" {
		t.Fatalf("thread = %+v, %v", thread, err)
	}
	older, err := f.discussion.ListThread(
		f.ctx,
		f.b,
		target,
		DiscussionPageOptions{Limit: 1, Cursor: *thread.OlderCursor},
	)
	if err != nil || len(older.Items) != 1 || older.Items[0].ID != reply.Message.ID || older.OlderCursor != nil {
		t.Fatalf("older replies = %+v, %v", older, err)
	}
	if _, err = f.discussion.ListMessages(
		f.ctx,
		f.b,
		id,
		DiscussionPageOptions{Cursor: thread.RefreshCursor},
	); !errors.Is(
		err,
		ErrDiscussionInvalidCursor,
	) {
		t.Fatalf("thread cursor on root = %v", err)
	}
	context, err := f.discussion.MessageContext(
		f.ctx,
		f.b,
		DiscussionMessageTarget{ConversationID: id, MessageID: nested.Message.ID},
	)
	if err != nil || context.Root == nil || context.Root.ID != root.Message.ID || len(context.Items) != 2 {
		t.Fatalf("reply context = %+v, %v", context, err)
	}
	if _, err = f.discussion.EditMessage(
		f.ctx,
		f.b,
		target,
		DiscussionEditInput{ExpectedRevision: 1, Body: textSend("forged").Body},
	); !errors.Is(
		err,
		ErrDiscussionForbidden,
	) {
		t.Fatalf("non-author edit = %v", err)
	}
	edited, err := f.discussion.EditMessage(
		f.ctx,
		f.a,
		target,
		DiscussionEditInput{ExpectedRevision: 1, Body: textSend("Edited root").Body},
	)
	if err != nil || edited.Message.Revision != 2 || edited.Message.EditedAt == nil {
		t.Fatalf("edit = %+v, %v", edited, err)
	}
	replay, err := f.discussion.Send(f.ctx, f.a, id, input)
	if err != nil || replay.Message.Body.Markdown != "Edited root" || !replay.Replayed {
		t.Fatalf("edited replay = %+v, %v", replay, err)
	}
	if _, err = f.discussion.DeleteMessage(f.ctx, f.a, target, 1); !errors.Is(err, ErrDiscussionRevisionConflict) {
		t.Fatalf("stale delete = %v", err)
	}
	deleted, err := f.discussion.DeleteMessage(f.ctx, f.a, target, 2)
	if err != nil || deleted.Message.Body != nil || deleted.Message.DeletedAt == nil ||
		deleted.Message.Seq != root.Message.Seq {
		t.Fatalf("delete = %+v, %v", deleted, err)
	}
	replay, err = f.discussion.Send(f.ctx, f.a, id, input)
	if err != nil || replay.Message.Body != nil || replay.Message.DeletedAt == nil {
		t.Fatalf("deleted replay = %+v, %v", replay, err)
	}
	thread, err = f.discussion.ListThread(f.ctx, f.b, target, DiscussionPageOptions{})
	if err != nil || thread.Root.Body != nil || len(thread.Items) != 2 {
		t.Fatalf("deleted root thread = %+v, %v", thread, err)
	}
	replay, err = f.discussion.Send(f.ctx, f.a, id, nestedInput)
	if err != nil || replay.Message.ID != nested.Message.ID || !replay.Replayed {
		t.Fatalf("nested replay = %+v, %v", replay, err)
	}
	for _, p := range []auth.UserPrincipal{f.c, f.owner} {
		if _, err = f.discussion.MessageContext(f.ctx, p, target); !errors.Is(err, ErrDiscussionNotFound) {
			t.Fatalf("private context = %v", err)
		}
		if _, err = f.discussion.ListThread(
			f.ctx,
			p,
			target,
			DiscussionPageOptions{},
		); !errors.Is(
			err,
			ErrDiscussionNotFound,
		) {
			t.Fatalf("private thread = %v", err)
		}
	}
}

func TestDiscussionSharedPinsReactionsAndFollow(t *testing.T) {
	t.Parallel()
	f := newDiscussionFixture(t)
	channel, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{
			RequestKey:    uuid.New(),
			Name:          "Collaboration",
			MemberUserIDs: []uuid.UUID{f.b.UserID},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	id := channel.Conversation.ID
	root, err := f.discussion.Send(f.ctx, f.a, id, textSend("Root"))
	if err != nil {
		t.Fatal(err)
	}
	target := DiscussionMessageTarget{ConversationID: id, MessageID: root.Message.ID}
	replyInput := textSend("Pinned reply")
	replyInput.ThreadRootID = &root.Message.ID
	reply, err := f.discussion.Send(f.ctx, f.b, id, replyInput)
	if err != nil {
		t.Fatal(err)
	}
	replyTarget := DiscussionMessageTarget{ConversationID: id, MessageID: reply.Message.ID}
	pin, err := f.discussion.SetMessagePin(f.ctx, f.b, replyTarget, true)
	if err != nil || !pin.Message.Pinned {
		t.Fatalf("pin = %+v, %v", pin, err)
	}
	again, err := f.discussion.SetMessagePin(f.ctx, f.a, replyTarget, true)
	if err != nil || again.ConversationRevision != pin.ConversationRevision {
		t.Fatalf("pin replay = %+v, %v", again, err)
	}
	pins, err := f.discussion.ListPins(f.ctx, f.a, id, DiscussionPageOptions{Limit: 1})
	if err != nil || pins.TotalCount != 1 || len(pins.Items) != 1 || pins.Items[0].Message.ID != reply.Message.ID ||
		pins.Items[0].Root == nil ||
		pins.Items[0].Root.ID != root.Message.ID {
		t.Fatalf("pins = %+v, %v", pins, err)
	}
	for _, p := range []auth.UserPrincipal{f.a, f.b} {
		for range 2 {
			if _, err = f.discussion.SetReaction(
				f.ctx,
				p,
				replyTarget,
				DiscussionReactionInput{Emoji: "👍🏽", Active: true},
			); err != nil {
				t.Fatal(err)
			}
		}
	}
	reactions, err := f.discussion.ListReactions(f.ctx, f.b, replyTarget, DiscussionPageOptions{})
	if err != nil || len(reactions.Items) != 1 || reactions.Items[0].Count != 2 || !reactions.Items[0].Reacted ||
		len(reactions.Items[0].Users) != 2 {
		t.Fatalf("reactions = %+v, %v", reactions, err)
	}
	thread, err := f.discussion.ListThread(f.ctx, f.b, target, DiscussionPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	state, err := f.discussion.UpdateThreadState(
		f.ctx,
		f.b,
		target,
		DiscussionThreadStateInput{ExpectedRevision: thread.State.Revision, Follow: "unfollowed"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.discussion.EditMessage(
		f.ctx,
		f.b,
		replyTarget,
		DiscussionEditInput{ExpectedRevision: 1, Body: textSend("Still unfollowed").Body},
	); err != nil {
		t.Fatal(err)
	}
	thread, err = f.discussion.ListThread(f.ctx, f.b, target, DiscussionPageOptions{})
	if err != nil || thread.State.Follow != "unfollowed" || thread.State.Revision != state.Revision {
		t.Fatalf("explicit unfollow = %+v, %v", thread.State, err)
	}
	replyInput.RequestKey = uuid.New()
	if _, err = f.discussion.Send(f.ctx, f.b, id, replyInput); err != nil {
		t.Fatal(err)
	}
	thread, err = f.discussion.ListThread(f.ctx, f.b, target, DiscussionPageOptions{})
	if err != nil || thread.State.Follow != "auto" {
		t.Fatalf("reply opt-in = %+v, %v", thread.State, err)
	}
	if _, err = f.discussion.DeleteMessage(f.ctx, f.b, target, 1); !errors.Is(err, ErrDiscussionForbidden) {
		t.Fatalf("member delete other = %v", err)
	}
	if _, err = f.discussion.DeleteMessage(f.ctx, f.a, replyTarget, 2); err != nil {
		t.Fatal(err)
	}
	pins, err = f.discussion.ListPins(f.ctx, f.b, id, DiscussionPageOptions{})
	if err != nil || pins.TotalCount != 0 || len(pins.Items) != 0 {
		t.Fatalf("deleted pins = %+v, %v", pins, err)
	}
	reactions, err = f.discussion.ListReactions(f.ctx, f.b, replyTarget, DiscussionPageOptions{})
	if err != nil || len(reactions.Items) != 0 {
		t.Fatalf("deleted reactions = %+v, %v", reactions, err)
	}
	if _, err = f.discussion.SetReaction(
		f.ctx,
		f.b,
		replyTarget,
		DiscussionReactionInput{Emoji: "👍", Active: true},
	); !errors.Is(
		err,
		ErrDiscussionForbidden,
	) {
		t.Fatalf("react tombstone = %v", err)
	}
	meta, err := f.conversations.FetchChannelManagement(f.ctx, f.a, id)
	if err != nil {
		t.Fatal(err)
	}
	oldCursor := thread.RefreshCursor
	meta, err = f.conversations.SetChannelMember(
		f.ctx,
		f.a,
		id,
		ChannelMemberInput{ExpectedRevision: meta.ConfigurationRevision, UserID: f.b.UserID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.discussion.ListPins(
		f.ctx,
		f.b,
		id,
		DiscussionPageOptions{},
	); !errors.Is(
		err,
		ErrDiscussionNotFound,
	) {
		t.Fatalf("revoked pins = %v", err)
	}
	if _, err = f.discussion.UpdateThreadState(
		f.ctx,
		f.b,
		target,
		DiscussionThreadStateInput{ExpectedRevision: 0, Follow: "following"},
	); !errors.Is(
		err,
		ErrDiscussionNotFound,
	) {
		t.Fatalf("revoked follow = %v", err)
	}
	_, err = f.conversations.SetChannelMember(
		f.ctx,
		f.a,
		id,
		ChannelMemberInput{ExpectedRevision: meta.ConfigurationRevision, UserID: f.b.UserID, Active: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.discussion.ListThread(
		f.ctx,
		f.b,
		target,
		DiscussionPageOptions{Cursor: oldCursor},
	); !errors.Is(
		err,
		ErrDiscussionCursorExpired,
	) {
		t.Fatalf("regrant cursor = %v", err)
	}
	thread, err = f.discussion.ListThread(f.ctx, f.b, target, DiscussionPageOptions{})
	if err != nil || thread.State.Follow != "" || thread.State.Revision != 0 {
		t.Fatalf("regrant follow = %+v, %v", thread.State, err)
	}
}
