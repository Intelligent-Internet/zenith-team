package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"github.com/google/uuid"
)

type huddleAIHTTPStub struct {
	conversation.HuddleAIService
	input     conversation.SetHuddleAIInput
	heartbeat conversation.HuddleAIHeartbeat
	setErr    error
}

func (s *huddleAIHTTPStub) Heartbeat(_ context.Context, _ uuid.UUID, _ string, input conversation.HuddleAIHeartbeat) (conversation.HuddleAILease, error) {
	s.heartbeat = input
	return conversation.HuddleAILease{}, nil
}

func TestHuddleWorkNoticeHeartbeatBoundary(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, body string
		want       int
	}{
		{"poll", `{"pollWork":true}`, http.StatusOK},
		{"claim", `{"claimWorkNotice":"receipt-run-state"}`, http.StatusOK},
		{"bounded", `{"claimWorkNotice":"` + strings.Repeat("x", 161) + `"}`, http.StatusUnprocessableEntity},
		{"forged scope", `{"pollWork":true,"companyId":"other"}`, http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service := &huddleAIHTTPStub{}
			h := New(Handler{HuddleAIService: service})
			response := serveBearerJSON(h, http.MethodPost, "/api/v1/internal/huddle-ai/"+uuid.NewString()+"/heartbeat", []byte(tt.body))
			if response.Code != tt.want {
				t.Fatalf("status = %d %s, want %d", response.Code, response.Body, tt.want)
			}
			if tt.name == "poll" && !service.heartbeat.PollWork {
				t.Fatal("poll flag not forwarded")
			}
			if tt.name == "claim" && service.heartbeat.ClaimWorkNotice != "receipt-run-state" {
				t.Fatal("claim not forwarded")
			}
		})
	}
}

func (s *huddleAIHTTPStub) SetEnabled(_ context.Context, _ auth.UserPrincipal, _ uuid.UUID, input conversation.SetHuddleAIInput) (conversation.HuddleAIStatus, error) {
	s.input = input
	return conversation.HuddleAIStatus{}, s.setErr
}

func serveSetHuddleAI(service *huddleAIHTTPStub, fields string) *httptest.ResponseRecorder {
	principal := auth.UserPrincipal{UserID: uuid.New(), CompanyID: uuid.New(), Role: auth.CompanyRoleMember}
	resolve := func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
		return principal, nil
	}
	handler := New(Handler{
		AuthService:     authenticatedAuthStub(principal.UserID),
		CompanyService:  &companyServiceStub{authorize: resolve, requirePermission: resolve},
		HuddleAIService: service,
	})
	path := "/api/v1/companies/" + principal.CompanyID.String() + "/huddles/" + uuid.NewString() + "/ai"
	body := []byte(`{` + fields + `,"expectedRevision":1,"requestKey":"` + uuid.NewString() + `"}`)
	return serveBearerJSON(handler, http.MethodPut, path, body)
}

func TestSetHuddleAIContextDefault(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		fields string
		want   bool
	}{
		{"invite defaults on", `"enabled":true`, true},
		{"invite can opt out", `"enabled":true,"allowCompanyContext":false`, false},
		{"disable defaults off", `"enabled":false`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service := &huddleAIHTTPStub{}
			response := serveSetHuddleAI(service, tt.fields)
			if response.Code != http.StatusOK || service.input.AllowCompanyContext != tt.want {
				t.Fatalf("response = %d %s, context = %v, want %v", response.Code, response.Body, service.input.AllowCompanyContext, tt.want)
			}
		})
	}
}

// Sharing Company context from outside the call is refused by the service
// (D31, B5); the client sees 403 huddle_operation_forbidden.
func TestSetHuddleAIContextOutsideTheCallIsForbidden(t *testing.T) {
	t.Parallel()
	service := &huddleAIHTTPStub{setErr: conversation.ErrHuddleForbidden}
	response := serveSetHuddleAI(
		service, `"enabled":true,"agentId":"`+uuid.NewString()+`","allowCompanyContext":true`,
	)
	if response.Code != http.StatusForbidden ||
		!strings.Contains(response.Body.String(), `"code":"huddle_operation_forbidden"`) {
		t.Fatalf("response = %d %s, want 403 huddle_operation_forbidden", response.Code, response.Body)
	}
}
