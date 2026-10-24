package main

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
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/Intelligent-Internet/zenith-team/server/internal/authfile"
	daemonpkg "github.com/Intelligent-Internet/zenith-team/server/internal/daemon"
	"github.com/Intelligent-Internet/zenith-team/server/internal/runchannel"
)

// apiClient performs product API calls for every command family. The
// command code composes one request — method, /api/v1-relative path, JSON
// body — and never learns which transport carried it: Operator mode sends
// it to the server with the stored bearer session, Agent mode hands it to
// the daemon's Run channel. Both produce the same apiError shape from the
// server's own error envelope, so a command behaves identically in a
// terminal and inside a Run, differing only in what the server admits.
type apiClient struct {
	context   commandContext
	companyID string
	transport apiTransport
}

type apiTransport interface {
	// do sends one request and returns the server's status and body. A
	// transport-level failure (no network, daemon gone) is a networkError.
	do(ctx context.Context, method, path string, body []byte, requestKey string) (int, []byte, error)
}

// apiError is the server's error envelope plus the HTTP status, so callers
// and the exit-code mapping branch on Status and Code, never on Detail.
type apiError struct {
	Status int
	Code   string
	Detail string
	// ServerDetail is the server's own detail, before the request line is
	// put in front of it for Detail.
	ServerDetail string
	RequestID    string
	Method       string
	Path         string
}

func (e *apiError) Error() string {
	var builder strings.Builder
	builder.WriteString(e.Detail)
	if e.Code != "" {
		builder.WriteString(" (code: ")
		builder.WriteString(e.Code)
		builder.WriteString(")")
	}
	if e.RequestID != "" {
		builder.WriteString(" [request ")
		builder.WriteString(e.RequestID)
		builder.WriteString("]")
	}
	return builder.String()
}

// networkError is a failure before the server answered.
type networkError struct {
	Method string
	Path   string
	Err    error
}

func (e *networkError) Error() string {
	return fmt.Sprintf("%s %s: %v", e.Method, e.Path, e.Err)
}

func (e *networkError) Unwrap() error { return e.Err }

// newAPIClient resolves the mode, the credential source and the Company.
func newAPIClient(cmd *cobra.Command) (*apiClient, error) {
	context := resolveCommandContext()
	if key, _ := cmd.Flags().GetString("request-key"); strings.TrimSpace(key) != "" {
		if _, err := uuid.Parse(strings.TrimSpace(key)); err != nil {
			return nil, usagef("--request-key %q is not a UUID", key)
		}
		context.explicitRequestKey = strings.TrimSpace(key)
	}
	// Sign-in comes before Company selection in the guidance: an operator
	// with no session is told to log in, not to pick a Company first.
	var transport apiTransport
	if context.mode == modeAgent {
		transport = &runChannelTransport{client: runchannel.NewClient(context.run)}
	} else {
		operator, err := newOperatorTransport()
		if err != nil {
			return nil, err
		}
		transport = operator
	}
	companyID, err := resolveCompanyID(cmd, context)
	if err != nil {
		return nil, err
	}
	return &apiClient{context: context, companyID: companyID, transport: transport}, nil
}

