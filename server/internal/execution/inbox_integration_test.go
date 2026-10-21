//go:build integration

package execution

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/runfailure"
)

type notificationEventLog struct {
	hints []inbox.ToastHint
}

func recordNotificationEvents(t *testing.T, bus *events.Bus) *notificationEventLog {
	t.Helper()

	log := &notificationEventLog{}
	if err := bus.Subscribe(inbox.TopicNotification, func(_ context.Context, event events.Event) error {
		hint, err := events.PayloadOf[inbox.ToastHint](event)
		if err != nil {
			return err
		}
		log.hints = append(log.hints, hint)
		return nil
	}); err != nil {
		t.Fatalf("subscribe notification events: %v", err)
	}
	return log
}

func TestRunTerminalTransitionsCreateAndPresentInboxItems(t *testing.T) {
	tests := []struct {
		name       string
		itemType   inbox.Type
		title      string
		transition func(context.Context, *DefaultRunService, uuid.UUID) error
	}{
		{
			name:     "completed run",
			itemType: inbox.TypeRunCompleted,
			title:    "Reply ready: Direct",
			transition: func(ctx context.Context, service *DefaultRunService, runID uuid.UUID) error {
				_, err := service.CompleteRun(ctx, runID, emptyTerminalProof(), "", false, "")
				return err
			},
		},
		{
			name:     "failed run",
			itemType: inbox.TypeRunFailed,
			title:    "Reply failed: Direct",
			transition: func(ctx context.Context, service *DefaultRunService, runID uuid.UUID) error {
				_, err := service.FailRun(
					ctx,
					runID,
					emptyTerminalProof(),
					"provider failed",
					"",
					string(runfailure.ReasonAgentUnknown),
					false,
					"",
				)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newRunHarness(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			notifications := recordNotificationEvents(t, h.bus)
			h.svc.InboxPresenter = inbox.NewRealtimePresenter(h.bus)
			runID := seedRunningTranscriptRun(t, h)

			if err := test.transition(ctx, h.svc, runID); err != nil {
				t.Fatalf("terminal transition error = %v", err)
			}

			var (
				itemID      string
				itemType    string
				entityType  string
				entityID    string
				title       string
				body        string
				deepLink    string
				recipientID string
			)
			if err := h.pool.QueryRow(
				ctx, `
				SELECT id::text, kind, entity_type, entity_id, title, body, deep_link,
				       recipient_user_id::text
				FROM inbox_items
				WHERE company_id = $1 AND entity_type = 'run' AND entity_id = $2`,
				h.fixture.CompanyA,
				runID.String(),
			).Scan(
				&itemID,
				&itemType,
				&entityType,
				&entityID,
				&title,
				&body,
				&deepLink,
				&recipientID,
			); err != nil {
				t.Fatalf("read terminal Run Inbox item: %v", err)
			}
			if itemType != string(test.itemType) || entityType != "run" ||
				entityID != runID.String() || title != test.title ||
				recipientID != h.fixture.UserA.String() || !strings.HasPrefix(body, "Agent A ") {
				t.Fatalf(
					"terminal Run Inbox item = %s/%s/%s/%q recipient=%s",
					itemType,
					entityType,
					entityID,
					title,
					recipientID,
				)
			}

			parsed, err := url.Parse(deepLink)
			if err != nil {
				t.Fatalf("parse terminal Run deep link: %v", err)
			}
			query := parsed.Query()
			if parsed.Path != "/app" || query.Get("runId") != runID.String() ||
				query.Get("conversationKind") != "direct" ||
				query.Get("agentId") != h.fixture.AgentA.String() {
				t.Fatalf("terminal Run deep link = %q", deepLink)
			}

			if len(notifications.hints) != 1 {
				t.Fatalf("notification hints = %+v, want one", notifications.hints)
			}
			hint := notifications.hints[0]
			if hint.NotificationID.String() != itemID || hint.Type != test.itemType ||
				hint.Title != test.title || hint.DeepLink == nil || *hint.DeepLink != deepLink ||
				hint.CompanyID != h.fixture.CompanyA ||
				hint.RecipientUserID != h.fixture.UserA {
				t.Fatalf("notification hint = %+v", hint)
			}

			if err := test.transition(ctx, h.svc, runID); err != nil {
				t.Fatalf("terminal transition replay error = %v", err)
			}
			var itemCount int
			if err := h.pool.QueryRow(
				ctx, `
				SELECT count(*) FROM inbox_items
				WHERE company_id = $1 AND entity_type = 'run' AND entity_id = $2`,
				h.fixture.CompanyA,
				runID.String(),
			).Scan(&itemCount); err != nil {
				t.Fatalf("count terminal Run Inbox items: %v", err)
			}
			if itemCount != 1 || len(notifications.hints) != 1 {
				t.Fatalf(
					"terminal replay produced %d Inbox items and %d hints, want one each",
					itemCount,
					len(notifications.hints),
				)
			}
		})
	}
}

func TestRunTerminalInboxNamesTheTaskAndSchedule(t *testing.T) {
	tests := []struct {
		name      string
		wantTitle string
		seed      func(*testing.T, *runHarness) uuid.UUID
	}{
		{
			// The task is named by the label every surface displays, the
			// company prefix and the task number — not by its key, which
			// here is the plan slug cap_task_101.
			name:      "task title differs from conversation title",
			wantTitle: "Ready to review: SCA-101 · Brand direction",
			seed: func(t *testing.T, h *runHarness) uuid.UUID {
				t.Helper()
				taskID, conversationID := h.seedTask(t, 101)
				dbtest.MustExec(t, context.Background(), h.pool,
					`UPDATE tasks SET title = 'Brand direction' WHERE id = $1`, taskID)
				return h.seedTaskRun(t, taskID, conversationID, "running", 0)
			},
		},
		{
			// Older rows can hold a key shaped like another task's label.
			// Quoting it would send the reader to the wrong task on the board.
			name:      "legacy label-shaped key names the task by its number",
			wantTitle: "Ready to review: SCA-4 · Design-partner program",
			seed: func(t *testing.T, h *runHarness) uuid.UUID {
				t.Helper()
				taskID, conversationID := h.seedTask(t, 4)
				dbtest.MustExec(
					t,
					context.Background(),
					h.pool,
					`UPDATE tasks SET key = 'SCA-12', title = 'Design-partner program' WHERE id = $1`,
					taskID,
				)
				return h.seedTaskRun(t, taskID, conversationID, "running", 0)
			},
		},
		{
			name:      "schedule name",
			wantTitle: "Schedule finished: Daily brief",
			seed: func(t *testing.T, h *runHarness) uuid.UUID {
				t.Helper()
				schedule := insertCancellationTestSchedule(
					t, context.Background(), h.queries, h.fixture, "Daily brief",
				)
				runID := h.seedQueuedRun(t, schedule.ConversationID)
				dbtestUpdateRunStatus(t, h, runID, "running")
				return runID
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newRunHarness(t)
			ctx := context.Background()
			runID := test.seed(t, h)
			dbtest.MustExec(t, ctx, h.pool,
				`UPDATE agents SET instructions = 'private instructions: secret-123' WHERE id = $1`,
				h.fixture.AgentA)
			if _, err := h.svc.CompleteRun(ctx, runID, emptyTerminalProof(), "", false, ""); err != nil {
				t.Fatalf("CompleteRun() error = %v", err)
			}
			var title, body string
			if err := h.pool.QueryRow(
				ctx, `
				SELECT title, body FROM inbox_items
				WHERE entity_type = 'run' AND entity_id = $1`, runID.String(),
			).Scan(&title, &body); err != nil {
				t.Fatalf("read contextual notification: %v", err)
			}
			if title != test.wantTitle || !strings.HasPrefix(body, "Agent A ") ||
				strings.Contains(body, "secret-123") {
				t.Fatalf(
					"notification = %q/%q, want %q with agent context only",
					title,
					body,
					test.wantTitle,
				)
			}
		})
	}
}

// terminalRunNotices lists the terminal items one Run raised as kind:recipient.
func terminalRunNotices(t *testing.T, h *runHarness, runID uuid.UUID) []string {
	t.Helper()
	rows, err := h.pool.Query(context.Background(), `
		SELECT kind || ':' || recipient_user_id::text FROM inbox_items
		WHERE entity_type = 'run' AND entity_id = $1
		ORDER BY kind, recipient_user_id`, runID.String())
	if err != nil {
		t.Fatalf("read terminal Run Inbox items: %v", err)
	}
	notices, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("collect terminal Run Inbox items: %v", err)
	}
	return notices
}

