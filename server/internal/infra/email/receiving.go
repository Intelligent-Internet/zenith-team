package email

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	ErrReceivedEmailNotFound = errors.New("received email not found")
	ErrRateLimited           = errors.New("email provider rate limited")
	ErrUnavailable           = errors.New("email provider unavailable")
	ErrInvalidWebhook        = errors.New("invalid email webhook")
)

// RequestGate coordinates provider request admission before network I/O.
type RequestGate interface {
	Wait(context.Context) error
}

// RateLimitError preserves the provider-request retry delay without exposing
// provider response bodies to the caller.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%s; retry after %s", ErrRateLimited, e.RetryAfter)
}

func (e *RateLimitError) Unwrap() error {
	return ErrRateLimited
}

// ReceivedAttachment is provider-neutral attachment metadata.
type ReceivedAttachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   *int64 `json:"size,omitempty"`
	Disposition string `json:"content_disposition"`
	ContentID   string `json:"content_id"`
}

// ReceivedEmail is provider-derived content fetched only after Boardly has
// authorized a Company-scoped message index.
type ReceivedEmail struct {
	ID          string
	From        string
	To          []string
	CC          []string
	BCC         []string
	ReplyTo     []string
	Subject     string
	HTML        string
	Text        string
	ReceivedAt  time.Time
	Attachments []ReceivedAttachment
}

// ReceivedEvent is the authenticated provider-neutral metadata available at
// webhook ingress. It intentionally excludes message body and raw email.
type ReceivedEvent struct {
	EventID     string
	EmailID     string
	From        string
	To          []string
	CC          []string
	BCC         []string
	Subject     string
	ReceivedAt  time.Time
	Attachments []ReceivedAttachment
}

// AttachmentDownload is a short-lived provider URL. It is never persisted.
type AttachmentDownload struct {
	URL       string
	ExpiresAt time.Time
}

// ReceivedProvider retrieves received content and attachment URLs on demand.
type ReceivedProvider interface {
	RetrieveReceivedEmail(context.Context, string) (ReceivedEmail, error)
	RetrieveReceivedAttachment(context.Context, string, string) (AttachmentDownload, error)
}

// WebhookHeaders contains the Resend/Svix signing envelope copied from HTTP.
type WebhookHeaders struct {
	ID        string
	Timestamp string
	Signature string
}

// WebhookVerifier authenticates the exact raw request bytes before decoding.
type WebhookVerifier interface {
	Verify([]byte, WebhookHeaders) error
}
