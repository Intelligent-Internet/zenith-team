package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

type localToolRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	CompanyID     string `json:"companyId"`
	RuntimeID     string `json:"runtimeId"`
	AgentID       string `json:"agentId"`
	Provider      string `json:"provider"`
	ToolKey       string `json:"toolKey"`
	Action        string `json:"action"`
}
type localToolResponse struct {
	Items []protocol.ToolObservation `json:"items"`
}

func (d *Daemon) localToolHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeLocalCapabilityError(w, 405, "method_not_allowed", "Method is not allowed.")
			return
		}
		if d.localPreviewToken == "" ||
			!validLocalPreviewAuthorization(r.Header.Get("Authorization"), d.localPreviewToken) {
			writeLocalCapabilityError(w, 401, "unauthorized", "Local capability is invalid.")
			return
		}
		var request localToolRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maximumLocalOperationBody))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
			request.SchemaVersion != 1 ||
			!validToolAgentID(request.AgentID) ||
			!validLocalRuntimeBinding(request.CompanyID, request.RuntimeID) ||
			!slices.Contains(
				[]string{"status", "repair", "connect", "cancel"},
				request.Action,
			) {
			writeLocalCapabilityError(w, 400, "invalid_request", "Tool request is invalid.")
			return
		}
		runtime := d.findRuntime(request.RuntimeID)
		release, known := findToolRelease(request.ToolKey)
		if !known || runtime == nil || runtime.CompanyID != request.CompanyID ||
			runtime.Provider != request.Provider {
			writeLocalCapabilityError(w, 404, "runtime_not_found", "This runtime is not on this computer.")
			return
		}
		// Recheck current cloud membership and runtime ownership before a
		// browser login; the local runtime map alone can outlive revocation.
		if !d.hasToolRuntimeAccess(r.Context(), *runtime) ||
			!d.hasToolAgentAccess(r.Context(), *runtime, request.AgentID) {
			writeLocalCapabilityError(w, 404, "runtime_not_found", "Connect this runtime and try again.")
			return
		}
		if d.tools == nil {
			writeLocalCapabilityError(w, 503, "daemon_unavailable", "Tool setup is unavailable.")
			return
		}
		if release.HomeEnv == "" && (isToolLogin(request.Action) || request.Action == "cancel") {
			writeLocalCapabilityError(
				w,
				400,
				"invalid_request",
				"This local tool does not require sign-in.",
			)
			return
		}
		if request.Action == "cancel" {
			d.tools.cancelLogin(toolAuthScope{request.CompanyID, request.AgentID}, request.ToolKey)
			writeLocalCapabilityJSON(
				w,
				200,
				localCapabilityResponse{
					OK: true,
					Value: localToolResponse{
						Items: d.tools.observations(request.CompanyID, request.AgentID),
					},
				},
			)
			return
		}
		if err := d.tools.schedule(
			toolJob{
				CompanyID: request.CompanyID,
				AgentID:   request.AgentID,
				Key:       request.ToolKey,
				Action:    request.Action,
			},
		); err != nil {
			writeLocalCapabilityError(w, 409, "operation_pending", "Tool setup is busy. Try again shortly.")
			return
		}
		writeLocalCapabilityJSON(
			w,
			200,
			localCapabilityResponse{
				OK: true,
				Value: localToolResponse{
					Items: d.tools.observations(request.CompanyID, request.AgentID),
				},
			},
		)
	}
}

// runtimeBindings is one of this daemon's runtimes as the server holds it,
// and its person's Agent bindings there (GET /daemon/runtimes/{id}/agent-
// bindings). The daemon plane answers it for the daemon's own session.
type runtimeBindings struct {
	Runtime struct {
		ID        string `json:"id"`
		CompanyID string `json:"companyId"`
		Provider  string `json:"provider"`
		DaemonID  string `json:"daemonId"`
	} `json:"runtime"`
	Items []localToolBinding `json:"items"`
}

// liveRuntimeBindings reads the runtime and its bindings, and answers an
// error unless the server still holds the runtime as this daemon's, in its
// Company, for the same provider.
func (d *Daemon) liveRuntimeBindings(ctx context.Context, runtime Runtime) ([]localToolBinding, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var live runtimeBindings
	if err := d.client.getJSON(ctx, "/api/v1/daemon/runtimes/"+runtime.ID+"/agent-bindings", &live); err != nil {
		return nil, err
	}
	if live.Runtime.ID != runtime.ID || live.Runtime.CompanyID != runtime.CompanyID ||
		live.Runtime.DaemonID != d.cfg.DaemonID || live.Runtime.Provider != runtime.Provider {
		return nil, errors.New("the server no longer holds this runtime as this daemon's")
	}
	return live.Items, nil
}

func (d *Daemon) hasToolRuntimeAccess(ctx context.Context, runtime Runtime) bool {
	_, err := d.liveRuntimeBindings(ctx, runtime)
	return err == nil
}

func validToolAgentID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil && parsed.String() == id
}

type localToolBinding struct {
	AgentID string `json:"agentId"`
	Runtime struct {
		ID string `json:"id"`
	} `json:"runtime"`
}

func (d *Daemon) toolBindings(ctx context.Context, runtime Runtime) ([]localToolBinding, error) {
	return d.liveRuntimeBindings(ctx, runtime)
}

func (d *Daemon) hasToolAgentAccess(ctx context.Context, runtime Runtime, agentID string) bool {
	bindings, err := d.toolBindings(ctx, runtime)
	if err != nil {
		return false
	}
	for _, binding := range bindings {
		if binding.AgentID == agentID && binding.Runtime.ID == runtime.ID {
			return true
		}
	}
	return false
}
