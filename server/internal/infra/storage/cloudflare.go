package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CloudflareRoutes projects publication routes into Workers KV. PostgreSQL
// retains ownership; the Worker only receives a hostname and its public origin.
type CloudflareRoutes struct {
	configuration CloudflareRoutesConfig
	client        *http.Client
	endpoint      string
	key           string
}

type CloudflareRoutesConfig struct {
	APIToken    string
	AccountID   string
	NamespaceID string
	BaseDomain  string
}

func NewCloudflareRoutes(configuration CloudflareRoutesConfig) (*CloudflareRoutes, error) {
	if configuration.APIToken == "" || configuration.AccountID == "" || configuration.NamespaceID == "" ||
		configuration.BaseDomain == "" {
		return nil, errors.New("Cloudflare routing configuration is incomplete")
	}
	authority := strings.Join([]string{
		"cloudflare_kv", configuration.AccountID, configuration.NamespaceID, configuration.BaseDomain,
	}, "\x00")
	digest := sha256.Sum256([]byte(authority))
	return &CloudflareRoutes{
		configuration: configuration,
		key:           "sha256:" + hex.EncodeToString(digest[:]),
		client: &http.Client{
			Timeout:       15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		endpoint: "https://api.cloudflare.com/client/v4",
	}, nil
}

func (r *CloudflareRoutes) BindingID() string  { return r.key }
func (r *CloudflareRoutes) BaseDomain() string { return r.configuration.BaseDomain }

func (r *CloudflareRoutes) Put(ctx context.Context, hostname, origin string) error {
	body, err := json.Marshal(struct {
		OriginURL string `json:"originUrl"`
	}{OriginURL: origin})
	if err != nil {
		return fmt.Errorf("encode publication route: %w", err)
	}
	return r.request(ctx, http.MethodPut, hostname, body)
}

func (r *CloudflareRoutes) Delete(ctx context.Context, hostname string) error {
	return r.request(ctx, http.MethodDelete, hostname, nil)
}

func (r *CloudflareRoutes) request(ctx context.Context, method, hostname string, body []byte) error {
	endpoint := r.endpoint + "/accounts/" + url.PathEscape(r.configuration.AccountID) +
		"/storage/kv/namespaces/" + url.PathEscape(r.configuration.NamespaceID) + "/values/" + url.PathEscape(hostname)
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("construct Cloudflare route request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+r.configuration.APIToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return fmt.Errorf("send Cloudflare route request: %w", err)
	}
	defer response.Body.Close()
	if method == http.MethodDelete && response.StatusCode == http.StatusNotFound {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Cloudflare route request returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result); err != nil {
		return fmt.Errorf("decode Cloudflare route response: %w", err)
	}
	if !result.Success {
		return errors.New("Cloudflare did not accept the route update")
	}
	return nil
}
