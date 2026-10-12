//go:build integration

package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

type channelInvocationFixture struct {
	discussionFixture
	channelID uuid.UUID
	service   *DefaultChannelInvocationService
}

func newChannelInvocationFixture(t *testing.T) channelInvocationFixture {
	t.Helper()
	f := newDiscussionFixture(t)
	// UserA already runs AgentA on their own runtime through the fixture's
	// binding (ADR 0014); here they also become its creator.
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE agents SET created_by_user_id=$2 WHERE id=$1`,
		f.seed.AgentA,
		f.a.UserID,
	)
	channel, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{
			RequestKey:    uuid.New(),
			Name:          "Agent requests",
			MemberUserIDs: []uuid.UUID{f.b.UserID},
			AgentIDs:      []uuid.UUID{f.seed.AgentA},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return channelInvocationFixture{
		discussionFixture: f,
		channelID:         channel.Conversation.ID,
		service:           NewChannelInvocationService(f.store, f.bus),
	}
}

func taggedSend(t *testing.T, agents ...uuid.UUID) DiscussionSendInput {
	t.Helper()
	input := textSend("")
	for _, id := range agents {
		start := len(input.Body.Markdown)
		input.Body.Markdown += "@Agent "
		ref, err := json.Marshal(discussionReference{Type: "agent", AgentID: &id, Start: start, End: start + 6})
		if err != nil {
			t.Fatal(err)
		}
		input.Body.References = append(input.Body.References, ref)
	}
	return input
}

func (f channelInvocationFixture) send(t *testing.T, input DiscussionSendInput) DiscussionMessage {
	t.Helper()
	receipt, err := f.discussion.Send(f.ctx, f.a, f.channelID, input)
	if err != nil {
		t.Fatal(err)
	}
	return receipt.Message
}

func (f channelInvocationFixture) invocation(t *testing.T, messageID uuid.UUID) generated.ChannelInvocation {
	t.Helper()
	var id uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT id FROM channel_invocations WHERE trigger_message_id=$1`, messageID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	row, err := f.store.Queries().GetChannelInvocation(f.ctx, generated.GetChannelInvocationParams{CompanyID: f.a.CompanyID, InvocationID: id})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func (f channelInvocationFixture) run(t *testing.T, id uuid.UUID) generated.Run {
	t.Helper()
	var runID uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT id FROM runs WHERE invocation_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, id).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	row, err := f.store.Queries().GetRun(f.ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// Exercise the transaction participant used by terminal Run processing; provider
// execution itself is represented by a durable terminal row and transcript.
func (f channelInvocationFixture) finish(
	t *testing.T,
	run generated.Run,
	status, answer string,
	final bool,
) QueuePromotion {
	t.Helper()
	var promotion QueuePromotion
	err := f.store.WithinRawTx(f.ctx, pgx.TxOptions{}, func(tx generated.DBTX, q *generated.Queries) error {
		if _, err := q.LockConversationForSystemRunQueueMutation(
			f.ctx,
			generated.LockConversationForSystemRunQueueMutationParams{
				CompanyID:      run.CompanyID,
				ConversationID: run.ConversationID,
			},
		); err != nil {
			return err
		}
		if answer != "" {
			parts, err := json.Marshal([]map[string]any{{"type": "text", "text": answer, "final": final}})
			if err != nil {
				return err
			}
			_, err = tx.Exec(
				f.ctx,
				`INSERT INTO conversation_items(company_id,conversation_id,run_id,seq,type,author_kind,author_id,content,parts,item_key)
    SELECT $1,$2,$3,COALESCE(max(seq),0)+1,'agent_text','agent',$4,$5,$6,'answer' FROM conversation_items WHERE conversation_id=$2`,
				run.CompanyID,
				run.ConversationID,
				run.ID,
				run.AgentID,
				answer,
				parts,
			)
			if err != nil {
				return err
			}
		}
		// A proven final answer is recorded on the Run by the transcript append.
		if _, err := tx.Exec(f.ctx, `UPDATE runs SET status=$2,updated_at=now(),
    final_answer_item_key=CASE WHEN $3::boolean THEN 'answer' ELSE final_answer_item_key END WHERE id=$1`,
			run.ID, status, answer != "" && final); err != nil {
			return err
		}
		updated, err := q.GetRun(f.ctx, run.ID)
		if err != nil {
			return err
		}
		var pending []events.Event
		promotion, err = FinalizeRunConversationInTx(
			f.ctx,
			q,
			&pending,
			testTaskMessageRuns(),
			updated,
			"not a proven final answer",
		)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return promotion
}

func TestChannelInvocationAdmissionAndReplay(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	input := taggedSend(t, f.seed.AgentA, f.seed.AgentA)
	message := f.send(t, input)
	replay, err := f.discussion.Send(f.ctx, f.a, f.channelID, input)
	if err != nil || !replay.Replayed || replay.Message.ID != message.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	invocation := f.invocation(t, message.ID)
	if invocation.Status != "active" || invocation.ThreadRootID != message.ID {
		t.Fatalf("invocation = %+v", invocation)
	}
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM channel_invocations WHERE trigger_message_id=$1`, message.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("invocations = %d, want 1", count)
	}
	run := f.run(t, invocation.ID)
	if run.RunType != "channel_agent" || run.RuntimeID != f.seed.RuntimeA || run.TaskID.Valid {
		t.Fatalf("run lane = %+v", run)
	}
	input.Body.Markdown += "changed"
	if _, err := f.discussion.Send(f.ctx, f.a, f.channelID, input); !errors.Is(err, ErrDiscussionRequestConflict) {
		t.Fatalf("changed replay = %v", err)
	}
}

func TestChannelInvocationAdmissionRejectsForeignAuthorityAtomically(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		principal func(channelInvocationFixture) auth.UserPrincipal
		mutate    func(*testing.T, channelInvocationFixture)
	}{
		{name: "channel outsider", principal: func(f channelInvocationFixture) auth.UserPrincipal { return f.c }},
		{name: "foreign company", principal: func(f channelInvocationFixture) auth.UserPrincipal {
			return auth.UserPrincipal{UserID: f.b.UserID, CompanyID: f.seed.CompanyB, Role: auth.CompanyRoleOwner}
		}},
		{name: "tagger has not set the Agent up", mutate: func(t *testing.T, f channelInvocationFixture) {
			dbtest.MustExec(t, f.ctx, f.pool, `DELETE FROM agent_runtime_bindings WHERE agent_id=$1 AND user_id=$2`, f.seed.AgentA, f.a.UserID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newChannelInvocationFixture(t)
			if tc.mutate != nil {
				tc.mutate(t, f)
			}
			p := f.a
			if tc.principal != nil {
				p = tc.principal(f)
			}
			input := taggedSend(t, f.seed.AgentA)
			if _, err := f.discussion.Send(f.ctx, p, f.channelID, input); err == nil {
				t.Fatal("unauthorized send succeeded")
			}
			var count int
			if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM conversation_items WHERE request_key=$1`, input.RequestKey).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("rejected send persisted %d messages", count)
			}
		})
	}
}

