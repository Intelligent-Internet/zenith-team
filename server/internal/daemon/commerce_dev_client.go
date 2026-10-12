package daemon

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
)

// CommercePublicJWK is the public half of the daemon-managed P-256 key.
// Private key material never crosses the daemon boundary.
type CommercePublicJWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// RegisterLocalCommerceSigningKeyRequest registers a reconstructible local
// development key against an existing sandbox Site. Registration never
// creates, publishes, or activates the Site; the server decides whether the
// run proof may install or rotate the key.
type RegisterLocalCommerceSigningKeyRequest struct {
	SchemaVersion int               `json:"schemaVersion"`
	Origin        string            `json:"origin"`
	PublicJWK     CommercePublicJWK `json:"publicJwk"`
	Fingerprint   string            `json:"fingerprint"`
}

// RegisterLocalCommerceSigningKeyResponse is the server-authoritative Site
// binding returned after the run proof and existing manifest are checked.
type RegisterLocalCommerceSigningKeyResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	SiteID        string `json:"siteId"`
	Environment   string `json:"environment"`
	KeyID         string `json:"keyId"`
}

// RegisterLocalCommerceSigningKey binds the public key to an existing Site.
// The server requires both the daemon's ordinary authenticated session and
// the exact run-scoped credential that created the Task bootstrap; neither is
// accepted as a fallback authority for registration or rotation on its own.
func (c *Client) RegisterLocalCommerceSigningKey(
	ctx context.Context,
	runAuthToken string,
	companyID string,
	siteAlias string,
	request RegisterLocalCommerceSigningKeyRequest,
) (RegisterLocalCommerceSigningKeyResponse, error) {
	if !validRunScopedAuthTokenValue(runAuthToken) {
		return RegisterLocalCommerceSigningKeyResponse{}, errors.New(
			"local Commerce signing-key registration requires a run-scoped auth token",
		)
	}
	daemonToken, err := c.bearer(ctx)
	if err != nil {
		return RegisterLocalCommerceSigningKeyResponse{}, err
	}
	if daemonToken == "" {
		return RegisterLocalCommerceSigningKeyResponse{}, errors.New(
			"local Commerce signing-key registration requires an authenticated daemon session",
		)
	}
	var response RegisterLocalCommerceSigningKeyResponse
	path := fmt.Sprintf(
		"/api/v1/daemon/companies/%s/commerce/sites/%s/local-signing-keys",
		url.PathEscape(companyID),
		url.PathEscape(siteAlias),
	)
	payload, err := json.Marshal(request)
	if err != nil {
		return RegisterLocalCommerceSigningKeyResponse{}, fmt.Errorf(
			"encode local Commerce signing-key registration: %w",
			err,
		)
	}
	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+path,
		bytes.NewReader(payload),
	)
	if err != nil {
		return RegisterLocalCommerceSigningKeyResponse{}, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+daemonToken)
	httpRequest.Header.Set("X-Boardly-Run-Token", runAuthToken)
	httpRequest.Header.Set("Content-Type", "application/json")
	c.setIdentityHeaders(httpRequest)
	runAuthClient := &http.Client{
		Transport: c.client.Transport,
		Timeout:   c.client.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	httpResponse, err := runAuthClient.Do(httpRequest)
	if err != nil {
		return RegisterLocalCommerceSigningKeyResponse{}, err
	}
	if httpResponse.StatusCode == http.StatusUnauthorized {
		_ = httpResponse.Body.Close()
		renewedToken, renewErr := c.renewBearer(ctx, daemonToken)
		if renewErr != nil {
			return RegisterLocalCommerceSigningKeyResponse{}, renewErr
		}
		if renewedToken == "" {
			return RegisterLocalCommerceSigningKeyResponse{}, errors.New(
				"local Commerce signing-key registration lost its authenticated daemon session",
			)
		}
		retry := httpRequest.Clone(ctx)
		retry.Body = io.NopCloser(bytes.NewReader(payload))
		retry.ContentLength = int64(len(payload))
		retry.Header.Set("Authorization", "Bearer "+renewedToken)
		httpResponse, err = runAuthClient.Do(retry)
		if err != nil {
			return RegisterLocalCommerceSigningKeyResponse{}, err
		}
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < http.StatusOK || httpResponse.StatusCode >= http.StatusMultipleChoices {
		data, _ := io.ReadAll(io.LimitReader(httpResponse.Body, 4096))
		return RegisterLocalCommerceSigningKeyResponse{}, newRequestError(
			http.MethodPost,
			path,
			httpResponse.StatusCode,
			strings.TrimSpace(string(data)),
		)
	}
	decoder := json.NewDecoder(io.LimitReader(httpResponse.Body, 64<<10))
	if err := decoder.Decode(&response); err != nil {
		return RegisterLocalCommerceSigningKeyResponse{}, err
	}
	return response, nil
}

func validCommerceAPIBaseURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Path != "/api/v1" || parsed.Hostname() == "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	return parsed.Scheme == "http" && isLoopbackCommerceHost(parsed.Hostname())
}
