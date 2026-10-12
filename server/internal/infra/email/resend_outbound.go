package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type resendOutboundMessage struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	CC      []string `json:"cc,omitempty"`
	Subject string   `json:"subject"`
	Text    string   `json:"text"`
}

func (p *ResendProvider) Send(
	ctx context.Context,
	message OutboundMessage,
	idempotencyKey string,
) (Delivery, error) {
	if err := validateOutboundRequest([]OutboundMessage{message}, idempotencyKey); err != nil {
		return Delivery{}, err
	}
	var response struct {
		ID string `json:"id"`
	}
	if err := p.postOutboundJSON(
		ctx, "/emails", resendMessage(message), idempotencyKey, &response,
	); err != nil {
		return Delivery{}, err
	}
	if response.ID == "" {
		return Delivery{}, fmt.Errorf("%w: invalid send response", ErrUnavailable)
	}
	return Delivery{ID: response.ID}, nil
}

func (p *ResendProvider) SendBatch(
	ctx context.Context,
	messages []OutboundMessage,
	idempotencyKey string,
) ([]Delivery, error) {
	if err := validateOutboundRequest(messages, idempotencyKey); err != nil {
		return nil, err
	}
	payload := make([]resendOutboundMessage, len(messages))
	for index, message := range cloneOutboundMessages(messages) {
		payload[index] = resendMessage(message)
	}
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := p.postOutboundJSON(
		ctx, "/emails/batch", payload, idempotencyKey, &response,
	); err != nil {
		return nil, err
	}
	if len(response.Data) != len(messages) {
		return nil, fmt.Errorf("%w: invalid batch response", ErrUnavailable)
	}
	deliveries := make([]Delivery, len(response.Data))
	for index, item := range response.Data {
		if item.ID == "" {
			return nil, fmt.Errorf("%w: invalid batch response", ErrUnavailable)
		}
		deliveries[index] = Delivery{ID: item.ID}
	}
	return deliveries, nil
}

func resendMessage(message OutboundMessage) resendOutboundMessage {
	return resendOutboundMessage{
		From: message.From, To: append([]string(nil), message.To...),
		CC: append([]string(nil), message.CC...), Subject: message.Subject, Text: message.Text,
	}
}

func (p *ResendProvider) postOutboundJSON(
	ctx context.Context,
	path string,
	payload any,
	idempotencyKey string,
	destination any,
) error {
	if p.requestGate != nil {
		if err := p.requestGate.Wait(ctx); err != nil {
			return fmt.Errorf("wait for email provider request gate: %w", err)
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%w: encode send request", ErrUnavailable)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: create send request", ErrUnavailable)
	}
	request.Header.Set("Authorization", "Bearer "+p.apiKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	request.Header.Set("User-Agent", "Boardly-Agent-Mail/1")
	response, err := p.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("%w: send request failed", ErrUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		if response.StatusCode == http.StatusTooManyRequests {
			return &RateLimitError{RetryAfter: parseRetryAfter(response.Header, p.now())}
		}
		if response.StatusCode >= http.StatusBadRequest &&
			response.StatusCode < http.StatusInternalServerError {
			return fmt.Errorf(
				"%w: provider rejected request with status %d",
				ErrInvalidMessage,
				response.StatusCode,
			)
		}
		return fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResendResponseBytes+1))
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: invalid send response", ErrUnavailable)
	}
	return nil
}