func TestChannelInvocationPlainTextAndEditsDoNotInvoke(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	message := f.send(t, textSend("hello @Agent"))
	input := taggedSend(t, f.seed.AgentA)
	_, err := f.discussion.EditMessage(
		f.ctx,
		f.a,
		DiscussionMessageTarget{ConversationID: f.channelID, MessageID: message.ID},
		DiscussionEditInput{ExpectedRevision: 1, Body: input.Body},
	)
	if !errors.Is(err, ErrDiscussionInvalid) {
		t.Fatalf("adding Agent on edit = %v", err)
	}
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM channel_invocations WHERE channel_id=$1`, f.channelID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invocations = %d, want 0", count)
	}
}

func TestChannelInvocationFIFOAndCanonicalThreadPublication(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	root := f.send(t, textSend("Root"))
	input := taggedSend(t, f.seed.AgentA)
	input.ThreadRootID = &root.ID
	first := f.send(t, input)
	second := f.send(t, taggedSend(t, f.seed.AgentA))
	one, two := f.invocation(t, first.ID), f.invocation(t, second.ID)
	if one.ThreadRootID != root.ID || two.Status != "queued" || one.ConversationID != two.ConversationID {
		t.Fatalf("queue = %+v / %+v", one, two)
	}
	page, err := f.service.List(f.ctx, f.a, f.channelID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.ID == two.ID && item.RunStatus != nil {
			t.Fatalf("queued Run status = %v", *item.RunStatus)
		}
	}
	run := f.run(t, one.ID)
	promotion := f.finish(t, run, "completed", "Answer @Agent", true)
	if promotion.RunID == nil {
		t.Fatal("FIFO successor was not promoted")
	}
	// Replayed terminal processing must never copy the answer twice or enqueue twice.
	f.finish(t, run, "completed", "", false)
	var count int
	var thread, author uuid.UUID
	var runID *uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM conversation_items WHERE metadata->>'invocationId'=$1`, one.ID.String()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("published replies = %d, want 1", count)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT thread_root_id,author_id,run_id FROM conversation_items WHERE metadata->>'invocationId'=$1`, one.ID.String()).Scan(&thread, &author, &runID); err != nil {
		t.Fatal(err)
	}
	if thread != root.ID || author != f.seed.AgentA || runID != nil {
		t.Fatalf("public reply thread=%s author=%s run=%v", thread, author, runID)
	}
	if got := f.invocation(t, second.ID); got.Status != "active" {
		t.Fatalf("successor status = %q", got.Status)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM channel_invocations WHERE channel_id=$1`, f.channelID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("Agent reply triggered another invocation: %d", count)
	}
}

