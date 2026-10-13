package execution

import (
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/inbox"
	db "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

func TestRunInboxContentMatchesTheTerminalOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    string
		target    runInboxTarget
		wantType  inbox.Type
		wantTitle string
		wantBody  string
	}{
		{
			name:   "completed task run",
			status: "completed",
			target: runInboxTarget{
				agentName:        "Marketing Agent",
				conversationKind: "task",
				workTitle:        "MKT-1 · Brand direction",
			},
			wantType:  inbox.TypeRunCompleted,
			wantTitle: "Ready to review: MKT-1 · Brand direction",
			wantBody:  "Marketing Agent finished its task run. Open the task to review the result.",
		},
		{
			name:   "failed task run",
			status: "failed",
			target: runInboxTarget{
				agentName:        "Marketing Agent",
				conversationKind: "task",
				workTitle:        "MKT-1 · Brand direction",
				provider:         "codex",
				failureReason:    "agent_error.provider_quota_limit",
				failureText: "You've hit your usage limit. Visit https://chatgpt.com/codex/settings/usage " +
					"to purchase more credits or try again at Sep 22nd, 2026 11:27 AM.",
			},
			wantType:  inbox.TypeRunFailed,
			wantTitle: "Run failed: MKT-1 · Brand direction",
			wantBody: "Marketing Agent could not finish its task run. " +
				"Codex has hit its usage limit or run out of credit. " +
				"Retrying will not help until the plan or balance is topped up. " +
				"Codex said: You've hit your usage limit. Visit https://chatgpt.com/codex/settings/usage " +
				"to purchase more credits or try again at Sep 22nd, 2026 11:27 AM. " +
				"Open the task to review the failure and retry.",
		},
		{
			name:   "failed task run without provider words",
			status: "failed",
			target: runInboxTarget{
				agentName:        "Marketing Agent",
				conversationKind: "task",
				workTitle:        "MKT-1 · Brand direction",
				provider:         "claude",
				failureReason:    "runtime_offline",
				failureText:      "runtime went offline",
			},
			wantType:  inbox.TypeRunFailed,
			wantTitle: "Run failed: MKT-1 · Brand direction",
			wantBody: "Marketing Agent could not finish its task run. " +
				"The machine running this agent went offline before the run finished. " +
				"Bring it back online and try again. " +
				"Details: Runtime went offline. " +
				"Open the task to review the failure and retry.",
		},
		{
			name:   "failed before the provider started",
			status: "failed",
			target: runInboxTarget{
				agentName:        "Marketing Agent",
				conversationKind: "task",
				workTitle:        "MKT-1 · Brand direction",
				provider:         "codex",
				failureReason:    "agent_error.provider_auth_or_access",
				failureText:      "run.preparation_failed.provider_availability",
			},
			wantType:  inbox.TypeRunFailed,
			wantTitle: "Run failed: MKT-1 · Brand direction",
			wantBody: "Marketing Agent could not finish its task run. " +
				"Codex rejected the credentials on the machine running this agent. " +
				"Sign in to Codex again on that machine, then try the run. " +
				"Details: The provider CLI was not available on the machine running this agent. " +
				"Open the task to review the failure and retry.",
		},
		{
			name:   "completed direct conversation",
			status: "completed",
			target: runInboxTarget{
				agentName:        "CEO Agent",
				conversationKind: "direct",
				workTitle:        "Launch planning",
			},
			wantType:  inbox.TypeRunCompleted,
			wantTitle: "Reply ready: Launch planning",
			wantBody:  "CEO Agent finished its reply. Open the conversation to review it.",
		},
		{
			name:   "untitled direct conversation",
			status: "failed",
			target: runInboxTarget{
				agentName:        "CEO Agent",
				conversationKind: "direct",
			},
			wantType:  inbox.TypeRunFailed,
			wantTitle: "Reply failed: CEO Agent",
			wantBody:  "CEO Agent could not finish its reply. Open the conversation to review the failure and retry.",
		},
		{
			name:   "completed schedule",
			status: "completed",
			target: runInboxTarget{
				agentName:        "CEO Agent",
				conversationKind: "schedule",
				workTitle:        "Daily brief",
			},
			wantType:  inbox.TypeRunCompleted,
			wantTitle: "Schedule finished: Daily brief",
			wantBody:  "CEO Agent finished the scheduled run. Open the conversation to review the result.",
		},
		{
			name:   "failed schedule",
			status: "failed",
			target: runInboxTarget{
				agentName:        "CEO Agent",
				conversationKind: "schedule",
				workTitle:        "Daily brief",
			},
			wantType:  inbox.TypeRunFailed,
			wantTitle: "Schedule failed: Daily brief",
			wantBody:  "CEO Agent could not finish the scheduled run. Open the conversation to review the failure and retry.",
		},
		{
			// The assignee cannot decide the Approval and holds no item for
			// it, so this notice is the only word that the Task is waiting.
			name:   "task run waiting for an approval",
			status: "completed",
			target: runInboxTarget{
				agentName:        "Marketing Agent",
				conversationKind: "task",
				workTitle:        "MKT-1 · Brand direction",
				awaiting:         protocol.EntityApproval,
			},
			wantType:  inbox.TypeRunCompleted,
			wantTitle: "Paused for approval: MKT-1 · Brand direction",
			wantBody: "Marketing Agent paused its task run to ask for an approval and continues " +
				"once the request is decided. Open the task to follow it.",
		},
		{
			name:   "direct reply waiting for an answer",
			status: "completed",
			target: runInboxTarget{
				agentName:        "CEO Agent",
				conversationKind: "direct",
				workTitle:        "Launch planning",
				awaiting:         protocol.EntityQuestion,
			},
			wantType:  inbox.TypeRunCompleted,
			wantTitle: "Paused for an answer: Launch planning",
			wantBody: "CEO Agent paused its reply to ask a question and continues once it is answered. " +
				"Open the conversation to follow it.",
		},
		{
			name:   "scheduled run waiting for an approval",
			status: "completed",
			target: runInboxTarget{
				agentName:        "CEO Agent",
				conversationKind: "schedule",
				workTitle:        "Daily brief",
				awaiting:         protocol.EntityApproval,
			},
			wantType:  inbox.TypeRunCompleted,
			wantTitle: "Paused for approval: Daily brief",
			wantBody: "CEO Agent paused the scheduled run to ask for an approval and continues " +
				"once the request is decided. Open the conversation to follow it.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			gotType, gotTitle, gotBody := runInboxContent(test.status, test.target)
			if gotType != test.wantType || gotTitle != test.wantTitle || gotBody != test.wantBody {
				t.Fatalf(
					"runInboxContent(%q, %+v) = %q/%q/%q, want %q/%q/%q",
					test.status,
					test.target,
					gotType,
					gotTitle,
					gotBody,
					test.wantType,
					test.wantTitle,
					test.wantBody,
				)
			}
		})
	}
}

