package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
)

type inboxServiceStub struct {
	counts           func(context.Context, auth.UserPrincipal) (inbox.Counts, error)
	get              func(context.Context, auth.UserPrincipal, uuid.UUID) (inbox.Item, error)
	readAll          func(context.Context, auth.UserPrincipal) (inbox.ReadAllResult, error)
	readConversation func(context.Context, auth.UserPrincipal, uuid.UUID) (inbox.ReadAllResult, error)
	mutate           func(context.Context, auth.UserPrincipal, uuid.UUID, inbox.Mutation, int64) (inbox.MutationResult, error)
}

func (inboxServiceStub) List(context.Context, auth.UserPrincipal, inbox.ListOptions) (inbox.Page, error) {
	return inbox.Page{}, nil
}

func (s inboxServiceStub) Counts(ctx context.Context, principal auth.UserPrincipal) (inbox.Counts, error) {
	if s.counts != nil {
		return s.counts(ctx, principal)
	}
	return inbox.Counts{}, nil
}

func (s inboxServiceStub) Get(ctx context.Context, principal auth.UserPrincipal, itemID uuid.UUID) (inbox.Item, error) {
	if s.get != nil {
		return s.get(ctx, principal, itemID)
	}
	return inbox.Item{}, nil
}

func (s inboxServiceStub) Mutate(
	ctx context.Context,
	principal auth.UserPrincipal,
	itemID uuid.UUID,
	mutation inbox.Mutation,
	expectedRevision int64,
) (inbox.MutationResult, error) {
	if s.mutate != nil {
		return s.mutate(ctx, principal, itemID, mutation, expectedRevision)
	}
	return inbox.MutationResult{}, nil
}

func TestMutateInboxItemReturnsFlatItemWithCounts(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	itemID := uuid.Must(uuid.NewV7())
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: uuid.Must(uuid.NewV7()), AuthKind: auth.TokenKindBearer,
		CompanyID: companyID, Role: auth.CompanyRoleMember,
	}
	service := inboxServiceStub{mutate: func(
		_ context.Context,
		gotPrincipal auth.UserPrincipal,
		gotItemID uuid.UUID,
		mutation inbox.Mutation,
		expectedRevision int64,
	) (inbox.MutationResult, error) {
		if gotPrincipal != principal || gotItemID != itemID || mutation != inbox.MutationRead ||
			expectedRevision != 4 {
			t.Fatalf("Mutate() = %+v / %s / %s / %d", gotPrincipal, gotItemID, mutation, expectedRevision)
		}
		return inbox.MutationResult{
			Item: inbox.Item{
				ID: itemID, CompanyID: &companyID,
				Type:  inbox.TypeBlocker,
				Title: "Blocked", Body: "Needs input", Revision: 5,
			},
			Counts: inbox.Counts{UnreadCount: 2, CountsVersion: 9},
		}, nil
	}}
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Params = gin.Params{
		{Key: "itemId", Value: itemID.String()},
		{Key: "mutation", Value: string(inbox.MutationRead)},
	}
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"expectedRevision":4}`))
	request.Header.Set("Content-Type", "application/json")
	ginContext.Request = request.WithContext(auth.WithUserPrincipal(request.Context(), principal))

	(&Handler{InboxService: service}).mutateInboxItem(ginContext)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, nested := body["item"]; nested || string(body["id"]) != `"`+itemID.String()+`"` ||
		len(body["counts"]) == 0 {
		t.Fatalf("response = %s", recorder.Body.String())
	}
}

func (s inboxServiceStub) ReadAll(ctx context.Context, principal auth.UserPrincipal) (inbox.ReadAllResult, error) {
	if s.readAll != nil {
		return s.readAll(ctx, principal)
	}
	return inbox.ReadAllResult{}, nil
}

func (s inboxServiceStub) ReadConversation(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
) (inbox.ReadAllResult, error) {
	return s.readConversation(ctx, principal, conversationID)
}

func TestReadConversationInboxBindsCanonicalBodyAndPrincipal(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: uuid.Must(uuid.NewV7()), AuthKind: auth.TokenKindBearer,
		CompanyID: companyID, Role: auth.CompanyRoleMember,
	}
	service := inboxServiceStub{readConversation: func(
		_ context.Context,
		gotPrincipal auth.UserPrincipal,
		gotConversationID uuid.UUID,
	) (inbox.ReadAllResult, error) {
		if gotPrincipal != principal || gotConversationID != conversationID {
			t.Fatalf("ReadConversation() = %+v / %s", gotPrincipal, gotConversationID)
		}
		return inbox.ReadAllResult{Items: []inbox.Item{}, Counts: inbox.Counts{CountsVersion: 1}}, nil
	}}
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(
		http.MethodPost,
		"/",
		strings.NewReader(`{"conversationId":"`+conversationID.String()+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	ginContext.Request = request.WithContext(auth.WithUserPrincipal(request.Context(), principal))

	(&Handler{InboxService: service}).readConversationInbox(ginContext)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestReadConversationInboxRejectsQueryAndUnknownBodyFields(t *testing.T) {
	t.Parallel()
	principal := auth.UserPrincipal{
		UserID: uuid.Must(uuid.NewV7()), SessionID: uuid.Must(uuid.NewV7()), AuthKind: auth.TokenKindBearer,
		CompanyID: uuid.Must(uuid.NewV7()), Role: auth.CompanyRoleMember,
	}
	service := inboxServiceStub{readConversation: func(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
	) (inbox.ReadAllResult, error) {
		t.Fatal("ReadConversation() called for invalid input")
		return inbox.ReadAllResult{}, nil
	}}
	// A conversationId that is not a uuid is a field rule that did not hold,
	// so it is 422 validation_failed like a missing one — the body is
	// well-formed JSON. Only malformed transport (an unknown field here) is
	// 400 invalid_request.
	for _, testCase := range []struct {
		name   string
		path   string
		body   string
		status int
		code   string
	}{
		{
			name: "query", path: "/?extra=true",
			body:   `{"conversationId":"0197f58a-4411-7c62-b930-5e1d2a7f6c08"}`,
			status: http.StatusUnprocessableEntity, code: "validation_failed",
		},
		{
			name: "unknown field", path: "/",
			body:   `{"conversationId":"0197f58a-4411-7c62-b930-5e1d2a7f6c08","extra":true}`,
			status: http.StatusBadRequest, code: "invalid_request",
		},
		{
			name: "missing conversationId", path: "/", body: `{}`,
			status: http.StatusUnprocessableEntity, code: "validation_failed",
		},
		{
			name: "non-uuid conversationId", path: "/", body: `{"conversationId":"nope"}`,
			status: http.StatusUnprocessableEntity, code: "validation_failed",
		},
		{
			// Spelled like a uuid, so the binding rule passes it, but it
			// names no Conversation.
			name: "nil-uuid conversationId", path: "/",
			body:   `{"conversationId":"00000000-0000-0000-0000-000000000000"}`,
			status: http.StatusUnprocessableEntity, code: "validation_failed",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, testCase.path, strings.NewReader(testCase.body))
			request.Header.Set("Content-Type", "application/json")
			ginContext.Request = request.WithContext(auth.WithUserPrincipal(request.Context(), principal))

			(&Handler{InboxService: service}).readConversationInbox(ginContext)

			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if recorder.Code != testCase.status || body.Code != testCase.code {
				t.Fatalf("status = %d, body = %s; want %d %s",
					recorder.Code, recorder.Body.String(), testCase.status, testCase.code)
			}
		})
	}
}

