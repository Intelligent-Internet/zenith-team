package conversation

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestDiscussionSkillReference(t *testing.T) {
	t.Parallel()
	id, version := uuid.New(), uuid.New()
	for _, test := range []struct {
		name, text string
		start, end int
		valid      bool
	}{
		{"ordinary", "Use $review", 4, 11, true},
		{"unicode", "😀 $review", 3, 10, true},
		{"code", "`$review`", 1, 8, false},
		{"link", "[$review](https://example.com)", 1, 8, false},
		{"wrong marker", "@review", 0, 7, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ref, _ := json.Marshal(map[string]any{"type": "skill", "skillId": id, "versionId": version, "start": test.start, "end": test.end})
			_, err := decodeDiscussionReferences(DiscussionBody{Version: 1, Markdown: test.text, References: []json.RawMessage{ref}})
			if (err == nil) != test.valid {
				t.Fatalf("valid = %v, want %v (%v)", err == nil, test.valid, err)
			}
		})
	}
}