func TestRunInboxAwaitingNotifiesOnlyWhenNothingElseTellsTheRecipient(t *testing.T) {
	t.Parallel()

	approval := string(protocol.EntityApproval)
	question := string(protocol.EntityQuestion)
	tests := []struct {
		name         string
		waits        []db.ListRunHumanInputWaitsRow
		wantAwaiting protocol.EntityType
		wantNotify   bool
	}{
		{name: "nothing waiting", wantNotify: true},
		{
			name:  "the recipient holds the request's item",
			waits: []db.ListRunHumanInputWaitsRow{{EntityType: question, RecipientNotified: true}},
		},
		{
			name:         "the recipient holds no item for the approval",
			waits:        []db.ListRunHumanInputWaitsRow{{EntityType: approval}},
			wantAwaiting: protocol.EntityApproval,
			wantNotify:   true,
		},
		{
			// The question's item does not tell them about the approval.
			name: "one of two requests reached the recipient",
			waits: []db.ListRunHumanInputWaitsRow{
				{EntityType: approval},
				{EntityType: question, RecipientNotified: true},
			},
			wantAwaiting: protocol.EntityApproval,
			wantNotify:   true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			awaiting, notify := runInboxAwaiting(test.waits)
			if awaiting != test.wantAwaiting || notify != test.wantNotify {
				t.Fatalf("runInboxAwaiting(%+v) = %q/%t, want %q/%t",
					test.waits, awaiting, notify, test.wantAwaiting, test.wantNotify)
			}
		})
	}
}

