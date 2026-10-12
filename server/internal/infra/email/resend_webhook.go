package email

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const resendWebhookTolerance = 5 * time.Minute

type ResendWebhookVerifier struct {
	secret []byte
	now    func() time.Time
}

func NewResendWebhookVerifier(
	signingSecret string,
	now func() time.Time,
) (*ResendWebhookVerifier, error) {
	encodedSecret := strings.TrimSpace(signingSecret)
	if !strings.HasPrefix(encodedSecret, "whsec_") {
		return nil, fmt.Errorf("%w: signing secret is invalid", ErrInvalidResendConfig)
	}
	secret, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encodedSecret, "whsec_"))
	if err != nil || len(secret) < 16 {
		return nil, fmt.Errorf("%w: signing secret is invalid", ErrInvalidResendConfig)
	}
	if now == nil {
		now = time.Now
	}
	return &ResendWebhookVerifier{secret: secret, now: now}, nil
}

func (v *ResendWebhookVerifier) Verify(body []byte, headers WebhookHeaders) error {
	if v == nil || len(v.secret) == 0 {
		return fmt.Errorf("%w: verifier is not configured", ErrInvalidWebhook)
	}
	id := strings.TrimSpace(headers.ID)
	timestampText := strings.TrimSpace(headers.Timestamp)
	if id == "" || timestampText == "" || strings.TrimSpace(headers.Signature) == "" {
		return fmt.Errorf("%w: signing headers are required", ErrInvalidWebhook)
	}
	timestamp, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: timestamp is invalid", ErrInvalidWebhook)
	}
	signedAt := time.Unix(timestamp, 0)
	age := v.now().Sub(signedAt)
	if age < -resendWebhookTolerance || age > resendWebhookTolerance {
		return fmt.Errorf("%w: timestamp is outside tolerance", ErrInvalidWebhook)
	}

	mac := hmac.New(sha256.New, v.secret)
	_, _ = fmt.Fprintf(mac, "%s.%s.", id, timestampText)
	_, _ = mac.Write(body)
	expected := mac.Sum(nil)
	for _, candidate := range strings.Fields(headers.Signature) {
		version, encoded, ok := strings.Cut(candidate, ",")
		if !ok || version != "v1" {
			continue
		}
		decoded, decodeErr := base64.StdEncoding.DecodeString(encoded)
		if decodeErr == nil && hmac.Equal(decoded, expected) {
			return nil
		}
	}
	return fmt.Errorf("%w: signature does not match", ErrInvalidWebhook)
}

var _ WebhookVerifier = (*ResendWebhookVerifier)(nil)

func DecodeResendReceivedEvent(body []byte, eventID string) (ReceivedEvent, error) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" || len(eventID) > 512 || len(body) == 0 {
		return ReceivedEvent{}, fmt.Errorf("%w: event envelope is invalid", ErrInvalidWebhook)
	}
	var payload struct {
		Type string `json:"type"`
		Data struct {
			EmailID     string    `json:"email_id"`
			CreatedAt   time.Time `json:"created_at"`
			From        string    `json:"from"`
			To          []string  `json:"to"`
			CC          []string  `json:"cc"`
			BCC         []string  `json:"bcc"`
			Subject     string    `json:"subject"`
			Attachments []struct {
				ID                 string `json:"id"`
				Filename           string `json:"filename"`
				ContentType        string `json:"content_type"`
				ContentDisposition string `json:"content_disposition"`
				ContentID          string `json:"content_id"`
				Size               *int64 `json:"size"`
			} `json:"attachments"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&payload); err != nil {
		return ReceivedEvent{}, fmt.Errorf("%w: payload is invalid", ErrInvalidWebhook)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return ReceivedEvent{}, err
	}
	if payload.Type != "email.received" || strings.TrimSpace(payload.Data.EmailID) == "" ||
		payload.Data.CreatedAt.IsZero() || len(payload.Data.To) == 0 {
		return ReceivedEvent{}, fmt.Errorf("%w: event type or metadata is invalid", ErrInvalidWebhook)
	}
	attachments := make([]ReceivedAttachment, 0, len(payload.Data.Attachments))
	for _, attachment := range payload.Data.Attachments {
		attachments = append(attachments, ReceivedAttachment{
			ID: attachment.ID, Filename: attachment.Filename,
			ContentType: attachment.ContentType, SizeBytes: attachment.Size,
			Disposition: attachment.ContentDisposition, ContentID: attachment.ContentID,
		})
	}
	return ReceivedEvent{
		EventID: eventID, EmailID: payload.Data.EmailID, From: payload.Data.From,
		To: payload.Data.To, CC: payload.Data.CC, BCC: payload.Data.BCC,
		Subject: payload.Data.Subject, ReceivedAt: payload.Data.CreatedAt,
		Attachments: attachments,
	}, nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	return fmt.Errorf("%w: payload has trailing data", ErrInvalidWebhook)
}
