package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/pkg/protocol"
)

func TestConversationTranscriptRouteReturnsRunIdentityAcrossOneStream(t *testing.T) {
	t.Parallel()

	principal := auth.UserPrincipal{
		UserID:    uuid.Must(uuid.NewV7()),
		SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
		AuthKind:  auth.TokenKindBearer,
		CompanyID: uuid.Must(uuid.NewV7()),
		Role:      auth.CompanyRoleOwner,
	}
	conversationID := uuid.Must(uuid.NewV7())
	entryID := uuid.Must(uuid.NewV7())
	service := &conversationServiceStub{
		transcript: func(
			_ context.Context,
			gotPrincipal auth.UserPrincipal,
			gotConversationID uuid.UUID,
			options conversation.TranscriptListOptions,
		) (conversation.TranscriptPage, error) {
			if gotPrincipal != principal || gotConversationID != conversationID ||
				options.AfterSeq != 63 || options.Limit != 20 {
				t.Fatalf("Transcript input = %+v / %s / %+v", gotPrincipal, gotConversationID, options)
			}
			return conversation.TranscriptPage{
				Items: []conversation.TranscriptEntry{{
					ID: entryID, RunID: uuid.MustParse("00000000-0000-4000-8000-000000000004"), Seq: 64, ItemKey: "agent-64",
					Role: protocol.RunItemRoleAgent,
					Parts: []protocol.RunTranscriptPart{
						protocol.RunTextPart{Type: protocol.RunTextPartTypeText, Text: "done"},
					},
					ItemDigest: "1111111111111111111111111111111111111111111111111111111111111111",
					CreatedAt:  time.Date(2026, time.August, 26, 1, 2, 3, 0, time.UTC),
				}},
				ThroughSeq: 64,
			}, nil
		},
	}
	handler := phase4ConversationHandler(t, principal, service)
	response := serveBearerRequest(
		handler,
		http.MethodGet,
		"/api/v1/companies/"+principal.CompanyID.String()+"/conversations/"+
			conversationID.String()+"/transcript?after_seq=63&limit=20",
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("Conversation transcript status = %d; body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Items []struct {
			RunID uuid.UUID `json:"runId"`
			Seq   int64     `json:"seq"`
		} `json:"items"`
		ThroughSeq int64 `json:"throughSeq"`
		HasMore    bool  `json:"hasMore"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode Conversation transcript: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].RunID != uuid.MustParse("00000000-0000-4000-8000-000000000004") || body.Items[0].Seq != 64 ||
		body.ThroughSeq != 64 || body.HasMore {
		t.Fatalf("Conversation transcript response = %+v", body)
	}
}

func TestParseConversationTranscriptQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		rawQuery     string
		wantAfterSeq int64
		wantLimit    int32
		wantErr      bool
	}{
		{name: "defaults", wantLimit: 200},
		{name: "catch up", rawQuery: "after_seq=41&limit=20", wantAfterSeq: 41, wantLimit: 20},
		{
			name: "sequence beyond int32", rawQuery: "after_seq=4294967296",
			wantAfterSeq: 4294967296, wantLimit: 200,
		},
		{name: "sequence beyond JSON safe integer", rawQuery: "after_seq=9007199254740992", wantErr: true},
		{name: "negative sequence", rawQuery: "after_seq=-1", wantErr: true},
		{name: "noncanonical sequence", rawQuery: "after_seq=041", wantErr: true},
		{name: "limit over maximum", rawQuery: "limit=201", wantErr: true},
		{name: "unknown parameter", rawQuery: "run_id=4", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseConversationTranscriptQuery(runQueryContext(test.rawQuery))
			if (err != nil) != test.wantErr {
				t.Fatalf("parseConversationTranscriptQuery() error = %v, wantErr %t", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if got.afterSeq != test.wantAfterSeq || got.limit != test.wantLimit {
				t.Fatalf("query = %+v, want afterSeq %d, limit %d",
					got, test.wantAfterSeq, test.wantLimit)
			}
		})
	}
}

func TestConversationMessageRoutesUseGlobalTargetsAndExactCatchupShape(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	requestKey := uuid.Must(uuid.NewV7())
	messageID := uuid.Must(uuid.NewV7())
	runID := uuid.MustParse("00000000-0000-4000-8000-000000004711")
	principal := auth.UserPrincipal{
		UserID:    userID,
		SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
		AuthKind:  auth.TokenKindBearer,
	}
	view := phase4MessageView(messageID, conversationID, userID)
	service := &conversationServiceStub{}
	service.listMessages = func(_ context.Context, got auth.UserPrincipal, target conversation.MessageTarget, options conversation.MessageListOptions) (conversation.MessagePage, error) {
		if got != principal ||
			target != (conversation.MessageTarget{Kind: conversation.TargetConversation, ID: conversationID}) ||
			options.AfterSeq != 0 ||
			options.ThroughSeq == nil ||
			*options.ThroughSeq != 1 ||
			options.Limit != 20 {
			t.Fatalf("ListMessages input = %+v / %+v / %+v", got, target, options)
		}
		return conversation.MessagePage{
			CompanyID: companyID, ConversationID: conversationID, Target: target,
			Items: []conversation.MessageView{view}, EarliestAvailableSeq: 1, ThroughSeq: 1,
		}, nil
	}
	service.send = func(_ context.Context, got auth.UserPrincipal, target conversation.MessageTarget, input conversation.SendInput) (conversation.SendResult, error) {
		if got != principal ||
			target != (conversation.MessageTarget{Kind: conversation.TargetConversation, ID: conversationID}) ||
			input.RequestKey != requestKey ||
			input.Content != " Inspect this " ||
			len(input.AttachmentIDs) != 0 {
			t.Fatalf("Send input = %+v / %+v / %+v", got, target, input)
		}
		return conversation.SendResult{
			CompanyID: companyID, ConversationKind: conversation.KindDirect, Target: target,
			Disposition: conversation.SendDispositionMessage, View: view,
			RunID: runIDPointer(runID), Created: true,
		}, nil
	}
	handler := New(Handler{AuthService: authenticatedAuthStub(userID), ConversationService: service})

	listed := serveBearerRequest(handler, http.MethodGet,
		"/api/v1/conversations/"+conversationID.String()+"/messages?after_seq=0&through_seq=1&limit=20", nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list Messages status = %d; body=%s", listed.Code, listed.Body.String())
	}
	// The window carries only items and its navigation bounds: whether older
	// or newer rows exist is derived by the client, never carried.
	var listBody map[string]json.RawMessage
	if err := json.Unmarshal(
		listed.Body.Bytes(),
		&listBody,
	); err != nil || len(listBody) != 3 || listBody["resetRequired"] != nil ||
		string(listBody["throughSeq"]) != "1" ||
		string(listBody["earliestAvailableSeq"]) != "1" {
		t.Fatalf("list Messages response = %s, %v", listed.Body.String(), err)
	}
	legacyQuery := serveBearerRequest(handler, http.MethodGet,
		"/api/v1/conversations/"+conversationID.String()+"/messages?afterSeq=0", nil)
	if legacyQuery.Code != http.StatusUnprocessableEntity {
		t.Fatalf("legacy Message query status = %d; want 422", legacyQuery.Code)
	}

	sent := serveBearerJSON(
		handler,
		http.MethodPost,
		"/api/v1/conversations/"+conversationID.String()+"/messages",
		[]byte(
			`{"requestKey":"`+requestKey.String()+`","content":" Inspect this ","attachmentIds":[]}`,
		),
	)
	if sent.Code != http.StatusCreated {
		t.Fatalf("send Message status = %d; body=%s", sent.Code, sent.Body.String())
	}
	var sendBody map[string]json.RawMessage
	if err := json.Unmarshal(
		sent.Body.Bytes(),
		&sendBody,
	); err != nil || len(sendBody) != 3 ||
		string(sendBody["disposition"]) != `"message"` ||
		string(sendBody["runId"]) != `"00000000-0000-4000-8000-000000004711"` {
		t.Fatalf("send Message response = %s, %v", sent.Body.String(), err)
	}
	if _, exists := sendBody["message"]; !exists {
		t.Fatalf("send Message response omitted Message view: %s", sent.Body.String())
	}
	var sentMessage map[string]json.RawMessage
	if err := json.Unmarshal(sendBody["message"], &sentMessage); err != nil ||
		string(sentMessage["id"]) != `"`+messageID.String()+`"` ||
		sentMessage["attachments"] == nil || sentMessage["message"] != nil {
		t.Fatalf("send Message response is not flat: %s, %v", sendBody["message"], err)
	}
	invalid := serveBearerRequest(handler, http.MethodGet,
		"/api/v1/conversations/"+conversationID.String()+"/messages?limit=020", nil)
	assertErrorResponse(t, invalid, http.StatusUnprocessableEntity, "validation_failed")
}

func TestTaskMessageAliasUsesTaskTarget(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	taskID := uuid.Must(uuid.NewV7())
	service := &conversationServiceStub{}
	service.listMessages = func(_ context.Context, _ auth.UserPrincipal, target conversation.MessageTarget, _ conversation.MessageListOptions) (conversation.MessagePage, error) {
		if target != (conversation.MessageTarget{Kind: conversation.TargetTask, ID: taskID}) {
			t.Fatalf("Task Message target = %+v", target)
		}
		return conversation.MessagePage{
			CompanyID: uuid.Must(uuid.NewV7()), ConversationID: uuid.Must(uuid.NewV7()), Target: target,
			Items: []conversation.MessageView{}, EarliestAvailableSeq: 1,
		}, nil
	}
	handler := New(Handler{AuthService: authenticatedAuthStub(userID), ConversationService: service})
	response := serveBearerRequest(handler, http.MethodGet, "/api/v1/tasks/"+taskID.String()+"/messages", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("Task Message alias status = %d; body=%s", response.Code, response.Body.String())
	}
}

func TestConversationRunQueueRoutesExposeProjectionAndVersionedMutations(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	firstID := uuid.Must(uuid.NewV7())
	secondID := uuid.Must(uuid.NewV7())
	projection := conversation.RunQueueProjection{
		Items: []conversation.RunQueueItem{},
		Limit: 20,
	}
	service := &conversationServiceStub{}
	service.getRunQueue = func(_ context.Context, got auth.UserPrincipal, gotID uuid.UUID) (conversation.RunQueueProjection, error) {
		if got.UserID != userID || got.AuthKind != auth.TokenKindBearer || gotID != conversationID {
			t.Fatalf("FetchRunQueue input = %+v / %s", got, gotID)
		}
		return projection, nil
	}
	service.reorderQueue = func(_ context.Context, got auth.UserPrincipal, gotID uuid.UUID, input conversation.RunQueueOrderInput) (conversation.RunQueueProjection, error) {
		if got.UserID != userID || got.AuthKind != auth.TokenKindBearer || gotID != conversationID ||
			len(input.Items) != 2 ||
			input.Items[0] != (conversation.RunQueueOrderItem{ID: secondID, ExpectedRevision: 4}) ||
			input.Items[1] != (conversation.RunQueueOrderItem{ID: firstID, ExpectedRevision: 2}) {
			t.Fatalf("ReorderRunQueue input = %+v / %s / %+v", got, gotID, input)
		}
		return projection, nil
	}
	service.deleteQueueItem = func(_ context.Context, got auth.UserPrincipal, gotConversationID, gotItemID uuid.UUID, input conversation.DeleteRunQueueItemInput) (conversation.RunQueueProjection, error) {
		if got.UserID != userID || got.AuthKind != auth.TokenKindBearer ||
			gotConversationID != conversationID ||
			gotItemID != firstID ||
			input.ExpectedRevision != 3 {
			t.Fatalf(
				"DeleteRunQueueItem input = %+v / %s / %s / %+v",
				got,
				gotConversationID,
				gotItemID,
				input,
			)
		}
		return projection, nil
	}
	handler := New(Handler{AuthService: authenticatedAuthStub(userID), ConversationService: service})

	fetched := serveBearerRequest(handler, http.MethodGet,
		"/api/v1/conversations/"+conversationID.String()+"/run-queue", nil)
	if fetched.Code != http.StatusOK || !bytes.Contains(fetched.Body.Bytes(), []byte(`"limit":20`)) {
		t.Fatalf("GET Run queue = %d; body=%s", fetched.Code, fetched.Body.String())
	}
	reordered := serveBearerJSON(
		handler,
		http.MethodPut,
		"/api/v1/conversations/"+conversationID.String()+"/run-queue/order",
		[]byte(
			`{"items":[{"id":"`+secondID.String()+`","expectedRevision":4},{"id":"`+firstID.String()+`","expectedRevision":2}]}`,
		),
	)
	if reordered.Code != http.StatusOK {
		t.Fatalf("PUT Run queue order = %d; body=%s", reordered.Code, reordered.Body.String())
	}
	deleted := serveBearerJSON(handler, http.MethodDelete,
		"/api/v1/conversations/"+conversationID.String()+"/run-queue/"+firstID.String(),
		[]byte(`{"expectedRevision":3}`))
	if deleted.Code != http.StatusOK {
		t.Fatalf("DELETE Run queue item = %d; body=%s", deleted.Code, deleted.Body.String())
	}
}

func TestConversationQueuedSendReturnsAcceptedUnionWithoutTranscriptMessage(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	requestKey := uuid.Must(uuid.NewV7())
	messageID := uuid.Must(uuid.NewV7())
	queueItemID := uuid.Must(uuid.NewV7())
	service := &conversationServiceStub{}
	service.send = func(_ context.Context, _ auth.UserPrincipal, _ conversation.MessageTarget, input conversation.SendInput) (conversation.SendResult, error) {
		return conversation.SendResult{
			CompanyID:        uuid.Must(uuid.NewV7()),
			ConversationKind: conversation.KindDirect,
			Target: conversation.MessageTarget{
				Kind: conversation.TargetConversation,
				ID:   conversationID,
			},
			Disposition: conversation.SendDispositionQueued,
			QueueItem: &conversation.RunQueueItem{
				ID: queueItemID, MessageID: messageID, Content: input.Content,
				Attachments: []attachment.Response{}, QueuedAt: time.Now().UTC(), Revision: 1,
			},
			Created: true,
		}, nil
	}
	handler := New(Handler{
		AuthService:         authenticatedAuthStub(userID),
		ConversationService: service,
	})
	response := serveBearerJSON(handler, http.MethodPost,
		"/api/v1/conversations/"+conversationID.String()+"/messages",
		[]byte(`{"requestKey":"`+requestKey.String()+`","content":"later"}`))
	if response.Code != http.StatusAccepted {
		t.Fatalf("queued send status = %d; body=%s", response.Code, response.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil ||
		string(body["disposition"]) != `"queued"` || body["queueItem"] == nil || body["message"] != nil {
		t.Fatalf("queued send body = %s, %v", response.Body.String(), err)
	}
	// The queue-position receipts are gone: a client renders the queue from
	// the run-queue projection, not from per-send counters.
	for _, key := range []string{"queuedPosition", "queuedCount", "removalReason"} {
		if _, exists := body[key]; exists {
			t.Fatalf("queued send body still carries %q: %s", key, response.Body.String())
		}
	}
	// A queued prompt has no run yet — runs.id is taken by the promotion's
	// INSERT — so the receipt must not name one. A client follows queueItem.id
	// until the promotion publishes a real run.
	if _, exists := body["runId"]; exists {
		t.Fatalf("queued send body carries a runId: %s", response.Body.String())
	}
	var queueItem map[string]json.RawMessage
	if err := json.Unmarshal(body["queueItem"], &queueItem); err != nil ||
		string(queueItem["id"]) != `"`+queueItemID.String()+`"` {
		t.Fatalf("queued send queueItem = %s, %v", body["queueItem"], err)
	}
}

// runIDPointer names a run in a fixture; runs.id is a bigserial, so a literal
// cannot have its address taken.
func runIDPointer(id uuid.UUID) *uuid.UUID { return &id }

func TestConversationUserStateRoutesExposeAbsentStateAndAtomicCASMutation(t *testing.T) {
	t.Parallel()

	userID, conversationID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	now := time.Now().UTC()
	service := &conversationServiceStub{}
	service.getState = func(
		_ context.Context,
		principal auth.UserPrincipal,
		gotConversationID uuid.UUID,
	) (conversation.UserState, error) {
		if principal.UserID != userID || gotConversationID != conversationID {
			t.Fatalf("FetchState input = %+v / %s", principal, gotConversationID)
		}
		return conversation.UserState{
			ConversationID: conversationID, UserID: userID,
			UpdatedAt: now, Revision: 0,
		}, nil
	}
	service.updateState = func(
		_ context.Context,
		principal auth.UserPrincipal,
		gotConversationID uuid.UUID,
		input conversation.UpdateUserStateInput,
	) (conversation.UserState, error) {
		if principal.UserID != userID || gotConversationID != conversationID || input.ExpectedRevision != 3 ||
			input.Pinned == nil || !*input.Pinned ||
			input.Archived == nil || *input.Archived {
			t.Fatalf("UpdateState input = %+v / %s / %+v", principal, gotConversationID, input)
		}
		return conversation.UserState{
			ConversationID: conversationID, UserID: userID,
			PinnedAt: &now, UpdatedAt: now, Revision: 4,
		}, nil
	}
	handler := New(Handler{AuthService: authenticatedAuthStub(userID), ConversationService: service})
	read := serveBearerRequest(
		handler,
		http.MethodGet,
		"/api/v1/conversations/"+conversationID.String()+"/state",
		nil,
	)
	if read.Code != http.StatusOK || !bytes.Contains(read.Body.Bytes(), []byte(`"revision":0`)) ||
		!bytes.Contains(read.Body.Bytes(), []byte(`"pinnedAt":null`)) {
		t.Fatalf("GET Conversation state = %d; body=%s", read.Code, read.Body.String())
	}
	response := serveBearerJSON(
		handler,
		http.MethodPatch,
		"/api/v1/conversations/"+conversationID.String()+"/state",
		[]byte(`{
		"expectedRevision":3,"pinned":true,"archived":false
	}`),
	)
	if response.Code != http.StatusOK ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"revision":4`)) {
		t.Fatalf("PATCH Conversation state = %d; body=%s", response.Code, response.Body.String())
	}
}

