//go:build integration

package conversation

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// seedBoard makes a private Board owned by the given member.
func seedBoard(t *testing.T, f discussionFixture, owner auth.UserPrincipal) uuid.UUID {
	t.Helper()
	var board uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO boards (company_id, title, created_by) VALUES ($1, 'Sprint plan', $2) RETURNING id`,
		owner.CompanyID, owner.UserID).
		Scan(&board); err != nil {
		t.Fatal(err)
	}
	return board
}

// A Board's chat lives on its Board: Chat never lists it, nobody manages it
// by hand, no Huddle starts from it, and it is read only while its people can
// still read the Board.
func TestBoardChannelStaysOnItsBoard(t *testing.T) {
	t.Parallel()
	f := newDiscussionFixture(t)
	board := seedBoard(t, f, f.a)
	channel := dbtest.SeedBoardChannel(
		t,
		f.ctx,
		f.pool,
		f.a.CompanyID,
		board,
		f.a.UserID,
		[]uuid.UUID{f.seed.AgentA},
	)

	listed := func(items []DiscussionSummary) bool {
		return slices.ContainsFunc(items, func(item DiscussionSummary) bool { return item.ID == channel })
	}
	for name, options := range map[string]DiscussionListOptions{
		"Channels": {}, "a name search": {Query: "whiteboard"},
	} {
		page, err := f.conversations.ListChannels(f.ctx, f.a, options)
		if err != nil || listed(page.Items) {
			t.Fatalf("%s lists the Board's chat: %+v, %v", name, page.Items, err)
		}
	}
	managed, err := f.conversations.ListChannelManagement(f.ctx, f.a, DiscussionListOptions{})
	if err != nil ||
		slices.ContainsFunc(managed.Items, func(item ChannelManagement) bool { return item.ID == channel }) {
		t.Fatalf("channel management lists the Board's chat: %+v, %v", managed.Items, err)
	}

	detail, err := f.conversations.FetchChannel(f.ctx, f.a, channel)
	if err != nil {
		t.Fatal(err)
	}
	if got := detail.Conversation; got.BoardID == nil || *got.BoardID != board || !got.Capabilities.Send ||
		got.Capabilities.ManageMembers || got.Capabilities.Archive || got.Capabilities.Restore {
		t.Fatalf("the Board's chat = %+v; want it bound to the Board, open, and not managed by hand", got)
	}
	revision := *detail.Conversation.ConfigurationRevision
	name := "Renamed"
	for action, mutate := range map[string]func() error{
		"rename": func() error {
			_, err := f.conversations.UpdateChannel(f.ctx, f.a, channel, ChannelUpdateInput{
				ExpectedRevision: revision, Name: &name,
			})
			return err
		},
		"add a member": func() error {
			_, err := f.conversations.SetChannelMember(f.ctx, f.a, channel, ChannelMemberInput{
				ExpectedRevision: revision, UserID: f.b.UserID, Active: true,
			})
			return err
		},
		"remove an Agent": func() error {
			_, err := f.conversations.SetChannelAgent(f.ctx, f.a, channel, ChannelAgentInput{
				ExpectedRevision: revision, AgentID: f.seed.AgentA,
			})
			return err
		},
		"archive": func() error {
			_, err := f.conversations.SetChannelArchived(f.ctx, f.a, channel, ChannelArchiveInput{
				ExpectedRevision: revision, Archived: true,
			})
			return err
		},
		"leave": func() error { return f.conversations.LeaveChannel(f.ctx, f.a, channel, revision) },
	} {
		// Channel management never sees a Board's chat (not found); a member
		// acting on it is refused.
		if err := mutate(); !errors.Is(err, ErrDiscussionForbidden) && !errors.Is(err, ErrDiscussionNotFound) {
			t.Fatalf("%s the Board's chat = %v, want it refused", action, err)
		}
	}
	huddles, err := NewHuddleService(
		HuddleServiceOptions{Store: f.store, Bus: f.bus, Media: &lifecycleHuddleMedia{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := huddles.Start(f.ctx, f.a, channel, StartHuddleRequest{RequestKey: uuid.New()}); !errors.Is(
		err,
		ErrHuddleNotFound,
	) {
		t.Fatalf("a Huddle from the Board's chat = %v, want ErrHuddleNotFound", err)
	}

	// A joined member who loses the Board loses its chat at once, before
	// membership catches up.
	dbtest.MustExec(t, f.ctx, f.pool,
		`INSERT INTO board_member_access (company_id, board_id, user_id, access) VALUES ($1, $2, $3, 'read')`,
		f.b.CompanyID, board, f.b.UserID)
	dbtest.AddDiscussionMember(t, f.ctx, f.pool, f.b.CompanyID, channel, f.b.UserID)
	if _, err := f.conversations.FetchChannel(f.ctx, f.b, channel); err != nil {
		t.Fatalf("a joined reader of the Board reading its chat = %v", err)
	}
	dbtest.MustExec(t, f.ctx, f.pool,
		`DELETE FROM board_member_access WHERE board_id = $1 AND user_id = $2`, board, f.b.UserID)
	if _, err := f.conversations.FetchChannel(f.ctx, f.b, channel); !errors.Is(err, ErrDiscussionNotFound) {
		t.Fatalf("reading the chat after losing the Board = %v, want ErrDiscussionNotFound", err)
	}
	if _, err := f.discussion.Send(
		f.ctx,
		f.b,
		channel,
		textSend("still here?"),
	); !errors.Is(
		err,
		ErrDiscussionNotFound,
	) {
		t.Fatalf("writing to the chat after losing the Board = %v, want ErrDiscussionNotFound", err)
	}
	var readers int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM conversation_readers WHERE conversation_id = $1 AND user_id = $2`,
		channel, f.b.UserID).
		Scan(&readers); err != nil ||
		readers != 0 {
		t.Fatalf("readers of the chat who lost the Board = %d, %v; want none", readers, err)
	}

	// An archived Board's chat is read-only.
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE boards SET archived_at = now() WHERE id = $1`, board)
	if _, err := f.discussion.Send(f.ctx, f.a, channel, textSend("hello")); !errors.Is(err, ErrDiscussionReadOnly) {
		t.Fatalf("writing to an archived Board's chat = %v, want ErrDiscussionReadOnly", err)
	}
}

// A tag in a Board's chat holds only while its caller can read the Board and
// the Agent is still invited to it.
func TestBoardChannelTagsFollowTheBoard(t *testing.T) {
	t.Parallel()
	f := newDiscussionFixture(t)
	board := seedBoard(t, f, f.a)
	channel := dbtest.SeedBoardChannel(
		t,
		f.ctx,
		f.pool,
		f.a.CompanyID,
		board,
		f.a.UserID,
		[]uuid.UUID{f.seed.AgentA},
	)
	dbtest.MustExec(t, f.ctx, f.pool,
		`INSERT INTO board_member_access (company_id, board_id, user_id, access) VALUES ($1, $2, $3, 'edit')`,
		f.b.CompanyID, board, f.b.UserID)
	dbtest.MustExec(t, f.ctx, f.pool,
		`INSERT INTO board_agent_access (company_id, board_id, agent_id, access) VALUES ($1, $2, $3, 'edit')`,
		f.a.CompanyID, board, f.seed.AgentA)
	dbtest.AddDiscussionMember(t, f.ctx, f.pool, f.b.CompanyID, channel, f.b.UserID)
	run := dbtest.SeedTaggedRun(
		t,
		f.ctx,
		f.pool,
		f.a.CompanyID,
		channel,
		f.seed.AgentA,
		f.seed.RuntimeA,
		f.b.UserID,
	)
	var invocation uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT invocation_id FROM runs WHERE id = $1`, run).
		Scan(&invocation); err != nil {
		t.Fatal(err)
	}
	authorized := func() bool {
		t.Helper()
		ok, err := generated.New(f.pool).
			ChannelInvocationAuthorized(f.ctx, generated.ChannelInvocationAuthorizedParams{
				InvocationID: invocation,
				ActorRoles:   auth.RolesWith(auth.PermissionConversationsDiscuss),
			})
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !authorized() {
		t.Fatal("a tag by a reader of the Board, of an invited Agent, is not authorized")
	}
	dbtest.MustExec(t, f.ctx, f.pool, `DELETE FROM board_agent_access WHERE board_id = $1`, board)
	if authorized() {
		t.Fatal("a tag of an Agent no longer invited to the Board is still authorized")
	}
	dbtest.MustExec(t, f.ctx, f.pool,
		`INSERT INTO board_agent_access (company_id, board_id, agent_id, access) VALUES ($1, $2, $3, 'read')`,
		f.a.CompanyID, board, f.seed.AgentA)
	dbtest.MustExec(t, f.ctx, f.pool, `DELETE FROM board_member_access WHERE board_id = $1`, board)
	if authorized() {
		t.Fatal("a tag by a member who lost the Board is still authorized")
	}
}

