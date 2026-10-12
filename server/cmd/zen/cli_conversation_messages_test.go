package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An agent whose frozen history was cut reads older messages by scrolling
// back: --through-seq asks the server for the newest rows at or below that
// seq, which is the window the API already serves as through_seq.
func TestConversationMessagesScrollsBackWithThroughSeq(t *testing.T) {
	const conversationID = "0198aa00-0000-7000-8000-00000000e001"
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/conversations/"+conversationID+"/messages") {
			http.NotFound(w, r)
			return
		}
		queries = append(queries, r.URL.RawQuery)
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{
				"id":             "0198aa00-0000-7000-8000-00000000e201",
				"conversationId": conversationID,
				"runId":          "0198aa00-0000-7000-8000-00000000f001",
				"seq":            8,
				"type":           "user_message",
				"author": map[string]any{
					"id": "0198aa00-0000-7000-8000-00000000a001", "kind": "user",
					"name": "Alice Nguyen", "avatarUrl": nil,
				},
				"content":   "Can we ship the customer import by Friday?",
				"createdAt": "2026-09-18T09:10:00Z",
			}},
			"hasMore": false,
		})
	}))
	defer srv.Close()
	seedSession(t, srv.URL)
	t.Setenv("BOARDLY_RUN_ID", "")
	t.Setenv("BOARDLY_RUN_TOKEN", "")

	out, err := executeCLI(t, newRootCmd, "conversation", "messages", conversationID, "--through-seq", "9", "--limit", "100")
	if err != nil {
		t.Fatalf("conversation messages --through-seq: %v", err)
	}
	if !strings.Contains(out, "Alice Nguyen") {
		t.Fatalf("messages table %q does not name the author", out)
	}
	if _, err := executeCLI(t, newRootCmd, "conversation", "messages", conversationID, "--after-seq", "3"); err != nil {
		t.Fatalf("conversation messages --after-seq: %v", err)
	}
	if len(queries) != 2 || queries[0] != "through_seq=9&limit=100" || queries[1] != "after_seq=3&limit=50" {
		t.Fatalf("queries = %q, want the scroll-back window then the forward page", queries)
	}
}
