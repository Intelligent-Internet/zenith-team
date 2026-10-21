package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"

	"github.com/google/uuid"
)

type discussionEditHTTPStub struct {
	discussionHTTPStub
	edit func(conversation.DiscussionEditInput) (conversation.DiscussionMutation, error)
}

func (s discussionEditHTTPStub) EditMessage(
	_ context.Context,
	_ auth.UserPrincipal,
	_ conversation.DiscussionMessageTarget,
	input conversation.DiscussionEditInput,
) (conversation.DiscussionMutation, error) {
	return s.edit(input)
}

func TestDiscussionEditHTTPRevisionBoundary(t *testing.T) {
	t.Parallel()
	p := auth.UserPrincipal{UserID: uuid.New(), CompanyID: uuid.New(), Role: auth.CompanyRoleMember}
	called := 0
	h := discussionHTTPHandler(
		p,
		discussionEditHTTPStub{
			edit: func(input conversation.DiscussionEditInput) (conversation.DiscussionMutation, error) {
				called++
				if input.ExpectedRevision != 7 || input.Body.Markdown != "edited" {
					t.Fatalf("edit input = %+v", input)
				}
				return conversation.DiscussionMutation{}, nil
			},
		},
	)
	path := "/api/v1/companies/" + p.CompanyID.String() + "/conversations/" + uuid.NewString() + "/discussion-messages/" + uuid.NewString()
	for _, body := range []string{`{"body":{"version":1,"markdown":"edited"}}`, `{"expectedRevision":null,"body":{"version":1,"markdown":"edited"}}`, `{"expectedRevision":0,"body":{"version":1,"markdown":"edited"}}`, `{"expectedRevision":7,"body":{"version":1,"markdown":"edited"},"threadRootId":null}`} {
		response := serveBearerJSON(h, http.MethodPatch, path, []byte(body))
		if response.Code != 400 && response.Code != 422 {
			t.Fatalf("invalid edit = %d %s", response.Code, response.Body)
		}
	}
	response := serveBearerJSON(
		h,
		http.MethodPatch,
		path,
		[]byte(`{"expectedRevision":7,"body":{"version":1,"markdown":"edited"}}`),
	)
	if called != 1 || response.Code != 200 || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("edit = %d %s; calls=%d", response.Code, response.Body, called)
	}
	response = serveBearerJSON(h, http.MethodDelete, path, []byte(`{"expectedRevision":null}`))
	if response.Code != 400 || !strings.Contains(response.Body.String(), "invalid_request") {
		t.Fatalf("delete = %d %s", response.Code, response.Body)
	}
}
