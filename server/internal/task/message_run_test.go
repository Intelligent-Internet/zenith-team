package task

import "testing"

func TestTaskMessageRunStatusPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  Status
		allowed bool
		update  bool
	}{
		{name: "backlog", status: StatusBacklog, allowed: true, update: true},
		{name: "todo", status: StatusTodo, allowed: true, update: true},
		{name: "in progress", status: StatusInProgress, allowed: true},
		{name: "in review", status: StatusInReview, allowed: true, update: true},
		{name: "blocked", status: StatusBlocked, allowed: true, update: true},
		{name: "error", status: StatusError, allowed: true, update: true},
		{name: "done", status: StatusDone},
		{name: "failed", status: StatusFailed, allowed: true, update: true},
		{name: "cancelled", status: StatusCancelled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			allowed, update := taskMessageRunStatusPolicy(tt.status)
			if allowed != tt.allowed || update != tt.update {
				t.Fatalf(
					"taskMessageRunStatusPolicy(%q) = (%t, %t), want (%t, %t)",
					tt.status,
					allowed,
					update,
					tt.allowed,
					tt.update,
				)
			}
		})
	}
}

func TestTaskContinuationRunStatusPolicyRequiresNewMessageToResumeFailure(t *testing.T) {
	t.Parallel()

	allowed, update := taskContinuationRunStatusPolicy(StatusFailed)
	if allowed || update {
		t.Fatalf(
			"taskContinuationRunStatusPolicy(%q) = (%t, %t), want (false, false)",
			StatusFailed,
			allowed,
			update,
		)
	}

	allowed, update = taskContinuationRunStatusPolicy(StatusBlocked)
	if !allowed || !update {
		t.Fatalf(
			"taskContinuationRunStatusPolicy(%q) = (%t, %t), want (true, true)",
			StatusBlocked,
			allowed,
			update,
		)
	}
}