func TestChannelInvocationPublicationRequiresFinalProof(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, status, answer, publication string
		final                             bool
	}{
		{name: "commentary", status: "completed", answer: "Working on it", publication: "no_output"},
		{name: "empty", status: "completed", publication: "no_output"},
		{name: "failed", status: "failed", answer: "Uncommitted answer", final: true, publication: "no_output"},
		{name: "oversized", status: "completed", answer: strings.Repeat("x", maxDiscussionItemBytes), final: true, publication: "blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newChannelInvocationFixture(t)
			message := f.send(t, taggedSend(t, f.seed.AgentA))
			v := f.invocation(t, message.ID)
			f.finish(t, f.run(t, v.ID), tc.status, tc.answer, tc.final)
			got := f.invocation(t, message.ID)
			if got.PublicationStatus != tc.publication {
				t.Fatalf("publication = %q, want %q", got.PublicationStatus, tc.publication)
			}
			var count int
			if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM conversation_items WHERE metadata->>'invocationId'=$1`, v.ID.String()).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("unproven reply published %d times", count)
			}
		})
	}
}

func TestChannelInvocationWaitingForInputEmitsParentHint(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	message := f.send(t, taggedSend(t, f.seed.AgentA))
	invocation := f.invocation(t, message.ID)
	run := f.run(t, invocation.ID)
	_, err := f.store.Queries().CreateQuestion(f.ctx, generated.CreateQuestionParams{
		CompanyID: f.a.CompanyID, Title: "Choose a direction", AskedByAgentID: f.seed.AgentA,
		OriginRunID: run.ID, RequestKey: uuid.New(), ConversationID: run.ConversationID,
		OriginUserID:  uuid.NullUUID{UUID: f.a.UserID, Valid: true},
		Prompts:       []byte(`[{"id":"choice","text":"Which direction?","options":[]}]`),
		RequestSha256: make([]byte, 32),
		ExpiresAt:     pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE runs SET status='completed' WHERE id=$1`, run.ID)
	run, err = f.store.Queries().GetRun(f.ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var pending []events.Event
	err = f.store.WithinTx(f.ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		_, err := FinalizeRunConversationInTx(f.ctx, q, &pending, testTaskMessageRuns(), run, "")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.invocation(t, message.ID).Status; got != "waiting_input" {
		t.Fatalf("status = %s", got)
	}
	found := false
	for _, event := range pending {
		if event.Name != EventChannelAgentsChanged {
			continue
		}
		hint, err := events.PayloadOf[ChannelAgentsChanged](event)
		if err != nil || hint.ConversationID != f.channelID || hint.ActivityOnly ||
			event.ConversationID != f.channelID {
			t.Fatalf("waiting-input hint = %+v, %v", event, err)
		}
		found = true
	}
	if !found {
		t.Fatal("waiting-input transition committed without a channel hint")
	}
}

func TestChannelInvocationRetryIsCallerOnlyAndIdempotent(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	var changed []events.Event
	if err := f.bus.Subscribe("conversation.channel_agents_changed", func(_ context.Context, e events.Event) error {
		changed = append(changed, e)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	message := f.send(t, taggedSend(t, f.seed.AgentA))
	v := f.invocation(t, message.ID)
	f.finish(t, f.run(t, v.ID), "failed", "", false)
	retry := ChannelInvocationRetry{ChannelID: f.channelID, InvocationID: v.ID, RequestKey: uuid.New()}
	if _, err := f.service.Retry(f.ctx, f.b, retry); !errors.Is(err, ErrDiscussionForbidden) {
		t.Fatalf("foreign retry = %v", err)
	}
	before := len(changed)
	for i := range 2 {
		prior := len(changed)
		if _, err := f.service.Retry(f.ctx, f.a, retry); err != nil {
			t.Fatal(err)
		}
		if i == 1 && len(changed) != prior {
			t.Fatal("retry replay emitted another channel hint")
		}
	}
	if len(changed) <= before {
		t.Fatal("retry did not invalidate channel Agent status")
	}
	for _, event := range changed {
		if event.CompanyID != f.a.CompanyID || event.ConversationID != f.channelID {
			t.Fatalf("channel hint addressed execution conversation: %+v", event)
		}
	}
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM runs WHERE invocation_id=$1`, v.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("runs after retry replay = %d, want 2", count)
	}
	retry.RequestKey = uuid.New()
	if _, err := f.service.Retry(f.ctx, f.a, retry); !errors.Is(err, ErrDiscussionRevisionConflict) {
		t.Fatalf("retry active invocation = %v", err)
	}
}

// A tagged Run works for the person who tagged the Agent, with their access
// (D31), so the channel Agent activity view shows each person only the Runs
// they started (Access TECH B11). UserA created AgentA, and UserA and UserB
// each tag it in one channel: each reads only their own Run's items, so the
// Agent's creator no longer reads UserB's. A member who never tagged it opens
// the view too and reads an empty list; someone outside the chat is not
// found.
func TestChannelAgentActivityShowsEachPersonTheirOwnRuns(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	// UserB set AgentA up on a computer of their own (ADR 0014), so their
	// tag runs there, for them.
	callerRuntimeID := dbtest.NewUUID(t)
	dbtest.MustExec(t, f.ctx, f.pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, 'daemon-channel-activity', 'Caller laptop', 'codex', 'online', $3, now())`,
		callerRuntimeID, f.seed.CompanyA, f.b.UserID)
	dbtest.SeedAgentBinding(t, f.ctx, f.pool, f.seed.CompanyA, f.seed.AgentA, f.b.UserID, callerRuntimeID, "codex")
	// The Company owner is in the chat and never tags the Agent.
	channel, err := f.conversations.CreateChannel(f.ctx, f.a, CreateChannelInput{
		RequestKey:    uuid.New(),
		Name:          "Agent activity",
		MemberUserIDs: []uuid.UUID{f.b.UserID, f.owner.UserID},
		AgentIDs:      []uuid.UUID{f.seed.AgentA},
	})
	if err != nil {
		t.Fatal(err)
	}
	channelID := channel.Conversation.ID
	tag := func(caller auth.UserPrincipal) generated.ChannelInvocation {
		t.Helper()
		receipt, err := f.discussion.Send(f.ctx, caller, channelID, taggedSend(t, f.seed.AgentA))
		if err != nil {
			t.Fatal(err)
		}
		return f.invocation(t, receipt.Message.ID)
	}
	creatorTag, memberTag := tag(f.a), tag(f.b)
	creatorRun := f.run(t, creatorTag.ID)
	if promotion := f.finish(t, creatorRun, "completed", "Creator final", true); promotion.RunID == nil {
		t.Fatal("UserB's tag did not start after UserA's")
	}
	memberRun := f.run(t, memberTag.ID)
	if memberRun.ExecutionAuthorizedByUserID.UUID != f.b.UserID {
		t.Fatalf("UserB's Run authorizer = %v", memberRun.ExecutionAuthorizedByUserID)
	}
	f.finish(t, memberRun, "completed", "Member final", true)

	target := ChannelAgentTarget{ChannelID: channelID, AgentID: f.seed.AgentA}
	for _, viewer := range []struct {
		name      string
		principal auth.UserPrincipal
		runID     uuid.UUID
	}{
		{name: "the Agent's creator", principal: f.a, runID: creatorRun.ID},
		{name: "another tagger", principal: f.b, runID: memberRun.ID},
	} {
		activity, err := f.service.Activity(f.ctx, viewer.principal, target)
		if err != nil || len(activity.Items) == 0 {
			t.Fatalf("%s's activity = %+v, %v", viewer.name, activity, err)
		}
		for _, item := range activity.Items {
			if item.RunID != viewer.runID {
				t.Fatalf("%s reads item %+v of Run %s, want only their own Run %s",
					viewer.name, item, item.RunID, viewer.runID)
			}
		}
		// Both reads serve UTC times, like every other projection,
		// whatever the server's local zone.
		if activity.Items[0].CreatedAt.Location() != time.UTC {
			t.Fatalf("%s's activity time = %v, want UTC", viewer.name, activity.Items[0].CreatedAt)
		}
	}
	none, err := f.service.Activity(f.ctx, f.owner, target)
	if err != nil || len(none.Items) != 0 {
		t.Fatalf("activity of a member who never tagged the Agent = %+v, %v, want empty", none, err)
	}
	roster, err := f.service.List(f.ctx, f.owner, channelID)
	if err != nil || len(roster.Agents) != 1 || !roster.Agents[0].CanViewDetail {
		t.Fatalf("roster of a member who never tagged the Agent = %+v, %v", roster, err)
	}
	if len(roster.Items) == 0 || roster.Items[0].CreatedAt.Location() != time.UTC {
		t.Fatalf("invocation times = %+v, want UTC", roster.Items)
	}
	callers := map[uuid.UUID]uuid.UUID{}
	for _, item := range roster.Items {
		callers[item.ID] = item.CallerUserID
	}
	if callers[creatorTag.ID] != f.a.UserID || callers[memberTag.ID] != f.b.UserID {
		t.Fatalf("invocation callers = %v, want each tag's own tagger", callers)
	}
	if _, err := f.service.Activity(f.ctx, f.c, target); !errors.Is(err, ErrDiscussionNotFound) {
		t.Fatalf("outsider activity = %v", err)
	}
}

// Tagging an Agent the channel does not have is refused with its own error,
// so a client can offer to add the Agent. Nothing is written.
func TestChannelInvocationTagOfAnAgentOutsideTheChannelIsRefused(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	outsider := dbtest.NewUUID(t)
	dbtest.SeedBoundAgent(t, f.ctx, f.pool, outsider, f.a.CompanyID, "Outsider", "codex",
		&f.seed.AgentA, f.a.UserID, f.seed.RuntimeA)
	input := taggedSend(t, outsider)
	if _, err := f.discussion.Send(f.ctx, f.a, f.channelID, input); !errors.Is(err, ErrDiscussionAgentRequired) {
		t.Fatalf("tag of an Agent outside the channel = %v, want ErrDiscussionAgentRequired", err)
	}
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM conversation_items WHERE request_key=$1`, input.RequestKey).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("refused send persisted %d messages", count)
	}
}

func TestChannelInvocationRevocationDoesNotResurrectOnRegrant(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	message := f.send(t, taggedSend(t, f.seed.AgentA))
	v := f.invocation(t, message.ID)
	queued := f.send(t, taggedSend(t, f.seed.AgentA))
	var revision int64
	if err := f.pool.QueryRow(f.ctx, `SELECT configuration_revision FROM conversation_channels WHERE conversation_id=$1`, f.channelID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	removed, err := f.conversations.SetChannelAgent(
		f.ctx,
		f.a,
		f.channelID,
		ChannelAgentInput{ExpectedRevision: revision, AgentID: f.seed.AgentA, Active: false},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.conversations.SetChannelAgent(
		f.ctx,
		f.a,
		f.channelID,
		ChannelAgentInput{
			ExpectedRevision: removed.ConfigurationRevision,
			AgentID:          f.seed.AgentA,
			Active:           true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{message.ID, queued.ID} {
		if got := f.invocation(t, id); got.Status != "cancelled" {
			t.Fatalf("revoked status = %q", got.Status)
		}
	}
	if got := f.run(t, v.ID); got.Status != "cancelled" {
		t.Fatalf("revoked Run = %q", got.Status)
	}
	fresh := f.send(t, taggedSend(t, f.seed.AgentA))
	if got := f.invocation(t, fresh.ID); got.Status != "active" || got.AgentGeneration == v.AgentGeneration {
		t.Fatalf("fresh grant = %+v", got)
	}
}

func TestChannelInvocationRunsOnTheCallersOwnBindingWithoutCreatorConstraint(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	// UserB did not create AgentA; they set it up on a computer of their own
	// (ADR 0014), and that is the machine their tag runs on.
	callerRuntimeID := dbtest.NewUUID(t)
	dbtest.MustExec(t, f.ctx, f.pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, 'daemon-channel-caller', 'Caller laptop', 'codex', 'online', $3, now())`,
		callerRuntimeID, f.seed.CompanyA, f.b.UserID)
	dbtest.SeedAgentBinding(t, f.ctx, f.pool, f.seed.CompanyA, f.seed.AgentA, f.b.UserID, callerRuntimeID, "codex")
	receipt, err := f.discussion.Send(f.ctx, f.b, f.channelID, taggedSend(t, f.seed.AgentA))
	if err != nil {
		t.Fatalf("member invoking an Agent they set up: %v", err)
	}
	invocation := f.invocation(t, receipt.Message.ID)
	if invocation.CallerUserID != f.b.UserID {
		t.Fatalf("caller = %s, want %s", invocation.CallerUserID, f.b.UserID)
	}
	run := f.run(t, invocation.ID)
	if run.ExecutionAuthorizedByUserID.UUID != f.b.UserID {
		t.Fatalf("Run authorizer = %v", run.ExecutionAuthorizedByUserID)
	}
	if run.RuntimeID != callerRuntimeID {
		t.Fatalf("Run runtime = %s, want the caller's own %s", run.RuntimeID, callerRuntimeID)
	}
	roster, err := f.service.List(f.ctx, f.b, f.channelID)
	// Every member may open the Agent's activity, which lists their own Runs
	// (Access TECH B11).
	if err != nil || len(roster.Agents) != 1 || !roster.Agents[0].CanInvoke || !roster.Agents[0].CanViewDetail {
		t.Fatalf("member capabilities = %+v, %v", roster, err)
	}
	f.finish(t, run, "failed", "", false)
	retry := ChannelInvocationRetry{ChannelID: f.channelID, InvocationID: invocation.ID, RequestKey: uuid.New()}
	if _, err := f.service.Retry(f.ctx, f.a, retry); !errors.Is(err, ErrDiscussionForbidden) {
		t.Fatalf("creator retrying another caller's request = %v", err)
	}
	if _, err := f.service.Retry(f.ctx, f.b, retry); err != nil {
		t.Fatalf("caller retry = %v", err)
	}
}

// A tag runs on its tagger's computer, and that computer is theirs alone
// (ADR 0014): the tagger reads its status on their tag, every other member
// of the channel reads none, so nobody can watch a colleague's machine come
// and go through the invocation list.
func TestChannelInvocationRuntimeStatusIsTheRegistrantsAlone(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	callerRuntimeID := dbtest.NewUUID(t)
	dbtest.MustExec(t, f.ctx, f.pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, 'daemon-channel-status', 'Caller laptop', 'codex', 'offline', $3, now())`,
		callerRuntimeID, f.seed.CompanyA, f.b.UserID)
	dbtest.SeedAgentBinding(t, f.ctx, f.pool, f.seed.CompanyA, f.seed.AgentA, f.b.UserID, callerRuntimeID, "codex")
	receipt, err := f.discussion.Send(f.ctx, f.b, f.channelID, taggedSend(t, f.seed.AgentA))
	if err != nil {
		t.Fatal(err)
	}
	invocationID := f.invocation(t, receipt.Message.ID).ID
	statusFor := func(viewer auth.UserPrincipal) *string {
		t.Helper()
		page, err := f.service.List(f.ctx, viewer, f.channelID)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if item.ID == invocationID {
				return item.RuntimeStatus
			}
		}
		t.Fatalf("invocation %s missing from %+v", invocationID, page.Items)
		return nil
	}
	if got := statusFor(f.b); got == nil || *got != "offline" {
		t.Fatalf("tagger's runtime status = %v, want offline", got)
	}
	if got := statusFor(f.a); got != nil {
		t.Fatalf("another member reads the tagger's runtime status %q", *got)
	}
}

// A bulk cancel of a member's queued work (an unbind or a runtime removal,
// ADR 0014) must not leave that member's tag holding its channel-Agent
// Conversation's one active slot: the tag closes, and the next member's tag
// of the Agent starts on their own computer. This is the step the Agent domain runs after the cancel.
func TestBindingCancelClosesTheTagAndStartsTheNextOne(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	callerRuntimeID := dbtest.NewUUID(t)
	dbtest.MustExec(t, f.ctx, f.pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, 'daemon-next-caller', 'Next caller laptop', 'codex', 'online', $3, now())`,
		callerRuntimeID, f.seed.CompanyA, f.b.UserID)
	dbtest.SeedAgentBinding(t, f.ctx, f.pool, f.seed.CompanyA, f.seed.AgentA, f.b.UserID, callerRuntimeID, "codex")

	first := f.invocation(t, f.send(t, taggedSend(t, f.seed.AgentA)).ID)
	if first.Status != "active" {
		t.Fatalf("first tag = %s, want active", first.Status)
	}
	receipt, err := f.discussion.Send(f.ctx, f.b, f.channelID, taggedSend(t, f.seed.AgentA))
	if err != nil {
		t.Fatal(err)
	}
	next := f.invocation(t, receipt.Message.ID)
	if next.Status != "queued" {
		t.Fatalf("next tag = %s, want queued behind the first", next.Status)
	}

	var promoted []uuid.UUID
	err = f.store.WithinTx(f.ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var pending []events.Event
		cancelled, err := execution.CancelPendingRunsForBindingsInTx(f.ctx, q, &pending, execution.BindingWork{
			CompanyID:       f.seed.CompanyA,
			SourceRuntimeID: f.seed.RuntimeA,
			UserID:          f.a.UserID,
			AgentIDs:        []uuid.UUID{f.seed.AgentA},
		}, execution.RunReasonAgentUnbound)
		if err != nil {
			return err
		}
		promoted, err = CloseCancelledChannelTagsInTx(f.ctx, q, &pending, cancelled)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if closed := f.invocation(
		t,
		first.TriggerMessageID,
	); closed.Status != "cancelled" ||
		closed.PublicationStatus != "blocked" {
		t.Fatalf("cancelled tag = %s/%s, want cancelled/blocked", closed.Status, closed.PublicationStatus)
	}
	started := f.invocation(t, receipt.Message.ID)
	if started.Status != "active" {
		t.Fatalf("next tag = %s, want active once the slot is free", started.Status)
	}
	run := f.run(t, started.ID)
	if run.Status != "queued" || run.RuntimeID != callerRuntimeID || run.ExecutionAuthorizedByUserID.UUID != f.b.UserID {
		t.Fatalf("next tag Run = %s on %s for %v, want queued on the caller's own %s",
			run.Status, run.RuntimeID, run.ExecutionAuthorizedByUserID, callerRuntimeID)
	}
	if len(promoted) != 1 || promoted[0] != run.ID {
		t.Fatalf("promoted = %v, want the next tag's Run %s to wake", promoted, run.ID)
	}
}
