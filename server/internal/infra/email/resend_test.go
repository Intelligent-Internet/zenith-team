package email

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type outboundWireTest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	CC      []string `json:"cc"`
	Subject string   `json:"subject"`
	Text    string   `json:"text"`
	HTML    string   `json:"html"`
	BCC     []string `json:"bcc"`
}

func TestProviderSendsPublishedTemplateWithoutOwningAuthLogic(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/emails" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer resend-test-key" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("Idempotency-Key") != "email-verification/flow-1/1" {
			t.Errorf("Idempotency-Key = %q", request.Header.Get("Idempotency-Key"))
		}
		var payload struct {
			To       string `json:"to"`
			Template struct {
				ID        string            `json:"id"`
				Variables map[string]string `json:"variables"`
			} `json:"template"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload.To != "founder@boardly.test" ||
			payload.Template.ID != "00000000-0000-4000-8000-000000000001" ||
			payload.Template.Variables["DISPLAY_NAME"] != "Boardly Founder" ||
			payload.Template.Variables["OTP_CODE"] != "123456" {
			t.Errorf("payload = %#v", payload)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"resend-delivery-id"}`))
	}))
	defer server.Close()

	provider, err := NewResendProvider(ResendOptions{
		APIKey: "resend-test-key", Endpoint: server.URL,
		HTTPClient: &http.Client{Timeout: time.Second},
	})
	if err != nil {
		t.Fatalf("NewResendProvider() error = %v", err)
	}
	delivery, err := provider.SendTemplate(context.Background(), TemplateMessage{
		To: "founder@boardly.test", TemplateID: "00000000-0000-4000-8000-000000000001",
		Variables:      map[string]string{"DISPLAY_NAME": "Boardly Founder", "OTP_CODE": "123456"},
		IdempotencyKey: "email-verification/flow-1/1",
	})
	if err != nil {
		t.Fatalf("SendTemplate() error = %v", err)
	}
	if delivery.ID != "resend-delivery-id" {
		t.Fatalf("delivery = %#v", delivery)
	}
}

func TestProviderSendsPlainTextEmailAndPrivateBatchWithOneIdempotencyFence(t *testing.T) {
	t.Parallel()

	requests := make(chan struct {
		path           string
		idempotencyKey string
		messages       []outboundWireTest
	}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var messages []outboundWireTest
		switch request.URL.Path {
		case "/emails":
			var message outboundWireTest
			if err := json.NewDecoder(request.Body).Decode(&message); err != nil {
				t.Fatalf("decode single payload: %v", err)
			}
			messages = []outboundWireTest{message}
		case "/emails/batch":
			if err := json.NewDecoder(request.Body).Decode(&messages); err != nil {
				t.Fatalf("decode batch payload: %v", err)
			}
		default:
			t.Fatalf("request path = %q", request.URL.Path)
		}
		requests <- struct {
			path           string
			idempotencyKey string
			messages       []outboundWireTest
		}{request.URL.Path, request.Header.Get("Idempotency-Key"), messages}
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/emails" {
			_, _ = writer.Write([]byte(`{"id":"single-id"}`))
			return
		}
		_, _ = writer.Write([]byte(`{"data":[{"id":"batch-a"},{"id":"batch-b"}]}`))
	}))
	defer server.Close()
	provider, err := NewResendProvider(ResendOptions{
		APIKey: "resend-test-key", Endpoint: server.URL,
		HTTPClient: &http.Client{Timeout: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	message := OutboundMessage{
		From: "Boardly <hello@mail.boardly.test>", To: []string{"a@example.com"},
		CC: []string{}, Subject: "Hello", Text: "Plain text",
	}
	delivery, err := provider.Send(context.Background(), message, "agent-mail/draft/1")
	if err != nil || delivery.ID != "single-id" {
		t.Fatalf("Send() = %#v, %v", delivery, err)
	}
	batch, err := provider.SendBatch(context.Background(), []OutboundMessage{
		message,
		{
			From:    message.From,
			To:      []string{"b@example.com"},
			CC:      []string{"owner@example.com"},
			Subject: message.Subject,
			Text:    message.Text,
		},
	}, "agent-mail/draft/2")
	if err != nil || len(batch) != 2 || batch[0].ID != "batch-a" || batch[1].ID != "batch-b" {
		t.Fatalf("SendBatch() = %#v, %v", batch, err)
	}
	for _, expected := range []struct {
		path string
		key  string
		to   string
	}{{"/emails", "agent-mail/draft/1", "a@example.com"}, {"/emails/batch", "agent-mail/draft/2", "a@example.com"}} {
		got := <-requests
		if got.path != expected.path || got.idempotencyKey != expected.key ||
			len(got.messages) == 0 || got.messages[0].To[0] != expected.to {
			t.Fatalf(
				"request = %#v, want path/key/to %q/%q/%q",
				got,
				expected.path,
				expected.key,
				expected.to,
			)
		}
		for _, outbound := range got.messages {
			if outbound.HTML != "" || outbound.BCC != nil {
				t.Fatalf("outbound message exposed unsupported content: %#v", outbound)
			}
		}
	}
}

func TestProviderMapsOutboundRateLimitAndUnavailableFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		status     int
		retryAfter string
		want       error
	}{
		{name: "rate limited", status: http.StatusTooManyRequests, retryAfter: "7", want: ErrRateLimited},
		{name: "provider rejected", status: http.StatusBadRequest, want: ErrInvalidMessage},
		{name: "provider unavailable", status: http.StatusBadGateway, want: ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(
				http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					if test.retryAfter != "" {
						writer.Header().Set("Retry-After", test.retryAfter)
					}
					writer.WriteHeader(test.status)
				}),
			)
			defer server.Close()
			provider, err := NewResendProvider(ResendOptions{
				APIKey: "resend-test-key", Endpoint: server.URL,
				HTTPClient: &http.Client{Timeout: time.Second},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = provider.Send(context.Background(), OutboundMessage{
				From: "Boardly <hello@mail.boardly.test>", To: []string{"a@example.com"},
				Subject: "Hello", Text: "Body",
			}, "agent-mail/draft/1")
			if !errors.Is(err, test.want) {
				t.Fatalf("Send() error = %v, want %v", err, test.want)
			}
			if errors.Is(test.want, ErrRateLimited) {
				var rateLimit *RateLimitError
				if !errors.As(err, &rateLimit) || rateLimit.RetryAfter != 7*time.Second {
					t.Fatalf("rate limit error = %#v", err)
				}
			}
		})
	}
}

func TestProviderReturnsOpaqueErrorForResendFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, `{"message":"sentinel-provider-detail"}`, http.StatusBadGateway)
	}))
	defer server.Close()
	provider, err := NewResendProvider(ResendOptions{
		APIKey: "resend-test-key", Endpoint: server.URL,
		HTTPClient: &http.Client{Timeout: time.Second},
	})
	if err != nil {
		t.Fatalf("NewResendProvider() error = %v", err)
	}

	_, err = provider.SendTemplate(context.Background(), TemplateMessage{
		To: "founder@boardly.test", TemplateID: "template-id",
		Variables: map[string]string{"OTP_CODE": "123456"}, IdempotencyKey: "verification/1",
	})
	if err == nil || err.Error() != "Resend email delivery failed with status 502" {
		t.Fatalf("SendTemplate() error = %v", err)
	}
}
