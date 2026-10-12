package authfile

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

	"github.com/google/uuid"
)

// ErrSessionRejected is the server's 401 to a stored session: it ended,
// so the file must be replaced, not retried.
var ErrSessionRejected = errors.New("Zenith Team rejected the stored session")

// Doer performs the refresh request. *http.Client satisfies it; tests pass a
// stub.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// NeedsRefresh reports whether the access token is missing, expired, or close
// enough to expiry that a caller should renew before using it. The 30s skew
// covers request latency and clock drift between the daemon and the server.
func NeedsRefresh(credential Credential, now time.Time) bool {
	if strings.TrimSpace(credential.AccessToken) == "" {
		return true
	}
	return credential.AccessExpiresAt != nil && !now.Add(30*time.Second).Before(*credential.AccessExpiresAt)
}

// EnsureOptions tunes Ensure for the retry-after-rejection path.
type EnsureOptions struct {
	// Force refreshes even when the stored access token still looks valid.
	// Used after the server rejects a token the file still advertises.
	Force bool
	// RejectedAccessToken is the token that was refused. When another process
	// has already rotated past it, Ensure returns that newer credential
	// instead of spending a refresh.
	RejectedAccessToken string
}

// Ensure returns a usable credential for path, refreshing and persisting the
// rotated pair when needed.
//
// Rotation makes the file the single source of truth: the server invalidates a
// refresh token once it is redeemed (reuse is treated as theft), so the new
// pair must reach disk before it is used. The refresh therefore happens under
// the cross-process lock, and re-reads the file inside it — a daemon and a CLI
// command racing to renew must not both redeem the same token.
func Ensure(
	ctx context.Context,
	doer Doer,
	path string,
	now time.Time,
	options EnsureOptions,
) (Credential, error) {
	credential, err := Load(path)
	if err != nil {
		return Credential{}, err
	}
	if !options.Force && !NeedsRefresh(credential, now) {
		return credential, nil
	}

	var result Credential
	err = WithLock(ctx, path, func() error {
		current, err := Load(path)
		if err != nil {
			return err
		}
		if options.Force && options.RejectedAccessToken != "" &&
			current.AccessToken != options.RejectedAccessToken {
			result = current
			return nil
		}
		if !options.Force && !NeedsRefresh(current, now) {
			result = current
			return nil
		}
		if current.RefreshExpiresAt != nil && !now.Before(*current.RefreshExpiresAt) {
			return errors.New("Zenith Team session has expired; run `zen auth login` again")
		}
		if current.PendingRotationID == nil {
			rotationID, err := uuid.NewRandom()
			if err != nil {
				return fmt.Errorf("create refresh rotation id: %w", err)
			}
			current.PendingRotationID = &rotationID
			if err := Save(path, current); err != nil {
				return fmt.Errorf("persist refresh rotation id: %w", err)
			}
		}
		result, err = Refresh(ctx, doer, current)
		if err != nil {
			return err
		}
		return Save(path, result)
	})
	if err != nil {
		return Credential{}, err
	}
	return result, nil
}

