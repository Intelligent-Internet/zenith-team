package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

type studioBridgeError string

func (e studioBridgeError) Error() string { return string(e) }

type studioBridgeReply struct {
	ProtocolVersion int             `json:"protocolVersion"`
	ID              uuid.UUID       `json:"id"`
	OK              bool            `json:"ok"`
	Data            json.RawMessage `json:"data"`
	Error           struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	} `json:"error"`
	Invalidated bool `json:"invalidated"`
}

type studioBridge struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	replies    chan studioBridgeReply
	done       chan struct{}
	busy       chan struct{}
	once       sync.Once
	generation uuid.UUID
	processID  uuid.UUID
	accountID  uuid.UUID
}

func startStudioBridge(cmd *exec.Cmd) (*studioBridge, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err = cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, err
	}
	b := &studioBridge{
		cmd:        cmd,
		stdin:      stdin,
		replies:    make(chan studioBridgeReply, 1),
		done:       make(chan struct{}),
		busy:       make(chan struct{}, 1),
		generation: uuid.New(),
	}
	go func() {
		defer close(b.done)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), protocol.StudioResponseLimit+1)
		for scanner.Scan() {
			var reply studioBridgeReply
			if len(scanner.Bytes()) > protocol.StudioResponseLimit ||
				json.Unmarshal(scanner.Bytes(), &reply) != nil {
				break
			}
			// No reader can justify an unsolicited second frame. Stop instead of
			// accumulating output from a malfunctioning process.
			select {
			case b.replies <- reply:
			default:
				b.close()
			}
		}
		b.close()
		_ = cmd.Wait()
	}()
	return b, nil
}

func (b *studioBridge) close() {
	b.once.Do(func() { _ = b.stdin.Close(); _ = b.cmd.Process.Kill() })
}

func (b *studioBridge) call(ctx context.Context, request any) (json.RawMessage, error) {
	select {
	case b.busy <- struct{}{}:
	default:
		return nil, studioBridgeError("rate_limited")
	}
	defer func() { <-b.busy }()
	select {
	case <-b.done:
		return nil, studioBridgeError("runtime_unavailable")
	default:
	}
	raw, err := json.Marshal(request)
	if err != nil || len(raw) > protocol.StudioRequestLimit {
		return nil, studioBridgeError("unsupported")
	}
	var header struct {
		ID uuid.UUID `json:"id"`
	}
	if json.Unmarshal(raw, &header) != nil || header.ID == uuid.Nil {
		return nil, studioBridgeError("upstream_invalid")
	}
	// Closing the pipes releases a blocked write as well as a blocked read.
	stop := context.AfterFunc(ctx, b.close)
	defer stop()
	if _, err = b.stdin.Write(append(raw, '\n')); err != nil {
		b.close()
		return nil, studioBridgeError("outcome_unknown")
	}
	select {
	case <-ctx.Done():
		b.close()
		return nil, studioBridgeError("outcome_unknown")
	case <-b.done:
		return nil, studioBridgeError("runtime_unavailable")
	case reply := <-b.replies:
		if ctx.Err() != nil {
			b.close()
			return nil, studioBridgeError("outcome_unknown")
		}
		if reply.ProtocolVersion != 1 || reply.ID != header.ID {
			b.close()
			return nil, studioBridgeError("upstream_invalid")
		}
		if reply.Invalidated {
			b.close()
		}
		if !reply.OK {
			return nil, studioBridgeError(reply.Error.Code)
		}
		return reply.Data, nil
	}
}

func studioBridgeCode(err error) string {
	var e studioBridgeError
	if errors.As(err, &e) {
		return string(e)
	}
	return "runtime_unavailable"
}
