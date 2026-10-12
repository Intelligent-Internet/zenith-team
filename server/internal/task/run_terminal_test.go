package task

import "testing"

func TestRunTerminalTaskStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		runStatus string
		want      Status
		valid     bool
	}{
		{runStatus: "completed", want: StatusInReview, valid: true},
		{runStatus: "failed", want: StatusError, valid: true},
		{runStatus: "cancelled", want: StatusCancelled, valid: true},
		{runStatus: "running"},
	}
	for _, test := range tests {
		t.Run(test.runStatus, func(t *testing.T) {
			t.Parallel()
			got, valid := runTerminalTaskStatus(test.runStatus)
			if got != test.want || valid != test.valid {
				t.Fatalf(
					"runTerminalTaskStatus(%q) = (%q, %t), want (%q, %t)",
					test.runStatus,
					got,
					valid,
					test.want,
					test.valid,
				)
			}
		})
	}
}