// The run-queue mutations answer with the three codes docs/api/conversations.md
// documents: an unknown queue item is the Conversation's 404, an order that is
// not a permutation of the queue is 422, and a queue that changed under the
// write is 409. The service decides which; the handler must not collapse them.
func TestConversationRunQueueMutationsMapDocumentedErrorCodes(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	queueItemID := uuid.Must(uuid.NewV7())
	queue := "/api/v1/conversations/" + conversationID.String() + "/run-queue"
	order := `{"items":[{"id":"` + queueItemID.String() + `","expectedRevision":1}]}`
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"unknown item":      {conversation.ErrNotFound, http.StatusNotFound, "conversation_not_found"},
		"not a permutation": {conversation.ErrInvalidInput, http.StatusUnprocessableEntity, "validation_failed"},
		"queue changed":     {conversation.ErrRunQueueConflict, http.StatusConflict, "run_queue_conflict"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			service := &conversationServiceStub{}
			service.reorderQueue = func(context.Context, auth.UserPrincipal, uuid.UUID, conversation.RunQueueOrderInput) (conversation.RunQueueProjection, error) {
				return conversation.RunQueueProjection{}, tc.err
			}
			service.deleteQueueItem = func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, conversation.DeleteRunQueueItemInput) (conversation.RunQueueProjection, error) {
				return conversation.RunQueueProjection{}, tc.err
			}
			handler := New(Handler{
				AuthService: authenticatedAuthStub(userID), ConversationService: service,
			})

			reordered := serveBearerJSON(handler, http.MethodPut, queue+"/order", []byte(order))
			assertErrorResponse(t, reordered, tc.status, tc.code)
			deleted := serveBearerJSON(handler, http.MethodDelete, queue+"/"+queueItemID.String(),
				[]byte(`{"expectedRevision":1}`))
			assertErrorResponse(t, deleted, tc.status, tc.code)
		})
	}
}

