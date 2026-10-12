package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
)

func TestRenameCompanyConversationReturnsFlatEntity(t *testing.T) {
	t.Parallel()
	for _, title := range []string{"Release review", strings.Repeat("界", 120)} {
		t.Run(title, func(t *testing.T) {
			t.Parallel()
			principal := phase4Principal(uuid.New(), uuid.New())
			conversationID := uuid.New()
			direct := phase4DirectConversation(
				conversationID,
				principal.CompanyID,
				uuid.New(),
				principal.UserID,
			)
			direct.Title = &title
			direct.Revision = 8
			service := &conversationServiceStub{
				renameDirect: func(
					_ context.Context,
					got auth.UserPrincipal,
					gotID uuid.UUID,
					input conversation.RenameDirectInput,
				) (conversation.Response, error) {
					if got != principal || gotID != conversationID {
						t.Fatalf("RenameDirect identity = %+v / %s", got, gotID)
					}
					if input.Title != title || input.ExpectedRevision != 7 {
						t.Fatalf("RenameDirect input = %+v", input)
					}
					return direct, nil
				},
			}
			requestTitle := title
			if title == "Release review" {
				requestTitle = "\ufeff\u0085 Release review \u3000"
			}
			body, err := json.Marshal(map[string]any{"title": requestTitle, "expectedRevision": 7})
			if err != nil {
				t.Fatalf("encode rename: %v", err)
			}
			response := serveBearerJSON(
				phase4ConversationHandler(t, principal, service),
				http.MethodPatch,
				"/api/v1/companies/"+principal.CompanyID.String()+"/conversations/"+conversationID.String(),
				body,
			)
			if response.Code != http.StatusOK {
				t.Fatalf("rename = %d %s, want 200", response.Code, response.Body.String())
			}
			var got directConversationResponse
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode rename: %v", err)
			}
			if got.ID != conversationID || got.CompanyID != principal.CompanyID {
				t.Fatalf("rename identity = %s / %s", got.ID, got.CompanyID)
			}
			if got.Title == nil || *got.Title != title {
				t.Fatalf("rename title = %v, want %q", got.Title, title)
			}
			if got.Revision != 8 {
				t.Fatalf("rename revision = %d, want 8", got.Revision)
			}
			if got.AgentID == nil || got.CreatedByUserID == nil {
				t.Fatalf("rename response = %+v", got)
			}
		})
	}
}

func TestRenameCompanyConversationRejectsInvalidRequests(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		body       string
		invalidID  bool
		wantStatus int
		wantCode   string
	}{
		{name: "missing title", body: `{"expectedRevision":7}`, wantStatus: 422, wantCode: "validation_failed"},
		{name: "null title", body: `{"title":null,"expectedRevision":7}`, wantStatus: 422, wantCode: "validation_failed"},
		{name: "empty title", body: `{"title":"","expectedRevision":7}`, wantStatus: 422, wantCode: "validation_failed"},
		{
			name: "blank title", body: `{"title":"\ufeff\u0085 \u3000","expectedRevision":7}`,
			wantStatus: 422, wantCode: "validation_failed",
		},
		{
			name: "long unicode title", body: `{"title":"` + strings.Repeat("界", 121) + `","expectedRevision":7}`,
			wantStatus: 422, wantCode: "validation_failed",
		},
		{name: "missing revision", body: `{"title":"Review"}`, wantStatus: 422, wantCode: "validation_failed"},
		{
			name: "zero revision", body: `{"title":"Review","expectedRevision":0}`,
			wantStatus: 422, wantCode: "validation_failed",
		},
		{
			name: "negative revision", body: `{"title":"Review","expectedRevision":-1}`,
			wantStatus: 422, wantCode: "validation_failed",
		},
		{
			name: "unsafe revision", body: `{"title":"Review","expectedRevision":9007199254740992}`,
			wantStatus: 422, wantCode: "validation_failed",
		},
		{
			name: "unknown field", body: `{"title":"Review","expectedRevision":7,"agentId":"anything"}`,
			wantStatus: 400, wantCode: "invalid_request",
		},
		{
			name: "invalid id", body: `{"title":"Review","expectedRevision":7}`, invalidID: true,
			wantStatus: 404, wantCode: "conversation_not_found",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			principal := phase4Principal(uuid.New(), uuid.New())
			conversationID := uuid.NewString()
			if test.invalidID {
				conversationID = "invalid"
			}
			called := false
			service := &conversationServiceStub{renameDirect: func(
				context.Context,
				auth.UserPrincipal,
				uuid.UUID,
				conversation.RenameDirectInput,
			) (conversation.Response, error) {
				called = true
				return conversation.Response{}, nil
			}}
			response := serveBearerJSON(
				phase4ConversationHandler(t, principal, service),
				http.MethodPatch,
				"/api/v1/companies/"+principal.CompanyID.String()+"/conversations/"+conversationID,
				[]byte(test.body),
			)
			if response.Code != test.wantStatus || called {
				t.Fatalf(
					"rename = %d %s, called %t; want %d without service call",
					response.Code,
					response.Body.String(),
					called,
					test.wantStatus,
				)
			}
			var got struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || got.Code != test.wantCode {
				t.Fatalf(
					"error code = %q, decode error = %v; want %q",
					got.Code,
					err,
					test.wantCode,
				)
			}
		})
	}
}

func TestRenameCompanyConversationMapsServiceErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "unreadable", err: conversation.ErrNotFound, wantStatus: 404, wantCode: "conversation_not_found"},
		{name: "stale revision", err: conversation.ErrConflict, wantStatus: 409, wantCode: "conversation_conflict"},
		{name: "invalid input", err: conversation.ErrInvalidInput, wantStatus: 422, wantCode: "validation_failed"},
		{name: "unexpected", err: errors.New("database unavailable"), wantStatus: 500, wantCode: "internal_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			principal := phase4Principal(uuid.New(), uuid.New())
			service := &conversationServiceStub{renameDirect: func(
				context.Context,
				auth.UserPrincipal,
				uuid.UUID,
				conversation.RenameDirectInput,
			) (conversation.Response, error) {
				return conversation.Response{}, test.err
			}}
			response := serveBearerJSON(
				phase4ConversationHandler(t, principal, service),
				http.MethodPatch,
				"/api/v1/companies/"+principal.CompanyID.String()+"/conversations/"+uuid.NewString(),
				[]byte(`{"title":"Review","expectedRevision":7}`),
			)
			var got struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if response.Code != test.wantStatus || got.Code != test.wantCode {
				t.Fatalf(
					"rename = %d %s, want %d %q",
					response.Code,
					response.Body.String(),
					test.wantStatus,
					test.wantCode,
				)
			}
		})
	}
}

