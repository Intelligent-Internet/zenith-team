package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRunHistoryFiltersRequireSourceScope(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		query string
		valid bool
	}{
		{"agent type", "agent_id=38fe9daf-d939-432e-b662-415c18016601&run_type=task", true},
		{"scoped revision", "task_id=38fe9daf-d939-432e-b662-415c18016601&revision=7", true},
		{"schedule revision", "conversation_id=38fe9daf-d939-432e-b662-415c18016601&run_type=schedule&revision=7", true},
		{"both scopes revision", "task_id=38fe9daf-d939-432e-b662-415c18016601&conversation_id=38fe9daf-d939-432e-b662-415c18016601&revision=7", true},
		{"unscoped revision", "revision=7", false},
		{"wrong source type", "task_id=38fe9daf-d939-432e-b662-415c18016601&run_type=direct_chat", false},
		{"unknown type", "run_type=chat", false},
		{"unsafe revision", "task_id=38fe9daf-d939-432e-b662-415c18016601&revision=9007199254740992", false},
		{"reversed window", "from=2026-09-13T01:00:00Z&to=2026-09-13T00:00:00Z", false},
		{"created window", "from=2026-09-13T00:00:00Z&to=2026-09-14T00:00:00Z", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodGet, "/?"+tt.query, nil)
			_, err := parseRunListQuery(ctx)
			if (err == nil) != tt.valid {
				t.Fatalf("parseRunListQuery(%q) error = %v, want valid=%v", tt.query, err, tt.valid)
			}
		})
	}
}