// The run-queue revision bound is the binding tag's.
//
// `revision` reads it off protocol.MaxJSONSafeInteger, and both restatements
// of it are gone: conversation.normalizeDeleteRunQueueItemInput, which was the
// whole of that function, and the two clauses in the item loop of
// normalizeRunQueueOrderInput. The stub has nothing registered for either
// method, so reaching the service would surface as a 500 rather than as the
// 422 asked for here.
func TestRunQueueRejectsAnUnusableRevisionBeforeTheService(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	queueItemID := uuid.Must(uuid.NewV7())
	handler := New(Handler{
		AuthService:         authenticatedAuthStub(userID),
		ConversationService: &conversationServiceStub{},
	})

	queue := "/api/v1/conversations/" + conversationID.String() + "/run-queue"
	item := queue + "/" + queueItemID.String()
	order := func(revision string) string {
		return `{"items":[{"id":"` + queueItemID.String() + `","expectedRevision":` + revision + `}]}`
	}
	for name, route := range map[string]struct {
		method string
		path   string
		body   string
	}{
		"delete zero":          {http.MethodDelete, item, `{"expectedRevision":0}`},
		"delete past ceiling":  {http.MethodDelete, item, `{"expectedRevision":9007199254740992}`},
		"reorder zero":         {http.MethodPut, queue + "/order", order("0")},
		"reorder past ceiling": {http.MethodPut, queue + "/order", order("9007199254740992")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			response := serveBearerJSON(handler, route.method, route.path, []byte(route.body))
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s %s = %d %s, want 422",
					route.method, route.path, response.Code, response.Body.String())
			}
		})
	}
}