// The list row is flat: the conversation fields and the caller's pinnedAt sit
// at the top level, with the shared activity `summary` — the same object the
// conversation.created and conversation.updated events carry — nested beside
// them. The domain's conversation/userState nesting does not appear on the
// wire, and no flattened run counters ride the row.
func TestListCompanyConversationsFlattensRows(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	principal := phase4Principal(userID, companyID)
	direct := phase4DirectConversation(conversationID, companyID, agentID, userID)

	item := phase4ConversationListItem(direct, userID)
	pinnedAt := time.Date(2026, time.August, 18, 9, 0, 0, 0, time.UTC)
	item.Agent = &conversation.AgentRef{
		ID: agentID, Name: "Ada", Title: "Engineer", RuntimeAvailable: true,
	}
	item.UserState.PinnedAt = &pinnedAt
	item.Summary.Runs.QueuedCount = 2
	item.Summary.Runs.ActiveCount = 1
	item.Summary.Runs.Latest = &conversation.LatestRunSummary{
		RunID: uuid.MustParse("00000000-0000-4000-8000-000000004711"), AgentID: agentID, Status: conversation.RunStatusRunning,
	}
	item.Summary.LastMessage = &conversation.LastMessageSummary{
		Seq: 41, Content: "ship it", Type: conversation.ItemTypeUserMessage,
		AuthorKind: conversation.ActorUser, CreatedAt: direct.UpdatedAt,
	}

	service := &conversationServiceStub{
		listDirect: func(_ context.Context, got auth.UserPrincipal, _ bool) (conversation.DirectPage, error) {
			if got != principal {
				t.Fatalf("ListDirect principal = %+v", got)
			}
			return conversation.DirectPage{Conversations: []conversation.ListItem{item}}, nil
		},
	}
	handler := phase4ConversationHandler(t, principal, service)

	response := serveBearerRequest(handler, http.MethodGet,
		"/api/v1/companies/"+companyID.String()+"/conversations", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list conversations = %d %s", response.Code, response.Body.String())
	}

	var body struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(body.Items))
	}
	row := body.Items[0]
	for _, key := range []string{
		"id", "companyId", "kind", "taskId", "agent", "title",
		"pinnedAt", "archivedAt", "summary", "createdAt", "updatedAt", "revision",
	} {
		if _, ok := row[key]; !ok {
			t.Fatalf("row missing %q: %s", key, response.Body.String())
		}
	}
	for _, key := range []string{
		"conversation", "userState", "createdByUserId",
		"activeRunCount", "queuedRunCount", "latestRunStatus", "lastMessage",
	} {
		if _, ok := row[key]; ok {
			t.Fatalf("row still carries %q: %s", key, response.Body.String())
		}
	}

	var agent conversation.AgentRef
	if err := json.Unmarshal(row["agent"], &agent); err != nil {
		t.Fatalf("decode agent: %v", err)
	}
	if agent.ID != agentID || agent.Name != "Ada" || agent.Title != "Engineer" || !agent.RuntimeAvailable {
		t.Fatalf("agent = %+v", agent)
	}

	// The row's summary is the exact ListSummary the events carry: counted run
	// state plus the preview line, one wire type for row and frame.
	var summary conversation.ListSummary
	if err := json.Unmarshal(row["summary"], &summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if summary.Runs.QueuedCount != 2 || summary.Runs.ActiveCount != 1 ||
		summary.Runs.Latest == nil || summary.Runs.Latest.Status != conversation.RunStatusRunning {
		t.Fatalf("summary.runs = %+v", summary.Runs)
	}
	if summary.LastMessage == nil || summary.LastMessage.Seq != 41 ||
		summary.LastMessage.Content != "ship it" ||
		summary.LastMessage.AuthorKind != conversation.ActorUser {
		t.Fatalf("summary.lastMessage = %+v", summary.LastMessage)
	}
}

func TestListCompanyConversationsValidatesArchiveFilter(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		query        string
		wantStatus   int
		wantArchived bool
	}{
		{query: "", wantStatus: http.StatusOK},
		{query: "?include_archived=false", wantStatus: http.StatusOK},
		{query: "?include_archived=true", wantStatus: http.StatusOK, wantArchived: true},
		{query: "?include_archived=1", wantStatus: http.StatusUnprocessableEntity},
		{query: "?include_archived=", wantStatus: http.StatusUnprocessableEntity},
		{query: "?include_archived=true&include_archived=false", wantStatus: http.StatusUnprocessableEntity},
		{query: "?unknown=true", wantStatus: http.StatusUnprocessableEntity},
	} {
		t.Run(testCase.query, func(t *testing.T) {
			t.Parallel()
			principal := phase4Principal(uuid.New(), uuid.New())
			called := false
			service := &conversationServiceStub{listDirect: func(_ context.Context, _ auth.UserPrincipal, includeArchived bool) (conversation.DirectPage, error) {
				called = true
				if includeArchived != testCase.wantArchived {
					t.Fatalf("includeArchived = %t, want %t", includeArchived, testCase.wantArchived)
				}
				return conversation.DirectPage{}, nil
			}}
			handler := phase4ConversationHandler(t, principal, service)
			response := serveBearerRequest(handler, http.MethodGet, "/api/v1/companies/"+principal.CompanyID.String()+"/conversations"+testCase.query, nil)
			if response.Code != testCase.wantStatus || called != (testCase.wantStatus == http.StatusOK) {
				t.Fatalf("response = %d, called %t, want status %d", response.Code, called, testCase.wantStatus)
			}
		})
	}
}

