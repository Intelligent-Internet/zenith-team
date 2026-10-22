package canvas

import (
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

func TestAcceptsLatestPageText(t *testing.T) {
	t.Parallel()
	review := suggestionReviewState{
		operations: []wiki.BlockOperation{{Type: "update"}, {Type: "insert"}, {Type: "update"}},
		updates:    [][]byte{{1}, nil, nil},
	}
	for _, tt := range []struct {
		name    string
		action  string
		indexes []int
		want    bool
	}{
		{"text", "apply", []int{0}, true},
		{"mixed structural", "apply", []int{0, 1}, false},
		{"legacy without delta", "apply", []int{2}, false},
		{"empty", "apply", nil, false},
		{"reject", "reject", []int{0}, false},
		{"revise", "request_changes", []int{0}, false},
		{"undo", "undo", []int{0}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := acceptsLatestPageText(review, tt.action, tt.indexes); got != tt.want {
				t.Fatalf("acceptsLatestPageText = %v, want %v", got, tt.want)
			}
		})
	}
}
