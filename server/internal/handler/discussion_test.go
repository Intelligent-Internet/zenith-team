package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/attachment"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
)

type discussionHTTPStub struct {
	conversation.DiscussionService
	send func(auth.UserPrincipal, uuid.UUID, conversation.DiscussionSendInput) (conversation.DiscussionReceipt, error)
	list func(conversation.DiscussionPageOptions) (conversation.DiscussionPage, error)
}

func (s discussionHTTPStub) Send(
	_ context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
	input conversation.DiscussionSendInput,
) (conversation.DiscussionReceipt, error) {
	return s.send(principal, id, input)
}

func (s discussionHTTPStub) ListMessages(
	_ context.Context,
	_ auth.UserPrincipal,
	_ uuid.UUID,
	input conversation.DiscussionPageOptions,
) (conversation.DiscussionPage, error) {
	return s.list(input)
}

func discussionHTTPHandler(principal auth.UserPrincipal, service conversation.DiscussionService) *Handler {
	resolve := func(_ context.Context, _ auth.UserPrincipal, _ uuid.UUID) (auth.UserPrincipal, error) {
		return principal, nil
	}
	return New(Handler{
		AuthService: authenticatedAuthStub(
			principal.UserID,
		),
		CompanyService:           &companyServiceStub{authorize: resolve, requirePermission: resolve},
		HumanConversationService: &conversation.DefaultConversationService{},
		DiscussionService:        service,
	})
}

type channelListHTTPStub struct {
	conversation.ConversationService
	list func(auth.UserPrincipal, conversation.DiscussionListOptions) (conversation.DiscussionList, error)
}

func (s channelListHTTPStub) ListChannels(
	_ context.Context,
	principal auth.UserPrincipal,
	options conversation.DiscussionListOptions,
) (conversation.DiscussionList, error) {
	return s.list(principal, options)
}

func TestChannelHTTPHiddenFilter(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		query      string
		wantHidden bool
		wantStatus int
	}{
		{name: "default", wantStatus: http.StatusOK},
		{name: "include hidden", query: "?include_hidden=true", wantHidden: true, wantStatus: http.StatusOK},
		{name: "exclude hidden", query: "?include_hidden=false", wantStatus: http.StatusOK},
		{name: "invalid value", query: "?include_hidden=maybe", wantStatus: http.StatusUnprocessableEntity},
		{name: "duplicate value", query: "?include_hidden=true&include_hidden=false", wantStatus: http.StatusUnprocessableEntity},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			principal := auth.UserPrincipal{
				UserID:    uuid.New(),
				CompanyID: uuid.New(),
				Role:      auth.CompanyRoleMember,
			}
			called := false
			handler := discussionHTTPHandler(principal, discussionHTTPStub{})
			handler.HumanConversationService = channelListHTTPStub{
				list: func(got auth.UserPrincipal, options conversation.DiscussionListOptions) (conversation.DiscussionList, error) {
					called = true
					if got.UserID != principal.UserID || got.CompanyID != principal.CompanyID ||
						got.Role != principal.Role || options.IncludeHidden != test.wantHidden {
						t.Fatalf(
							"principal, include hidden = %#v, %v; want %#v, %v",
							got,
							options.IncludeHidden,
							principal,
							test.wantHidden,
						)
					}
					return conversation.DiscussionList{
						Items: []conversation.DiscussionSummary{},
					}, nil
				},
			}
			path := "/api/v1/companies/" + principal.CompanyID.String() + "/channels" + test.query
			result := serveBearerRequest(handler, http.MethodGet, path, nil)
			if result.Code != test.wantStatus || called != (test.wantStatus == http.StatusOK) {
				t.Fatalf(
					"status, called = %d, %v; want %d, %v; body = %s",
					result.Code,
					called,
					test.wantStatus,
					test.wantStatus == http.StatusOK,
					result.Body,
				)
			}
		})
	}
}

