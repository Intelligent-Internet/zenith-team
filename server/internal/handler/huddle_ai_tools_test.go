package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"boardly.dev/boardly/server/internal/agent/voice"
	"boardly.dev/boardly/server/internal/conversation"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/google/uuid"
)

type huddleContextStub struct {
	conversation.HuddleAIService
	calls int
}

func (s *huddleContextStub) ReadContext(context.Context, uuid.UUID, string, voice.ContextRequest) (voice.ContextResult, error) {
	s.calls++
	return voice.ContextResult{Items: []voice.ContextItem{}}, nil
}

func TestHuddleToolHTTPRejectsForgedScopeAndNilID(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		want       int
	}{
		{"read", `{"requestId":"r1","tool":"get_company_context"}`, http.StatusOK},
		{"work status", `{"requestId":"r1","tool":"get_work_status"}`, http.StatusOK},
		{"work result", `{"requestId":"r1","tool":"get_work_result","proposalId":"22222222-2222-4222-8222-222222222222"}`, http.StatusOK},
		{"missing proposal", `{"requestId":"r1","tool":"get_work_result"}`, http.StatusUnprocessableEntity},
		{"nil proposal", `{"requestId":"r1","tool":"get_work_result","proposalId":"00000000-0000-0000-0000-000000000000"}`, http.StatusUnprocessableEntity},
		{"proposal on unrelated tool", `{"requestId":"r1","tool":"get_task","taskId":"22222222-2222-4222-8222-222222222222","proposalId":"22222222-2222-4222-8222-222222222222"}`, http.StatusUnprocessableEntity},
		{"forged company", `{"requestId":"r1","tool":"get_company_context","companyId":"22222222-2222-4222-8222-222222222222"}`, http.StatusBadRequest},
		{"forged actor", `{"requestId":"r1","tool":"get_company_context","userId":"22222222-2222-4222-8222-222222222222"}`, http.StatusBadRequest},
		{"nil task", `{"requestId":"r1","tool":"get_task","taskId":"00000000-0000-0000-0000-000000000000"}`, http.StatusUnprocessableEntity},
		{"unknown tool", `{"requestId":"r1","tool":"delete_task"}`, http.StatusUnprocessableEntity},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stub := &huddleContextStub{}
			h := &Handler{HuddleAIService: stub}
			router := gin.New()
			h.registerHuddleAIRoutes(router.Group("/api/v1"))
			request := httptest.NewRequest(http.MethodPost, "/api/v1/internal/huddle-ai/22222222-2222-4222-8222-222222222222/tools", strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer test")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, tt.want, response.Body.String())
			}
			if tt.want != http.StatusOK && stub.calls != 0 {
				t.Fatal("invalid request reached service")
			}
		})
	}
}

func TestHuddleToolRequestBoundary(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		request huddleToolRequest
		want    bool
	}{
		{"company", huddleToolRequest{Tool: "get_company_context", RequestID: "read-1"}, true},
		{"unknown tool", huddleToolRequest{Tool: "shell", RequestID: "read-1"}, false},
		{"missing task", huddleToolRequest{Tool: "get_task", RequestID: "read-1"}, false},
		{"bad status", huddleToolRequest{Tool: "search_tasks", RequestID: "read-1", Status: "made-up"}, false},
		{"negative cursor", huddleToolRequest{Tool: "search_tasks", RequestID: "read-1", BeforeNumber: -1}, false},
		{"task", huddleToolRequest{Tool: "get_task", RequestID: "read-1", TaskID: "22222222-2222-4222-8222-222222222222"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := binding.Validator.ValidateStruct(tt.request); (err == nil) != tt.want {
				t.Fatalf("validation = %v, want valid %v", err, tt.want)
			}
		})
	}
}
