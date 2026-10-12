package conversation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestDiscussionBodyValidationAndFingerprint(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, markdown string
		valid          bool
	}{
		{"Vietnamese", "Chào **bạn** 👋\n`code`", true},
		{"empty", " \n\t", false},
		{"CRLF", "a\r\nb", false},
		{"control", "a\x00b", false},
		{"HTML", "<script>alert(1)</script>", false},
		{"image", "![image](https://example.test/a.png)", false},
		{"unsafe link", "[open](javascript:alert(1))", false},
		{"https link", "[open](https://example.test)", true},
		{"escaped HTML in code", "`<script>`", true},
		{"encoded budget", strings.Repeat("&", 12000), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeDiscussionSend(
				DiscussionSendInput{
					RequestKey: uuid.New(),
					Body:       DiscussionBody{Version: 1, Markdown: tc.markdown},
				},
			)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
	var body DiscussionBody
	if err := json.Unmarshal([]byte(`{"version":1,"markdown":"\ud800","references":[]}`), &body); err == nil {
		t.Fatal("unpaired surrogate accepted")
	}
	attachmentOnly, err := normalizeDiscussionSend(DiscussionSendInput{
		RequestKey:    uuid.New(),
		Body:          DiscussionBody{Version: 1, Markdown: "", References: []json.RawMessage{}},
		AttachmentIDs: []uuid.UUID{uuid.New()},
	})
	if err != nil || attachmentOnly.Content != "" {
		t.Fatalf("attachment-only discussion send = %#v, %v", attachmentOnly, err)
	}
	mention, err := normalizeDiscussionSend(DiscussionSendInput{
		RequestKey: uuid.New(),
		Body: DiscussionBody{Version: 1, Markdown: "Hi @you", References: []json.RawMessage{
			json.RawMessage(`{"start":3,"end":7,"type":"user","userId":"11111111-1111-4111-8111-111111111111"}`),
		}},
	})
	if err != nil || len(mention.Input.Body.References) != 1 {
		t.Fatalf("typed discussion reference = %#v, %v", mention, err)
	}
	_, err = normalizeDiscussionSend(DiscussionSendInput{
		RequestKey: uuid.New(),
		Body: DiscussionBody{Version: 1, Markdown: "😀", References: []json.RawMessage{
			json.RawMessage(`{"start":1,"end":2,"type":"user","userId":"11111111-1111-4111-8111-111111111111"}`),
		}},
	})
	if err == nil {
		t.Fatal("reference split inside UTF-16 surrogate accepted")
	}
}

func TestDiscussionReferenceMarkdownContexts(t *testing.T) {
	for _, tc := range []struct {
		markdown string
		start    int
		want     bool
	}{
		{"@you", 0, true},
		{"**@you**", 2, true},
		{"`@you`", 1, false},
		{"> @you", 2, false},
		{"[@you](https://example.test)", 1, false},
		{"\\@you", 1, false},
		{"```\n@you\n```", 4, false},
	} {
		t.Run(tc.markdown, func(t *testing.T) {
			if got := discussionReferenceRangeAllowed(tc.markdown, tc.start, tc.start+4); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDiscussionFileOnlySendAndFingerprint(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	input := DiscussionSendInput{
		RequestKey:    uuid.New(),
		Body:          DiscussionBody{Version: 1},
		AttachmentIDs: []uuid.UUID{id},
	}
	first, err := normalizeDiscussionSend(input)
	if err != nil {
		t.Fatal(err)
	}
	input.AttachmentIDs = []uuid.UUID{uuid.New()}
	second, err := normalizeDiscussionSend(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Fingerprint) == string(second.Fingerprint) {
		t.Fatal("different attachments replay as the same message")
	}
	input.AttachmentIDs = []uuid.UUID{id, id}
	if _, err := normalizeDiscussionSend(input); err == nil {
		t.Fatal("duplicate attachments accepted")
	}
}
