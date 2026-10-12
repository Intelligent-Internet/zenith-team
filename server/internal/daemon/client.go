package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/pkg/agent"
	"boardly.dev/boardly/server/pkg/protocol"
)

// requestError is returned by postJSON/getJSON when the server responds with
// an error status. Code carries the machine-readable value from the standard
// {detail, code, requestId} envelope; the contract's own rule is "branch on
// code, never on detail — detail may be reworded without notice", and the
// daemon is a consumer like any other.
type requestError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
	Code       string
}

// newRequestError captures the response body and lifts the envelope's code
// out of it. A non-envelope body (proxy HTML, truncation) leaves Code empty.
func newRequestError(method, path string, statusCode int, body string) *requestError {
	e := &requestError{Method: method, Path: path, StatusCode: statusCode, Body: body}
	var envelope struct {
		Code string `json:"code"`
	}
	if json.Unmarshal([]byte(body), &envelope) == nil {
		e.Code = envelope.Code
	}
	return e
}

func (e *requestError) Error() string {
	return fmt.Sprintf("%s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// isRunNotFoundError reports the run_not_found error code: the run row was
// deleted server-side while the local agent was still executing, so the
// daemon interrupts the agent rather than letting it emit tool calls against
// a dead run. Branches on the envelope code, never the detail prose.
func isRunNotFoundError(err error) bool {
	var reqErr *requestError
	if !errors.As(err, &reqErr) {
		return false
	}
	return reqErr.StatusCode == http.StatusNotFound && reqErr.Code == "run_not_found"
}

// isRuntimeNotFoundError reports the runtime_not_found error code: the
// runtime row was deleted server-side, so the daemon prunes the stale runtime
// and re-registers instead of looping on the dead UUID. The server pairs this
// code with a definitive miss (pgx.ErrNoRows or a revoked membership), never
// a transient failure, so it cannot trigger spurious self-cleanup. Branches
// on the envelope code, never the detail prose.
func isRuntimeNotFoundError(err error) bool {
	var reqErr *requestError
	if !errors.As(err, &reqErr) {
		return false
	}
	return reqErr.StatusCode == http.StatusNotFound && reqErr.Code == "runtime_not_found"
}

// isProjectPermissionRequiredError reports the project_permission_required
// error code: the daemon's member lacks the Project permission the call
// needs (ADR 0017), such as project.tasks.execute for a manual merge. The
// desktop is told that reason, never a stale review.
func isProjectPermissionRequiredError(err error) bool {
	var reqErr *requestError
	if !errors.As(err, &reqErr) {
		return false
	}
	return reqErr.StatusCode == http.StatusForbidden && reqErr.Code == "project_permission_required"
}

// runTranscriptHeadMismatchCode is the envelope code a terminal transition is
// refused with when the proof does not name the run's committed transcript
// prefix. It is the one 409 the daemon can recover from — the other terminal
// 409 (an invalid final item) carries no head to reconcile against.
const runTranscriptHeadMismatchCode = "run_transcript_head_mismatch"

// terminalHeadMismatch lifts the server's OWN committed transcript head out of
// a refused terminal transition, so the daemon can build a proof that is
// accepted instead of re-sending the one that was not.
//
// Bug R1: without the head the daemon had nothing to reconcile against. It
// re-sent the identical stale proof through FailRun, was refused again, and
// gave up — leaving the run in 'running' for a sweeper to kill while the
// agent's answer sat durable in the conversation. The head is validated, never
// merely decoded: a body that does not name THIS run with a usable seq returns
// false, because the daemon must not invent a head it was not told.
func terminalHeadMismatch(err error, runID uuid.UUID) (protocol.RunTerminalHeadMismatch, bool) {
	var reqErr *requestError
	if !errors.As(err, &reqErr) ||
		reqErr.StatusCode != http.StatusConflict ||
		reqErr.Code != runTranscriptHeadMismatchCode {
		return protocol.RunTerminalHeadMismatch{}, false
	}
	// The head rides flat on the standard {detail, code, requestId} envelope,
	// which is the shape pkg/protocol declares for this 409.
	var head protocol.RunTerminalHeadMismatch
	if json.Unmarshal([]byte(reqErr.Body), &head) != nil {
		return protocol.RunTerminalHeadMismatch{}, false
	}
	if head.RunID != runID || head.ThroughSeq < 0 {
		return protocol.RunTerminalHeadMismatch{}, false
	}
	return head, true
}

// Client handles HTTP communication with the Boardly server daemon API
// (/api/v1/daemon/*).
type Client struct {
	baseURL string
	session Session
	client  *http.Client

	// bundleClient downloads skill bundles. It carries no fixed timeout: the
	// caller supplies a per-request, size-scaled deadline via context.
	bundleClient *http.Client

	platform string
	version  string
	os       string
	daemonID string
}

// daemonIDHeader carries the machine identity (Config.DaemonID, the
// `daemonId` sent on register) on every control-plane request and on the
// wakeup WS upgrade; the server fences runtime-scoped routes on it. The
// literal is repeated here rather than imported from internal/middleware,
// which cmd/zen must not link; the wire round-trip test in
// internal/handler pins the two together.
const daemonIDHeader = "X-Boardly-Daemon-ID"

// NewClient creates a new daemon API client.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL:      baseURL,
		client:       &http.Client{Timeout: 30 * time.Second, Transport: cloneDefaultTransport()},
		bundleClient: &http.Client{},
		platform:     "daemon",
		os:           normalizeGOOS(runtime.GOOS),
	}
}

