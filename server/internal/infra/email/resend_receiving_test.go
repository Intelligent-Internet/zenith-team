package email

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestResendProviderRetrievesReceivedEmail(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/emails/receiving/provider-email-1" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer mailbox-key" {
			t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"id":"provider-email-1",
			"from":"Customer <customer@example.com>",
			"to":["sales@mail.example.com"],
			"cc":[],"bcc":[],"reply_to":["reply@example.com"],
			"subject":"A question",
			"html":"<p>Hello</p><script>alert(1)</script>",
			"text":"Hello",
			"created_at":"2026-08-18T02:03:04Z",
			"attachments":[{"id":"attachment-1","filename":"brief.pdf","content_type":"application/pdf","content_disposition":"attachment","content_id":"","size":42}]
		}`))
	}))
	defer server.Close()

	provider := newReceivingTestProvider(t, server.URL)
	message, err := provider.RetrieveReceivedEmail(context.Background(), "provider-email-1")
	if err != nil {
		t.Fatalf("RetrieveReceivedEmail() error = %v", err)
	}
	if message.ID != "provider-email-1" || message.Subject != "A question" || message.HTML == "" {
		t.Fatalf("message = %#v", message)
	}
	if len(message.Attachments) != 1 || message.Attachments[0].ID != "attachment-1" {
		t.Fatalf("attachments = %#v", message.Attachments)
	}
}

func TestResendProviderMapsReceivingFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    int
		header    string
		reset     string
		want      error
		wantRetry time.Duration
	}{
		{name: "not found", status: http.StatusNotFound, want: ErrReceivedEmailNotFound},
		{
			name:      "rate limited seconds",
			status:    http.StatusTooManyRequests,
			header:    "7",
			want:      ErrRateLimited,
			wantRetry: 7 * time.Second,
		},
		{
			name:      "rate limited date",
			status:    http.StatusTooManyRequests,
			header:    "Tue, 18 Aug 2026 02:03:11 GMT",
			want:      ErrRateLimited,
			wantRetry: 7 * time.Second,
		},
		{
			name:      "rate limited reset",
			status:    http.StatusTooManyRequests,
			reset:     "9",
			want:      ErrRateLimited,
			wantRetry: 9 * time.Second,
		},
		{name: "provider unavailable", status: http.StatusServiceUnavailable, want: ErrUnavailable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(
				http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					if test.header != "" {
						writer.Header().Set("Retry-After", test.header)
					}
					if test.reset != "" {
						writer.Header().Set("RateLimit-Reset", test.reset)
					}
					http.Error(
						writer,
						`{"message":"provider detail must stay opaque"}`,
						test.status,
					)
				}),
			)
			defer server.Close()
			provider := newReceivingTestProvider(t, server.URL)
			provider.now = func() time.Time {
				return time.Date(2026, 8, 18, 2, 3, 4, 0, time.UTC)
			}

			_, err := provider.RetrieveReceivedEmail(context.Background(), "provider-email-1")
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if test.wantRetry > 0 {
				var rateLimitErr *RateLimitError
				if !errors.As(err, &rateLimitErr) || rateLimitErr.RetryAfter != test.wantRetry {
					t.Fatalf("rate limit error = %#v, want retry %s", rateLimitErr, test.wantRetry)
				}
			}
		})
	}
}

func TestResendProviderRetrievesAttachmentURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/emails/receiving/provider-email-1/attachments/attachment-1" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"id":"attachment-1","filename":"brief.pdf","content_type":"application/pdf",
			"content_disposition":"attachment","content_id":"",
			"download_url":"https://downloads.example.test/signed","expires_at":"2026-08-18T03:03:04Z"
		}`))
	}))
	defer server.Close()

	provider := newReceivingTestProvider(t, server.URL)
	download, err := provider.RetrieveReceivedAttachment(
		context.Background(), "provider-email-1", "attachment-1",
	)
	if err != nil {
		t.Fatalf("RetrieveReceivedAttachment() error = %v", err)
	}
	if download.URL != "https://downloads.example.test/signed" || download.ExpiresAt.IsZero() {
		t.Fatalf("download = %#v", download)
	}
}

