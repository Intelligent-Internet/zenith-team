package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
)

type transcriptHTTPStub struct{ conversation.HuddleAIService }

func (s *transcriptHTTPStub) AppendTranscript(
	_ context.Context,
	_ uuid.UUID,
	_ string,
	_ conversation.HuddleTranscriptBatch,
) (conversation.HuddleTranscriptReceipt, error) {
	return conversation.HuddleTranscriptReceipt{}, nil
}

func TestHuddleTranscriptBoundary(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, body string
		want       int
	}{
		{"silence", `{"sequence":1,"entries":[]}`, http.StatusOK},
		{"speech", `{"sequence":1,"entries":[{"speaker":"participants","text":"hello"}]}`, http.StatusOK},
		{"speaker", `{"sequence":1,"entries":[{"speaker":"someone","text":"hello"}]}`, http.StatusUnprocessableEntity},
		{"sequence", `{"sequence":0,"entries":[]}`, http.StatusUnprocessableEntity},
		{"scope", `{"sequence":1,"companyId":"other"}`, http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := New(Handler{HuddleAIService: &transcriptHTTPStub{}})
			response := serveBearerJSON(
				h,
				http.MethodPost,
				"/api/v1/internal/huddle-ai/"+uuid.NewString()+"/transcript",
				[]byte(tt.body),
			)
			if response.Code != tt.want {
				t.Fatalf("status = %d %s, want %d", response.Code, response.Body, tt.want)
			}
		})
	}
}
