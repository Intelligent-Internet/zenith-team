package wiki

import (
	"errors"
	"strings"
	"testing"
)

func TestWorkBlockInputBoundary(t *testing.T) {
	for _, test := range []struct {
		name  string
		node  BlockNode
		valid bool
	}{
		{"workflow", BlockNode{Type: "workflowReference", Attrs: map[string]any{"workflowKey": "market-research", "display": "card"}}, true},
		{"invalid workflow", BlockNode{Type: "workflowReference", Attrs: map[string]any{"workflowKey": "../private"}}, false},
		{"task picker", BlockNode{Type: "taskReference", Attrs: map[string]any{"taskId": "", "capturedTasks": nil, "capturedAt": nil}}, true},
		{"invented state", BlockNode{Type: "taskReference", Attrs: map[string]any{"capturedTasks": []any{map[string]any{"status": "done"}}}}, false},
		{"results", BlockNode{Type: "outputCollection", Attrs: map[string]any{"filter": "new", "capturedOutputs": nil}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateBlockImport(BlockNode{Type: "doc", Content: []BlockNode{test.node}})
			if test.valid && err != nil || !test.valid && !errors.Is(err, ErrPageInvalidInput) {
				t.Fatalf("validation = %v; valid %v", err, test.valid)
			}
		})
	}
}

func TestCapturedWorkMarkdownContainsEvidence(t *testing.T) {
	node := BlockNode{
		Type: "taskCollection",
		Attrs: map[string]any{
			"capturedTasks": []map[string]any{{"title": "Review evidence", "status": "done"}},
		},
	}
	if body := BlocksMarkdown(node); !strings.Contains(body, "Review evidence") || !strings.Contains(body, "done") {
		t.Fatalf("missing Task evidence: %s", body)
	}
}