type boardChannelFixture struct {
	discussionFixture
	channels *DefaultBoardChannelService
	board    uuid.UUID
}

// newBoardChannelFixture gives A a private Board that invites AgentA to edit
// it; A created AgentA and runs it on their own runtime.
func newBoardChannelFixture(t *testing.T) boardChannelFixture {
	t.Helper()
	f := newDiscussionFixture(t)
	channels, err := NewBoardChannelService(BoardChannelServiceOptions{Store: f.store, Bus: f.bus})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE agents SET created_by_user_id = $2 WHERE id = $1`,
		f.seed.AgentA,
		f.a.UserID,
	)
	board := seedBoard(t, f, f.a)
	dbtest.MustExec(t, f.ctx, f.pool,
		`INSERT INTO board_agent_access (company_id, board_id, agent_id, access) VALUES ($1, $2, $3, 'edit')`,
		f.a.CompanyID, board, f.seed.AgentA)
	return boardChannelFixture{discussionFixture: f, channels: channels, board: board}
}

func (f boardChannelFixture) join(t *testing.T, p auth.UserPrincipal) BoardChannel {
	t.Helper()
	channel, err := f.channels.Join(f.ctx, p, f.board)
	if err != nil {
		t.Fatal(err)
	}
	return channel
}

func (f boardChannelFixture) share(t *testing.T, p auth.UserPrincipal, access string) {
	t.Helper()
	dbtest.MustExec(t, f.ctx, f.pool, `
		INSERT INTO board_member_access (company_id, board_id, user_id, access) VALUES ($1, $2, $3, $4)
		ON CONFLICT (board_id, user_id) DO UPDATE SET access = excluded.access`,
		p.CompanyID, f.board, p.UserID, access)
}

func (f boardChannelFixture) joined(t *testing.T, channel uuid.UUID, p auth.UserPrincipal) bool {
	t.Helper()
	var active bool
	err := f.pool.QueryRow(f.ctx,
		`SELECT status = 'active' FROM conversation_members WHERE conversation_id = $1 AND user_id = $2`,
		channel, p.UserID).Scan(&active)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	return active
}

func mentionSend(text string, people ...uuid.UUID) DiscussionSendInput {
	input := textSend("")
	for _, id := range people {
		start := len(input.Body.Markdown)
		input.Body.Markdown += "@Person "
		ref, _ := json.Marshal(discussionReference{Type: "user", UserID: &id, Start: start, End: start + 7})
		input.Body.References = append(input.Body.References, ref)
	}
	input.Body.Markdown += text
	return input
}

// Opening a Board's chat starts it once however many open it at the same
// time, joins its reader with mentions-only notices, lists who can read the
// Board to mention, and makes the Agents invited to the Board its Agents.
// Someone who cannot read the Board cannot open it.
func TestBoardChannelJoinFollowsTheBoard(t *testing.T) {
	t.Parallel()
	f := newBoardChannelFixture(t)
	if _, err := f.channels.Join(f.ctx, f.b, f.board); !errors.Is(err, ErrDiscussionNotFound) {
		t.Fatalf("opening an unshared Board's chat = %v, want ErrDiscussionNotFound", err)
	}
	if _, err := f.channels.Channel(f.ctx, f.b, f.board); !errors.Is(err, ErrDiscussionNotFound) {
		t.Fatalf("reading an unshared Board's chat = %v, want ErrDiscussionNotFound", err)
	}
	unopened, err := f.channels.Channel(f.ctx, f.a, f.board)
	if err != nil || unopened.ConversationID != nil || unopened.Joined {
		t.Fatalf("an unopened Board's chat = %+v, %v; want none yet", unopened, err)
	}

	const openers = 8
	ids := make(chan uuid.UUID, openers)
	failures := make(chan error, openers)
	for range openers {
		go func() {
			channel, err := f.channels.Join(f.ctx, f.a, f.board)
			if err != nil {
				failures <- err
				return
			}
			ids <- *channel.ConversationID
		}()
	}
	var channel uuid.UUID
	for range openers {
		select {
		case err := <-failures:
			t.Fatal(err)
		case id := <-ids:
			if channel != uuid.Nil && id != channel {
				t.Fatalf("concurrent opens started two chats: %s and %s", channel, id)
			}
			channel = id
		}
	}

	f.share(t, f.b, "read")
	opened := f.join(t, f.b)
	if !opened.Joined || *opened.ConversationID != channel {
		t.Fatalf("B opening the shared Board's chat = %+v", opened)
	}
	people := map[uuid.UUID]bool{}
	for _, person := range opened.People {
		people[person.UserID] = person.Joined
	}
	if len(people) != 2 || !people[f.a.UserID] || !people[f.b.UserID] {
		t.Fatalf("people of a Board shared with B = %+v, want A and B", opened.People)
	}
	detail, err := f.conversations.FetchChannel(f.ctx, f.b, channel)
	if err != nil {
		t.Fatal(err)
	}
	if state := detail.Conversation.UserState; state == nil || state.NotificationLevel != "mentions" {
		t.Fatalf("B's notices in the Board's chat = %+v, want mentions only", state)
	}
	if len(detail.Agents) != 1 || detail.Agents[0].AgentID != f.seed.AgentA {
		t.Fatalf("the Board chat's Agents = %+v, want the Agent invited to the Board", detail.Agents)
	}

	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE boards SET visibility = 'company' WHERE id = $1`, f.board)
	everyone, err := f.channels.Channel(f.ctx, f.c, f.board)
	if err != nil || len(everyone.People) != 4 || everyone.Joined {
		t.Fatalf(
			"a Company Board's chat as C sees it = %+v, %v; want all four members, C not joined",
			everyone,
			err,
		)
	}
}

