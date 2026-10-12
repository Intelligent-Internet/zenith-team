package conversation

import (
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
)

// Only a Task chat's frames belong to a Project (ADR 0017): the stamp decides
// their audience from the chat, and every other chat keeps the route its kind
// and creator give it. None of these payloads names the Project itself.
func TestOnlyTaskChatFramesAreProjectFrames(t *testing.T) {
	t.Parallel()
	for _, kind := range []Kind{KindTask, KindDirect, KindSchedule} {
		want := kind == KindTask
		for name, payload := range map[string]auth.ProjectFramePayload{
			"conversation snapshot": Snapshot{Conversation: Response{Kind: kind}},
			"posted message":        PostedMessage{ConversationKind: kind},
			"message updated":       MessageUpdated{ConversationKind: kind},
		} {
			projectID, scoped := payload.ProjectFrameScope()
			if scoped != want || projectID != uuid.Nil {
				t.Errorf("%s in a %s chat: scope = (%s, %t), want (nil, %t)", name, kind, projectID, scoped, want)
			}
			audience := &auth.ProjectAudience{UserIDs: []uuid.UUID{uuid.New()}}
			stamped, ok := payload.WithProjectAudience(audience).(auth.ProjectFramePayload)
			if !ok || stamped.ProjectAudience() != audience {
				t.Errorf("%s in a %s chat does not keep the audience it is given", name, kind)
			}
		}
	}
}
