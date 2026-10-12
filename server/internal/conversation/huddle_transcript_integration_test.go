//go:build integration

package conversation

import (
	"errors"
	"strings"
	"testing"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"boardly.dev/boardly/server/internal/usage"
	"github.com/google/uuid"
)

func transcriptFixture(t *testing.T) (discussionFixture, *DefaultHuddleAIService, Huddle, uuid.UUID, HuddleAILease) {
	t.Helper()
	f := newDiscussionFixture(t)
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE agents SET name='Eli',settings=jsonb_set(settings,'{voice}','{"enabled":true,"voice":"marin","participation":"addressed"}') WHERE id=$1`, f.seed.AgentA)
	budget, err := usage.NewHuddleBudgetService(f.store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = budget.Configure(f.ctx, f.a, usage.ConfigureHuddleBudgetInput{MonthlyLimitUSDTicks: 100000000000}); err != nil {
		t.Fatal(err)
	}
	huddles, err := NewHuddleService(HuddleServiceOptions{Store: f.store, Bus: f.bus, Media: &lifecycleHuddleMedia{}})
	if err != nil {
		t.Fatal(err)
	}
	ai, err := NewHuddleAIService(HuddleAIServiceOptions{Store: f.store, Bus: f.bus, Enabled: true, TicksPerSecond: 100, ReasoningReserveTicks: 1000})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := f.conversations.CreateHumanDirect(f.ctx, f.a, CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}})
	if err != nil {
		t.Fatal(err)
	}
	started, err := huddles.Start(f.ctx, f.a, direct.Conversation.ID, StartHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := huddles.Join(f.ctx, f.a, started.Huddle.ID, JoinHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = huddles.ConfirmConnected(f.ctx, f.a.CompanyID, joined.Connection.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ai.SetEnabled(f.ctx, f.a, started.Huddle.ID, SetHuddleAIInput{Enabled: true, AgentID: f.seed.AgentA, ExpectedRevision: 1, RequestKey: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	attempt, lease := claimTranscriptAttempt(t, f, ai, started.Huddle.ID)
	return f, ai, started.Huddle, attempt, lease
}

func claimTranscriptAttempt(t *testing.T, f discussionFixture, ai *DefaultHuddleAIService, huddleID uuid.UUID) (uuid.UUID, HuddleAILease) {
	t.Helper()
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE huddle_ai_states SET retry_at=now()-interval '1 second' WHERE huddle_id=$1`, huddleID)
	if err := ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	status, err := ai.Get(f.ctx, f.a, huddleID)
	if err != nil || status.AttemptID == nil {
		t.Fatalf("attempt = %+v, %v", status, err)
	}
	lease, err := ai.Claim(f.ctx, *status.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	return *status.AttemptID, lease
}

