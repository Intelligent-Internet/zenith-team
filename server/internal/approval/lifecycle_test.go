package approval

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/task"
	"boardly.dev/boardly/server/pkg/protocol"
)

// A continuation the enqueue kernel refuses is abandoned with the reason the
// refusal names; the kernel's Project access refusal — the assignee lost
// project.tasks.execute (ADR 0017) — abandons it with project_access_revoked.
// Any other error is not a refusal and fails the attempt.
func TestClassifyContinuationRefusal(t *testing.T) {
	t.Parallel()

	executeRefusal := fmt.Errorf("enqueue: %w", &execution.ExecutePermissionError{
		AssigneeID: uuid.New(), AgentName: "Lyra",
	})
	for _, test := range []struct {
		name   string
		err    error
		status task.Status
		want   continuationAbandonReason
		ok     bool
	}{
		{"lost project.tasks.execute", executeRefusal, task.StatusInProgress, abandonProjectAccessRevoked, true},
		{"Task failed", errContinuationAbandoned, task.StatusFailed, abandonTaskFailed, true},
		{"Task closed", errContinuationAbandoned, task.StatusDone, abandonTaskClosed, true},
		{"dependency blocked", task.ErrDependencyBlocked, task.StatusTodo, abandonTaskDependencyBlocked, true},
		{"Agent not bound", execution.ErrRunAgentNotBound, task.StatusTodo, abandonAgentNotBound, true},
		{"any other error", errors.New("connection reset"), task.StatusTodo, "", false},
	} {
		got, ok := classifyContinuationRefusal(test.err, test.status)
		if got != test.want || ok != test.ok {
			t.Errorf("%s: classifyContinuationRefusal() = %q, %t; want %q, %t", test.name, got, ok, test.want, test.ok)
		}
	}
}

// Both continuation reconcilers have to reach the Run insert the same way. That
// insert can lose the one-pending-per-(task, agent) unique index, which aborts
// the whole transaction, and the "retry later" write that follows only commits
// because the insert ran behind a savepoint. A savepoint needs the transaction
// handle, which WithinTx deliberately does not hand out — so each reconciler
// opens its transaction with WithinRawTx and passes that handle down.
//
// This is asserted on the source because the behavior it guards needs a
// PostgreSQL server: the database tests below skip on a developer machine, and
// a fix applied to one reconciler and not the other would wedge half the
// human-input surface with nothing in the unit lane to say so.
func TestBothContinuationReconcilersInsertBehindASavepoint(t *testing.T) {
	t.Parallel()

	file, err := parser.ParseFile(token.NewFileSet(), "lifecycle.go", nil, 0)
	if err != nil {
		t.Fatalf("parse lifecycle.go: %v", err)
	}
	for _, reconciler := range []string{
		"reconcileApprovalContinuation",
		"reconcileQuestionContinuation",
	} {
		calls := calledNames(t, file, reconciler)
		if !calls["WithinRawTx"] {
			t.Errorf("%s does not open its transaction with WithinRawTx; a savepoint needs the raw handle",
				reconciler)
		}
		if calls["WithinTx"] {
			t.Errorf("%s uses WithinTx, which hands out no handle to take a savepoint on", reconciler)
		}
		if !calls["enqueueContinuation"] {
			t.Errorf("%s no longer routes its Run insert through enqueueContinuation", reconciler)
		}
	}
	if calls := calledNames(t, file, "enqueueContinuation"); !calls["TryWithinSavepoint"] {
		t.Error("enqueueContinuation inserts the continuation Run with no savepoint under it")
	} else {
		if !calls["LockConversationForSystemRunQueueMutation"] {
			t.Error("enqueueContinuation does not take the caller-owned Conversation lock")
		}
		if !calls["GetConversationCurrentRun"] {
			t.Error("enqueueContinuation does not serialize against the Conversation's current Run")
		}
		if !calls["PrepareContinuationRunInTx"] {
			t.Error("enqueueContinuation bypasses the Task continuation admission seam")
		}
	}
}