func TestDiscussionHTTPReceiptAndBoundary(t *testing.T) {
	t.Parallel()
	principal := auth.UserPrincipal{UserID: uuid.New(), CompanyID: uuid.New(), Role: auth.CompanyRoleMember}
	id, key := uuid.New(), uuid.New()
	calls := 0
	handler := discussionHTTPHandler(
		principal,
		discussionHTTPStub{
			send: func(got auth.UserPrincipal, target uuid.UUID, input conversation.DiscussionSendInput) (conversation.DiscussionReceipt, error) {
				calls++
				if got.UserID != principal.UserID || got.CompanyID != principal.CompanyID ||
					got.Role != principal.Role ||
					target != id ||
					input.RequestKey != key ||
					input.Body.Markdown != "Chào 👋" {
					t.Fatalf("send input = %#v %#v %v", got, input, target)
				}
				return conversation.DiscussionReceipt{
					RequestKey: key,
					Message: conversation.DiscussionMessage{
						ID:             uuid.New(),
						ConversationID: id,
						Attachments:    []attachment.Response{},
					},
					ConversationRevision: 2,
					HeadSeq:              1,
				}, nil
			},
		},
	)
	path := "/api/v1/companies/" + principal.CompanyID.String() + "/conversations/" + id.String() + "/discussion-messages"
	body := `{"requestKey":"` + key.String() + `","body":{"version":1,"markdown":"Chào 👋","references":[]},"attachmentIds":[],"threadRootId":null}`
	response := serveBearerJSON(handler, http.MethodPost, path, []byte(body))
	if response.Code != 201 || strings.Contains(response.Body.String(), `"runId"`) ||
		response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("receipt = %d %s %v", response.Code, response.Body, response.Header())
	}
	for i, invalid := range []string{strings.Replace(body, `"body":`, `"unknown":1,"body":`, 1), strings.Replace(body, `"requestKey":"`+key.String()+`"`, `"requestKey":null`, 1), strings.Replace(body, `"references":[]`, `"references":[],"extra":true`, 1)} {
		response = serveBearerJSON(handler, http.MethodPost, path, []byte(invalid))
		expected := http.StatusBadRequest
		if i == 1 {
			expected = http.StatusUnprocessableEntity
		}
		if response.Code != expected {
			t.Fatalf("invalid request = %d %s", response.Code, response.Body)
		}
	}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer access-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Zenith-Team-Run-Token", "run-proof")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != 403 || !strings.Contains(recorder.Body.String(), `"human_actor_required"`) || calls != 1 {
		t.Fatalf("agent route = %d %s, sends=%d", recorder.Code, recorder.Body, calls)
	}
}

func TestDiscussionHTTPQueryBounds(t *testing.T) {
	t.Parallel()
	principal := auth.UserPrincipal{UserID: uuid.New(), CompanyID: uuid.New(), Role: auth.CompanyRoleMember}
	called := false
	handler := discussionHTTPHandler(
		principal,
		discussionHTTPStub{
			list: func(options conversation.DiscussionPageOptions) (conversation.DiscussionPage, error) {
				called = true
				if options.Limit != 50 {
					t.Fatalf("default limit = %d", options.Limit)
				}
				return conversation.DiscussionPage{Items: []conversation.DiscussionMessage{}}, nil
			},
		},
	)
	path := "/api/v1/companies/" + principal.CompanyID.String() + "/conversations/" + uuid.NewString() + "/discussion-messages"
	for _, query := range []string{"?limit=101", "?limit=0", "?limit=2&limit=3", "?offset=1", "?cursor=", "?limit=1.0"} {
		result := serveBearerRequest(handler, http.MethodGet, path+query, nil)
		if result.Code != 422 || called {
			t.Fatalf("%s = %d %s, called=%v", query, result.Code, result.Body, called)
		}
	}
	result := serveBearerRequest(handler, http.MethodGet, path, nil)
	if result.Code != 200 || !called || result.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("read = %d %s", result.Code, result.Body)
	}
}

