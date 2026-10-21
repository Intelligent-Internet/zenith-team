package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
)

type huddleLeaveHTTPStub struct {
	conversation.HuddleAIService
	calls int
}

func (s *huddleLeaveHTTPStub) Leave(
	_ context.Context,
	_ uuid.UUID,
	token string,
) (conversation.HuddleAILeaveResult, error) {
	s.calls++
	if token != "test-capability" {
		return conversation.HuddleAILeaveResult{}, conversation.ErrHuddleForbidden
	}
	return conversation.HuddleAILeaveResult{Left: true}, nil
}

func TestHuddleLeaveHTTPBoundary(t *testing.T) {
	for _, tt := range []struct {
		name, body, token string
		want              int
	}{
		{"own attempt", `{}`, "test-capability", 200},
		{"missing capability", `{}`, "", 401},
		{"bad capability", `{}`, "wrong", 403},
		{"cannot end call", `{"endMeeting":true}`, "test-capability", 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stub := &huddleLeaveHTTPStub{}
			h := &Handler{HuddleAIService: stub}
			r := gin.New()
			h.registerHuddleAIRoutes(r.Group("/api/v1"))
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/internal/huddle-ai/"+uuid.NewString()+"/leave",
				strings.NewReader(tt.body),
			)
			req.Header.Set("Content-Type", "application/json")
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d %s, want %d", rec.Code, rec.Body, tt.want)
			}
			if (tt.want == 400 || tt.want == 401) && stub.calls != 0 {
				t.Fatal("invalid request reached service")
			}
		})
	}
}
