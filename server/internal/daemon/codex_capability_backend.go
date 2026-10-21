package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// errCodexCapabilityManaged marks an app-server refusal that the operator's
// own Codex policy caused — a managed setting or an unmet requirement. It is
// not a daemon fault, so callers surface it instead of retrying.
var errCodexCapabilityManaged = errors.New("codex capability is managed")

// codexRPCError is a JSON-RPC error answer from the app-server. It keeps only
// the numeric code, never Codex's message text, so it is safe to log.
type codexRPCError struct{ code int }

func (e *codexRPCError) Error() string {
	return fmt.Sprintf("Codex app-server request failed (code %d)", e.code)
}

type codexRPCSession struct {
	command              *exec.Cmd
	cancel               context.CancelFunc
	stdin                io.WriteCloser
	messages             chan []byte
	scanDone             chan error
	nextID               int
	notifications        []codexRPCNotification
	notificationOverflow bool
}

// startCodexRPCSessionWithLimit launches `codex app-server` on stdio and
// completes the JSON-RPC handshake. messageLimit bounds one scanned frame;
// the Connector and Plugin subsystems each own their own ceiling.
func startCodexRPCSessionWithLimit(
	ctx context.Context,
	path string,
	messageLimit int,
) (*codexRPCSession, error) {
	processCtx, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(processCtx, path, "app-server", "--listen", "stdio://")
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		cancel()
		return nil, err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), messageLimit)
	session := &codexRPCSession{
		command: command, cancel: cancel, stdin: stdin,
		messages: make(chan []byte), scanDone: make(chan error, 1),
	}
	go func() {
		for scanner.Scan() {
			message := append([]byte(nil), scanner.Bytes()...)
			select {
			case session.messages <- message:
			case <-processCtx.Done():
				session.scanDone <- processCtx.Err()
				return
			}
		}
		session.scanDone <- scanner.Err()
	}()
	if _, err := session.request(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "zenith-team-daemon", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true},
	}); err != nil {
		session.close()
		return nil, err
	}
	if err := session.notify("initialized", nil); err != nil {
		session.close()
		return nil, err
	}
	return session, nil
}

func (s *codexRPCSession) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	results, errs := s.requestAll(ctx, method, []any{params})
	return results[0], errs[0]
}

// requestAll pipelines one request per params and answers each in order.
// app-server serves them concurrently, so N slow reads cost about one. A
// transport failure fails every request still unanswered. Callers keep a
// batch small: nothing is read until every request is written.
func (s *codexRPCSession) requestAll(
	ctx context.Context,
	method string,
	params []any,
) ([]json.RawMessage, []error) {
	results := make([]json.RawMessage, len(params))
	errs := make([]error, len(params))
	pending := make(map[int]int, len(params))
	for index, value := range params {
		s.nextID++
		message := map[string]any{"jsonrpc": "2.0", "id": s.nextID, "method": method, "params": value}
		if err := json.NewEncoder(s.stdin).Encode(message); err != nil {
			for rest := index; rest < len(params); rest++ {
				errs[rest] = err
			}
			break
		}
		pending[s.nextID] = index
	}
	for len(pending) > 0 {
		message, err := s.nextMessage(ctx)
		if err != nil {
			for _, index := range pending {
				errs[index] = err
			}
			break
		}
		var envelope struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(message, &envelope) != nil {
			continue
		}
		if envelope.Method != "" {
			if notification, ok := decodeCodexRPCNotification(message, envelope.Method); ok {
				s.queueNotification(notification)
			}
			continue
		}
		index, ok := pending[envelope.ID]
		if !ok {
			continue
		}
		result, ok, err := decodeCodexRPCResponse(message)
		if !ok {
			continue
		}
		delete(pending, envelope.ID)
		results[index], errs[index] = result, err
	}
	return results, errs
}

// decodeCodexRPCResponse reads one response frame; ok is false for a frame
// that is not a response, which the caller skips.
func decodeCodexRPCResponse(message []byte) (json.RawMessage, bool, error) {
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(message, &response) != nil {
		return nil, false, nil
	}
	if response.Error != nil {
		if strings.Contains(strings.ToLower(response.Error.Message), "managed") ||
			strings.Contains(strings.ToLower(response.Error.Message), "requirement") {
			return nil, true, errCodexCapabilityManaged
		}
		return nil, true, &codexRPCError{code: response.Error.Code}
	}
	if len(response.Result) == 0 {
		return nil, true, errors.New("Codex app-server response is missing a result")
	}
	return append(json.RawMessage(nil), response.Result...), true, nil
}

func (s *codexRPCSession) nextMessage(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case message := <-s.messages:
		return message, nil
	case err := <-s.scanDone:
		if err != nil {
			return nil, err
		}
		return nil, errors.New("Codex app-server closed before responding")
	}
}

type codexRPCNotification struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func decodeCodexRPCNotification(
	message []byte,
	method string,
) (codexRPCNotification, bool) {
	switch method {
	case "mcpServer/oauthLogin/completed", "mcpServer/startupStatus/updated":
	default:
		return codexRPCNotification{}, false
	}
	var notification codexRPCNotification
	if json.Unmarshal(message, &notification) != nil || notification.Method != method {
		return codexRPCNotification{}, false
	}
	notification.Params = append(json.RawMessage(nil), notification.Params...)
	return notification, true
}

const codexRPCNotificationLimit = 1024

func (s *codexRPCSession) queueNotification(notification codexRPCNotification) {
	switch notification.Method {
	case "mcpServer/oauthLogin/completed", "mcpServer/startupStatus/updated":
	default:
		return
	}
	if len(s.notifications) >= codexRPCNotificationLimit {
		s.notificationOverflow = true
		return
	}
	s.notifications = append(s.notifications, notification)
}

func (s *codexRPCSession) drainNotifications() ([]codexRPCNotification, bool) {
	notifications := s.notifications
	s.notifications = nil
	overflow := s.notificationOverflow
	s.notificationOverflow = false
	return notifications, overflow
}

func (s *codexRPCSession) nextNotification(ctx context.Context) (codexRPCNotification, error) {
	notifications, overflow := s.drainNotifications()
	if overflow {
		return codexRPCNotification{}, errors.New("Codex app-server notification limit exceeded")
	}
	if len(notifications) > 0 {
		for index := 1; index < len(notifications); index++ {
			s.queueNotification(notifications[index])
		}
		return notifications[0], nil
	}
	for {
		message, err := s.nextMessage(ctx)
		if err != nil {
			return codexRPCNotification{}, err
		}
		var envelope struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(message, &envelope) != nil || envelope.Method == "" {
			continue
		}
		if notification, ok := decodeCodexRPCNotification(message, envelope.Method); ok {
			return notification, nil
		}
	}
}

func (s *codexRPCSession) notify(method string, params any) error {
	return json.NewEncoder(s.stdin).Encode(map[string]any{
		"jsonrpc": "2.0", "method": method, "params": params,
	})
}

func (s *codexRPCSession) close() {
	_ = s.stdin.Close()
	s.cancel()
	_ = s.command.Wait()
}
