package wiki

import (
	"errors"
	"testing"
)

func TestUIAuthoringPolicyPreservesExistingContent(t *testing.T) {
	t.Parallel()
	plain := BlocksFromMarkdown("Saved prose")
	reactive := BlockNode{Type: "doc", Content: []BlockNode{{Type: "uiMetric"}}}
	for _, tc := range []struct {
		name     string
		disabled bool
		saved    int
		next     BlockNode
		denied   bool
	}{
		{"default permits reactive", false, 1, reactive, false},
		{"plain remains writable", true, 1, plain, false},
		{"introducing reactive", true, 1, reactive, true},
		{"editing reactive", true, 2, reactive, true},
		{"stripping reactive", true, 2, plain, true},
		{"sticky schema", true, 2, BlockNode{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := UIAuthoringPolicy{Disabled: tc.disabled}
			err := policy.Check(tc.saved, tc.next)
			if errors.Is(err, ErrUIAuthoringDisabled) != tc.denied {
				t.Fatalf("policy error = %v, denied = %v", err, tc.denied)
			}
			if policy.AllowsSchema(tc.saved) != (!tc.disabled || tc.saved < 2) {
				t.Fatal("read capability differs from stored schema policy")
			}
		})
	}
}