func TestRunInboxContentFitsNotificationLimits(t *testing.T) {
	t.Parallel()

	_, title, body := runInboxContent("failed", runInboxTarget{
		agentName:        strings.Repeat("🤖", 200),
		conversationKind: "task",
		workTitle:        "  MKT-1\n" + strings.Repeat("界", 200),
		provider:         "claude",
		failureReason:    "agent_error.provider_server_error",
		failureText:      strings.Repeat("overloaded ", 400),
	})
	if utf8.RuneCountInString(title) > 160 || !utf8.ValidString(title) ||
		!strings.HasSuffix(title, "…") || strings.Contains(title, "\n") ||
		len(body) > 2048 || !utf8.ValidString(body) {
		t.Fatalf("notification exceeds display limits: title=%q body=%q", title, body)
	}
	_, _, emojiBody := runInboxContent("failed", runInboxTarget{
		agentName:        strings.Repeat("🤖", 128),
		conversationKind: "task",
		workTitle:        "MKT-1",
		provider:         "codex",
		failureReason:    "agent_error.provider_quota_limit",
		failureText:      strings.Repeat("🤖", 400),
	})
	if len(emojiBody) > 2048 || !utf8.ValidString(emojiBody) {
		t.Fatalf(
			"notification body is not safe: %d bytes, valid=%v",
			len(emojiBody),
			utf8.ValidString(emojiBody),
		)
	}
}

func TestRunInboxDeepLinkCarriesTheRunRoute(t *testing.T) {
	t.Parallel()

	run := db.Run{
		ID:             uuid.MustParse("00000000-0000-4000-8000-000000000042"),
		AgentID:        uuid.Must(uuid.NewV7()),
		ConversationID: uuid.Must(uuid.NewV7()),
		TaskID:         uuid.NullUUID{UUID: uuid.Must(uuid.NewV7()), Valid: true},
	}
	deepLink := runInboxDeepLink("task", run)
	parsed, err := url.Parse(deepLink)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", deepLink, err)
	}
	query := parsed.Query()
	if parsed.Path != "/app" || query.Get("runId") != run.ID.String() ||
		query.Get("agentId") != run.AgentID.String() ||
		query.Get("conversationId") != run.ConversationID.String() ||
		query.Get("conversationKind") != "task" || query.Get("taskId") != run.TaskID.UUID.String() {
		t.Fatalf("runInboxDeepLink() = %q", deepLink)
	}
}