// A conversation with no activity serves `"lastMessage": null` explicitly —
// never an absent key — so an event frame and a row stay one shape.
func TestListCompanyConversationsServesExplicitNullLastMessage(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	principal := phase4Principal(userID, companyID)
	direct := phase4DirectConversation(conversationID, companyID, agentID, userID)
	item := phase4ConversationListItem(direct, userID)

	service := &conversationServiceStub{
		listDirect: func(_ context.Context, _ auth.UserPrincipal, _ bool) (conversation.DirectPage, error) {
			return conversation.DirectPage{Conversations: []conversation.ListItem{item}}, nil
		},
	}
	handler := phase4ConversationHandler(t, principal, service)

	response := serveBearerRequest(handler, http.MethodGet,
		"/api/v1/companies/"+companyID.String()+"/conversations", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list conversations = %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body.Items) != 1 {
		t.Fatalf("decode list: %v %s", err, response.Body.String())
	}
	var summary map[string]json.RawMessage
	if err := json.Unmarshal(body.Items[0]["summary"], &summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if raw, ok := summary["lastMessage"]; !ok || string(raw) != "null" {
		t.Fatalf("summary.lastMessage = %s (present=%v)", raw, ok)
	}
}

// Create returns the flat conversation entity, and a fresh open is 201 while a
// replayed one is 200.
func TestCreateCompanyConversationReturnsFlatEntity(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	principal := phase4Principal(userID, companyID)
	direct := phase4DirectConversation(conversationID, companyID, agentID, userID)

	for name, test := range map[string]struct {
		created    bool
		wantStatus int
	}{
		"fresh open":    {created: true, wantStatus: http.StatusCreated},
		"existing pair": {created: false, wantStatus: http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			service := &conversationServiceStub{
				createDirect: func(_ context.Context, got auth.UserPrincipal, input conversation.CreateDirectInput) (conversation.CreateDirectResult, error) {
					if got != principal || input.AgentID != agentID || input.Title == nil ||
						*input.Title != "Release review" {
						t.Fatalf("CreateDirect input = %+v / %+v", got, input)
					}
					return conversation.CreateDirectResult{
						Conversation: direct,
						Created:      test.created,
					}, nil
				},
			}
			handler := phase4ConversationHandler(t, principal, service)

			response := serveBearerJSON(
				handler,
				http.MethodPost,
				"/api/v1/companies/"+companyID.String()+"/conversations",
				[]byte(
					`{"agentId":"`+agentID.String()+`","title":"Release review"}`,
				),
			)
			if response.Code != test.wantStatus {
				t.Fatalf(
					"create = %d %s, want %d",
					response.Code,
					response.Body.String(),
					test.wantStatus,
				)
			}

			var row map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &row); err != nil {
				t.Fatalf("decode create: %v", err)
			}
			// A direct chat carries taskId as an explicit null, and its agentId
			// and createdByUserId — the fields that make it a direct chat.
			if raw, ok := row["taskId"]; !ok || string(raw) != "null" {
				t.Fatalf("taskId = %s (present=%v)", row["taskId"], ok)
			}
			if _, ok := row["agentId"]; !ok {
				t.Fatalf("create response missing agentId: %s", response.Body.String())
			}
			if _, ok := row["createdByUserId"]; !ok {
				t.Fatalf("create response missing createdByUserId: %s", response.Body.String())
			}
		})
	}
}