// newOperatorTransport requires a stored session; it does not verify it,
// because the first request will, and a refresh may still rescue it.
func newOperatorTransport() (*operatorTransport, error) {
	path, err := daemonpkg.CredentialPath()
	if err != nil {
		return nil, err
	}
	if _, err := authfile.Load(path); errors.Is(err, authfile.ErrNotFound) {
		return nil, &modeError{msg: "not signed in: run `zen auth login` first"}
	} else if err != nil {
		return nil, fmt.Errorf("read Zenith Team session: %w", err)
	}
	return &operatorTransport{
		credentialPath: path,
		httpClient:     &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// operatorTransport is the direct bearer path. The token is renewed through
// authfile.Ensure before every request — the same primitive the daemon
// uses, so a CLI command and a running daemon share the rotation lock
// instead of racing each other's refresh tokens.
type operatorTransport struct {
	credentialPath string
	httpClient     *http.Client
}

func (t *operatorTransport) do(
	ctx context.Context,
	method, path string,
	body []byte,
	requestKey string,
) (int, []byte, error) {
	credential, err := authfile.Ensure(ctx, t, t.credentialPath, time.Now(), authfile.EnsureOptions{})
	if err != nil {
		return 0, nil, &modeError{msg: err.Error()}
	}
	// SessionURL validates the stored base (an /api/v1 URL, HTTPS unless
	// loopback) but rejects query strings in the path, so split them off.
	pathOnly, query, _ := strings.Cut(path, "?")
	endpoint, err := authfile.SessionURL(credential.APIBaseURL, pathOnly)
	if err != nil {
		return 0, nil, err
	}
	if query != "" {
		endpoint += "?" + query
	}
	var payload io.Reader
	if len(body) != 0 {
		payload = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	request.Header.Set("Accept", "application/json")
	if len(body) != 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	if requestKey != "" {
		request.Header.Set("Idempotency-Key", requestKey)
	}
	response, err := t.Do(request)
	if err != nil {
		return 0, nil, &networkError{Method: method, Path: pathOnly, Err: err}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return 0, nil, &networkError{Method: method, Path: pathOnly, Err: err}
	}
	if response.StatusCode == http.StatusUnauthorized {
		return response.StatusCode, data, &modeError{
			msg: "the server rejected the session; run `zen auth login` again",
		}
	}
	return response.StatusCode, data, nil
}

// Do sends request with the CLI's client metadata, which lets the server
// split logs and metrics by caller. authfile.Ensure refreshes through it
// too, so a refused refresh names the CLI in the server's request log.
func (t *operatorTransport) Do(request *http.Request) (*http.Response, error) {
	request.Header.Set("X-Client-Platform", "cli")
	request.Header.Set("X-Client-Version", version)
	request.Header.Set("X-Client-OS", runtime.GOOS)
	return t.httpClient.Do(request)
}

// runChannelTransport carries requests over the daemon's Run channel. The
// daemon's own refusals map onto apiError so the exit-code ladder applies.
type runChannelTransport struct {
	client *runchannel.Client
}

func (t *runChannelTransport) do(
	ctx context.Context,
	method, path string,
	body []byte,
	requestKey string,
) (int, []byte, error) {
	response, err := t.client.Do(ctx, runchannel.Request{
		Method:     method,
		Path:       path,
		RequestKey: requestKey,
		Body:       json.RawMessage(body),
	})
	var refusal *runchannel.Refusal
	if errors.As(err, &refusal) {
		status := refusal.Status
		if refusal.Code == "server_unreachable" {
			status = 0
		}
		pathOnly, _, _ := strings.Cut(path, "?")
		return 0, nil, &apiError{
			Status: status,
			Code:   refusal.Code,
			Detail: refusal.Reason,
			Method: method,
			Path:   pathOnly,
		}
	}
	if err != nil {
		pathOnly, _, _ := strings.Cut(path, "?")
		return 0, nil, &networkError{Method: method, Path: pathOnly, Err: err}
	}
	return response.Status, response.Body, nil
}

// apiContext bounds one CLI API command: the HTTP client timeout plus grace,
// so the transport timeout (a clean, classifiable error) fires first.
func apiContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 70*time.Second)
}

// companyPath joins path elements under the selected company.
func (c *apiClient) companyPath(elements ...string) string {
	return "/companies/" + c.companyID + "/" + strings.Join(elements, "/")
}

// messagesPath is the server's route for a Task's or Conversation's messages:
// served on the entity itself, not under the company (docs/api/conversations.md).
func messagesPath(kind, id string) string {
	return "/" + kind + "/" + id + "/messages"
}

func (c *apiClient) get(ctx context.Context, path string, into any) error {
	return c.call(ctx, http.MethodGet, path, nil, "", into)
}

func (c *apiClient) post(ctx context.Context, path string, body, into any) error {
	return c.mutate(ctx, http.MethodPost, path, body, into)
}

func (c *apiClient) patch(ctx context.Context, path string, body, into any) error {
	return c.mutate(ctx, http.MethodPatch, path, body, into)
}

func (c *apiClient) put(ctx context.Context, path string, body, into any) error {
	return c.mutate(ctx, http.MethodPut, path, body, into)
}

func (c *apiClient) delete(ctx context.Context, path string, body, into any) error {
	return c.mutate(ctx, http.MethodDelete, path, body, into)
}

// requestKey is the Idempotency-Key header for the next mutation: the
// operator's explicit --request-key; in Agent mode a key derived from the
// Run and the exact request, so a retried command converges on the server;
// otherwise none — an operator's plain create is not replayed, and the
// endpoints that need a key in their body get one from bodyRequestKey.
func (c *apiClient) requestKey(method, path string, body []byte) string {
	if c.context.explicitRequestKey != "" {
		return c.context.explicitRequestKey
	}
	if c.context.mode == modeAgent {
		digest := sha256.Sum256(body)
		seed := "zen:run:" + c.context.run.RunID + ":" + method + ":" + path + ":" + hex.EncodeToString(
			digest[:],
		)
		return uuid.NewSHA1(uuid.NameSpaceURL, []byte(seed)).String()
	}
	return ""
}

// bodyRequestKey feeds the `requestKey` field some POST endpoints carry in
// their body (messages, direct conversations, schedule firings).
func (c *apiClient) bodyRequestKey(path string, body []byte) string {
	if key := c.requestKey(http.MethodPost, path, body); key != "" {
		return key
	}
	return uuid.NewString()
}

func (c *apiClient) mutate(ctx context.Context, method, path string, body, into any) error {
	encoded, err := encodeBody(body)
	if err != nil {
		return err
	}
	return c.call(ctx, method, path, encoded, c.requestKey(method, path, encoded), into)
}

func encodeBody(body any) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	if raw, ok := body.(json.RawMessage); ok {
		return raw, nil
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	return encoded, nil
}

// call sends one request and decodes a 2xx body into `into` (nil discards
// it). Any other status becomes an apiError built from the server's envelope.
func (c *apiClient) call(ctx context.Context, method, path string, body []byte, requestKey string, into any) error {
	status, data, err := c.transport.do(ctx, method, path, body, requestKey)
	if err != nil {
		return err
	}
	pathOnly, _, _ := strings.Cut(path, "?")
	if status < 200 || status >= 300 {
		return decodeAPIError(status, data, method, pathOnly)
	}
	if into == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("decode %s %s response: %w", method, pathOnly, err)
	}
	return nil
}

func decodeAPIError(status int, data []byte, method, path string) *apiError {
	failure := &apiError{Status: status, Method: method, Path: path}
	var envelope struct {
		Detail    string `json:"detail"`
		Code      string `json:"code"`
		RequestID string `json:"requestId"`
	}
	if json.Unmarshal(data, &envelope) == nil {
		failure.Detail = strings.TrimSpace(envelope.Detail)
		failure.ServerDetail = failure.Detail
		failure.Code = envelope.Code
		failure.RequestID = envelope.RequestID
	}
	if failure.Detail == "" {
		failure.Detail = fmt.Sprintf("%s %s failed with status %d", method, path, status)
	} else {
		failure.Detail = fmt.Sprintf("%s %s: %s", method, path, failure.Detail)
	}
	return failure
}
