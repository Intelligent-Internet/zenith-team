package email

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxResendResponseBytes = 16 << 20

func (p *ResendProvider) RetrieveReceivedEmail(
	ctx context.Context,
	providerEmailID string,
) (ReceivedEmail, error) {
	providerEmailID = strings.TrimSpace(providerEmailID)
	if providerEmailID == "" {
		return ReceivedEmail{}, fmt.Errorf("%w: email ID is required", ErrInvalidMessage)
	}

	var response struct {
		ID          string               `json:"id"`
		From        string               `json:"from"`
		To          []string             `json:"to"`
		CC          []string             `json:"cc"`
		BCC         []string             `json:"bcc"`
		ReplyTo     []string             `json:"reply_to"`
		Subject     string               `json:"subject"`
		HTML        string               `json:"html"`
		Text        string               `json:"text"`
		CreatedAt   time.Time            `json:"created_at"`
		Attachments []ReceivedAttachment `json:"attachments"`
	}
	if err := p.getJSON(
		ctx,
		"/emails/receiving/"+url.PathEscape(providerEmailID),
		&response,
	); err != nil {
		return ReceivedEmail{}, err
	}
	if strings.TrimSpace(response.ID) == "" {
		return ReceivedEmail{}, fmt.Errorf("%w: invalid received email response", ErrUnavailable)
	}
	return ReceivedEmail{
		ID:          response.ID,
		From:        response.From,
		To:          response.To,
		CC:          response.CC,
		BCC:         response.BCC,
		ReplyTo:     response.ReplyTo,
		Subject:     response.Subject,
		HTML:        response.HTML,
		Text:        response.Text,
		ReceivedAt:  response.CreatedAt,
		Attachments: response.Attachments,
	}, nil
}

func (p *ResendProvider) RetrieveReceivedAttachment(
	ctx context.Context,
	providerEmailID string,
	providerAttachmentID string,
) (AttachmentDownload, error) {
	providerEmailID = strings.TrimSpace(providerEmailID)
	providerAttachmentID = strings.TrimSpace(providerAttachmentID)
	if providerEmailID == "" || providerAttachmentID == "" {
		return AttachmentDownload{}, fmt.Errorf("%w: email and attachment IDs are required", ErrInvalidMessage)
	}

	var response struct {
		DownloadURL string    `json:"download_url"`
		ExpiresAt   time.Time `json:"expires_at"`
	}
	path := "/emails/receiving/" + url.PathEscape(providerEmailID) +
		"/attachments/" + url.PathEscape(providerAttachmentID)
	if err := p.getJSON(ctx, path, &response); err != nil {
		return AttachmentDownload{}, err
	}
	parsedURL, err := url.Parse(response.DownloadURL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" || response.ExpiresAt.IsZero() {
		return AttachmentDownload{}, fmt.Errorf("%w: invalid attachment response", ErrUnavailable)
	}
	return AttachmentDownload{URL: response.DownloadURL, ExpiresAt: response.ExpiresAt}, nil
}

func (p *ResendProvider) getJSON(ctx context.Context, path string, destination any) error {
	if p.requestGate != nil {
		if err := p.requestGate.Wait(ctx); err != nil {
			return fmt.Errorf("wait for email provider request gate: %w", err)
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+path, nil)
	if err != nil {
		return fmt.Errorf("%w: create provider request", ErrUnavailable)
	}
	request.Header.Set("Authorization", "Bearer "+p.apiKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Zenith-Team-Mailbox/1")

	response, err := p.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("%w: provider request failed", ErrUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return p.receivingStatusError(response)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResendResponseBytes+1))
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: invalid provider response", ErrUnavailable)
	}
	return nil
}

func (p *ResendProvider) receivingStatusError(response *http.Response) error {
	switch response.StatusCode {
	case http.StatusNotFound:
		return ErrReceivedEmailNotFound
	case http.StatusTooManyRequests:
		return &RateLimitError{RetryAfter: parseRetryAfter(response.Header, p.now())}
	default:
		return fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	}
}

func parseRetryAfter(headers http.Header, now time.Time) time.Duration {
	value := strings.TrimSpace(headers.Get("Retry-After"))
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		return clampRetryAfter(time.Duration(seconds) * time.Second)
	}
	if retryAt, err := http.ParseTime(value); err == nil {
		return clampRetryAfter(retryAt.Sub(now))
	}
	if resetSeconds, err := strconv.ParseInt(
		strings.TrimSpace(headers.Get("RateLimit-Reset")),
		10,
		64,
	); err == nil {
		return clampRetryAfter(time.Duration(resetSeconds) * time.Second)
	}
	return time.Second
}

func clampRetryAfter(value time.Duration) time.Duration {
	if value < time.Second {
		return time.Second
	}
	if value > time.Hour {
		return time.Hour
	}
	return value
}

var _ ReceivedProvider = (*ResendProvider)(nil)
