package conversation

import (
	"testing"

	"github.com/google/uuid"
)

func TestNormalizeArtifactMessageInput(t *testing.T) {
	id := uuid.New()
	for _, test := range []struct {
		name       string
		ids        []uuid.UUID
		suppressed bool
		valid      bool
	}{
		{name: "artifact only", ids: []uuid.UUID{id}, valid: true},
		{name: "duplicate", ids: []uuid.UUID{id, id}},
		{name: "zero id", ids: []uuid.UUID{uuid.Nil}},
		{name: "hidden send", ids: []uuid.UUID{id}, suppressed: true},
		{name: "too many", ids: make([]uuid.UUID, 11)},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeSendInput(SendInput{RequestKey: uuid.New(), ArtifactIDs: test.ids, SuppressUserMessage: test.suppressed})
			if (err == nil) != test.valid {
				t.Fatalf("normalize = %+v, %v; valid=%v", got, err, test.valid)
			}
			if test.valid && got.Content != "Attached artifacts." {
				t.Fatalf("content = %q", got.Content)
			}
		})
	}
}

func TestArtifactMessageReplayIdentity(t *testing.T) {
	t.Parallel()
	id, other := uuid.New(), uuid.New()
	input := SendInput{Content: "Review", ArtifactIDs: []uuid.UUID{id}}
	prefix := "Review\n\nReferenced artifacts:\n"
	reference := "- artifactId: " + id.String()
	for _, test := range []struct {
		name, stored string
		matches      bool
	}{
		{"exact", prefix + reference, true},
		{"different body", "Other\n\nReferenced artifacts:\n" + reference, false},
		{"different id", prefix + "- artifactId: " + other.String(), false},
		{"extra reference", prefix + reference + "\n" + reference, false},
		{"extra metadata", prefix + reference + " — Summary", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := messageContentMatches(input, test.stored); got != test.matches {
				t.Fatalf("matches = %v, want %v", got, test.matches)
			}
		})
	}
}
