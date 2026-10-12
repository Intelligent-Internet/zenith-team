package handler

import (
	"strings"
	"testing"

	"github.com/gin-gonic/gin/binding"
)

func TestArtifactGrantPermissions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		permission string
		wantValid  bool
	}{
		{permission: "view", wantValid: true},
		{permission: "manage", wantValid: true},
		{permission: "edit", wantValid: true},
		{permission: "comment", wantValid: false},
	} {
		t.Run(test.permission, func(t *testing.T) {
			t.Parallel()
			var request artifactGrantRequest
			err := binding.JSON.BindBody([]byte(strings.ReplaceAll(
				`{"permission":"PERMISSION","expectedRevision":1}`,
				"PERMISSION",
				test.permission,
			)), &request)
			if (err == nil) != test.wantValid {
				t.Fatalf(
					"permission %q validation error = %v, want valid %v",
					test.permission,
					err,
					test.wantValid,
				)
			}
		})
	}
}
