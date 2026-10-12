// Package runchannel is the wire of the Run channel (ADR 0009): run.json in
// a Run's isolated BOARDLY_HOME, the request and answer documents, and the
// client that posts them to the daemon's loopback endpoint. zen in Agent mode
// and boardly-mcp are its clients; the daemon serves it, checks each request
// against the Run's policy and forwards it with its own session and the Run
// proof. Neither client ever holds a bearer.
//
// It is its own leaf package, importing only the standard library and uuid,
// because boardly-mcp is mcp-sealed and must never link the daemon.
package runchannel

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// ControlFileName is the document a Run's clients read from its isolated
	// BOARDLY_HOME to learn they work inside a Run. An operator home never
	// carries one, so Agent mode cannot be entered by accident.
	ControlFileName = "run.json"
	// Path is the daemon loopback route clients post Request documents to.
	Path = "/v1/run/api"
	// AuthorizationScheme prefixes the capability in the Authorization header
	// of every channel request.
	AuthorizationScheme = "ZenithRun"
	// MaxResponseBytes bounds the server body the daemon relays. It is the
	// bound boardly-mcp read with before it moved onto the channel: the
	// Company Plan export reads every Task in one answer. The daemon refuses
	// a larger answer rather than relay part of it.
	MaxResponseBytes = 16 << 20
	// MaxForwardTimeout is the longest the daemon waits for the server on one
	// call (a social publish). The client waits a little longer, so the
	// daemon's own timeout answers first.
	MaxForwardTimeout = 130 * time.Second
)

// RunControlConfig is the run.json document. Every identity field is the
// server-frozen Run identity, so `zen run context` needs no network call and
// nothing an agent passes on the command line can change what it reports.
// A Task Run also names its Project, the one where `zen task create` files
// new work.
type RunControlConfig struct {
	SchemaVersion    int    `json:"schemaVersion"`
	Endpoint         string `json:"endpoint"`
	Capability       string `json:"capability"`
	RunID            string `json:"runId"`
	CompanyID        string `json:"companyId"`
	AgentID          string `json:"agentId"`
	AgentName        string `json:"agentName,omitempty"`
	TaskID           string `json:"taskId,omitempty"`
	TaskKey          string `json:"taskKey,omitempty"`
	ProjectID        string `json:"projectId,omitempty"`
	ProjectKey       string `json:"projectKey,omitempty"`
	ConversationID   string `json:"conversationId"`
	ConversationKind string `json:"conversationKind"`
	// Commands lists the zen commands this Run's policy admits, in the form
	// the system prompt and `zen run context` print them. It is advisory
	// for the CLI's help output; the daemon enforces the policy itself.
	Commands []string `json:"commands"`
}

// Request is one product API call carried over the channel. Path is relative
// to /api/v1 and may carry a query string. Mutations must name a RequestKey
// (a UUID) which the daemon forwards as the Idempotency-Key the Run-proof
// endpoints require, so a retried call converges.
type Request struct {
	SchemaVersion int             `json:"schemaVersion"`
	Method        string          `json:"method"`
	Path          string          `json:"path"`
	RequestKey    string          `json:"requestKey,omitempty"`
	Body          json.RawMessage `json:"body,omitempty"`
}

// Response relays the server's verdict. Status and Body are the server's
// own, so a client in a Run sees the same API surface as an operator; a
// daemon-level refusal (bad capability, denied scope, unreachable server)
// arrives as a non-200 daemon status with an ErrorBody instead.
type Response struct {
	SchemaVersion int             `json:"schemaVersion"`
	Status        int             `json:"status"`
	Body          json.RawMessage `json:"body,omitempty"`
}

// ErrorBody is the daemon's own error body on the channel.
type ErrorBody struct {
	SchemaVersion int    `json:"schemaVersion"`
	Code          string `json:"code"`
	Reason        string `json:"reason"`
}

var errRunControlUnavailable = errors.New("not inside a Zenith Team Run: no run.json in BOARDLY_HOME")