func TestDiscussionErrorEnvelope(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err          error
		status       int
		code, detail string
	}{
		{
			conversation.ErrDiscussionNotFound,
			404,
			"discussion_not_found",
			"Conversation or message was not found.",
		},
		{
			conversation.ErrDiscussionForbidden,
			403,
			"discussion_operation_forbidden",
			"This action is not allowed.",
		},
		{conversation.ErrDiscussionInvalid, 422, "validation_failed", "request validation failed"},
		{
			conversation.ErrDiscussionAgentRequired,
			422,
			"discussion_agent_required",
			"Add the Agent to this conversation before you tag it.",
		},
		{
			conversation.ErrDiscussionRevisionConflict,
			409,
			"discussion_revision_conflict",
			"Conversation or message changed. Reload and try again.",
		},
		{
			conversation.ErrDiscussionRequestConflict,
			409,
			"discussion_request_conflict",
			"Request key was already used for different content.",
		},
		{
			conversation.ErrDiscussionCursorExpired,
			409,
			"discussion_cursor_expired",
			"Reload this conversation before continuing.",
		},
		{conversation.ErrDiscussionInvalidCursor, 422, "invalid_cursor", "invalid cursor"},
		{conversation.ErrDiscussionReadOnly, 409, "discussion_read_only", "This conversation is read-only."},
		{
			conversation.ErrDiscussionLimit,
			409,
			"discussion_limit_reached",
			"The conversation limit for this action was reached.",
		},
		{
			conversation.ErrDiscussionUnavailable,
			503,
			"discussion_unavailable",
			"Conversation is temporarily unavailable. Try again.",
		},
	}
	for _, test := range cases {
		t.Run(test.code, func(t *testing.T) {
			principal := auth.UserPrincipal{
				UserID:    uuid.New(),
				CompanyID: uuid.New(),
				Role:      auth.CompanyRoleMember,
			}
			handler := discussionHTTPHandler(
				principal,
				discussionHTTPStub{
					list: func(conversation.DiscussionPageOptions) (conversation.DiscussionPage, error) {
						return conversation.DiscussionPage{}, errors.Join(
							errors.New("private text never exposed"),
							test.err,
						)
					},
				},
			)
			result := serveBearerRequest(
				handler,
				http.MethodGet,
				"/api/v1/companies/"+principal.CompanyID.String()+"/conversations/"+uuid.NewString()+"/discussion-messages",
				nil,
			)
			if result.Code != test.status ||
				!strings.Contains(result.Body.String(), `"code":"`+test.code+`"`) ||
				!strings.Contains(result.Body.String(), `"detail":"`+test.detail+`"`) ||
				strings.Contains(result.Body.String(), "private text") {
				t.Fatalf("error = %d %s", result.Code, result.Body)
			}
		})
	}
}

func TestHumanConversationRecipientValidation(t *testing.T) {
	t.Parallel()
	id := uuid.NewString()
	other := uuid.NewString()
	for _, test := range []struct {
		name, body string
		valid      bool
	}{
		{"pair", `{"peerUserId":"` + id + `"}`, true},
		{"mixed", `{"peerUserIds":["` + id + `"],"agentIds":["` + other + `"]}`, true},
		{"agents only", `{"agentIds":["` + id + `","` + other + `"]}`, true},
		{"duplicate agents", `{"agentIds":["` + id + `","` + id + `"]}`, false},
		{"invalid agent", `{"agentIds":["invalid"]}`, false},
		{"empty agents", `{"agentIds":[]}`, false},
		{"group", `{"peerUserIds":["` + id + `","` + other + `"]}`, true},
		{"single in array", `{"peerUserIds":["` + id + `"]}`, true},
		{"missing", `{}`, false},
		{"empty", `{"peerUserIds":[]}`, false},
		{"both", `{"peerUserId":"` + id + `","peerUserIds":["` + other + `"]}`, false},
		{"duplicates", `{"peerUserIds":["` + id + `","` + id + `"]}`, false},
		{"invalid", `{"peerUserIds":["invalid"]}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			c.Request.Header.Set("Content-Type", "application/json")
			_, err := bindRequest[createHumanConversationRequest](c, maxDiscussionRequestBytes)
			if (err == nil) != test.valid {
				t.Fatalf("valid = %v, want %v: %v", err == nil, test.valid, err)
			}
		})
	}
}

func TestHumanConversationInviteValidation(t *testing.T) {
	t.Parallel()
	id := uuid.NewString()
	for _, test := range []struct {
		name, body string
		valid      bool
	}{
		{"human", `{"memberUserIds":["` + id + `"]}`, true},
		{"agent", `{"agentIds":["` + id + `"]}`, true},
		{"mixed", `{"memberUserIds":["` + id + `"],"agentIds":["` + uuid.NewString() + `"]}`, true},
		{"missing", `{}`, false},
		{"empty", `{"memberUserIds":[],"agentIds":[]}`, false},
		{"duplicates", `{"memberUserIds":["` + id + `","` + id + `"]}`, false},
		{"invalid", `{"agentIds":["invalid"]}`, false},
		{"unknown", `{"memberUserIds":["` + id + `"],"admin":true}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			c.Request.Header.Set("Content-Type", "application/json")
			_, err := bindRequest[inviteHumanConversationRequest](c, maxDiscussionRequestBytes)
			if (err == nil) != test.valid {
				t.Fatalf("valid = %v, want %v: %v", err == nil, test.valid, err)
			}
		})
	}
}