// Refresh exchanges a credential's refresh token for a fresh pair. The caller
// owns persistence; Ensure does it under the lock.
func Refresh(ctx context.Context, doer Doer, credential Credential) (Credential, error) {
	if credential.PendingRotationID == nil {
		return Credential{}, errors.New("refresh rotation id is required")
	}
	endpoint, err := SessionURL(credential.APIBaseURL, "/auth/refresh")
	if err != nil {
		return Credential{}, err
	}
	body, err := json.Marshal(map[string]string{
		"client":       "desktop",
		"refreshToken": credential.RefreshToken,
		"rotationId":   credential.PendingRotationID.String(),
	})
	if err != nil {
		return Credential{}, fmt.Errorf("encode refresh request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Credential{}, fmt.Errorf("create refresh request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := doer.Do(request)
	if err != nil {
		return Credential{}, fmt.Errorf("refresh boardly session: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Credential{}, sessionError(response)
	}
	return DecodeSession(credential, response.Body)
}

// MintDaemonSession asks the server for the daemon's own session (D31,
// POST /auth/daemon-session) with a person's credential, and returns it for
// the same API URL. The caller stores it in daemon-auth.json. The person's
// credential must hold a valid access token; Ensure it first.
func MintDaemonSession(ctx context.Context, doer Doer, person Credential) (Credential, error) {
	endpoint, err := SessionURL(person.APIBaseURL, "/auth/daemon-session")
	if err != nil {
		return Credential{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("{}"))
	if err != nil {
		return Credential{}, fmt.Errorf("create daemon session request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+person.AccessToken)
	response, err := doer.Do(request)
	if err != nil {
		return Credential{}, fmt.Errorf(
			"cannot reach the Zenith Team server to start the daemon session: %w", err,
		)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Credential{}, sessionError(response)
	}
	return DecodeSession(Credential{Version: CurrentVersion, APIBaseURL: person.APIBaseURL}, response.Body)
}

// Logout ends a stored session on the server with its refresh token
// (POST /auth/logout). The server answers a session that is already gone as
// signed out too, so only a refusal or an unreachable server is an error.
// The caller deletes the file.
func Logout(ctx context.Context, doer Doer, credential Credential) error {
	endpoint, err := SessionURL(credential.APIBaseURL, "/auth/logout")
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"client": "desktop", "refreshToken": credential.RefreshToken})
	if err != nil {
		return fmt.Errorf("encode logout request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create logout request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := doer.Do(request)
	if err != nil {
		return fmt.Errorf("sign out of Zenith Team: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return sessionError(response)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	return nil
}

// DecodeSession maps a bearer session response onto base, keeping base's
// version and API URL. Shared by refresh and by the login command, which sees
// the same payload shape. Unknown fields are ignored: the response carries
// more than the session (the user record, for one) and grows over time.
func DecodeSession(base Credential, body io.Reader) (Credential, error) {
	var payload struct {
		Session struct {
			Transport        string `json:"transport"`
			AccessToken      string `json:"accessToken"`
			RefreshToken     string `json:"refreshToken"`
			AccessExpiresAt  string `json:"accessExpiresAt"`
			RefreshExpiresAt string `json:"refreshExpiresAt"`
		} `json:"session"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&payload); err != nil {
		return Credential{}, fmt.Errorf("decode boardly session response: %w", err)
	}
	return sessionCredential(base, payload.Session.Transport, payload.Session.AccessToken,
		payload.Session.RefreshToken, payload.Session.AccessExpiresAt, payload.Session.RefreshExpiresAt)
}

func sessionCredential(
	base Credential,
	transport, accessToken, refreshToken, accessExpiresAt, refreshExpiresAt string,
) (Credential, error) {
	if transport != "bearer" || strings.TrimSpace(accessToken) == "" ||
		strings.TrimSpace(refreshToken) == "" {
		return Credential{}, errors.New("boardly session response is invalid")
	}
	accessExpiry, err := time.Parse(time.RFC3339, accessExpiresAt)
	if err != nil {
		return Credential{}, errors.New("boardly session access expiry is invalid")
	}
	refreshExpiry, err := time.Parse(time.RFC3339, refreshExpiresAt)
	if err != nil {
		return Credential{}, errors.New("boardly session refresh expiry is invalid")
	}
	version := base.Version
	if version == 0 {
		version = CurrentVersion
	}
	return Credential{
		Version:          version,
		APIBaseURL:       base.APIBaseURL,
		AccessToken:      strings.TrimSpace(accessToken),
		AccessExpiresAt:  &accessExpiry,
		RefreshToken:     strings.TrimSpace(refreshToken),
		RefreshExpiresAt: &refreshExpiry,
	}, nil
}

// SessionURL joins the stored API base URL with an auth path, enforcing the
// same shape the credential file promises: an /api/v1 base, no query or
// fragment, and HTTPS unless the host is loopback.
func SessionURL(baseURL, path string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil ||
		base.RawQuery != "" || base.Fragment != "" || base.Path != "/api/v1" {
		return "", errors.New("boardly API URL in the credential file is invalid")
	}
	if base.Scheme == "http" && !isLoopbackHost(base.Hostname()) {
		return "", errors.New("boardly API URL must use HTTPS unless it is loopback")
	}
	if !strings.HasPrefix(path, "/") {
		return "", errors.New("boardly API path is invalid")
	}
	return base.JoinPath(path).String(), nil
}

func isLoopbackHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func sessionError(response *http.Response) error {
	var payload struct {
		Detail string `json:"detail"`
		Code   string `json:"code"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload)
	detail := strings.TrimSpace(payload.Detail)
	if detail == "" {
		detail = http.StatusText(response.StatusCode)
	}
	if response.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w (%s); run `zen auth login` again", ErrSessionRejected, detail)
	}
	return fmt.Errorf("boardly session request failed: %s", detail)
}