// A continuation pass must take its locks in the order every Company write
// takes them: Company, then member, then Conversation. Account deletion holds
// its Companies FOR UPDATE and then abandons their receipts, and a Task
// mutation holds its Company FOR UPDATE and then locks the Task's
// Conversation. A pass that claimed a receipt or locked a Conversation first
// and only then waited for the Company — which the Inbox item for an
// abandonment needs — would deadlock with either. So each reconciler locks
// the Company before it claims, and the owner's membership before
// enqueueContinuation locks the Conversation.
//
// Asserted on the source for the reason the savepoint test above gives: the
// interleavings need two PostgreSQL sessions racing, and a regression is
// silent until one of them is aborted in production.
func TestContinuationPassesLockTheCompanyAndOwnerBeforeTheConversation(t *testing.T) {
	t.Parallel()

	file, err := parser.ParseFile(token.NewFileSet(), "lifecycle.go", nil, 0)
	if err != nil {
		t.Fatalf("parse lifecycle.go: %v", err)
	}
	for reconciler, kind := range map[string]string{
		"reconcileApprovalContinuation": "Approval",
		"reconcileQuestionContinuation": "Question",
	} {
		sequence := referenceSequence(t, file, reconciler)
		ordered := []string{
			"lockDueContinuationCompanyInTx",
			"PeekDue" + kind + "ContinuationCompany",
			"ClaimDue" + kind + "Continuation",
			"lockContinuationOwnerInTx",
			"enqueueContinuation",
			"noticeAbandonedContinuationInTx",
		}
		previous := -1
		for _, name := range ordered {
			at := indexOf(sequence, name)
			if at < 0 {
				t.Errorf("%s never reaches %s", reconciler, name)
				continue
			}
			if at < previous {
				t.Errorf("%s reaches %s out of order; want %v", reconciler, name, ordered)
			}
			previous = at
		}
	}
	if sequence := referenceSequence(t, file, "lockDueContinuationCompanyInTx"); indexOf(
		sequence, "LockCompanyForMembershipRead",
	) < 0 {
		t.Error("lockDueContinuationCompanyInTx no longer takes the Company share lock")
	}
	if sequence := referenceSequence(t, file, "lockContinuationOwnerInTx"); indexOf(
		sequence, "LockRecipientInTx",
	) < 0 {
		t.Error("lockContinuationOwnerInTx no longer takes the owner's membership lock")
	}
	// The notice may only use the owner resolved and locked up front; resolving
	// one there would reach a membership after the Conversation.
	for _, late := range []string{"continuationOwnerInTx", "lockContinuationOwnerInTx", "LockRecipientInTx"} {
		if indexOf(referenceSequence(t, file, "noticeAbandonedContinuationInTx"), late) >= 0 {
			t.Errorf("noticeAbandonedContinuationInTx calls %s after the Conversation lock", late)
		}
		if indexOf(referenceSequence(t, file, "enqueueContinuation"), late) >= 0 {
			t.Errorf("enqueueContinuation calls %s after the Conversation lock", late)
		}
	}
}

// Only a cause the owner can fix and retry raises an Inbox item. A handoff,
// an archived Conversation or a closed Task is a member's own decision —
// after a handoff the new assignee never saw the request — and the rest
// leave nobody to tell.
func TestOnlyActionableAbandonReasonsNotifyTheOwner(t *testing.T) {
	t.Parallel()

	want := map[continuationAbandonReason]bool{
		abandonAgentNotBound:           true,
		abandonAgentArchived:           true,
		abandonTaskFailed:              true,
		abandonTaskDependencyBlocked:   true,
		abandonProjectArchived:         true,
		abandonTaskAssigneeRequired:    true,
		abandonProjectAccessRevoked:    true,
		abandonTaskClosed:              false,
		abandonTaskUnavailable:         false,
		abandonConversationUnavailable: false,
		abandonOwnerUnavailable:        false,
		abandonChannelInvocationDenied: false,
	}
	for reason, notifies := range want {
		if got := reason.notifiesOwner(); got != notifies {
			t.Errorf("%s notifiesOwner() = %v, want %v", reason, got, notifies)
		}
	}
}

