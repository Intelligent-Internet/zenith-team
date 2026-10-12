package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrInvalidResendConfig = errors.New("invalid Resend configuration")

type ResendOptions struct {
	APIKey      string
	Endpoint    string
	HTTPClient  *http.Client
	RequestGate RequestGate
}

type ResendProvider struct {
	apiKey      string
	endpoint    string
	httpClient  *http.Client
	requestGate RequestGate
	now         func() time.Time
}

func NewResendProvider(options ResendOptions) (*ResendProvider, error) {
	apiKey := strings.TrimSpace(options.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: API key is required", ErrInvalidResendConfig)
	}
	endpoint := strings.TrimSpace(options.Endpoint)
	if endpoint == "" {
		endpoint = "https://api.resend.com"
	}
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil || parsedEndpoint.Scheme == "" || parsedEndpoint.Host == "" {
		return nil, fmt.Errorf("%w: endpoint is invalid", ErrInvalidResendConfig)
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &ResendProvider{
		apiKey:      apiKey,
		endpoint:    strings.TrimRight(endpoint, "/"),
		httpClient:  httpClient,
		requestGate: options.RequestGate,
		now:         time.Now,
	}, nil
}

func (p *ResendProvider) SendTemplate(
	ctx context.Context,
	message TemplateMessage,
) (Delivery, error) {
	payload := struct {
		To       string `json:"to"`
		Template struct {
			ID        string            `json:"id"`
			Variables map[string]string `json:"variables"`
		} `json:"template"`
	}{To: message.To}
	payload.Template.ID = message.TemplateID
	payload.Template.Variables = message.Variables

	body, err := json.Marshal(payload)
	if err != nil {
		return Delivery{}, errors.New("encode Resend email request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/emails", bytes.NewReader(body))
	if err != nil {
		return Delivery{}, errors.New("create Resend email request")
	}
	request.Header.Set("Authorization", "Bearer "+p.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", message.IdempotencyKey)

	response, err := p.httpClient.Do(request)
	if err != nil {
		return Delivery{}, errors.New("Resend email delivery request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return Delivery{}, fmt.Errorf("Resend email delivery failed with status %d", response.StatusCode)
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).
		Decode(&result); err != nil ||
		result.ID == "" {
		return Delivery{}, errors.New("Resend email delivery returned an invalid response")
	}
	return Delivery{ID: result.ID}, nil
}