// seedMemberRun inserts a running AgentA Run that member B started on their
// own machine: the enqueue kernel's shape, authorizer and runtime owner alike.
func seedMemberRun(
	t *testing.T,
	h *runHarness,
	memberB, runtimeB, conversationID uuid.UUID,
	taskID uuid.NullUUID,
	runType string,
	invocationID uuid.NullUUID,
) uuid.UUID {
	t.Helper()
	var runID uuid.UUID
	if err := h.pool.QueryRow(
		context.Background(), `
		INSERT INTO runs
			(company_id, agent_id, task_id, conversation_id, runtime_id, status, run_type,
			 invocation_id, execution_authorized_by_user_id)
		VALUES ($1, $2, $3, $4, $5, 'running', $6, $7, $8)
		RETURNING id`,
		h.fixture.CompanyA, h.fixture.AgentA, taskID, conversationID, runtimeB,
		runType, invocationID, memberB,
	).Scan(&runID); err != nil {
		t.Fatalf("seed member Run: %v", err)
	}
	return runID
}

// seedMemberTask is a Task the fixture owner created and handed to member B,
// with AgentA doing the work on member B's computer.
func seedMemberTask(
	t *testing.T,
	h *runHarness,
	memberB uuid.UUID,
	number int64,
) (taskID, conversationID uuid.UUID) {
	t.Helper()
	taskID, conversationID = h.seedTask(t, number)
	dbtest.MustExec(t, context.Background(), h.pool,
		`UPDATE tasks SET assignee_user_id = $2 WHERE id = $1`, taskID, memberB)
	return taskID, conversationID
}