// The detail read parses the message window off the query, and returns the
// conversation flat with its messages, bounds, and the co-resident panel state.
func TestGetCompanyConversationDetailAssemblesAndParsesWindow(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	messageID := uuid.Must(uuid.NewV7())
	principal := phase4Principal(userID, companyID)
	direct := phase4DirectConversation(conversationID, companyID, agentID, userID)

	detail := conversation.DirectDetail{
		Conversation: direct,
		Agent: &conversation.AgentRef{
			ID: agentID, Name: "Ada", Title: "Engineer", RuntimeAvailable: false,
		},
		Messages: []conversation.MessageView{{
			Message: conversation.MessageResponse{
				ID: messageID, ConversationID: conversationID, Seq: 40,
				Type:    conversation.ItemTypeUserMessage,
				Author:  conversation.ActorRef{Kind: conversation.ActorUser, ID: userID},
				RunID:   uuid.MustParse("00000000-0000-4000-8000-000000004711"),
				Content: "hello", CreatedAt: direct.UpdatedAt,
			},
			Attachments: []attachment.Response{},
		}},
		EarliestAvailableSeq: 1,
		ThroughSeq:           41,
		ResetRequired:        false,
		UserState: conversation.UserState{
			ConversationID: conversationID, UserID: userID, UpdatedAt: direct.UpdatedAt, Revision: 3,
		},
		Runs:     conversation.RunsSummary{},
		RunQueue: conversation.RunQueueProjection{Items: []conversation.RunQueueItem{}, Limit: 50},
	}

	var gotWindow conversation.MessageWindow
	service := &conversationServiceStub{
		getDirectDetail: func(_ context.Context, got auth.UserPrincipal, gotID uuid.UUID, window conversation.MessageWindow) (conversation.DirectDetail, error) {
			if got != principal || gotID != conversationID {
				t.Fatalf("FetchDirectDetail input = %+v / %s", got, gotID)
			}
			gotWindow = window
			return detail, nil
		},
	}
	handler := phase4ConversationHandler(t, principal, service)

	response := serveBearerRequest(handler, http.MethodGet,
		"/api/v1/companies/"+companyID.String()+"/conversations/"+conversationID.String()+
			"?limit=10&after_seq=5&through_seq=40", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("detail = %d %s", response.Code, response.Body.String())
	}
	if gotWindow.Limit != 10 || gotWindow.AfterSeq == nil || *gotWindow.AfterSeq != 5 ||
		gotWindow.ThroughSeq == nil || *gotWindow.ThroughSeq != 40 {
		t.Fatalf("window = %+v", gotWindow)
	}

	var row map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &row); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	for _, key := range []string{
		"id", "companyId", "kind", "taskId", "agent", "messages",
		"earliestAvailableSeq", "throughSeq",
		"resetRequired", "userState", "runs", "runQueue",
	} {
		if _, ok := row[key]; !ok {
			t.Fatalf("detail missing %q: %s", key, response.Body.String())
		}
	}
	if _, ok := row["conversation"]; ok {
		t.Fatalf("detail still nests conversation: %s", response.Body.String())
	}
	// Missing older/newer rows are derived from the window's seq range against
	// the bounds; the detail carries no flags.
	for _, key := range []string{"hasOlder", "hasNewer"} {
		if _, ok := row[key]; ok {
			t.Fatalf("detail still carries %q: %s", key, response.Body.String())
		}
	}

	// Each message is flat — the message fields at the top, attachments beside
	// them — not wrapped under a `message` key.
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(row["messages"], &messages); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	for _, key := range []string{
		"id", "conversationId", "seq", "type", "author", "runId", "content", "attachments",
	} {
		if _, ok := messages[0][key]; !ok {
			t.Fatalf("message missing %q: %s", key, row["messages"])
		}
	}
	if _, ok := messages[0]["message"]; ok {
		t.Fatalf("message still nests under `message`: %s", row["messages"])
	}
	if string(row["earliestAvailableSeq"]) != "1" || string(row["throughSeq"]) != "41" {
		t.Fatalf("bounds = earliest %s / through %s",
			row["earliestAvailableSeq"], row["throughSeq"])
	}
}