// A malformed conversation (or task) id in the path is 404 — the resource
// cannot exist — never 422, on every conversation-addressed route.
func TestConversationMalformedPathIDsAre404(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	handler := New(Handler{
		AuthService:         authenticatedAuthStub(userID),
		ConversationService: &conversationServiceStub{},
	})
	for _, route := range []struct {
		method string
		path   string
		body   []byte
	}{
		{http.MethodGet, "/api/v1/conversations/not-a-uuid/state", nil},
		{http.MethodGet, "/api/v1/conversations/not-a-uuid/run-queue", nil},
		{http.MethodGet, "/api/v1/conversations/not-a-uuid/messages", nil},
		{http.MethodGet, "/api/v1/tasks/not-a-uuid/messages", nil},
		{
			http.MethodPost,
			"/api/v1/conversations/not-a-uuid/messages",
			[]byte(`{"requestKey":"` + uuid.Must(uuid.NewV7()).String() + `","content":"x"}`),
		},
		{
			http.MethodDelete,
			"/api/v1/conversations/not-a-uuid/run-queue/" + uuid.Must(uuid.NewV7()).String(),
			[]byte(`{"expectedRevision":1}`),
		},
	} {
		var response *httptest.ResponseRecorder
		if route.body == nil {
			response = serveBearerRequest(handler, route.method, route.path, nil)
		} else {
			response = serveBearerJSON(handler, route.method, route.path, route.body)
		}
		if response.Code != http.StatusNotFound ||
			!bytes.Contains(response.Body.Bytes(), []byte(`"code":"conversation_not_found"`)) {
			t.Fatalf("%s %s = %d %s, want 404 conversation_not_found",
				route.method, route.path, response.Code, response.Body.String())
		}
	}
}