// The abandoned-continuation notice is written in the transaction that
// abandons the receipt, so an input the Inbox rejects would roll the
// abandonment back and leave the receipt due forever, wedging every receipt
// behind it. Every reason, for a Task and for a direct chat, at the longest
// names either side allows, must render an input the Inbox accepts — and say
// what the owner should do when there is something to do.
func TestAbandonedContinuationNoticeIsAlwaysAcceptedByTheInbox(t *testing.T) {
	t.Parallel()

	longAgent := strings.Repeat("😀", 200)
	longTitle := strings.Repeat("界", 240)
	task := uuid.NullUUID{UUID: uuid.New(), Valid: true}
	reasons := []continuationAbandonReason{
		abandonConversationUnavailable, abandonTaskUnavailable, abandonTaskClosed,
		abandonTaskFailed, abandonTaskDependencyBlocked, abandonProjectArchived,
		abandonOwnerUnavailable, abandonAgentArchived, abandonAgentNotBound,
		abandonTaskAssigneeRequired, abandonChannelInvocationDenied, abandonProjectAccessRevoked,
	}
	for _, reason := range reasons {
		for _, taskID := range []uuid.NullUUID{task, {}} {
			for _, names := range [][2]string{{"Eli", "Which color?"}, {longAgent, longTitle}} {
				notice := abandonedContinuation{
					entity:    inbox.Entity{Type: protocol.EntityQuestion, ID: uuid.NewString()},
					companyID: uuid.New(), conversationID: uuid.New(), taskID: taskID,
					originRunID: uuid.New(), agentID: uuid.New(),
					agentName: names[0], requestTitle: names[1], reason: reason,
					ownerUserID: uuid.New(),
				}
				input := abandonedContinuationInboxInput(notice)
				if !inbox.ValidCreateInput(input) {
					t.Fatalf("%s notice (task=%v, agent %d runes) is rejected by the Inbox: %+v",
						reason, taskID.Valid, len([]rune(names[0])), input)
				}
				if input.Type != inbox.TypeRunFailed ||
					!strings.Contains(*input.DeepLink, "runId="+notice.originRunID.String()) {
					t.Fatalf(
						"%s notice kind/deep link = %s %s",
						reason,
						input.Type,
						*input.DeepLink,
					)
				}
			}
		}
	}

	notBound := abandonedContinuation{
		entity:    inbox.Entity{Type: protocol.EntityQuestion, ID: uuid.NewString()},
		companyID: uuid.New(), conversationID: uuid.New(), taskID: task,
		originRunID: uuid.New(), agentID: uuid.New(),
		agentName: "Eli", requestTitle: "Which color?", reason: abandonAgentNotBound,
		ownerUserID: uuid.New(),
	}
	want := "Eli is not set up on your computer, so it could not continue after “Which color?”. " +
		"Set Eli up on your computer, then reply in the task's chat."
	if body := abandonedContinuationInboxInput(notBound).Body; body != want {
		t.Fatalf("Task notice body = %q, want %q", body, want)
	}
	notBound.taskID = uuid.NullUUID{}
	if body := abandonedContinuationInboxInput(notBound).Body; !strings.HasSuffix(body,
		"then ask again in the chat.") {
		t.Fatalf("direct chat notice body = %q, want it to end by asking again in the chat", body)
	}
}

