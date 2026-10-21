package daemonws

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// RPCDispatcher is the RPCHandler wired into the WS hub. It dispatches each
// daemon.rpc_request to the method's transport-free adapter: runs.claim calls
// the handler package's shared claim core, runs.transcript.append calls its
// execution adapter. No method is replayed through the HTTP router.
type RPCDispatcher struct {
	claim               RPCHandler
	runTranscriptAppend RPCHandler
}

// NewRPCDispatcher builds the dispatcher around the daemon plane's claim
// adapter. The dispatcher pins the connection identity — including the
// principal authenticated at upgrade — into every call, so the adapter must
// not re-demand a bearer token.
func NewRPCDispatcher(claim RPCHandler) *RPCDispatcher {
	return &RPCDispatcher{claim: claim}
}

// SetRunTranscriptAppendHandler installs the direct domain adapter for the
// durable item RPC. Wiring happens before the hub accepts daemon connections.
func (d *RPCDispatcher) SetRunTranscriptAppendHandler(handler RPCHandler) {
	d.runTranscriptAppend = handler
}

// Dispatch is the RPCHandler installed via Hub.SetRPCHandler.
func (d *RPCDispatcher) Dispatch(
	ctx context.Context,
	identity ClientIdentity,
	method string,
	body json.RawMessage,
) (int, json.RawMessage, error) {
	switch method {
	case protocol.DaemonRPCMethodRunsClaim:
		if d.claim == nil {
			return http.StatusServiceUnavailable, nil, fmt.Errorf("runs claim rpc is unavailable")
		}
		return d.claim(ctx, identity, method, body)
	case protocol.DaemonRPCMethodRunTranscriptAppend:
		if d.runTranscriptAppend == nil {
			return http.StatusServiceUnavailable, nil, fmt.Errorf(
				"run transcript append rpc is unavailable",
			)
		}
		return d.runTranscriptAppend(ctx, identity, method, body)
	default:
		return http.StatusNotFound, nil, fmt.Errorf("unknown rpc method %q", method)
	}
}
