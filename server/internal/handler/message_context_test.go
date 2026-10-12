package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
)

func TestMessageContextRouteValidatesIdentityAndLimit(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	conversationID, messageID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	createdAt := time.Date(2026, time.August, 5, 1, 2, 3, 0, time.UTC)
	calls := 0
	service := messageContextServiceStub(func(
		_ context.Context,
		principal auth.UserPrincipal,
		gotConversationID uuid.UUID,
		gotMessageID uuid.UUID,
		limit int,
	) (conversation.MessageContextPage, error) {
		calls++
		if principal.UserID != userID || gotConversationID != conversationID || gotMessageID != messageID ||
			limit != 25 {
			t.Fatalf("context input = %+v, %s, %s, %d", principal, gotConversationID, gotMessageID, limit)
		}
		return conversation.MessageContextPage{
			Items: []conversation.MessageView{{
				Message: conversation.MessageResponse{
					ID: messageID, ConversationID: conversationID, Seq: 42,
					Type: conversation.ItemTypeUserMessage,
					Author: conversation.ActorRef{
						Kind:        conversation.ActorUser,
						ID:          userID,
						DisplayName: "Dana",
					},
					RunID:   uuid.MustParse("00000000-0000-4000-8000-000000004711"),
					Content: "Find this", CreatedAt: createdAt,
				},
			}},
			TargetSeq:            42,
			EarliestAvailableSeq: 1,
			ThroughSeq:           80,
		}, nil
	})
	handler := New(Handler{
		AuthService:           authenticatedAuthStub(userID),
		ConversationService:   &conversationServiceStub{},
		MessageContextService: service,
	})
	path := "/api/v1/conversations/" + conversationID.String() + "/messages/" + messageID.String() + "/context"
	response := serveBearerRequest(handler, http.MethodGet, path+"?limit=25", nil)
	want := `{"items":[{"id":"` + messageID.String() + `","conversationId":"` + conversationID.String() +
		`","seq":42,"type":"user_message","author":{"id":"` + userID.String() +
		`","kind":"user","name":"Dana","avatarUrl":null},"runId":"00000000-0000-4000-8000-000000004711","content":"Find this","createdAt":"2026-08-05T01:02:03Z","attachments":[],"attachmentsWithheld":false}],` +
		`"targetSeq":42,"earliestAvailableSeq":1,"throughSeq":80}`
	if response.Code != http.StatusOK || response.Body.String() != want {
		t.Fatalf("context response = %d %s", response.Code, response.Body.String())
	}
	for _, invalidPath := range []string{
		"/api/v1/conversations/not-a-uuid/messages/" + messageID.String() + "/context",
		"/api/v1/conversations/" + conversationID.String() + "/messages/not-a-uuid/context",
	} {
		invalid := serveBearerRequest(handler, http.MethodGet, invalidPath, nil)
		assertErrorResponse(t, invalid, http.StatusNotFound, "conversation_not_found")
	}
	for _, query := range []string{"?limit=0", "?limit=101", "?limit=25&extra=1"} {
		invalid := serveBearerRequest(handler, http.MethodGet, path+query, nil)
		assertErrorResponse(t, invalid, http.StatusUnprocessableEntity, "validation_failed")
	}
	if calls != 1 {
		t.Fatalf("FetchMessageContext calls = %d, want 1", calls)
	}
}

// A message read whose service left a Project file out says so on the wire
// (ADR 0017): no file, and attachmentsWithheld true.
func TestMessageReadCarriesAttachmentsWithheld(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	conversationID, messageID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	service := messageContextServiceStub(func(
		context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int,
	) (conversation.MessageContextPage, error) {
		return conversation.MessageContextPage{
			Items: []conversation.MessageView{{
				Message: conversation.MessageResponse{
					ID: messageID, ConversationID: conversationID, Seq: 7,
					Type:   conversation.ItemTypeUserMessage,
					Author: conversation.ActorRef{Kind: conversation.ActorUser, ID: userID, DisplayName: "Dana"},
				},
				AttachmentsWithheld: true,
			}},
			TargetSeq: 7, EarliestAvailableSeq: 1, ThroughSeq: 7,
		}, nil
	})
	handler := New(Handler{
		AuthService:           authenticatedAuthStub(userID),
		ConversationService:   &conversationServiceStub{},
		MessageContextService: service,
	})
	path := "/api/v1/conversations/" + conversationID.String() + "/messages/" + messageID.String() + "/context"
	response := serveBearerRequest(handler, http.MethodGet, path, nil)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"attachments":[],"attachmentsWithheld":true}`) {
		t.Fatalf("context response = %d %s, want the withheld flag", response.Code, response.Body.String())
	}
}

type messageContextServiceStub func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int) (conversation.MessageContextPage, error)

func (stub messageContextServiceStub) FetchMessageContext(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	messageID uuid.UUID,
	limit int,
) (conversation.MessageContextPage, error) {
	return stub(ctx, principal, conversationID, messageID, limit)
}