// A Task whose continuation was abandoned is left in a status the Run button
// is usually not offered for (in progress, in review, failed), so the notice
// never says to run the task again. A reply in its chat is the one step the
// server accepts in every status a continuation can be abandoned in, and each
// Task cause says what is true of it: a failed Task is run again from its
// chat, one waiting on a prerequisite goes on once that is done, and one in
// an archived Project has nothing left to do.
func TestAbandonedTaskContinuationNoticeNamesOnlyWhatTheMemberCanDo(t *testing.T) {
	t.Parallel()

	task := uuid.NullUUID{UUID: uuid.New(), Valid: true}
	for _, test := range []struct {
		reason continuationAbandonReason
		want   string
	}{
		{
			abandonAgentNotBound,
			"Eli is not set up on your computer, so it could not continue after “Which color?”. " +
				"Set Eli up on your computer, then reply in the task's chat.",
		},
		{
			abandonAgentArchived,
			"Eli is archived, so it could not continue after “Which color?”. " +
				"Once Eli is restored, reply in the task's chat.",
		},
		{
			abandonTaskAssigneeRequired,
			"Eli could not continue after “Which color?”. To go on, reply in the task's chat.",
		},
		{
			abandonTaskFailed,
			"This task failed, so Eli could not continue after “Which color?”. " +
				"Reply in its chat to run Eli again.",
		},
		{
			abandonTaskDependencyBlocked,
			"This task is waiting on a task it depends on, so Eli could not continue after “Which color?”. " +
				"When that task is done, reply in this task's chat.",
		},
		{
			abandonProjectArchived,
			"This task's project is archived, so Eli could not continue after “Which color?”.",
		},
		{
			abandonProjectAccessRevoked,
			"Your access to this work no longer includes running tasks, so Eli could not continue after " +
				"“Which color?”. Ask someone who manages this work to let you run tasks, then reply in the task's chat.",
		},
	} {
		notice := abandonedContinuation{
			entity:    inbox.Entity{Type: protocol.EntityQuestion, ID: uuid.NewString()},
			companyID: uuid.New(), conversationID: uuid.New(), taskID: task,
			originRunID: uuid.New(), agentID: uuid.New(),
			agentName: "Eli", requestTitle: "Which color?", reason: test.reason,
			ownerUserID: uuid.New(),
		}
		body := abandonedContinuationInboxInput(notice).Body
		if body != test.want {
			t.Errorf("%s notice body = %q, want %q", test.reason, body, test.want)
		}
		if strings.Contains(body, "run the task again") || strings.Contains(body, "run it again") {
			t.Errorf("%s notice body = %q tells the member to start a Task that has no start", test.reason, body)
		}
	}
}

// calledNames returns the set of method and package-function names called
// anywhere in the named function's body.
func calledNames(t *testing.T, file *ast.File, name string) map[string]bool {
	t.Helper()
	for _, declaration := range file.Decls {
		function, isFunction := declaration.(*ast.FuncDecl)
		if !isFunction || function.Name.Name != name || function.Body == nil {
			continue
		}
		called := map[string]bool{}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, isCall := node.(*ast.CallExpr)
			if !isCall {
				return true
			}
			if selector, isSelector := call.Fun.(*ast.SelectorExpr); isSelector {
				called[selector.Sel.Name] = true
			}
			return true
		})
		return called
	}
	t.Fatalf("function %s not found in lifecycle.go", name)
	return nil
}

// referenceSequence lists, in source order, every function the named
// function calls by bare name and every selector it references — method
// calls and method values alike.
func referenceSequence(t *testing.T, file *ast.File, name string) []string {
	t.Helper()
	for _, declaration := range file.Decls {
		function, isFunction := declaration.(*ast.FuncDecl)
		if !isFunction || function.Name.Name != name || function.Body == nil {
			continue
		}
		var sequence []string
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.CallExpr:
				if ident, isIdent := typed.Fun.(*ast.Ident); isIdent {
					sequence = append(sequence, ident.Name)
				}
			case *ast.SelectorExpr:
				sequence = append(sequence, typed.Sel.Name)
			}
			return true
		})
		return sequence
	}
	t.Fatalf("function %s not found in lifecycle.go", name)
	return nil
}

func indexOf(sequence []string, name string) int {
	for at, candidate := range sequence {
		if candidate == name {
			return at
		}
	}
	return -1
}