// A terminal notice is for the member whose computer ran the Run: its
// authorizer (ADR 0014). A Task Run is always its assignee's, so neither the
// colleague whose queued words it carried nor the member who created the Task
// hears about it. A direct chat is its creator's. A channel tag answers in its
// thread and raises no notice.
func TestRunTerminalInboxGoesToTheMemberWhoseComputerRanIt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		fail bool
		seed func(*testing.T, *runHarness, uuid.UUID, uuid.UUID) uuid.UUID
		want func(memberB uuid.UUID) []string
	}{
		{
			// The colleague's queued message is materialized ahead of the
			// assignee's trigger, so it is the Run's first user message.
			name: "task run carrying a colleague's queued message",
			seed: func(t *testing.T, h *runHarness, memberB, runtimeB uuid.UUID) uuid.UUID {
				t.Helper()
				taskID, conversationID := seedMemberTask(t, h, memberB, 201)
				runID := seedMemberRun(t, h, memberB, runtimeB, conversationID,
					uuid.NullUUID{UUID: taskID, Valid: true}, "task", uuid.NullUUID{})
				seedChatItem(t, context.Background(), h.pool, h.fixture.CompanyA, conversationID, runID,
					1, "user_message", "user", h.fixture.UserA, "Please include the word TEAL.")
				seedChatItem(t, context.Background(), h.pool, h.fixture.CompanyA, conversationID, runID,
					2, "user_message", "user", memberB, "Write the tagline.")
				return runID
			},
			want: func(memberB uuid.UUID) []string { return []string{"run_completed:" + memberB.String()} },
		},
		{
			name: "failed task run carrying a colleague's queued message",
			fail: true,
			seed: func(t *testing.T, h *runHarness, memberB, runtimeB uuid.UUID) uuid.UUID {
				t.Helper()
				taskID, conversationID := seedMemberTask(t, h, memberB, 202)
				runID := seedMemberRun(t, h, memberB, runtimeB, conversationID,
					uuid.NullUUID{UUID: taskID, Valid: true}, "task", uuid.NullUUID{})
				seedChatItem(t, context.Background(), h.pool, h.fixture.CompanyA, conversationID, runID,
					1, "user_message", "user", h.fixture.UserA, "Please include the word TEAL.")
				return runID
			},
			want: func(memberB uuid.UUID) []string { return []string{"run_failed:" + memberB.String()} },
		},
		{
			// An assignment or handoff start and a human-input continuation
			// write no trigger message; the Task's creator did not run it.
			name: "task run started without a message",
			seed: func(t *testing.T, h *runHarness, memberB, runtimeB uuid.UUID) uuid.UUID {
				t.Helper()
				taskID, conversationID := seedMemberTask(t, h, memberB, 203)
				return seedMemberRun(t, h, memberB, runtimeB, conversationID,
					uuid.NullUUID{UUID: taskID, Valid: true}, "task", uuid.NullUUID{})
			},
			want: func(memberB uuid.UUID) []string { return []string{"run_completed:" + memberB.String()} },
		},
		{
			// Revocation cancels a member's Runs, so this is a backstop: the
			// notice is the assignee's alone and goes nowhere rather than to
			// the Task's creator.
			name: "task run whose assignee left the Company",
			seed: func(t *testing.T, h *runHarness, _, _ uuid.UUID) uuid.UUID {
				t.Helper()
				memberC := seedMemberB(t, h)
				runtimeC := seedRuntime(t, h, memberC, "codex", "daemon-member-c")
				bind(t, h, h.fixture.AgentA, memberC, runtimeC, "codex")
				taskID, conversationID := seedMemberTask(t, h, memberC, 204)
				runID := seedMemberRun(t, h, memberC, runtimeC, conversationID,
					uuid.NullUUID{UUID: taskID, Valid: true}, "task", uuid.NullUUID{})
				dbtest.MustExec(t, context.Background(), h.pool, `
					UPDATE company_members SET status = 'revoked', revoked_at = now()
					WHERE company_id = $1 AND user_id = $2`, h.fixture.CompanyA, memberC)
				return runID
			},
			want: func(uuid.UUID) []string { return nil },
		},
		{
			// A Run written before authorizers were recorded names nobody
			// whose computer ran it.
			name: "task run with no recorded authorizer",
			seed: func(t *testing.T, h *runHarness, memberB, runtimeB uuid.UUID) uuid.UUID {
				t.Helper()
				taskID, conversationID := seedMemberTask(t, h, memberB, 205)
				runID := seedMemberRun(t, h, memberB, runtimeB, conversationID,
					uuid.NullUUID{UUID: taskID, Valid: true}, "task", uuid.NullUUID{})
				dbtest.MustExec(t, context.Background(), h.pool,
					`UPDATE runs SET execution_authorized_by_user_id = NULL WHERE id = $1`, runID)
				return runID
			},
			want: func(uuid.UUID) []string { return nil },
		},
		{
			name: "direct chat",
			seed: func(t *testing.T, h *runHarness, memberB, runtimeB uuid.UUID) uuid.UUID {
				t.Helper()
				conversationID := dbtest.NewUUID(t)
				dbtest.MustExec(t, context.Background(), h.pool, `
					INSERT INTO conversations (id, company_id, kind, agent_id, created_by_user_id, title)
					VALUES ($1, $2, 'direct', $3, $4, 'B asks')`,
					conversationID, h.fixture.CompanyA, h.fixture.AgentA, memberB)
				runID := seedMemberRun(t, h, memberB, runtimeB, conversationID,
					uuid.NullUUID{}, "direct_chat", uuid.NullUUID{})
				seedChatItem(t, context.Background(), h.pool, h.fixture.CompanyA, conversationID, runID,
					1, "user_message", "user", memberB, "Hello")
				return runID
			},
			want: func(memberB uuid.UUID) []string { return []string{"run_completed:" + memberB.String()} },
		},
		{
			name: "schedule",
			seed: func(t *testing.T, h *runHarness, memberB, runtimeB uuid.UUID) uuid.UUID {
				t.Helper()
				ctx := context.Background()
				dbtest.MustExec(
					t,
					ctx,
					h.pool,
					`UPDATE company_members SET role = 'admin' WHERE company_id = $1 AND user_id = $2`,
					h.fixture.CompanyA,
					memberB,
				)
				schedule, err := h.queries.InsertSchedule(ctx, generated.InsertScheduleParams{
					ActorRoles:       auth.RolesWith(auth.PermissionSchedulesManage),
					CompanyID:        h.fixture.CompanyA,
					CreatorUserID:    memberB,
					RequestKey:       dbtest.NewUUID(t),
					AgentID:          h.fixture.AgentA,
					ReviewedProvider: "codex",
					Name:             "Daily brief",
					Instructions:     "Run exactly once.",
					TriggerType:      "once",
					RunAt: pgtype.Timestamptz{
						Time:  time.Now().UTC().Add(time.Hour),
						Valid: true,
					},
					Status: "draft",
				})
				if err != nil {
					t.Fatalf("InsertSchedule() error = %v", err)
				}
				return seedMemberRun(t, h, memberB, runtimeB, schedule.ConversationID,
					uuid.NullUUID{}, "schedule", uuid.NullUUID{})
			},
			want: func(memberB uuid.UUID) []string { return []string{"run_completed:" + memberB.String()} },
		},
		{
			name: "channel tag",
			seed: func(t *testing.T, h *runHarness, memberB, runtimeB uuid.UUID) uuid.UUID {
				t.Helper()
				ctx := context.Background()
				channelID := dbtest.SeedDiscussionChannel(
					t, ctx, h.pool, h.fixture.CompanyA, memberB, []uuid.UUID{h.fixture.AgentA},
				)
				var messageID, conversationID, invocationID uuid.UUID
				if err := h.pool.QueryRow(ctx, `
					INSERT INTO conversation_items
						(company_id, conversation_id, seq, type, author_kind, author_id,
						 content, body, request_key, send_fingerprint)
					VALUES ($1, $2, 1, 'user_message', 'user', $3, '@Agent please',
						'{"markdown":"@Agent please"}'::jsonb, gen_random_uuid(),
						decode(repeat('11', 32), 'hex'))
					RETURNING id`, h.fixture.CompanyA, channelID, memberB).Scan(&messageID); err != nil {
					t.Fatalf("seed channel message: %v", err)
				}
				if err := h.pool.QueryRow(
					ctx, `
					INSERT INTO conversations (company_id, kind, channel_id, agent_id, created_by_user_id, title)
					VALUES ($1, 'channel_agent', $2, $3, $4, 'Agent A')
					RETURNING id`, h.fixture.CompanyA, channelID, h.fixture.AgentA, h.fixture.UserA,
				).Scan(&conversationID); err != nil {
					t.Fatalf("seed channel-Agent Conversation: %v", err)
				}
				if err := h.pool.QueryRow(
					ctx, `
					INSERT INTO channel_invocations
						(company_id, channel_id, conversation_id, agent_id, trigger_message_id,
						 thread_root_id, caller_user_id, agent_generation, member_generation,
						 channel_generation, trigger_content, trigger_revision, status)
					VALUES ($1, $2, $3, $4, $5, $5, $6, 1, 1, 1, '@Agent please', 1, 'active')
					RETURNING id`, h.fixture.CompanyA, channelID, conversationID, h.fixture.AgentA,
					messageID, memberB,
				).Scan(&invocationID); err != nil {
					t.Fatalf("seed channel invocation: %v", err)
				}
				return seedMemberRun(
					t,
					h,
					memberB,
					runtimeB,
					conversationID,
					uuid.NullUUID{},
					"channel_agent",
					uuid.NullUUID{UUID: invocationID, Valid: true},
				)
			},
			want: func(uuid.UUID) []string { return nil },
		},
	}

	h := newRunHarness(t)
	memberB := seedMemberB(t, h)
	runtimeB := seedRuntime(t, h, memberB, "codex", "daemon-member-b")
	bind(t, h, h.fixture.AgentA, memberB, runtimeB, "codex")
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			runID := test.seed(t, h, memberB, runtimeB)

			var err error
			if test.fail {
				_, err = h.svc.FailRun(ctx, runID, emptyTerminalProof(), "provider failed", "",
					string(runfailure.ReasonAgentUnknown), false, "")
			} else {
				_, err = h.svc.CompleteRun(ctx, runID, emptyTerminalProof(), "", false, "")
			}
			if err != nil {
				t.Fatalf("terminal transition error = %v", err)
			}
			if got, want := terminalRunNotices(t, h, runID), test.want(memberB); !slices.Equal(got, want) {
				t.Fatalf(
					"terminal Run notices = %v, want %v (fixture owner %s)",
					got,
					want,
					h.fixture.UserA,
				)
			}
		})
	}
}

