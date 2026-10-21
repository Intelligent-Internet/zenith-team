package migrations

import (
	"strings"
	"testing"
)

func TestWorkBaselineDefinesTaskPlanOwnership(t *testing.T) {
	t.Parallel()

	contents, err := Files.ReadFile("000003_work.up.sql")
	if err != nil {
		t.Fatalf("read work baseline: %v", err)
	}
	workSQL := strings.ToLower(string(contents))
	for _, fragment := range []string{
		"milestone_type text",
		"tasks_milestone_idx",
		"'positioning'",
		"'growth'",
		"'support'",
	} {
		if !strings.Contains(workSQL, fragment) {
			t.Errorf("work baseline is missing %q", fragment)
		}
	}
	if strings.Contains(workSQL, "execution_type") {
		t.Error("work baseline retains retired execution_type")
	}
	for _, retired := range []string{"'website'", "'payments'", "'social_content'", "'seo'", "'scale'"} {
		if strings.Contains(workSQL, retired) {
			t.Errorf("work baseline retains retired milestone vocabulary %q", retired)
		}
	}
}
