package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/task"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type huddleProposalHTTPStub struct {
	conversation.HuddleAIService
	calls int
}

type proposalSubmitHTTPStub struct {
	TaskService
	calls     int
	principal auth.UserPrincipal
}

func (s *proposalSubmitHTTPStub) SubmitHuddleProposal(_ context.Context, p auth.UserPrincipal, _, _ uuid.UUID, in task.SubmitHuddleProposalInput) (conversation.HuddleProposal, error) {
	s.calls++
	s.principal = p
	return conversation.HuddleProposal{State: "submitted"}, nil
}

func TestSubmitHuddleProposalHTTPBoundary(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		want       int
	}{
		{"confirmed", `{"expectedRevision":1,"confirm":true,"milestoneType":"product"}`, 200},
		{"not confirmed", `{"expectedRevision":1,"confirm":false}`, 422},
		{"forged runtime", `{"expectedRevision":1,"confirm":true,"runtimeId":"other"}`, 400},
		{"forged authorizer", `{"expectedRevision":1,"confirm":true,"userId":"other"}`, 400},
		{"forged Agent", `{"expectedRevision":1,"confirm":true,"agentId":"other"}`, 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := auth.UserPrincipal{UserID: uuid.New(), CompanyID: uuid.New(), SessionID: uuid.New(), AuthKind: auth.TokenKindSession}
			stub := &proposalSubmitHTTPStub{}
			h := &Handler{TaskService: stub}
			router := gin.New()
			router.POST("/:huddleId/:proposalId", h.submitHuddleProposal)
			req := httptest.NewRequest("POST", "/"+uuid.NewString()+"/"+uuid.NewString(), strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(auth.WithUserPrincipal(req.Context(), p))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status=%d %s, want %d", rec.Code, rec.Body, tt.want)
			}
			if tt.want == 200 && (stub.calls != 1 || stub.principal != p) {
				t.Fatal("confirmation did not use authenticated principal")
			}
			if tt.want != 200 && stub.calls != 0 {
				t.Fatal("invalid input reached service")
			}
		})
	}
}

func (s *huddleProposalHTTPStub) ProposeWork(context.Context, uuid.UUID, string, conversation.HuddleProposalInput) (conversation.HuddleProposal, error) {
	s.calls++
	return conversation.HuddleProposal{State: "pending"}, nil
}

func TestHuddleProposalHTTPBoundary(t *testing.T) {
	for _, tt := range []struct {
		name, extra string
		want        int
	}{
		{name: "draft only", want: http.StatusOK},
		{name: "forged speaker", extra: `,"userId":"22222222-2222-4222-8222-222222222222"`, want: http.StatusBadRequest},
		{name: "forged agent", extra: `,"agentId":"22222222-2222-4222-8222-222222222222"`, want: http.StatusBadRequest},
		{name: "execution is unavailable", extra: `,"execute":true`, want: http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stub := &huddleProposalHTTPStub{}
			h := &Handler{HuddleAIService: stub}
			router := gin.New()
			h.registerHuddleAIRoutes(router.Group("/api/v1"))
			body := `{"requestKey":"draft-1","projectId":"22222222-2222-4222-8222-222222222222","title":"Fix login","brief":"Verify login"` + tt.extra + `}`
			request := httptest.NewRequest(http.MethodPost, "/api/v1/internal/huddle-ai/22222222-2222-4222-8222-222222222222/proposals", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer test")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tt.want {
				t.Fatalf("got = %d %s, want %d", response.Code, response.Body, tt.want)
			}
			if tt.want != http.StatusOK && stub.calls != 0 {
				t.Fatal("invalid request reached service")
			}
		})
	}
}