func TestResendWebhookVerifierAuthenticatesRawBody(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 18, 2, 3, 4, 0, time.UTC)
	secretBytes := []byte("mailbox webhook test secret")
	verifier, err := NewResendWebhookVerifier(
		"whsec_"+base64.StdEncoding.EncodeToString(secretBytes),
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("NewResendWebhookVerifier() error = %v", err)
	}
	body := []byte(`{"type":"email.received","data":{"email_id":"provider-email-1"}}`)
	headers := WebhookHeaders{
		ID:        "event-1",
		Timestamp: strconv.FormatInt(now.Unix(), 10),
	}
	headers.Signature = signWebhook(secretBytes, headers, body)

	if err := verifier.Verify(body, headers); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}

	invalidHeaders := headers
	invalidHeaders.Signature = "v1,invalid"
	if err := verifier.Verify(body, invalidHeaders); !errors.Is(err, ErrInvalidWebhook) {
		t.Fatalf("invalid signature error = %v", err)
	}

	staleHeaders := headers
	staleHeaders.Timestamp = strconv.FormatInt(now.Add(-6*time.Minute).Unix(), 10)
	staleHeaders.Signature = signWebhook(secretBytes, staleHeaders, body)
	if err := verifier.Verify(body, staleHeaders); !errors.Is(err, ErrInvalidWebhook) {
		t.Fatalf("stale signature error = %v", err)
	}
}

func TestDecodeResendReceivedEventUsesWebhookMetadataOnly(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"type":"email.received",
		"created_at":"2026-08-18T02:03:05Z",
		"data":{
			"email_id":"provider-email-1",
			"created_at":"2026-08-18T02:03:04Z",
			"from":"Customer <customer@example.com>",
			"to":["sales@mail.example.com"],
			"cc":[],"bcc":[],"subject":"A question",
			"attachments":[{
				"id":"attachment-1","filename":"brief.pdf",
				"content_type":"application/pdf","content_disposition":"attachment",
				"content_id":"","size":42
			}]
		}
	}`)
	event, err := DecodeResendReceivedEvent(body, "event-1")
	if err != nil {
		t.Fatalf("DecodeResendReceivedEvent() error = %v", err)
	}
	if event.EventID != "event-1" || event.EmailID != "provider-email-1" ||
		event.Subject != "A question" || len(event.Attachments) != 1 ||
		event.Attachments[0].SizeBytes == nil || *event.Attachments[0].SizeBytes != 42 {
		t.Fatalf("event = %#v", event)
	}

	for _, invalid := range [][]byte{
		[]byte(`{"type":"email.sent","data":{"email_id":"provider-email-1"}}`),
		[]byte(`{"type":"email.received","data":{}}`),
		append(body, []byte(` {}`)...),
	} {
		if _, err := DecodeResendReceivedEvent(invalid, "event-1"); !errors.Is(err, ErrInvalidWebhook) {
			t.Fatalf("invalid payload error = %v", err)
		}
	}
}

func newReceivingTestProvider(t *testing.T, endpoint string) *ResendProvider {
	t.Helper()
	provider, err := NewResendProvider(ResendOptions{
		APIKey: "mailbox-key", Endpoint: endpoint,
		HTTPClient: &http.Client{Timeout: time.Second},
	})
	if err != nil {
		t.Fatalf("NewResendProvider() error = %v", err)
	}
	return provider
}

func signWebhook(secret []byte, headers WebhookHeaders, body []byte) string {
	payload := fmt.Sprintf("%s.%s.%s", headers.ID, headers.Timestamp, body)
	signature := hmac.New(sha256.New, secret)
	_, _ = signature.Write([]byte(payload))
	return "v1," + base64.StdEncoding.EncodeToString(signature.Sum(nil))
}