// LoadRunControlConfig reads run.json from home, the BOARDLY_HOME the daemon
// gave the Run. It is how a client decides it works inside a Run; an operator
// home has no such file and returns an error IsRunControlUnavailable reports.
func LoadRunControlConfig(home string) (RunControlConfig, error) {
	if strings.TrimSpace(home) == "" {
		return RunControlConfig{}, errRunControlUnavailable
	}
	path := filepath.Join(home, ControlFileName)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return RunControlConfig{}, errRunControlUnavailable
	}
	if err != nil {
		return RunControlConfig{}, fmt.Errorf("open run control configuration: %w", err)
	}
	defer file.Close()
	if runtime.GOOS != "windows" {
		info, err := file.Stat()
		if err != nil {
			return RunControlConfig{}, fmt.Errorf("stat run control configuration: %w", err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return RunControlConfig{}, errors.New("run control configuration permissions are too open")
		}
	}
	var config RunControlConfig
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return RunControlConfig{}, fmt.Errorf("decode run control configuration: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return RunControlConfig{}, errors.New("run control configuration has trailing data")
	}
	if config.SchemaVersion != 1 || !ValidEndpoint(config.Endpoint) ||
		!ValidCapability(config.Capability) || !ValidID(config.RunID) ||
		!ValidID(config.CompanyID) || !ValidID(config.AgentID) ||
		!ValidID(config.ConversationID) ||
		(config.TaskID != "" && !ValidID(config.TaskID)) ||
		(config.ProjectID != "" && !ValidID(config.ProjectID)) ||
		(config.ConversationKind != "task" && config.ConversationKind != "direct" &&
			config.ConversationKind != "channel_agent") {
		return RunControlConfig{}, errors.New("run control configuration is invalid")
	}
	return config, nil
}

// IsRunControlUnavailable reports the one LoadRunControlConfig failure that
// means "this is an operator shell": no run.json at all.
func IsRunControlUnavailable(err error) bool {
	return errors.Is(err, errRunControlUnavailable)
}

// ValidID reports whether value is a canonical, non-nil UUID: the only form
// a Run identity takes in run.json and in a policy path.
func ValidID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

// ValidCapability reports whether value is a local capability: 32 random
// bytes in unpadded base64url, the form the daemon mints.
func ValidCapability(value string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == value
}

// ValidEndpoint reports whether value is a daemon loopback base URL in
// canonical form: http, a loopback host, an explicit port and nothing else.
// A Commerce Site origin has its own rule in the daemon.
func ValidEndpoint(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery ||
		parsed.Fragment != "" || len(value) > 2048 {
		return false
	}
	hostname := parsed.Hostname()
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 || port == 80 || !loopbackHost(hostname) {
		return false
	}
	canonical := hostname
	if address := net.ParseIP(hostname); address != nil {
		canonical = address.String()
	}
	return value == "http://"+net.JoinHostPort(canonical, strconv.Itoa(port))
}

func loopbackHost(hostname string) bool {
	if address := net.ParseIP(hostname); address != nil {
		return address.IsLoopback()
	}
	if hostname != "localhost" && !strings.HasSuffix(hostname, ".localhost") {
		return false
	}
	if len(hostname) > 253 {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character != '-' && (character < 'a' || character > 'z') &&
				(character < '0' || character > '9') {
				return false
			}
		}
	}
	return true
}

// Client posts Request documents to the daemon named by run.json.
type Client struct {
	control RunControlConfig
	client  *http.Client
}

// NewClient binds a client to one Run's channel.
func NewClient(control RunControlConfig) *Client {
	return &Client{
		control: control,
		client:  &http.Client{Timeout: MaxForwardTimeout + 5*time.Second},
	}
}

// Do carries one API call over the channel and returns the server's status
// and body. A daemon-level refusal is returned as a *Refusal.
func (c *Client) Do(ctx context.Context, request Request) (Response, error) {
	request.SchemaVersion = 1
	encoded, err := json.Marshal(request)
	if err != nil {
		return Response{}, fmt.Errorf("encode run channel request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		strings.TrimRight(c.control.Endpoint, "/")+Path,
		bytes.NewReader(encoded),
	)
	if err != nil {
		return Response{}, fmt.Errorf("create run channel request: %w", err)
	}
	httpRequest.Header.Set("Authorization", AuthorizationScheme+" "+c.control.Capability)
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(httpRequest)
	if err != nil {
		return Response{}, fmt.Errorf("reach the local Zenith Team daemon: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure ErrorBody
		_ = json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&failure)
		if failure.Code == "" {
			failure.Code = "run_channel_error"
		}
		if strings.TrimSpace(failure.Reason) == "" {
			failure.Reason = http.StatusText(response.StatusCode)
		}
		return Response{}, &Refusal{
			Status: response.StatusCode,
			Code:   failure.Code,
			Reason: failure.Reason,
		}
	}
	var relayed Response
	if err := json.NewDecoder(io.LimitReader(response.Body, MaxResponseBytes+1024)).
		Decode(&relayed); err != nil {
		return Response{}, fmt.Errorf("decode run channel response: %w", err)
	}
	if relayed.SchemaVersion != 1 || relayed.Status < 100 || relayed.Status > 599 {
		return Response{}, errors.New("run channel response is invalid")
	}
	return relayed, nil
}

// Refusal is a daemon-level refusal on the channel, distinct from any server
// verdict.
type Refusal struct {
	Status int
	Code   string
	Reason string
}

func (e *Refusal) Error() string {
	return fmt.Sprintf("run channel refused the request (%s): %s", e.Code, e.Reason)
}