// announceRequest writes a Question's or Approval's own Inbox items the way
// their producers do: a Task Question to every active member, an Approval only
// to the members who may decide it. A decided request's items are closed, as
// the decision closes them; they still count as delivered.
func announceRequest(t *testing.T, h *runHarness, requestType string, requestID uuid.UUID, decided bool) {
	t.Helper()
	ctx := context.Background()
	itemType, entityType := inbox.TypeQuestionRequest, protocol.EntityQuestion
	recipients, err := h.queries.ListActiveMemberUserIDs(ctx, h.fixture.CompanyA)
	if requestType == "approval" {
		itemType, entityType = inbox.TypeApprovalRequest, protocol.EntityApproval
		recipients, err = h.queries.ListActiveCompanyMemberIDsByRoles(
			ctx,
			generated.ListActiveCompanyMemberIDsByRolesParams{
				CompanyID: h.fixture.CompanyA,
				Roles:     auth.RolesWith(auth.PermissionApprovalsResolve),
			},
		)
	}
	if err != nil {
		t.Fatalf("list %s recipients: %v", requestType, err)
	}
	var pending []events.Event
	entity := inbox.Entity{Type: entityType, ID: requestID.String()}
	for _, recipientID := range recipients {
		if _, err := inbox.CreateInTx(ctx, h.queries, &pending, inbox.CreateInput{
			CompanyID:       h.fixture.CompanyA,
			RecipientUserID: recipientID,
			Type:            itemType,
			Entity:          entity,
			Title:           "Needs a person",
			Body:            "An agent needs a person before it can continue.",
			DedupeKey:       requestType + ":" + requestID.String() + ":requested",
			Actor:           inbox.Actor{Kind: "agent", ID: &h.fixture.AgentA},
		}); err != nil {
			t.Fatalf("create %s Inbox item: %v", requestType, err)
		}
	}
	if !decided {
		return
	}
	if err := inbox.CloseEntityInTx(ctx, h.queries, &pending, h.fixture.CompanyA, entity,
		inbox.Actor{Kind: "user", ID: &h.fixture.UserA}); err != nil {
		t.Fatalf("close %s Inbox items: %v", requestType, err)
	}
}

