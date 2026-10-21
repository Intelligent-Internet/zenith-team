package conversation

import (
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

func TestStudioProjectRejectsInvalidTextBeforeDatabaseWrite(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, title, description string
	}{
		{"NUL title", "Campaign\x00title", "Description"},
		{"NUL description", "Campaign", "Description\x00text"},
		{"invalid UTF-8 title", "Campaign\xff", "Description"},
		{"invalid UTF-8 description", "Campaign", "Description\xff"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := protocol.StudioProjectRequest{
				Action:      "reserve",
				RunToken:    "proof",
				Title:       test.title,
				Description: test.description,
			}
			if validStudioProjectRequest(request) {
				t.Fatal("accepted text that PostgreSQL cannot store")
			}
		})
	}
}