type conversationServiceStub struct {
	fetchModelSettings  func(context.Context, auth.UserPrincipal, uuid.UUID) (conversation.ModelSettings, error)
	updateModelSettings func(context.Context, auth.UserPrincipal, uuid.UUID, conversation.ModelSettingsInput) (conversation.ModelSettings, error)
	createDirect        func(context.Context, auth.UserPrincipal, conversation.CreateDirectInput) (conversation.CreateDirectResult, error)
	listDirect          func(context.Context, auth.UserPrincipal, bool) (conversation.DirectPage, error)
	getDirectDetail     func(context.Context, auth.UserPrincipal, uuid.UUID, conversation.MessageWindow) (conversation.DirectDetail, error)
	listMessages        func(context.Context, auth.UserPrincipal, conversation.MessageTarget, conversation.MessageListOptions) (conversation.MessagePage, error)
	transcript          func(context.Context, auth.UserPrincipal, uuid.UUID, conversation.TranscriptListOptions) (conversation.TranscriptPage, error)
	send                func(context.Context, auth.UserPrincipal, conversation.MessageTarget, conversation.SendInput) (conversation.SendResult, error)
	createWhiteboard    func(context.Context, auth.UserPrincipal, conversation.MessageTarget, string) (conversation.CreatedWhiteboard, error)
	getRunQueue         func(context.Context, auth.UserPrincipal, uuid.UUID) (conversation.RunQueueProjection, error)
	reorderQueue        func(context.Context, auth.UserPrincipal, uuid.UUID, conversation.RunQueueOrderInput) (conversation.RunQueueProjection, error)
	deleteQueueItem     func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, conversation.DeleteRunQueueItemInput) (conversation.RunQueueProjection, error)
	getState            func(context.Context, auth.UserPrincipal, uuid.UUID) (conversation.UserState, error)
	updateState         func(context.Context, auth.UserPrincipal, uuid.UUID, conversation.UpdateUserStateInput) (conversation.UserState, error)
	renameDirect        func(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		conversation.RenameDirectInput,
	) (conversation.Response, error)
}

