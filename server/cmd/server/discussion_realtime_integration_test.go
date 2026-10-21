//go:build integration

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/realtime"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type discussionBatchRecording struct {
	users  [][]string
	frames []realtime.BroadcastEvent
}

func (b *discussionBatchRecording) BroadcastToCompany(_ string, frame realtime.BroadcastEvent) error {
	b.frames = append(b.frames, frame)
	return nil
}

func (b *discussionBatchRecording) BroadcastToCompanyUsers(
	_ context.Context,
	_ string,
	users []string,
	frame realtime.BroadcastEvent,
) ([]realtime.RelayBatchResult, error) {
	b.users = append(b.users, users)
	b.frames = append(b.frames, frame)
	return nil, nil
}

func TestDiscussionDispatcherResolvesCurrentParticipantsAtDelivery(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'owner')`,
		f.CompanyA,
		f.UserB,
	)
	id := dbtest.SeedDiscussionChannel(t, ctx, pool, f.CompanyA, f.UserA, nil)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO conversation_members(company_id,conversation_id,user_id,source_type) VALUES($1,$2,$3,'channel')`,
		f.CompanyA,
		id,
		f.UserB,
	)
	recorder := &discussionBatchRecording{}
	dispatcher := newDiscussionDispatcher(generated.New(pool), recorder)
	changed := events.Event{
		CompanyID:      f.CompanyA,
		ConversationID: id,
		EntityType:     conversation.Topic,
		Name:           conversation.EventDiscussionChanged,
		Payload:        conversation.DiscussionChanged{ConversationID: id, Revision: 2, HeadSeq: 1},
	}
	if err := dispatcher.enqueue(ctx, changed); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE conversation_members SET status='revoked',revoked_at=now(),version=version+1 WHERE conversation_id=$1 AND user_id=$2`,
		id,
		f.UserB,
	)
	for _, job := range dispatcher.take() {
		dispatcher.deliver(ctx, job)
	}
	if len(recorder.users) != 1 || len(recorder.users[0]) != 1 || recorder.users[0][0] != f.UserA.String() {
		t.Fatalf("current recipients=%v", recorder.users)
	}
	revoked := events.Event{
		CompanyID:      f.CompanyA,
		ConversationID: id,
		EntityType:     conversation.Topic,
		Name:           conversation.EventDiscussionAccessRevoked,
		Payload: conversation.DiscussionAccessRevoked{
			ConversationID:  id,
			GrantGeneration: 1,
			UserID:          f.UserB,
		},
	}
	if err := dispatcher.enqueue(ctx, revoked); err != nil {
		t.Fatal(err)
	}
	for _, job := range dispatcher.take() {
		dispatcher.deliver(ctx, job)
	}
	if len(recorder.users) != 2 || len(recorder.users[1]) != 1 || recorder.users[1][0] != f.UserB.String() {
		t.Fatalf("former recipient=%v", recorder.users)
	}
	raw, err := json.Marshal(recorder.frames[1])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), f.UserB.String()) || strings.Contains(string(raw), "userId") ||
		len(raw) > 1024 {
		t.Fatalf("revoke frame exposes routing metadata: %s", raw)
	}
	state := events.Event{
		CompanyID:      f.CompanyA,
		ConversationID: id,
		EntityType:     conversation.Topic,
		Name:           conversation.EventDiscussionStateChanged,
		Payload:        conversation.DiscussionStateChanged{ConversationID: id, UserID: f.UserB, Revision: 2},
	}
	if err = dispatcher.enqueue(ctx, state); err != nil {
		t.Fatal(err)
	}
	for _, job := range dispatcher.take() {
		dispatcher.deliver(ctx, job)
	}
	if len(recorder.users) != 2 {
		t.Fatalf("revoked owner's personal state was sent: %v", recorder.users)
	}
}

func TestChannelAgentRunHintsNeverExposeExecutionPayload(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	id := dbtest.SeedDiscussionChannel(t, ctx, pool, f.CompanyA, f.UserA, nil)
	q := generated.New(pool)
	conv, err := q.EnsureChannelAgentConversation(
		ctx,
		generated.EnsureChannelAgentConversationParams{CompanyID: f.CompanyA, ChannelID: id, AgentID: f.AgentA},
	)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &discussionBatchRecording{}
	d := newDiscussionDispatcher(q, recorder)
	for _, name := range []string{execution.EventRunQueued, execution.EventRunDispatched, execution.EventRunRunning, execution.EventRunCompleted, execution.EventRunFailed, execution.EventRunCancelled, execution.EventRunAuthWaitChanged, execution.EventRunWaitingLocalDirectory, execution.EventRunStudioCanvasAdded, execution.EventRunMessageCreated, "question.asked", "question.resolved", "question.expired", "approval.requested", "approval.resolved", "approval.expired"} {
		t.Run(name, func(t *testing.T) {
			route := execution.ConversationRoute{
				ConversationKind:      "channel_agent",
				ConversationCreatedBy: &f.UserB,
			}
			var payload any = execution.RunSnapshot{ID: uuid.New(), ConversationID: conv.ID, ConversationRoute: route}
			if name == execution.EventRunMessageCreated {
				payload = execution.TranscriptItem{ConversationRoute: route}
			}
			topic := execution.Topic
			if strings.HasPrefix(name, "question.") {
				topic = "question"
				payload = struct{}{}
			}
			if strings.HasPrefix(name, "approval.") {
				topic = "approval"
				payload = struct{}{}
			}
			if err := d.enqueue(
				ctx,
				events.Event{
					EntityType:     topic,
					Name:           name,
					CompanyID:      f.CompanyA,
					ConversationID: conv.ID,
					CreatedAt:      time.Now(),
					Payload:        payload,
				},
			); err != nil {
				t.Fatal(err)
			}
			jobs := d.take()
			if len(jobs) != 1 {
				t.Fatalf("missing channel hint for %s: %+v", name, jobs)
			}
			before := len(recorder.frames)
			d.deliver(ctx, jobs[0])
			if len(recorder.frames) != before+1 || len(recorder.users[before]) != 1 ||
				recorder.users[before][0] != f.UserA.String() {
				t.Fatalf("channel hint audience = %v", recorder.users)
			}
			raw, err := json.Marshal(recorder.frames[before])
			if err != nil || strings.Contains(string(raw), conv.ID.String()) ||
				strings.Contains(string(raw), f.UserB.String()) ||
				!strings.Contains(string(raw), id.String()) ||
				strings.Contains(string(raw), "runId") ||
				strings.Contains(string(raw), "parts") {
				t.Fatalf("channel hint leaked execution: %s, %v", raw, err)
			}
		})
	}
}

func TestGeneralChannelDispatcherReachesEveryMember(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `WITH people AS (
 INSERT INTO users(email,display_name) SELECT 'general-'||n||'@example.test','Member '||n FROM generate_series(1,60) n RETURNING id
 ) INSERT INTO company_members(company_id,user_id,role) SELECT $1,id,'member' FROM people`, f.CompanyA)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	err = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := q.LockCompanyForMembershipMutation(ctx, f.CompanyA); err != nil {
			return err
		}
		pending := []events.Event{}
		return conversation.JoinGeneralChannelInTx(
			ctx,
			q,
			&pending,
			conversation.GeneralChannelMembership{CompanyID: f.CompanyA, UserID: f.UserA},
		)
	})
	if err != nil {
		t.Fatal(err)
	}
	row, err := store.Queries().GetGeneralChannelForUpdate(ctx, f.CompanyA)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &discussionBatchRecording{}
	dispatcher := newDiscussionDispatcher(store.Queries(), recorder)
	if err := dispatcher.enqueue(ctx, events.Event{
		CompanyID: f.CompanyA, ConversationID: row.ID, EntityType: conversation.Topic,
		Name:    conversation.EventDiscussionChanged,
		Payload: conversation.DiscussionChanged{ConversationID: row.ID, Revision: 1},
	}); err != nil {
		t.Fatal(err)
	}
	for _, job := range dispatcher.take() {
		dispatcher.deliver(ctx, job)
	}
	seen := map[string]bool{}
	for _, batch := range recorder.users {
		if len(batch) > 50 {
			t.Fatalf("oversized batch: %d", len(batch))
		}
		for _, id := range batch {
			if seen[id] || id == f.UserB.String() {
				t.Fatalf("duplicate or outside Company: %s", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != 61 || len(recorder.users) != 2 {
		t.Fatalf("recipients=%d batches=%d", len(seen), len(recorder.users))
	}
}
