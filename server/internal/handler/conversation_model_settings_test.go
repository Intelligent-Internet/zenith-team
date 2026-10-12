package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
)

func TestConversationModelSettingsRequestBoundary(t *testing.T) {
	t.Parallel()
	p := auth.UserPrincipal{UserID: uuid.New(), SessionID: uuid.New(), AuthKind: auth.TokenKindBearer}
	id, agentID := uuid.New(), uuid.New()
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"explicit defaults", `"model":null,"thinkingLevel":null,"expectedRevision":1`, 200},
		{"explicit choices", `"model":"gpt-model","thinkingLevel":"high","expectedRevision":1`, 200},
		{"missing model", `"thinkingLevel":null,"expectedRevision":1`, 422},
		{"missing thinking", `"model":null,"expectedRevision":1`, 422},
		{"missing revision", `"model":null,"thinkingLevel":null`, 422},
		{"null revision", `"model":null,"thinkingLevel":null,"expectedRevision":null`, 400},
		{"zero revision", `"model":null,"thinkingLevel":null,"expectedRevision":0`, 422},
		{"wrong type", `"model":42,"thinkingLevel":null,"expectedRevision":1`, 422},
		{"unknown field", `"model":null,"thinkingLevel":null,"expectedRevision":1,"other":true`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			called := false
			s := &conversationServiceStub{
				updateModelSettings: func(_ context.Context, got auth.UserPrincipal, gotID uuid.UUID, in conversation.ModelSettingsInput) (conversation.ModelSettings, error) {
					called = true
					if got.UserID != p.UserID || gotID != id || in.AgentID != agentID ||
						in.ExpectedRevision != 1 {
						t.Fatalf("input = %+v", in)
					}
					return conversation.ModelSettings{
						AgentID:       agentID,
						Provider:      "codex",
						Model:         in.Model,
						ThinkingLevel: in.ThinkingLevel,
						Revision:      2,
						CanEdit:       true,
					}, nil
				},
			}
			response := serveBearerJSON(
				phase4ConversationHandler(t, p, s),
				http.MethodPut,
				"/api/v1/conversations/"+id.String()+"/model-settings",
				[]byte(`{"agentId":"`+agentID.String()+`",`+tc.body+`}`),
			)
			if response.Code != tc.status || called != (tc.status == 200) {
				t.Fatalf(
					"status = %d, called = %v, body=%s",
					response.Code,
					called,
					response.Body.String(),
				)
			}
		})
	}
}