func (s *conversationServiceStub) Transcript(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	options conversation.TranscriptListOptions,
) (conversation.TranscriptPage, error) {
	if s.transcript == nil {
		return conversation.TranscriptPage{}, errors.New("unexpected Transcript")
	}
	return s.transcript(ctx, principal, conversationID, options)
}

func (s *conversationServiceStub) CreateDirect(
	ctx context.Context,
	principal auth.UserPrincipal,
	input conversation.CreateDirectInput,
) (conversation.CreateDirectResult, error) {
	if s.createDirect == nil {
		return conversation.CreateDirectResult{}, errors.New("unexpected CreateDirect")
	}
	return s.createDirect(ctx, principal, input)
}

func (s *conversationServiceStub) ListDirect(
	ctx context.Context,
	principal auth.UserPrincipal,
	includeArchived bool,
) (conversation.DirectPage, error) {
	if s.listDirect == nil {
		return conversation.DirectPage{}, errors.New("unexpected ListDirect")
	}
	return s.listDirect(ctx, principal, includeArchived)
}

func (s *conversationServiceStub) RenameDirect(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	input conversation.RenameDirectInput,
) (conversation.Response, error) {
	if s.renameDirect == nil {
		return conversation.Response{}, errors.New("unexpected RenameDirect")
	}
	return s.renameDirect(ctx, principal, conversationID, input)
}