// After the Board's access changes, its chat follows: people who lost the
// Board leave and their tags stop, and an Agent no longer invited leaves
// with its queued work.
func TestBoardChannelSyncFollowsAccessChanges(t *testing.T) {
	t.Parallel()
	f := newBoardChannelFixture(t)
	// B runs AgentA on a computer of their own, so B's tags run for B.
	runtimeID := dbtest.NewUUID(t)
	dbtest.MustExec(t, f.ctx, f.pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, 'daemon-board-chat', 'B laptop', 'codex', 'online', $3, now())`,
		runtimeID, f.seed.CompanyA, f.b.UserID)
	dbtest.SeedAgentBinding(t, f.ctx, f.pool, f.seed.CompanyA, f.seed.AgentA, f.b.UserID, runtimeID, "codex")
	channel := *f.join(t, f.a).ConversationID
	f.share(t, f.b, "edit")
	f.join(t, f.b)
	receipt, err := f.discussion.Send(f.ctx, f.b, channel, taggedSend(t, f.seed.AgentA))
	if err != nil {
		t.Fatal(err)
	}
	invocationStatus := func() string {
		t.Helper()
		var status string
		if err := f.pool.QueryRow(f.ctx, `SELECT status FROM channel_invocations WHERE trigger_message_id = $1`,
			receipt.Message.ID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	if status := invocationStatus(); status == "cancelled" {
		t.Fatalf("B's tag of the invited Agent = %s", status)
	}

	dbtest.MustExec(t, f.ctx, f.pool, `DELETE FROM board_member_access WHERE board_id = $1 AND user_id = $2`,
		f.board, f.b.UserID)
	if err := f.channels.Sync(f.ctx, f.a.CompanyID, f.board); err != nil {
		t.Fatal(err)
	}
	if f.joined(t, channel, f.b) {
		t.Fatal("B is still in the Board's chat after losing the Board")
	}
	if status := invocationStatus(); status != "cancelled" {
		t.Fatalf("B's tag after B lost the Board = %s, want cancelled", status)
	}

	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE boards SET visibility = 'company' WHERE id = $1`, f.board)
	f.join(t, f.c)
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE boards SET visibility = 'private' WHERE id = $1`, f.board)
	if err := f.channels.Sync(f.ctx, f.a.CompanyID, f.board); err != nil {
		t.Fatal(err)
	}
	if f.joined(t, channel, f.c) || !f.joined(t, channel, f.a) {
		t.Fatal("making the Board private kept C or dropped its owner")
	}

	dbtest.MustExec(t, f.ctx, f.pool, `DELETE FROM board_agent_access WHERE board_id = $1`, f.board)
	if err := f.channels.Sync(f.ctx, f.a.CompanyID, f.board); err != nil {
		t.Fatal(err)
	}
	if _, err := f.discussion.Send(f.ctx, f.a, channel, taggedSend(t, f.seed.AgentA)); !errors.Is(
		err,
		ErrDiscussionAgentRequired,
	) {
		t.Fatalf("tagging an Agent no longer invited to the Board = %v, want ErrDiscussionAgentRequired", err)
	}
	// A Board without a chat is left alone.
	if err := f.channels.Sync(f.ctx, f.a.CompanyID, seedBoard(t, f.discussionFixture, f.a)); err != nil {
		t.Fatal(err)
	}
}

// Mentioning someone who can read the Board brings them into its chat with a
// notice that opens the Board; someone who cannot read it cannot be
// mentioned. Plain messages do not notify mentions-only members.
func TestBoardChannelMentionsBringReadersIn(t *testing.T) {
	t.Parallel()
	f := newBoardChannelFixture(t)
	channel := *f.join(t, f.a).ConversationID
	f.share(t, f.b, "read")
	f.join(t, f.b)
	notices := func(p auth.UserPrincipal) []string {
		t.Helper()
		rows, err := f.pool.Query(
			f.ctx,
			`SELECT deep_link FROM inbox_items WHERE recipient_user_id = $1 AND company_id = $2
			ORDER BY created_at`,
			p.UserID,
			p.CompanyID,
		)
		if err != nil {
			t.Fatal(err)
		}
		links, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return links
	}
	if _, err := f.discussion.Send(f.ctx, f.a, channel, textSend("Morning")); err != nil {
		t.Fatal(err)
	}
	if links := notices(f.b); len(links) != 0 {
		t.Fatalf("a plain message notified a mentions-only member: %v", links)
	}

	if _, err := f.discussion.Send(
		f.ctx,
		f.a,
		channel,
		mentionSend("look", f.c.UserID),
	); !errors.Is(
		err,
		ErrDiscussionInvalid,
	) {
		t.Fatalf("mentioning someone who cannot read the Board = %v, want ErrDiscussionInvalid", err)
	}
	if f.joined(t, channel, f.c) {
		t.Fatal("mentioning someone who cannot read the Board brought them in")
	}

	f.share(t, f.c, "read")
	sent, err := f.discussion.Send(f.ctx, f.a, channel, mentionSend("look at this", f.c.UserID))
	if err != nil {
		t.Fatal(err)
	}
	if !f.joined(t, channel, f.c) {
		t.Fatal("a mentioned reader of the Board did not join its chat")
	}
	want := "/whiteboards/" + f.board.String() + "?companyId=" + f.a.CompanyID.String() +
		"&conversationId=" + channel.String() +
		"&messageId=" + sent.Message.ID.String()
	if links := notices(f.c); len(links) != 1 || links[0] != want {
		t.Fatalf("C's notices = %v, want %s", links, want)
	}
	threadRoot := sent.Message.ID
	reply := mentionSend("and here", f.b.UserID)
	reply.ThreadRootID = &threadRoot
	if _, err := f.discussion.Send(f.ctx, f.a, channel, reply); err != nil {
		t.Fatal(err)
	}
	if links := notices(
		f.b,
	); len(links) != 1 ||
		!strings.HasSuffix(links[0], "&threadRootId="+threadRoot.String()) {
		t.Fatalf("B's notices for a thread mention = %v, want the thread", links)
	}

	if _, err := f.discussion.Send(f.ctx, f.a, channel, taggedSend(t, f.seed.AgentA)); err != nil {
		t.Fatalf("tagging the Agent invited to the Board = %v", err)
	}
	var runs int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM runs JOIN conversations c ON c.id = runs.conversation_id
		WHERE c.channel_id = $1 AND runs.run_type = 'channel_agent'`, channel).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("Runs of the tag = %d, %v; want one", runs, err)
	}
}

