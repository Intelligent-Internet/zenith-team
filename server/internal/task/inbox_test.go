package task

import "testing"

// The task_assigned copy names who assigned the Task and never promises a
// start the server would refuse: an Agent the assignee has not set up, a
// computer that cannot run it, and a status the Run button is not offered
// for each get their own next step (ADR 0014).
func TestTaskAssignedBodySaysWhoAndWhatToDoNext(t *testing.T) {
	t.Parallel()

	reason := func(value string) *string { return &value }
	agentTask := func(status Status, ready bool, unready *string) Response {
		return Response{
			Title:      "Ship the page",
			Status:     status,
			ExecutedBy: ActorRef{Kind: ActorAgent, DisplayName: "Lyra"},
			Execution:  Execution{AssigneeReady: ready, UnreadyReason: unready},
		}
	}
	for _, test := range []struct {
		name    string
		current Response
		want    string
	}{
		{
			"work the assignee does themself",
			Response{Title: "Ship the page", Status: StatusTodo, ExecutedBy: ActorRef{Kind: ActorUser}},
			"Alice assigned you “Ship the page”.",
		},
		{
			"ready and waiting to start",
			agentTask(StatusTodo, true, nil),
			"Alice assigned you “Ship the page”. Start it on your computer: Lyra runs there.",
		},
		{
			"ready after a technical error",
			agentTask(StatusError, true, nil),
			"Alice assigned you “Ship the page”. Start it on your computer: Lyra runs there.",
		},
		{
			"in review is re-run from its chat",
			agentTask(StatusInReview, true, nil),
			"Alice assigned you “Ship the page”. Reply in its chat to run Lyra on your computer.",
		},
		{
			"in progress after a handoff cancelled its Run",
			agentTask(StatusInProgress, true, nil),
			"Alice assigned you “Ship the page”. Reply in its chat to run Lyra on your computer.",
		},
		{
			"closed Tasks have nothing to start",
			agentTask(StatusDone, true, nil),
			"Alice assigned you “Ship the page”.",
		},
		{
			"the Agent is not set up on their computer",
			agentTask(StatusTodo, false, reason("not_bound")),
			"Alice assigned you “Ship the page”. Set Lyra up on your computer to run it.",
		},
		{
			"their computer is offline",
			agentTask(StatusTodo, false, reason("runtime_offline")),
			"Alice assigned you “Ship the page”. Lyra runs on your computer, which is offline right now.",
		},
		{
			"their provider sign-in expired",
			agentTask(StatusInReview, false, reason("runtime_signed_out")),
			"Alice assigned you “Ship the page”. Lyra runs on your computer, where the provider sign-in has expired.",
		},
		{
			"their computer cannot run it",
			agentTask(StatusTodo, false, reason("runtime_unusable")),
			"Alice assigned you “Ship the page”. Lyra runs on your computer, which can't run it right now.",
		},
		{
			"the Agent is archived",
			agentTask(StatusTodo, false, reason("agent_archived")),
			"Alice assigned you “Ship the page”. Lyra is archived, so it cannot run.",
		},
		{
			"they may not run Tasks in its Project",
			agentTask(StatusTodo, false, reason("assignee_cannot_run")),
			"Alice assigned you “Ship the page”. You can't run tasks in this work yet, so Lyra cannot run it. Ask someone who manages this work to let you run tasks.",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := taskAssignedBody(test.current, "Alice"); got != test.want {
				t.Fatalf("taskAssignedBody() = %q, want %q", got, test.want)
			}
		})
	}
}
