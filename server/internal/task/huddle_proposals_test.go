package task

import (
	"testing"

	"github.com/google/uuid"
)

func TestHuddleProposalSubmissionRequiresExplicitReview(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		in    SubmitHuddleProposalInput
		valid bool
	}{
		{"new Task", SubmitHuddleProposalInput{ExpectedRevision: 1, Confirm: true, MilestoneType: MilestoneTypeProduct}, true},
		{"no confirmation", SubmitHuddleProposalInput{ExpectedRevision: 1, MilestoneType: MilestoneTypeProduct}, false},
		{"no revision", SubmitHuddleProposalInput{Confirm: true, MilestoneType: MilestoneTypeProduct}, false},
		{"no milestone", SubmitHuddleProposalInput{ExpectedRevision: 1, Confirm: true}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateProposalSubmission(tt.in, uuid.NullUUID{})
			if (err == nil) != tt.valid {
				t.Fatalf("error = %v, valid = %v", err, tt.valid)
			}
		})
	}
}
