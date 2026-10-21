package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

type studioConsumed struct {
	hash     [32]byte
	expires  time.Time
	response protocol.StudioControlResponse
}

type studioControlScope struct{ RuntimeID, AgentID uuid.UUID }

type studioRuntimeControl struct {
	agentID       uuid.UUID
	lastProcessID uuid.UUID
	pending       int
	serial        chan struct{}
	bridge        *studioBridge
	consumed      map[uuid.UUID]studioConsumed
}

// A control owns processes for exactly one authenticated WebSocket lifetime.
// It is never reused after reconnect, even for the same daemon credentials.
type studioControl struct {
	daemon   *Daemon
	mu       sync.Mutex
	runtimes map[studioControlScope]*studioRuntimeControl
	workers  sync.WaitGroup
	closed   bool
}

func (s *studioControl) close() {
	s.mu.Lock()
	s.closed = true
	var bridges []*studioBridge
	for _, r := range s.runtimes {
		if r.bridge != nil {
			r.bridge.close()
			bridges = append(bridges, r.bridge)
		}
	}
	s.mu.Unlock()
	s.workers.Wait()
	for _, b := range bridges {
		<-b.done
	}
}

func (s *studioControl) accept(ctx context.Context, kind string, raw json.RawMessage, send func([]byte)) {
	var scoped protocol.StudioScopedControl
	if decodeStudioControl(raw, &scoped) != nil || scoped.AgentID == uuid.Nil {
		return
	}
	if len(raw) > protocol.StudioRequestLimit {
		return
	}
	raw = scoped.Request
	var runtimeID, id uuid.UUID
	switch kind {
	case protocol.EventStudioConnectionProbe:
		var r protocol.StudioConnectionProbeRequest
		if decodeStudioControl(raw, &r) != nil {
			return
		}
		runtimeID, id = r.RuntimeID, r.ProbeID
	case protocol.EventStudioTokenRequest:
		var request protocol.StudioTokenRequest
		if decodeStudioControl(raw, &request) != nil {
			return
		}
		runtimeID, id = request.RuntimeID, request.RequestID
	case protocol.EventStudioEditorDispatch:
		var r protocol.StudioDaemonDispatch
		if decodeStudioControl(raw, &r) != nil {
			return
		}
		runtimeID, id = r.Authority.RuntimeID, r.DispatchID
	case protocol.EventStudioBindingVerify:
		var r protocol.StudioVerifyRequest
		if decodeStudioControl(raw, &r) != nil {
			return
		}
		runtimeID, id = r.Authority.RuntimeID, r.DispatchID
	default:
		return
	}
	if runtimeID == uuid.Nil || id == uuid.Nil {
		return
	}
	reply := func(response protocol.StudioControlResponse) {
		frame, _ := json.Marshal(protocol.Message{Type: kind + ".response", Payload: marshalRaw(response)})
		if len(frame) > protocol.StudioResponseLimit {
			frame, _ = json.Marshal(
				protocol.Message{
					Type: kind + ".response",
					Payload: marshalRaw(
						protocol.StudioControlResponse{
							CorrelationID: id,
							ErrorCode:     "unsupported",
						},
					),
				},
			)
		}
		send(frame)
	}
	if len(raw) > protocol.StudioRequestLimit {
		reply(protocol.StudioControlResponse{CorrelationID: id, ErrorCode: "unsupported"})
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if s.runtimes == nil {
		s.runtimes = make(map[studioControlScope]*studioRuntimeControl)
	}
	scope := studioControlScope{runtimeID, scoped.AgentID}
	r := s.runtimes[scope]
	if r == nil {
		if len(s.runtimes) >= 64 || s.daemon.findRuntime(runtimeID.String()) == nil {
			s.mu.Unlock()
			reply(protocol.StudioControlResponse{CorrelationID: id, ErrorCode: "runtime_unavailable"})
			return
		}
		r = &studioRuntimeControl{agentID: scoped.AgentID, consumed: make(map[uuid.UUID]studioConsumed)}
		s.runtimes[scope] = r
	}
	if r.pending >= 8 {
		s.mu.Unlock()
		reply(protocol.StudioControlResponse{CorrelationID: id, ErrorCode: "rate_limited"})
		return
	}
	if r.serial == nil {
		r.serial = make(chan struct{}, 1)
	}
	r.pending++
	s.workers.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.workers.Done()
		defer func() { s.mu.Lock(); r.pending--; s.mu.Unlock() }()
		budget := 15 * time.Second
		if kind == protocol.EventStudioEditorDispatch {
			var dispatch protocol.StudioDaemonDispatch
			_ = json.Unmarshal(raw, &dispatch)
			if dispatch.Request.Operation == "ensureBinding" {
				budget = 30 * time.Second
			}
		}
		operation, cancel := context.WithTimeout(ctx, budget)
		defer cancel()
		// The CLI owns one ordered pipe. Bound queued work while allowing
		// independent editors to share it; expiry is checked after this wait.
		select {
		case r.serial <- struct{}{}:
			defer func() { <-r.serial }()
		case <-operation.Done():
			reply(protocol.StudioControlResponse{CorrelationID: id, ErrorCode: "outcome_unknown"})
			return
		}
		response := s.execute(operation, r, runtimeID, id, kind, raw)
		if ctx.Err() == nil {
			reply(response)
		}
	}()
}