func (s *conversationServiceStub) FetchDirectDetail(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
	window conversation.MessageWindow,
) (conversation.DirectDetail, error) {
	if s.getDirectDetail == nil {
		return conversation.DirectDetail{}, errors.New("unexpected FetchDirectDetail")
	}
	return s.getDirectDetail(ctx, principal, id, window)
}

func (s *conversationServiceStub) ListMessages(
	ctx context.Context,
	principal auth.UserPrincipal,
	target conversation.MessageTarget,
	options conversation.MessageListOptions,
) (conversation.MessagePage, error) {
	if s.listMessages == nil {
		return conversation.MessagePage{}, errors.New("unexpected ListMessages")
	}
	return s.listMessages(ctx, principal, target, options)
}

func (s *conversationServiceStub) Send(
	ctx context.Context,
	principal auth.UserPrincipal,
	target conversation.MessageTarget,
	input conversation.SendInput,
) (conversation.SendResult, error) {
	if s.send == nil {
		return conversation.SendResult{}, errors.New("unexpected Send")
	}
	return s.send(ctx, principal, target, input)
}

func (s *conversationServiceStub) CreateWhiteboard(
	ctx context.Context,
	principal auth.UserPrincipal,
	target conversation.MessageTarget,
	title string,
) (conversation.CreatedWhiteboard, error) {
	if s.createWhiteboard == nil {
		return conversation.CreatedWhiteboard{}, errors.New("unexpected CreateWhiteboard")
	}
	return s.createWhiteboard(ctx, principal, target, title)
}

func (s *conversationServiceStub) FetchRunQueue(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
) (conversation.RunQueueProjection, error) {
	if s.getRunQueue == nil {
		return conversation.RunQueueProjection{}, errors.New("unexpected FetchRunQueue")
	}
	return s.getRunQueue(ctx, principal, id)
}

func (s *conversationServiceStub) ReorderRunQueue(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
	input conversation.RunQueueOrderInput,
) (conversation.RunQueueProjection, error) {
	if s.reorderQueue == nil {
		return conversation.RunQueueProjection{}, errors.New("unexpected ReorderRunQueue")
	}
	return s.reorderQueue(ctx, principal, id, input)
}

