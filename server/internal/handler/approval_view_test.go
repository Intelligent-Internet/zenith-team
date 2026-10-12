package handler

import (
	"net/url"
	"testing"
)

// view=decide asks the Approval and Question lists for what the caller may
// decide (the Company Approvals page and its counts). It is the only view;
// any other value, a repeat or an empty one is refused like any bad query.
func TestHITLListViewDecide(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		query  string
		decide bool
		valid  bool
	}{
		{"", false, true},
		{"view=decide", true, true},
		{"view=decide&status=pending,approved", true, true},
		{"view=all", false, false},
		{"view=", false, false},
		{"view=decide&view=decide", false, false},
	} {
		values, err := url.ParseQuery(testCase.query)
		if err != nil {
			t.Fatal(err)
		}
		approvals, err := parseApprovalListOptions(values)
		if (err == nil) != testCase.valid || approvals.Decide != testCase.decide {
			t.Errorf("approvals %q = %+v, %v", testCase.query, approvals, err)
		}
		if testCase.query == "view=decide&status=pending,approved" {
			values.Set("status", "pending,answered")
		}
		questions, err := parseQuestionListOptions(values)
		if (err == nil) != testCase.valid || questions.Decide != testCase.decide {
			t.Errorf("questions %q = %+v, %v", testCase.query, questions, err)
		}
	}
}