func cloneDefaultTransport() http.RoundTripper {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		return transport.Clone()
	}
	return http.DefaultTransport
}

// CloseIdleConnections drops pooled control-plane HTTP connections. Called
// after repeated heartbeat transport failures so a stale keep-alive socket
// from a server restart cannot delay recovery indefinitely.
func (c *Client) CloseIdleConnections() {
	if c == nil || c.client == nil {
		return
	}
	c.client.CloseIdleConnections()
}

func normalizeGOOS(goos string) string {
	switch goos {
	case "darwin":
		return "macos"
	default:
		return goos
	}
}

// SetVersion records the daemon's CLI version, sent as X-Client-Version.
func (c *Client) SetVersion(v string) {
	c.version = v
}

// SetDaemonID records the machine identity the daemon registers with, sent
// as X-Boardly-Daemon-ID on every request.
func (c *Client) SetDaemonID(id string) {
	c.daemonID = id
}

func (c *Client) setIdentityHeaders(req *http.Request) {
	if c.platform != "" {
		req.Header.Set("X-Client-Platform", c.platform)
	}
	if c.version != "" {
		req.Header.Set("X-Client-Version", c.version)
	}
	if c.os != "" {
		req.Header.Set("X-Client-OS", c.os)
	}
	if c.daemonID != "" {
		req.Header.Set(daemonIDHeader, c.daemonID)
	}
	req.Header.Set("X-Client-Capabilities", daemonClientCapabilities())
}

// daemonClientCapabilities is advertised on both the HTTP control plane and
// the WS handshake so a claim built over WS gets the same capability gating.
func daemonClientCapabilities() string {
	return strings.Join([]string{
		protocol.DaemonCapabilityRPCV1,
		protocol.DaemonCapabilityModelDiscoveryV1,
	}, ",")
}

// SetSession installs the credential source used for authenticated requests.
func (c *Client) SetSession(session Session) {
	c.session = session
}

// SetToken pins a fixed bearer token. Tests use it; the daemon installs a
// file-backed Session so token rotation is picked up.
func (c *Client) SetToken(token string) {
	c.session = staticSession(token)
}

// bearer returns the access token to send, or "" when the client is
// unauthenticated (health probes and tests).
func (c *Client) bearer(ctx context.Context) (string, error) {
	if c.session == nil {
		return "", nil
	}
	return c.session.AccessToken(ctx)
}