func (s *conversationServiceStub) DeleteRunQueueItem(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	queueItemID uuid.UUID,
	input conversation.DeleteRunQueueItemInput,
) (conversation.RunQueueProjection, error) {
	if s.deleteQueueItem == nil {
		return conversation.RunQueueProjection{}, errors.New("unexpected DeleteRunQueueItem")
	}
	return s.deleteQueueItem(ctx, principal, conversationID, queueItemID, input)
}

func (s *conversationServiceStub) UpdateState(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
	input conversation.UpdateUserStateInput,
) (conversation.UserState, error) {
	if s.updateState == nil {
		return conversation.UserState{}, errors.New("unexpected UpdateState")
	}
	return s.updateState(ctx, principal, id, input)
}

func (s *conversationServiceStub) FetchState(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
) (conversation.UserState, error) {
	if s.getState == nil {
		return conversation.UserState{}, errors.New("unexpected FetchState")
	}
	return s.getState(ctx, principal, id)
}

func phase4ConversationHandler(
	t *testing.T,
	principal auth.UserPrincipal,
	service ConversationService,
) http.Handler {
	t.Helper()
	companyService := &companyServiceStub{
		authorize: func(_ context.Context, user auth.UserPrincipal, companyID uuid.UUID) (auth.UserPrincipal, error) {
			if user.UserID != principal.UserID || companyID != principal.CompanyID {
				return auth.UserPrincipal{}, company.ErrNotFound
			}
			return principal, nil
		},
	}
	return New(
		Handler{
			AuthService:         authenticatedAuthStub(principal.UserID),
			CompanyService:      companyService,
			ConversationService: service,
		},
	)
}

func phase4DirectConversation(id, companyID, agentID, creatorID uuid.UUID) conversation.Response {
	now := time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC)
	title := "Release review"
	return conversation.Response{
		ID:              id,
		CompanyID:       companyID,
		Kind:            conversation.KindDirect,
		AgentID:         &agentID,
		CreatedByUserID: &creatorID,
		Title:           &title,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func phase4ConversationListItem(value conversation.Response, userID uuid.UUID) conversation.ListItem {
	return conversation.ListItem{
		Conversation: value,
		Summary: conversation.ListSummary{
			UpdatedAt: value.UpdatedAt,
			Runs:      conversation.RunsSummary{},
		},
		UserState: conversation.UserState{
			ConversationID: value.ID, UserID: userID, UpdatedAt: value.UpdatedAt, Revision: 1,
		},
	}
}

func phase4MessageView(messageID, conversationID, userID uuid.UUID) conversation.MessageView {
	return conversation.MessageView{
		Message: conversation.MessageResponse{
			ID:             messageID,
			ConversationID: conversationID,
			Seq:            1,
			Type:           conversation.ItemTypeUserMessage,
			RunID:          uuid.MustParse("00000000-0000-4000-8000-000000004711"),
			Author: conversation.ActorRef{
				Kind:        conversation.ActorUser,
				ID:          userID,
				DisplayName: "Dana",
			},
			Content:   "Inspect this",
			CreatedAt: time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC),
		},
		Attachments: []attachment.Response{},
	}
}

func (s *conversationServiceStub) ListPins(context.Context, auth.UserPrincipal) ([]conversation.UserState, error) {
	return []conversation.UserState{}, nil
}

func (s *conversationServiceStub) ListStates(context.Context, auth.UserPrincipal) ([]conversation.UserState, error) {
	return []conversation.UserState{}, nil
}

func (s *conversationServiceStub) FetchModelSettings(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
) (conversation.ModelSettings, error) {
	if s.fetchModelSettings == nil {
		return conversation.ModelSettings{}, errors.New("unexpected FetchModelSettings")
	}
	return s.fetchModelSettings(ctx, p, id)
}

func (s *conversationServiceStub) UpdateModelSettings(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	in conversation.ModelSettingsInput,
) (conversation.ModelSettings, error) {
	if s.updateModelSettings == nil {
		return conversation.ModelSettings{}, errors.New("unexpected UpdateModelSettings")
	}
	return s.updateModelSettings(ctx, p, id, in)
}