func TestHuddleTranscriptRecoveryAndHistory(t *testing.T) {
	f, ai, huddle, attempt, lease := transcriptFixture(t)
	batch := HuddleTranscriptBatch{Sequence: 1, Entries: []HuddleTranscriptEntry{{Speaker: "participants", Text: "before recovery"}}}
	if _, err := ai.AppendTranscript(f.ctx, attempt, lease.Capability, batch); err != nil {
		t.Fatal(err)
	}
	if err := ai.Finish(f.ctx, attempt, lease.Capability, HuddleAIUsage{ModelSessionID: "first", ModelSeconds: 1, Final: true, Closed: true, StopReason: "provider_error"}); err != nil {
		t.Fatal(err)
	}
	second, nextLease := claimTranscriptAttempt(t, f, ai, huddle.ID)
	if second == attempt {
		t.Fatal("recovery reused attempt")
	}
	batch.Entries[0].Text = "after recovery"
	if _, err := ai.AppendTranscript(f.ctx, second, nextLease.Capability, batch); err != nil {
		t.Fatal(err)
	}
	page, err := f.discussion.ListMessages(f.ctx, f.a, huddle.ChatConversationID, DiscussionPageOptions{})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("premature card: %+v, %v", page, err)
	}
	if _, err = ai.Leave(f.ctx, second, nextLease.Capability); err != nil {
		t.Fatal(err)
	}
	if err = ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	page, err = f.discussion.ListMessages(f.ctx, f.a, huddle.ChatConversationID, DiscussionPageOptions{})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("saved card: %+v, %v", page, err)
	}
	item := page.Items[0]
	if item.System == nil || item.System.AgentName != "Eli" || item.Body == nil ||
		!strings.Contains(item.Body.Markdown, "before recovery") || !strings.Contains(item.Body.Markdown, "after recovery") ||
		!strings.Contains(item.Body.Markdown, "Connection interrupted") ||
		strings.Contains(item.Body.Markdown, "Automatically transcribed while the Agent was present") {
		t.Fatalf("card = %+v", item)
	}
	if _, err = f.discussion.ListMessages(f.ctx, f.b, huddle.ChatConversationID, DiscussionPageOptions{}); !errors.Is(err, ErrDiscussionNotFound) {
		t.Fatalf("source-conversation member read transcript without joining: %v", err)
	}
	if _, err = f.pool.Exec(f.ctx, `UPDATE conversation_items SET body=NULL,metadata='{"action":"channel_updated","actorUserId":null}' WHERE id=$1`, item.ID); err == nil {
		t.Fatal("transcript immutability bypassed by action change")
	}
	// Replacing the invitation creates a separate card and fences the old attempt.
	if err = ai.Finish(f.ctx, second, nextLease.Capability, HuddleAIUsage{ModelSessionID: "second", ModelSeconds: 1, Final: true, Closed: true, StopReason: "agent_left"}); err != nil {
		t.Fatal(err)
	}
	status, err := ai.Get(f.ctx, f.a, huddle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ai.SetEnabled(f.ctx, f.a, huddle.ID, SetHuddleAIInput{Enabled: true, AgentID: f.seed.AgentA, ExpectedRevision: status.Revision, RequestKey: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	third, thirdLease := claimTranscriptAttempt(t, f, ai, huddle.ID)
	if _, err = ai.AppendTranscript(f.ctx, third, thirdLease.Capability, HuddleTranscriptBatch{Sequence: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = ai.AppendTranscript(f.ctx, second, nextLease.Capability, HuddleTranscriptBatch{Sequence: 2}); !errors.Is(err, ErrHuddleUnavailable) {
		t.Fatalf("old attempt append = %v", err)
	}
	if _, err = ai.Leave(f.ctx, third, thirdLease.Capability); err != nil {
		t.Fatal(err)
	}
	if err = ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	page, err = f.discussion.ListMessages(f.ctx, f.a, huddle.ChatConversationID, DiscussionPageOptions{})
	if err != nil || len(page.Items) != 2 || !strings.Contains(page.Items[1].Body.Markdown, "No speech was captured") {
		t.Fatalf("new invitation = %+v, %v", page, err)
	}
}

func TestHuddleTranscriptTerminalBudgetAndCap(t *testing.T) {
	f, ai, huddle, attempt, lease := transcriptFixture(t)
	truncated := false
	for seq := int64(1); seq <= 20; seq++ {
		receipt, err := ai.AppendTranscript(f.ctx, attempt, lease.Capability, HuddleTranscriptBatch{Sequence: seq, Entries: []HuddleTranscriptEntry{{Speaker: "participants", Text: strings.Repeat("é", 1500)}}})
		if err != nil {
			t.Fatal(err)
		}
		truncated = truncated || receipt.Truncated
	}
	if !truncated {
		t.Fatal("storage was not bounded")
	}
	budget, err := usage.NewHuddleBudgetService(f.store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = budget.Configure(f.ctx, f.a, usage.ConfigureHuddleBudgetInput{ExpectedRevision: 1, MonthlyLimitUSDTicks: 0}); err != nil {
		t.Fatal(err)
	}
	if err = ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err = ai.Finish(f.ctx, attempt, lease.Capability, HuddleAIUsage{ModelSessionID: "terminal", ModelSeconds: 1, Final: true, Closed: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = budget.Configure(f.ctx, f.a, usage.ConfigureHuddleBudgetInput{ExpectedRevision: 2, MonthlyLimitUSDTicks: 100000000000}); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE huddle_ai_states SET retry_at=now()-interval '1 second' WHERE huddle_id=$1`, huddle.ID)
	if err = ai.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	status, err := ai.Get(f.ctx, f.a, huddle.ID)
	if err != nil || status.AttemptID != nil {
		t.Fatalf("sealed invitation restarted = %+v, %v", status, err)
	}
	page, err := f.discussion.ListMessages(f.ctx, f.a, huddle.ChatConversationID, DiscussionPageOptions{})
	if err != nil || len(page.Items) != 1 || !strings.Contains(page.Items[0].Body.Markdown, "Partial transcript") {
		t.Fatalf("partial card = %+v, %v", page, err)
	}
}

func TestHuddleTranscriptTerminalPaths(t *testing.T) {
	for _, terminal := range []string{"remove", "ended and settled", "voice revoked", "recovery exhausted"} {
		t.Run(terminal, func(t *testing.T) {
			f, ai, huddle, attempt, lease := transcriptFixture(t)
			if _, err := ai.AppendTranscript(f.ctx, attempt, lease.Capability, HuddleTranscriptBatch{Sequence: 1, Entries: []HuddleTranscriptEntry{{Speaker: "agent", Text: "saved before departure"}}}); err != nil {
				t.Fatal(err)
			}
			switch terminal {
			case "remove":
				if _, err := ai.SetEnabled(f.ctx, f.a, huddle.ID, SetHuddleAIInput{ExpectedRevision: 2, RequestKey: uuid.New()}); err != nil {
					t.Fatal(err)
				}
			case "ended and settled":
				huddles, err := NewHuddleService(HuddleServiceOptions{Store: f.store, Bus: f.bus})
				if err != nil {
					t.Fatal(err)
				}
				if err = huddles.EndFromMedia(f.ctx, f.a.CompanyID, HuddleRoomName(huddle.ID)); err != nil {
					t.Fatal(err)
				}
				if err = ai.Finish(f.ctx, attempt, lease.Capability, HuddleAIUsage{ModelSessionID: "ended", ModelSeconds: 1, Final: true, Closed: true}); err != nil {
					t.Fatal(err)
				}
			case "voice revoked":
				dbtest.MustExec(t, f.ctx, f.pool, `UPDATE agents SET settings=jsonb_set(settings,'{voice,enabled}','false') WHERE id=$1`, f.seed.AgentA)
			case "recovery exhausted":
				dbtest.MustExec(t, f.ctx, f.pool, `UPDATE huddle_ai_sessions SET lease_expires_at=now()-interval '1 minute' WHERE id=$1`, attempt)
				dbtest.MustExec(t, f.ctx, f.pool, `UPDATE huddle_ai_states SET failures=4 WHERE huddle_id=$1`, huddle.ID)
			}
			for range 2 {
				if err := ai.Reconcile(f.ctx); err != nil {
					t.Fatal(err)
				}
			}
			page, err := f.discussion.ListMessages(f.ctx, f.a, huddle.ChatConversationID, DiscussionPageOptions{})
			if err != nil || len(page.Items) != 1 || !strings.Contains(page.Items[0].Body.Markdown, "saved before departure") {
				t.Fatalf("terminal card = %+v, %v", page, err)
			}
			if _, err = ai.AppendTranscript(f.ctx, attempt, lease.Capability, HuddleTranscriptBatch{Sequence: 2}); !errors.Is(err, ErrHuddleUnavailable) {
				t.Fatalf("late capture = %v", err)
			}
		})
	}
}