// The stopped-Run notice (ADR 0017) is a run_failed item from the system,
// one per Run, that names the Agent and the work the recipient still reads
// and nothing else: it is built from no Project field at all.
func TestProjectAccessRevokedInboxInputNamesTheAgentAndTheWorkOnly(t *testing.T) {
	t.Parallel()

	run := db.Run{
		ID:             uuid.MustParse("00000000-0000-4000-8000-000000000042"),
		CompanyID:      uuid.Must(uuid.NewV7()),
		AgentID:        uuid.Must(uuid.NewV7()),
		ConversationID: uuid.Must(uuid.NewV7()),
		TaskID:         uuid.NullUUID{UUID: uuid.Must(uuid.NewV7()), Valid: true},
	}
	recipient := uuid.Must(uuid.NewV7())
	got := projectAccessRevokedInboxInput(run, recipient, "Marketing  Agent", "MKT-1 · Brand direction", "task")

	if !inbox.ValidCreateInput(got) {
		t.Fatalf("projectAccessRevokedInboxInput() = %+v; the Inbox would reject it", got)
	}
	if got.Type != inbox.TypeRunFailed || got.Actor.Kind != "system" || got.Actor.ID != nil ||
		got.Entity != (inbox.Entity{Type: protocol.EntityRun, ID: run.ID.String()}) ||
		got.CompanyID != run.CompanyID || got.RecipientUserID != recipient ||
		got.ConversationID == nil || *got.ConversationID != run.ConversationID {
		t.Fatalf("projectAccessRevokedInboxInput() = %+v; want a system run_failed item on the Run", got)
	}
	if want := "run:00000000-0000-4000-8000-000000000042:project_access_revoked"; got.DedupeKey != want {
		t.Fatalf("DedupeKey = %q; want %q", got.DedupeKey, want)
	}
	if want := "Run stopped: MKT-1 · Brand direction"; got.Title != want {
		t.Fatalf("Title = %q; want %q", got.Title, want)
	}
	if want := "Marketing Agent stopped: your access to this work no longer includes running tasks. " +
		"Ask someone who manages this work to let you run tasks."; got.Body != want {
		t.Fatalf("Body = %q; want %q", got.Body, want)
	}
	if got.DeepLink == nil || *got.DeepLink != runInboxDeepLink("task", run) {
		t.Fatalf("DeepLink = %v; want the Run route", got.DeepLink)
	}
	if strings.Contains(strings.ToLower(got.Title+" "+got.Body), "project") {
		t.Fatalf("notice names a Project: title=%q body=%q", got.Title, got.Body)
	}

	// No work title falls back to the Agent; long names are cut to the
	// Inbox display limits.
	fallback := projectAccessRevokedInboxInput(run, recipient, "Marketing Agent", "  ", "chat")
	if fallback.Title != "Run stopped: Marketing Agent" {
		t.Fatalf("Title without a work title = %q", fallback.Title)
	}
	long := projectAccessRevokedInboxInput(run, recipient, strings.Repeat("🤖", 200), strings.Repeat("界", 200), "task")
	if utf8.RuneCountInString(long.Title) > 160 || !strings.HasSuffix(long.Title, "…") ||
		!strings.HasPrefix(long.Body, strings.Repeat("🤖", 127)+"… stopped:") || !inbox.ValidCreateInput(long) {
		t.Fatalf("long notice not cut to the limits: title=%q body=%q", long.Title, long.Body)
	}
}

func TestCompanyResearchInboxContentIsDedicated(t *testing.T) {
	t.Parallel()
	page := uuid.New()
	target := runInboxTarget{agentName: "Andrew", conversationKind: "direct"}
	itemType, title, body, link := companyResearchInboxContent("completed", target, companyResearchOutcome{research: true, pages: 1, overview: &page})
	if itemType != inbox.TypeRunCompleted || title != "Your company wiki is ready" || !strings.Contains(body, "1 page ") || link != "/wiki/pages/"+page.String() {
		t.Fatalf("completed = %s/%q/%q/%q", itemType, title, body, link)
	}
	_, _, body, link = companyResearchInboxContent("completed", target, companyResearchOutcome{research: true})
	if link != "/wiki" || strings.Contains(body, "Reply") {
		t.Fatalf("empty completed = %q/%q", body, link)
	}
	itemType, title, body, link = companyResearchInboxContent("failed", target, companyResearchOutcome{research: true})
	if itemType != inbox.TypeRunFailed || title != "Company research could not finish" || !strings.Contains(body, "retry") || link != "/wiki" {
		t.Fatalf("failed = %s/%q/%q/%q", itemType, title, body, link)
	}
	// A cancelled research Run did not finish either: same copy, never "ready".
	itemType, title, body, link = companyResearchInboxContent("cancelled", target, companyResearchOutcome{research: true})
	if itemType != inbox.TypeRunFailed || title != "Company research could not finish" || !strings.Contains(body, "retry") || link != "/wiki" {
		t.Fatalf("cancelled = %s/%q/%q/%q", itemType, title, body, link)
	}
	// A failure to prepare the founder's computer reads as that, not as an
	// error Zenith Team could not classify.
	prepared := target
	prepared.failureReason, prepared.failureText, prepared.provider = "agent_error.unknown", "run.preparation_failed.workspace", "codex"
	_, _, body, _ = companyResearchInboxContent("failed", prepared, companyResearchOutcome{research: true})
	if strings.Contains(body, "classify") || strings.Contains(body, "Details") || !strings.Contains(body, "workspace") {
		t.Fatalf("preparation failure body = %q", body)
	}
}
