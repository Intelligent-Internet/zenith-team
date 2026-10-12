package canvas

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestNormalizeOutputReviewCommand(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	for _, test := range []struct {
		name    string
		command Command
		want    error
	}{
		{"comment", Command{RequestKey: id, Type: "comment", Body: " hello ", Anchor: json.RawMessage(`{"type":"version"}`)}, nil},
		{"empty body", Command{RequestKey: id, Type: "comment", Body: "  "}, ErrInvalid},
		{"cannot change artifact", Command{RequestKey: id, Type: "restore_version"}, ErrInvalid},
		{"missing thread", Command{RequestKey: id, Type: "reply", Body: "hello"}, ErrInvalid},
		{"resolve revision required", Command{RequestKey: id, Type: "resolve", ThreadID: &id, Action: "resolve"}, ErrInvalid},
		{"invalid anchor", Command{RequestKey: id, Type: "comment", Body: "hello", Anchor: json.RawMessage(`{"type":"unknown"}`)}, ErrInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := normalizeOutputReviewCommand(test.command)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}