// renewBearer forces a refresh after the server rejected a token.
func (c *Client) renewBearer(ctx context.Context, rejected string) (string, error) {
	if c.session == nil {
		return "", nil
	}
	return c.session.Renew(ctx, rejected)
}

// batchClaimRequestTimeout is the short request-scoped deadline for the
// machine-level batch claim: the batch call covers every runtime the daemon
// hosts, so a slow claim would delay all of them.
const batchClaimRequestTimeout = 5 * time.Second

// ClaimRuns is the machine-level batch claim: up to maxRuns runs across every
// runtime the daemon hosts in one request. daemonID scopes the request to
// this machine; each returned Run carries its RuntimeID for local routing.
func (c *Client) ClaimRuns(ctx context.Context, daemonID string, runtimeIDs []string, maxRuns int) ([]*Run, error) {
	reqCtx, cancel := context.WithTimeout(ctx, batchClaimRequestTimeout)
	defer cancel()
	var resp struct {
		Items []*Run `json:"items"`
	}
	if err := c.postJSON(reqCtx, "/api/v1/daemon/runs/claim", map[string]any{
		"daemonId":   daemonID,
		"runtimeIds": runtimeIDs,
		"maxRuns":    maxRuns,
	}, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// skillBundleResolveRef is the (source, id) pair the resolve endpoint binds
// (server-side daemonSkillRef). The endpoint rejects unknown fields, so the
// request must carry exactly this pair — never a whole SkillRefData, whose
// extra keys turn the call into a 400.
type skillBundleResolveRef struct {
	ID     string `json:"id"`
	Source string `json:"source"`
}

type skillBundleResolveRequest struct {
	ContractVersion int                     `json:"contractVersion"`
	Skills          []skillBundleResolveRef `json:"skills"`
}

// ResolveSkillBundle downloads a single skill bundle under a caller-supplied,
// size-scaled deadline (bundleClient has no fixed timeout), retrying
// transient blips within whatever budget ctx leaves.
func (c *Client) ResolveSkillBundle(
	ctx context.Context,
	runtimeID string,
	runID uuid.UUID,
	ref SkillRefData,
) (SkillData, error) {
	var resp struct {
		Items []SkillData `json:"items"`
	}
	path := fmt.Sprintf("/api/v1/daemon/runtimes/%s/runs/%s/skill-bundles/resolve", runtimeID, runID)
	if err := c.postJSONViaWithRetry(ctx, c.bundleClient, path, skillBundleResolveRequest{
		ContractVersion: 1,
		Skills:          []skillBundleResolveRef{{ID: ref.ID, Source: ref.Source}},
	}, &resp, skillBundleResolveRetrySchedule); err != nil {
		return SkillData{}, err
	}
	if len(resp.Items) != 1 {
		return SkillData{}, fmt.Errorf("resolve skill bundle: expected 1 bundle, got %d", len(resp.Items))
	}
	return resp.Items[0], nil
}

// DownloadRunAttachment streams one founder-attached file into dst. It uses
// bundleClient for the same reason skill bundles do: an attachment is
// arbitrarily large, so the deadline belongs to the caller's ctx rather than a
// fixed client timeout.
func (c *Client) DownloadRunAttachment(
	ctx context.Context,
	runtimeID string,
	runID uuid.UUID,
	attachmentID string,
	dst io.Writer,
) error {
	path := fmt.Sprintf(
		"/api/v1/daemon/runtimes/%s/runs/%s/attachments/%s",
		runtimeID, runID, attachmentID,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	token, err := c.authorize(ctx, req)
	if err != nil {
		return err
	}
	c.setIdentityHeaders(req)

	resp, err := c.bundleClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized && token != "" {
		resp.Body.Close()
		resp, err = c.retryUnauthorized(ctx, c.bundleClient, req, token, nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
	}
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return newRequestError(http.MethodGet, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	_, err = io.Copy(dst, resp.Body)
	return err
}

func (c *Client) ExtendRunPrepareLease(ctx context.Context, runtimeID string, runID uuid.UUID) error {
	return c.postJSON(
		ctx,
		fmt.Sprintf("/api/v1/daemon/runtimes/%s/runs/%s/prepare-lease", runtimeID, runID),
		map[string]any{},
		nil,
	)
}

// StartRun names the runtime that is about to execute the run. The server's
// dispatched → running transition is fenced on it: only the daemon holding the
// claim can start the run, so a second daemon that somehow reached the same row
// is refused instead of executing it too. The run's own runtime_id cannot
// supply that — comparing the row to itself always matches.
func (c *Client) StartRun(ctx context.Context, runtimeID string, runID uuid.UUID) error {
	return c.postJSON(
		ctx,
		fmt.Sprintf("/api/v1/daemon/runs/%s/start", runID),
		map[string]any{"runtimeId": runtimeID},
		nil,
	)
}

// MarkRunWaitingLocalDirectory parks a dispatched run in the
// waiting_local_directory state while the daemon blocks on the path lock.
func (c *Client) MarkRunWaitingLocalDirectory(ctx context.Context, runID uuid.UUID) error {
	return c.postJSON(
		ctx,
		fmt.Sprintf("/api/v1/daemon/runs/%s/wait-local-directory", runID),
		map[string]any{},
		nil,
	)
}

// WaitForStudioAuth preserves the Run until Studio sign-in completes.
func (c *Client) WaitForStudioAuth(ctx context.Context, runID uuid.UUID, waiting bool) error {
	return c.postJSONWithRetry(ctx, fmt.Sprintf("/api/v1/daemon/runs/%s/wait-studio-auth", runID),
		map[string]any{"waiting": waiting}, []time.Duration{200 * time.Millisecond, time.Second})
}

// AckRunCancelled tells the server this daemon observed the run's
// cancellation and finished flushing the transcript. Idempotent server-side.
func (c *Client) AckRunCancelled(ctx context.Context, runID uuid.UUID) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/v1/daemon/runs/%s/cancel-ack", runID), map[string]any{}, nil)
}

func (c *Client) CompleteRun(
	ctx context.Context,
	runID uuid.UUID,
	transcript RunTranscriptProof,
	sessionID string,
	sessionRolloutMissing bool,
	retiredSessionID string,
) error {
	body := terminalTranscriptProofBody(transcript)
	if sessionID != "" {
		body["sessionId"] = sessionID
	}
	if sessionRolloutMissing {
		body["sessionRolloutMissing"] = true
	}
	if retiredSessionID != "" {
		body["retiredSessionId"] = retiredSessionID
	}
	return c.postJSONWithRetry(
		ctx,
		fmt.Sprintf("/api/v1/daemon/runs/%s/complete", runID),
		body,
		defaultTerminalRetrySchedule,
	)
}

func (c *Client) FailRun(
	ctx context.Context,
	runID uuid.UUID,
	transcript RunTranscriptProof,
	errMsg, sessionID, failureReason string,
	sessionRolloutMissing bool,
	retiredSessionID string,
) error {
	body := terminalTranscriptProofBody(transcript)
	body["error"] = errMsg
	if sessionID != "" {
		body["sessionId"] = sessionID
	}
	if failureReason != "" {
		body["failureReason"] = failureReason
	}
	if sessionRolloutMissing {
		body["sessionRolloutMissing"] = true
	}
	if retiredSessionID != "" {
		body["retiredSessionId"] = retiredSessionID
	}
	return c.postJSONWithRetry(
		ctx,
		fmt.Sprintf("/api/v1/daemon/runs/%s/fail", runID),
		body,
		defaultTerminalRetrySchedule,
	)
}

func terminalTranscriptProofBody(proof RunTranscriptProof) map[string]any {
	body := map[string]any{
		"throughSeq": proof.ThroughSeq,
	}
	if proof.FinalItemKey != "" {
		body["finalItemKey"] = proof.FinalItemKey
	}
	return body
}

func (c *Client) ReportRunUsage(ctx context.Context, runID uuid.UUID, usage []RunUsageEntry) error {
	if len(usage) == 0 {
		return nil
	}
	return c.postJSONWithRetry(
		ctx,
		fmt.Sprintf("/api/v1/daemon/runs/%s/usage", runID),
		map[string]any{"usage": usage},
		defaultTerminalRetrySchedule,
	)
}

// PinRunSession persists the provider session id mid-flight. Workspaces stay
// daemon-local and are reconstructed after a crash.
func (c *Client) PinRunSession(ctx context.Context, runID uuid.UUID, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	body := map[string]any{"sessionId": sessionID}
	return c.postJSON(ctx, fmt.Sprintf("/api/v1/daemon/runs/%s/session", runID), body, nil)
}

// RecoverOrphans tells the server to fail any in-flight runs the previous
// daemon process for this runtime left behind.
func (c *Client) RecoverOrphans(ctx context.Context, runtimeID string) error {
	return c.postJSON(
		ctx,
		fmt.Sprintf("/api/v1/daemon/runtimes/%s/recover-orphans", runtimeID),
		map[string]any{},
		nil,
	)
}

// RunStatus is what the daemon's status reads learn about a run. Reason says
// why a cancelled run was cancelled — task_reassigned for a confirmed
// handoff — and is empty for every other status.
type RunStatus struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// handedOff reports whether the server cancelled the run for a confirmed
// handoff (ADR 0014).
func (s RunStatus) handedOff() bool {
	return s.Status == "cancelled" && s.Reason == protocol.RunCancelReasonTaskReassigned
}

// GetRunStatus returns the current status of a run — the daemon's
// cancellation poll while a run executes, and its handoff check when a Task
// Run ends.
func (c *Client) GetRunStatus(ctx context.Context, runID uuid.UUID) (RunStatus, error) {
	var resp RunStatus
	if err := c.getJSON(ctx, fmt.Sprintf("/api/v1/daemon/runs/%s/status", runID), &resp); err != nil {
		return RunStatus{}, err
	}
	return resp, nil
}

// HeartbeatResponse aliases the wire type so HTTP and WS heartbeat paths
// share a single decoder shape.
type HeartbeatResponse = protocol.DaemonHeartbeatAckPayload

type ModelDiscoveryError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ModelDiscoveryResult struct {
	Status string               `json:"status"`
	Models []agent.RuntimeModel `json:"models"`
	Error  *ModelDiscoveryError `json:"error,omitempty"`
}

// SendHeartbeat keeps runtimeID online and reports the machine's current
// credential isolation with it.
func (c *Client) SendHeartbeat(
	ctx context.Context,
	runtimeID string,
	credentialIsolation string,
) (*HeartbeatResponse, error) {
	var resp HeartbeatResponse
	if err := c.postJSON(ctx, "/api/v1/daemon/heartbeat", protocol.DaemonHeartbeatRequestPayload{
		RuntimeID:           runtimeID,
		CredentialIsolation: credentialIsolation,
	}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) ReportRuntimeModelDiscovery(
	ctx context.Context,
	runtimeID string,
	requestID string,
	result ModelDiscoveryResult,
) error {
	return c.postJSONWithRetry(
		ctx,
		fmt.Sprintf(
			"/api/v1/daemon/runtimes/%s/model-discoveries/%s/result",
			runtimeID,
			requestID,
		),
		result,
		modelDiscoveryReportRetrySchedule,
	)
}

// CompanyInfo holds minimal company metadata returned by the API.
type CompanyInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListCompanies fetches the caller's company membership set.
func (c *Client) ListCompanies(ctx context.Context) ([]CompanyInfo, error) {
	var resp struct {
		Items []CompanyInfo `json:"items"`
	}
	if err := c.getJSON(ctx, "/api/v1/daemon/companies", &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

func (c *Client) Deregister(ctx context.Context, runtimeIDs []string) error {
	return c.postJSON(ctx, "/api/v1/daemon/deregister", map[string]any{
		"runtimeIds": runtimeIDs,
	}, nil)
}

// RegisterRuntime is one runtime entry in a registration request. Like the
// rest of this file's request shapes it is deliberately separate from — but
// identical to — the server's binding struct (daemonRegisterRuntime); the
// wire round-trip suite in internal/handler pins the two together.
type RegisterRuntime struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Version  string `json:"version"`
	Status   string `json:"status"`
	// UnusableReason is set for a provider the daemon found but cannot run
	// (below the minimum version, not executable); the server stores such
	// an entry offline. Omitted from the wire when nil so an older server
	// that rejects unknown keys keeps accepting healthy registrations.
	UnusableReason *protocol.RuntimeUnusableReason `json:"unusableReason,omitempty"`
	// SignIn is the provider CLI's own sign-in verdict from this probe
	// round. Omitted when the probe did not reach it, so an older server
	// keeps accepting the registration.
	SignIn *protocol.RuntimeSignIn `json:"signIn,omitempty"`
	// CredentialIsolation is this round's sandbox verdict for the machine
	// (Access TECH B3d): protocol.CredentialIsolationEnforced or
	// CredentialIsolationNotEnforced. Heartbeats repeat the current one.
	CredentialIsolation string `json:"credentialIsolation,omitempty"`
}

// RegisterRequest is the daemon registration body (bound server-side as
// daemonRegisterRequest).
type RegisterRequest struct {
	CompanyID  string            `json:"companyId"`
	DaemonID   string            `json:"daemonId"`
	DeviceName string            `json:"deviceName"`
	CLIVersion string            `json:"cliVersion"`
	LaunchedBy string            `json:"launchedBy"`
	Runtimes   []RegisterRuntime `json:"runtimes"`
}

// RegisterResponse holds the server's response to a daemon registration.
type RegisterResponse struct {
	Runtimes []Runtime `json:"items"`
}

func (c *Client) Register(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	var resp RegisterResponse
	if err := c.postJSON(ctx, "/api/v1/daemon/register", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// defaultTerminalRetrySchedule is the backoff used by postJSONWithRetry for
// terminal run callbacks. N entries → N+1 attempts (one immediate + N
// retries); five backoffs totalling 124s ride out short upstream blips.
var defaultTerminalRetrySchedule = []time.Duration{
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	32 * time.Second,
	64 * time.Second,
}

var modelDiscoveryReportRetrySchedule = []time.Duration{
	500 * time.Millisecond,
	2 * time.Second,
	4 * time.Second,
}

// skillBundleResolveRetrySchedule rides out brief transport blips on a single
// bundle download; the real budget is the size-scaled context deadline.
var skillBundleResolveRetrySchedule = []time.Duration{
	500 * time.Millisecond,
	2 * time.Second,
}

// retrySleep is the sleep between retry attempts; a package var so tests can
// swap in an instant sleep.
var retrySleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// isTransientError reports whether err looks like a hiccup likely to resolve
// on retry: transport errors, 5xx, and 408/429. Other 4xx codes are permanent.
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	var reqErr *requestError
	if errors.As(err, &reqErr) {
		if reqErr.StatusCode >= 500 {
			return true
		}
		if reqErr.StatusCode == http.StatusRequestTimeout || reqErr.StatusCode == http.StatusTooManyRequests {
			return true
		}
		return false
	}
	return true
}

// postJSONWithRetry posts a JSON body with bounded exponential backoff for
// "must reach the server" terminal callbacks. Transient errors retry per
// isTransientError; permanent 4xx responses stop immediately. Server-side
// CompleteRun/FailRun treat "already terminal" as idempotent success, so a
// duplicate replay is safe.
func (c *Client) postJSONWithRetry(
	ctx context.Context,
	path string,
	reqBody any,
	schedule []time.Duration,
) error {
	return c.postJSONViaWithRetry(ctx, c.client, path, reqBody, nil, schedule)
}

func (c *Client) postJSONViaWithRetry(
	ctx context.Context,
	httpClient *http.Client,
	path string,
	reqBody, respBody any,
	schedule []time.Duration,
) error {
	var lastErr error
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return lastErr
			}
			return err
		}
		err := c.postJSONVia(ctx, httpClient, path, reqBody, respBody)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isTransientError(err) {
			return err
		}
		if attempt >= len(schedule) {
			return err
		}
		if sleepErr := retrySleep(ctx, schedule[attempt]); sleepErr != nil {
			return err
		}
	}
}

func (c *Client) postJSON(ctx context.Context, path string, reqBody, respBody any) error {
	return c.postJSONVia(ctx, c.client, path, reqBody, respBody)
}

func (c *Client) postJSONStatus(ctx context.Context, path string, reqBody, respBody any) (int, error) {
	return c.requestJSONStatusVia(ctx, c.client, http.MethodPost, path, reqBody, respBody)
}

func (c *Client) postJSONVia(ctx context.Context, httpClient *http.Client, path string, reqBody, respBody any) error {
	return c.requestJSONVia(ctx, httpClient, http.MethodPost, path, reqBody, respBody)
}

func (c *Client) putJSON(ctx context.Context, path string, reqBody, respBody any) error {
	return c.requestJSONVia(ctx, c.client, http.MethodPut, path, reqBody, respBody)
}

func (c *Client) requestJSONVia(
	ctx context.Context,
	httpClient *http.Client,
	method, path string,
	reqBody, respBody any,
) error {
	_, err := c.requestJSONStatusVia(ctx, httpClient, method, path, reqBody, respBody)
	return err
}

func (c *Client) requestJSONStatusVia(
	ctx context.Context,
	httpClient *http.Client,
	method, path string,
	reqBody, respBody any,
) (int, error) {
	var payload []byte
	var body io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return 0, err
		}
		payload = data
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	token, err := c.authorize(ctx, req)
	if err != nil {
		return 0, err
	}
	c.setIdentityHeaders(req)

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized && token != "" {
		resp.Body.Close()
		resp, err = c.retryUnauthorized(ctx, httpClient, req, token, payload)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
	}

	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, newRequestError(method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if respBody == nil || resp.StatusCode == http.StatusNoContent {
		_, err := io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, err
	}
	if err := json.NewDecoder(resp.Body).Decode(respBody); err != nil {
		return resp.StatusCode, err
	}
	return resp.StatusCode, nil
}

func (c *Client) getJSON(ctx context.Context, path string, respBody any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	token, err := c.authorize(ctx, req)
	if err != nil {
		return err
	}
	c.setIdentityHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized && token != "" {
		resp.Body.Close()
		resp, err = c.retryUnauthorized(ctx, c.client, req, token, nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
	}

	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return newRequestError(http.MethodGet, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if respBody == nil {
		// Drain so the connection can be reused. The caller asked for no
		// body, so a read failure here has nothing to report and nothing to
		// act on — the response already succeeded.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(respBody)
}

// authorize stamps the bearer header and returns the token it used, or "" for
// an unauthenticated client.
func (c *Client) authorize(ctx context.Context, req *http.Request) (string, error) {
	token, err := c.bearer(ctx)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return token, nil
}

// retryUnauthorized renews the rejected token once and replays the request.
// A 401 on a token the file still advertises means the server revoked or
// expired it early (clock skew, an admin revocation, a restart while the
// daemon slept), which one refresh cures; a second 401 is a real auth failure
// and flows back to the caller.
func (c *Client) retryUnauthorized(
	ctx context.Context,
	httpClient *http.Client,
	req *http.Request,
	rejected string,
	body []byte,
) (*http.Response, error) {
	token, err := c.renewBearer(ctx, rejected)
	if err != nil {
		return nil, err
	}
	retry := req.Clone(ctx)
	if body != nil {
		retry.Body = io.NopCloser(bytes.NewReader(body))
		retry.ContentLength = int64(len(body))
	}
	retry.Header.Set("Authorization", "Bearer "+token)
	return httpClient.Do(retry)
}