// A Run that ended to wait for a person has not finished its work, so it is
// never "ready to review" — the rule that keeps the Task in progress. When the
// assignee holds the request's own item, that item asks them and the
// continuation Run's notice reports the result, so this Run raises none. When
// they do not — an Approval goes only to the members who may decide it — the
// Run's notice is the only word they get, and it says the Task is paused.
func TestCompletedRunWaitingOnHumanInputNotifiesOnlyWhoTheRequestMissed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		memberTask   bool // the Task is a plain member's, not the owner's
		requestType  string
		status       string
		continuation string
		fail         bool
		want         string // the item kind the assignee gets, or none
		wantTitle    string
	}{
		{name: "pending question", requestType: "question", status: "pending"},
		{name: "pending approval", requestType: "approval", status: "pending"},
		{
			name: "answered question awaiting its continuation", requestType: "question",
			status: "answered", continuation: "pending",
		},
		{
			name: "approved action awaiting its continuation", requestType: "approval",
			status: "approved", continuation: "pending",
		},
		{
			name: "inline approval already applied", requestType: "approval",
			status: "approved", continuation: "not_required",
			want: "run_completed", wantTitle: "Ready to review: ",
		},
		{
			name: "failure with a pending question", requestType: "question", status: "pending", fail: true,
			want: "run_failed", wantTitle: "Run failed: ",
		},
		{
			// Every member holds a Task Question's item.
			name: "member's pending question", memberTask: true, requestType: "question", status: "pending",
		},
		{
			name: "member's pending approval", memberTask: true, requestType: "approval", status: "pending",
			want: "run_completed", wantTitle: "Paused for approval: ",
		},
		{
			name: "member's approved action awaiting its continuation", memberTask: true,
			requestType: "approval", status: "approved", continuation: "pending",
			want: "run_completed", wantTitle: "Paused for approval: ",
		},
	}

	h := newRunHarness(t)
	memberB := seedMemberB(t, h)
	runtimeB := seedRuntime(t, h, memberB, "codex", "daemon-member-b")
	bind(t, h, h.fixture.AgentA, memberB, runtimeB, "codex")
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			assignee := h.fixture.UserA
			var taskID, conversationID, runID uuid.UUID
			if test.memberTask {
				assignee = memberB
				taskID, conversationID = seedMemberTask(t, h, memberB, int64(301+index))
				runID = seedMemberRun(t, h, memberB, runtimeB, conversationID,
					uuid.NullUUID{UUID: taskID, Valid: true}, "task", uuid.NullUUID{})
			} else {
				taskID, conversationID = h.seedTask(t, int64(301+index))
				runID = h.seedTaskRun(t, taskID, conversationID, "running", 0)
			}
			var requestID uuid.UUID
			switch test.requestType {
			case "question":
				if err := h.pool.QueryRow(ctx, `
					INSERT INTO questions (company_id, title, asked_by_agent_id,
					    origin_run_id, request_key, task_id, conversation_id, prompts,
					    request_sha256, expires_at, status, continuation_status)
					VALUES ($1,'Choose a color',$2,$3,gen_random_uuid(),$4,$5,
					        '[]',decode(repeat('00',32),'hex'),now()+interval '1 hour',$6,NULLIF($7,''))
					RETURNING id`,
					h.fixture.CompanyA, h.fixture.AgentA, runID, taskID, conversationID,
					test.status, test.continuation).Scan(&requestID); err != nil {
					t.Fatalf("seed Question: %v", err)
				}
			case "approval":
				if err := h.pool.QueryRow(ctx, `
					INSERT INTO approvals (company_id, rule_key, title, preview_markdown,
					    requested_by_agent_id, origin_run_id, request_key, task_id,
					    conversation_id, canonical_action, request_sha256, action_sha256,
					    expires_at, status, continuation_status)
					VALUES ($1,'plan','Approve this step','Review',$2,$3,gen_random_uuid(),
					        $4,$5,'{}',decode(repeat('00',32),'hex'),decode(repeat('00',32),'hex'),
					        now()+interval '1 hour',$6,NULLIF($7,''))
					RETURNING id`,
					h.fixture.CompanyA, h.fixture.AgentA, runID, taskID, conversationID,
					test.status, test.continuation).Scan(&requestID); err != nil {
					t.Fatalf("seed Approval: %v", err)
				}
			}
			announceRequest(t, h, test.requestType, requestID, test.status != "pending")

			var err error
			if test.fail {
				_, err = h.svc.FailRun(ctx, runID, emptyTerminalProof(), "provider failed", "",
					string(runfailure.ReasonAgentUnknown), false, "")
			} else {
				_, err = h.svc.CompleteRun(ctx, runID, emptyTerminalProof(), "", false, "")
			}
			if err != nil {
				t.Fatalf("terminal transition error = %v", err)
			}
			var want []string
			if test.want != "" {
				want = []string{test.want + ":" + assignee.String()}
			}
			if got := terminalRunNotices(t, h, runID); !slices.Equal(got, want) {
				t.Fatalf("terminal Run notices = %v, want %v (owner %s, member %s)",
					got, want, h.fixture.UserA, memberB)
			}
			if test.want == "" {
				return
			}
			var title string
			if err := h.pool.QueryRow(ctx, `
				SELECT title FROM inbox_items WHERE entity_type = 'run' AND entity_id = $1`,
				runID.String()).Scan(&title); err != nil {
				t.Fatalf("read terminal Run notice title: %v", err)
			}
			if !strings.HasPrefix(title, test.wantTitle) {
				t.Fatalf("terminal Run notice title = %q, want prefix %q", title, test.wantTitle)
			}
		})
	}
}

// A terminal notice names its Task, so a recipient who can no longer read the
// Task's Project (ADR 0017) gets none. The Run still finishes: skipping the
// notice never fails the transition.
func TestRunTerminalInboxSkipsARecipientOutsideTheProject(t *testing.T) {
	h := newRunHarness(t)
	ctx := context.Background()
	taskID, conversationID := h.seedTask(t, 102)
	runID := h.seedTaskRun(t, taskID, conversationID, "running", 0)
	dbtest.MustExec(t, ctx, h.pool, `
		DELETE FROM project_grants
		WHERE user_id = $1 AND project_id = (SELECT project_id FROM tasks WHERE id = $2)`,
		h.fixture.UserA, taskID)
	if _, err := h.svc.CompleteRun(ctx, runID, emptyTerminalProof(), "", false, ""); err != nil {
		t.Fatalf("CompleteRun() error = %v", err)
	}
	var status string
	if err := h.pool.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1`, runID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("Run status = %q, want completed", status)
	}
	if notices := terminalRunNotices(t, h, runID); len(notices) != 0 {
		t.Fatalf("a recipient outside the Project got terminal notices %v", notices)
	}
}