// The counts, single-item, read-all and read-conversation routes accept no
// query string at all, and a stray parameter is 422 validation_failed before
// the service is reached — the list route already answers a parameter it does
// not know that way, and these four used to ignore theirs.
func TestInboxRoutesWithoutQueryParametersRejectAStrayOne(t *testing.T) {
	t.Parallel()
	principal := auth.UserPrincipal{
		UserID: uuid.Must(uuid.NewV7()), SessionID: uuid.Must(uuid.NewV7()), AuthKind: auth.TokenKindBearer,
		CompanyID: uuid.Must(uuid.NewV7()), Role: auth.CompanyRoleMember,
	}
	itemID := uuid.Must(uuid.NewV7())
	for _, testCase := range []struct {
		name   string
		method string
		path   string
		params gin.Params
		body   string
		serve  func(*Handler) gin.HandlerFunc
	}{
		{
			name: "counts", method: http.MethodGet, path: "/?extra=true",
			serve: func(h *Handler) gin.HandlerFunc { return h.inboxCounts },
		},
		{
			name: "counts with a list parameter", method: http.MethodGet, path: "/?status=unread",
			serve: func(h *Handler) gin.HandlerFunc { return h.inboxCounts },
		},
		{
			name: "single item", method: http.MethodGet, path: "/" + itemID.String() + "?extra=true",
			params: gin.Params{{Key: "itemId", Value: itemID.String()}},
			serve:  func(h *Handler) gin.HandlerFunc { return h.getInboxItem },
		},
		{
			name: "read all", method: http.MethodPost, path: "/?extra=true",
			serve: func(h *Handler) gin.HandlerFunc { return h.readAllInbox },
		},
		{
			name: "read conversation", method: http.MethodPost, path: "/?extra=true",
			body:  `{"conversationId":"` + itemID.String() + `"}`,
			serve: func(h *Handler) gin.HandlerFunc { return h.readConversationInbox },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			ginContext.Params = testCase.params
			request := httptest.NewRequest(testCase.method, testCase.path, strings.NewReader(testCase.body))
			request.Header.Set("Content-Type", "application/json")
			ginContext.Request = request.WithContext(auth.WithUserPrincipal(request.Context(), principal))

			testCase.serve(&Handler{InboxService: unreachableInboxService(t)})(ginContext)

			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if recorder.Code != http.StatusUnprocessableEntity || body.Code != "validation_failed" {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

// unreachableInboxService fails the test from whichever read or mutation the
// handler under test reaches: it exists for requests the boundary must refuse.
func unreachableInboxService(t *testing.T) inboxServiceStub {
	t.Helper()
	return inboxServiceStub{
		counts: func(context.Context, auth.UserPrincipal) (inbox.Counts, error) {
			t.Fatal("Counts() called for a request the boundary must refuse")
			return inbox.Counts{}, nil
		},
		get: func(context.Context, auth.UserPrincipal, uuid.UUID) (inbox.Item, error) {
			t.Fatal("Get() called for a request the boundary must refuse")
			return inbox.Item{}, nil
		},
		readAll: func(context.Context, auth.UserPrincipal) (inbox.ReadAllResult, error) {
			t.Fatal("ReadAll() called for a request the boundary must refuse")
			return inbox.ReadAllResult{}, nil
		},
		readConversation: func(context.Context, auth.UserPrincipal, uuid.UUID) (inbox.ReadAllResult, error) {
			t.Fatal("ReadConversation() called for a request the boundary must refuse")
			return inbox.ReadAllResult{}, nil
		},
	}
}