// Opening, mentioning and access changes racing each other never deadlock,
// and the chat ends with exactly the Board's readers it took in.
func TestBoardChannelSurvivesConcurrentChanges(t *testing.T) {
	t.Parallel()
	f := newBoardChannelFixture(t)
	channel := *f.join(t, f.a).ConversationID
	const rounds = 12
	errs := make(chan error, 4*rounds)
	var wg sync.WaitGroup
	for i := range rounds {
		wg.Add(4)
		go func() {
			defer wg.Done()
			if _, err := f.channels.Join(f.ctx, f.a, f.board); err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			_, err := f.discussion.Send(f.ctx, f.a, channel, mentionSend("ping", f.b.UserID))
			if err != nil && !errors.Is(err, ErrDiscussionInvalid) {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				_, err := f.pool.Exec(
					f.ctx,
					`INSERT INTO board_member_access (company_id, board_id, user_id, access)
					VALUES ($1, $2, $3, 'read') ON CONFLICT DO NOTHING`,
					f.b.CompanyID,
					f.board,
					f.b.UserID,
				)
				if err != nil {
					errs <- err
				}
				return
			}
			if _, err := f.pool.Exec(
				f.ctx,
				`DELETE FROM board_member_access WHERE board_id = $1 AND user_id = $2`,
				f.board,
				f.b.UserID,
			); err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			if err := f.channels.Sync(f.ctx, f.a.CompanyID, f.board); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if err := f.channels.Sync(f.ctx, f.a.CompanyID, f.board); err != nil {
		t.Fatal(err)
	}
	var reads bool
	if err := f.pool.QueryRow(f.ctx, `SELECT EXISTS (SELECT 1 FROM board_access WHERE board_id = $1 AND user_id = $2)`,
		f.board, f.b.UserID).
		Scan(&reads); err != nil {
		t.Fatal(err)
	}
	if f.joined(t, channel, f.b) && !reads {
		t.Fatal("B is in the Board's chat without reading the Board")
	}
}