func decodeStudioControl(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return studioBridgeError("invalid")
	}
	return nil
}

func (s *studioControl) bridge(
	ctx context.Context,
	r *studioRuntimeControl,
	runtimeID uuid.UUID,
) (*studioBridge, error) {
	s.mu.Lock()
	b := r.bridge
	s.mu.Unlock()
	if b != nil {
		select {
		case <-b.done:
			b = nil
		default:
		}
	}
	if b != nil {
		return b, nil
	}
	runtime := s.daemon.findRuntime(runtimeID.String())
	if runtime == nil || s.daemon.tools == nil {
		return nil, studioBridgeError("runtime_unavailable")
	}
	tools := s.daemon.tools
	release, _ := findToolRelease("zenith-studio")
	if tools.apiOrigin(release) == "" {
		// Studio is off on this server.
		return nil, studioBridgeError("unsupported")
	}
	tools.mu.Lock()
	installed, ok := tools.installed[release.ID()]
	tools.mu.Unlock()
	if !ok {
		return nil, studioBridgeError("unsupported")
	}
	home, homeErr := tools.authHome(toolAuthScope{runtime.CompanyID, r.agentID.String()}, release)
	if homeErr != nil {
		return nil, studioBridgeError("authority_unavailable")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(context.Background(), installed.Node, installed.Entrypoint, "bridge")
	cmd.Env = os.Environ()
	for key, value := range map[string]string{release.HomeEnv: home, release.APIURLEnv: tools.apiOrigin(release), "ZENITH_STUDIO_CLI_SKIP_SETUP": "1"} {
		filtered := cmd.Env[:0]
		for _, entry := range cmd.Env {
			if !strings.HasPrefix(entry, key+"=") {
				filtered = append(filtered, entry)
			}
		}
		cmd.Env = filtered
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var err error
	b, err = startStudioBridge(cmd)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		b.close()
		return nil, studioBridgeError("runtime_unavailable")
	}
	r.bridge = b
	r.consumed = make(map[uuid.UUID]studioConsumed)
	s.mu.Unlock()
	raw, err := b.call(
		ctx,
		map[string]any{
			"protocolVersion":      1,
			"id":                   uuid.New(),
			"operation":            "handshake",
			"runtimeId":            runtimeID,
			"daemonId":             s.daemon.cfg.DaemonID,
			"connectionGeneration": b.generation,
			"editorSchemaVersion":  2,
		},
	)
	if err != nil {
		b.close()
		return nil, err
	}
	var hello struct {
		ProcessID            uuid.UUID `json:"processId"`
		RuntimeID            uuid.UUID `json:"runtimeId"`
		DaemonID             string    `json:"daemonId"`
		ConnectionGeneration uuid.UUID `json:"connectionGeneration"`
		Origin               string    `json:"origin"`
		StudioAccountID      uuid.UUID `json:"studioAccountId"`
		EditorSchemaVersion  int       `json:"editorSchemaVersion"`
		RequestLimit         int       `json:"requestLimit"`
		ResponseLimit        int       `json:"responseLimit"`
		MaxInFlight          int       `json:"maxInFlight"`
	}
	if json.Unmarshal(raw, &hello) != nil || hello.ProcessID == uuid.Nil || hello.ProcessID == r.lastProcessID ||
		hello.RuntimeID != runtimeID ||
		hello.DaemonID != s.daemon.cfg.DaemonID ||
		hello.ConnectionGeneration != b.generation ||
		hello.Origin != tools.apiOrigin(release) ||
		hello.StudioAccountID == uuid.Nil ||
		hello.EditorSchemaVersion != 2 ||
		hello.RequestLimit != protocol.StudioRequestLimit ||
		hello.ResponseLimit != protocol.StudioResponseLimit ||
		hello.MaxInFlight != 1 {
		b.close()
		return nil, studioBridgeError("unsupported")
	}
	b.processID, b.accountID = hello.ProcessID, hello.StudioAccountID
	r.lastProcessID = hello.ProcessID
	return b, nil
}

func (s *studioControl) execute(
	ctx context.Context,
	r *studioRuntimeControl,
	runtimeID, id uuid.UUID,
	kind string,
	raw []byte,
) protocol.StudioControlResponse {
	response := protocol.StudioControlResponse{CorrelationID: id}
	b, err := s.bridge(ctx, r, runtimeID)
	if err != nil {
		response.ErrorCode = studioBridgeCode(err)
		return response
	}
	if kind == protocol.EventStudioConnectionProbe {
		result, e := b.call(
			ctx,
			map[string]any{
				"protocolVersion": 1,
				"id":              id,
				"operation":       "probe",
				"request": protocol.StudioConnectionProbeRequest{
					ProbeID:   id,
					RuntimeID: runtimeID,
				},
			},
		)
		if e != nil {
			response.ErrorCode = studioBridgeCode(e)
			return response
		}
		var probe protocol.StudioConnectionProbeResponse
		if json.Unmarshal(result, &probe) != nil || probe.ProbeID != id || probe.RuntimeID != runtimeID ||
			probe.ConnectionGeneration != b.generation ||
			probe.StudioAccountID != b.accountID {
			b.close()
			response.ErrorCode = "upstream_invalid"
			return response
		}
		response.Probe = &probe
		return response
	}
	if kind == protocol.EventStudioTokenRequest {
		result, e := b.call(ctx, map[string]any{
			"protocolVersion": 1,
			"id":              id,
			"operation":       "token",
		})
		if e != nil {
			response.ErrorCode = studioBridgeCode(e)
			return response
		}
		var token protocol.StudioAccessToken
		if json.Unmarshal(result, &token) != nil || token.AccessToken == "" ||
			token.TokenType != "Bearer" || token.AccountID != b.accountID ||
			token.Origin != studioControlOrigin(s.daemon.tools) ||
			token.ExpiresAt.Before(time.Now()) {
			response.ErrorCode = "upstream_invalid"
			return response
		}
		response.Token = &token
		return response
	}
	var authority protocol.StudioAuthority
	var dispatch protocol.StudioDaemonDispatch
	var verify protocol.StudioVerifyRequest
	if kind == protocol.EventStudioEditorDispatch {
		_ = json.Unmarshal(raw, &dispatch)
		authority = dispatch.Authority
	} else {
		_ = json.Unmarshal(raw, &verify)
		authority = verify.Authority
	}
	runtime := s.daemon.findRuntime(runtimeID.String())
	if authority.ActorUserID == uuid.Nil || authority.ActorSessionID == uuid.Nil ||
		authority.ConversationID == uuid.Nil ||
		runtime == nil ||
		authority.CompanyID.String() != runtime.CompanyID ||
		authority.DaemonID != s.daemon.cfg.DaemonID ||
		authority.ConnectionGeneration != b.generation ||
		authority.StudioAccountID != b.accountID ||
		authority.Origin != studioControlOrigin(s.daemon.tools) {
		response.ErrorCode = "generation_conflict"
		return response
	}
	now := time.Now()
	digest := sha256.Sum256(append([]byte(kind), raw...))
	for key, entry := range r.consumed {
		if now.After(entry.expires) {
			delete(r.consumed, key)
		}
	}
	if prior, ok := r.consumed[id]; ok {
		if prior.hash != digest {
			response.ErrorCode = "request_conflict"
			return response
		}
		return prior.response
	}
	if authority.AuthorizedAt.After(now.Add(time.Second)) || !authority.DispatchExpiresAt.After(now) ||
		authority.DispatchExpiresAt.Sub(authority.AuthorizedAt) > 5*time.Second ||
		!authority.DispatchExpiresAt.After(authority.AuthorizedAt) {
		response.ErrorCode = "generation_conflict"
		return response
	}
	if len(r.consumed) >= 128 {
		response.ErrorCode = "rate_limited"
		return response
	}
	// Consume before any effect. Failure is retained too: a retry needs new admission.
	response.ErrorCode = "outcome_unknown"
	r.consumed[id] = studioConsumed{
		hash:     digest,
		expires:  authority.DispatchExpiresAt.Add(30 * time.Second),
		response: response,
	}
	response.ErrorCode = ""
	var result json.RawMessage
	if kind == protocol.EventStudioEditorDispatch &&
		(dispatch.SchemaVersion != 2 || dispatch.Request.SchemaVersion != 2 || dispatch.Request.RuntimeID != runtimeID || dispatch.Request.RequestID == uuid.Nil) {
		response.ErrorCode = "unsupported"
		entry := r.consumed[id]
		entry.response = response
		r.consumed[id] = entry
		return response
	}
	switch {
	case kind == protocol.EventStudioEditorDispatch && dispatch.Request.Operation != "ensureBinding":
		response.ErrorCode = "unsupported"
	case kind == protocol.EventStudioEditorDispatch:
		result, err = b.call(
			ctx,
			map[string]any{
				"protocolVersion": 1,
				"id":              id,
				"operation":       "dispatch",
				"dispatch":        dispatch,
				"binding":         nil,
			},
		)
		response.Dispatch = result
	default:
		result, err = b.call(
			ctx,
			map[string]any{
				"protocolVersion": 1,
				"id":              id,
				"operation":       "verifyBinding",
				"authority":       authority,
				"binding":         verify.Binding,
			},
		)
		var caps protocol.StudioCapabilities
		if err == nil && json.Unmarshal(result, &caps) != nil {
			err = studioBridgeError("upstream_invalid")
		}
		if err == nil {
			response.Capabilities = &caps
		}
	}
	if err != nil {
		response.ErrorCode = studioBridgeCode(err)
	}
	cached := response
	if len(response.Dispatch) > 64<<10 {
		cached = protocol.StudioControlResponse{CorrelationID: id, ErrorCode: "outcome_unknown"}
	}
	r.consumed[id] = studioConsumed{
		hash:     digest,
		expires:  authority.DispatchExpiresAt.Add(30 * time.Second),
		response: cached,
	}
	return response
}

func studioControlOrigin(tools *localToolService) string {
	release, _ := findToolRelease("zenith-studio")
	return tools.apiOrigin(release)
}

// Idle checks never mint human authority. Active clients re-enter Team admission;
// this sweep only validates the local credential connection.
func (s *studioControl) startIdle(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.maintainIdle(ctx)
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (s *studioControl) maintainIdle(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	for scope, r := range s.runtimes {
		runtimeID := scope.RuntimeID
		if r.pending != 0 {
			continue
		}
		if r.serial == nil {
			r.serial = make(chan struct{}, 1)
		}
		r.pending++
		// Reserve the pipe before releasing the mutex to incoming requests.
		r.serial <- struct{}{}
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			defer func() { s.mu.Lock(); r.pending--; s.mu.Unlock() }()
			defer func() { <-r.serial }()
			b := r.bridge
			if b == nil {
				return
			}
			operation, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			id := uuid.New()
			raw, err := b.call(
				operation,
				map[string]any{
					"protocolVersion": 1,
					"id":              id,
					"operation":       "probe",
					"request": protocol.StudioConnectionProbeRequest{
						ProbeID:   id,
						RuntimeID: runtimeID,
					},
				},
			)
			var probe protocol.StudioConnectionProbeResponse
			if err != nil || json.Unmarshal(raw, &probe) != nil || probe.ProbeID != id ||
				probe.RuntimeID != runtimeID ||
				probe.ConnectionGeneration != b.generation ||
				probe.StudioAccountID != b.accountID ||
				probe.Origin != studioControlOrigin(s.daemon.tools) {
				b.close()
			}
		}()
	}
}
