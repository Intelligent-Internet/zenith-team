package daemon

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

//go:embed tool_studio.cjs
var studioLauncher string

type studioLauncherConfig struct {
	Entrypoint string `json:"entrypoint"`
	Endpoint   string `json:"endpoint"`
	RunToken   string `json:"runToken"`
	Home       string `json:"home"`
	APIURL     string `json:"apiUrl"`
}

func writeStudioLauncher(
	root string,
	installed toolInstallation,
	config studioLauncherConfig,
) (toolInstallation, error) {
	config.Entrypoint = installed.Entrypoint
	data, err := json.Marshal(config)
	if err != nil {
		return toolInstallation{}, err
	}
	filename := filepath.Join(root, "zenith-studio.cjs")
	if err := os.WriteFile(
		filename,
		[]byte("const managed = "+string(data)+";\n"+studioLauncher),
		0o600,
	); err != nil {
		return toolInstallation{}, err
	}
	installed.Entrypoint = filename
	return installed, nil
}

// The loopback bridge keeps the User session in the daemon. Each request also
// proves its live Run in the cloud; a provider never receives daemon authority.
func (d *Daemon) localStudioProjectHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || token == r.Header.Get("Authorization") {
			writeLocalCapabilityError(w, 401, "unauthorized", "Run proof is required.")
			return
		}
		if d.cfg.StudioAPIOrigin == "" {
			writeLocalCapabilityError(w, 409, "studio_not_configured", errStudioNotConfigured.Error())
			return
		}
		runtimeID, err := uuid.Parse(r.PathValue("runtimeId"))
		if err != nil {
			writeLocalCapabilityError(w, 400, "invalid_request", "Runtime is invalid.")
			return
		}
		runID, err := uuid.Parse(r.PathValue("runId"))
		if err != nil || runID == uuid.Nil {
			writeLocalCapabilityError(w, 400, "invalid_request", "Run is invalid.")
			return
		}
		var request protocol.StudioProjectRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			writeLocalCapabilityError(w, 400, "invalid_request", "Studio project operation is invalid.")
			return
		}
		request.RunToken = token
		request.APIOrigin = d.cfg.StudioAPIOrigin
		authAction := request.Action
		if authAction == "auth_wait" || authAction == "auth_ready" {
			// Authorize the live Run and its frozen Studio capability through
			// the existing project read before changing its visible wait state.
			request.Action = "get"
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		var result protocol.StudioProject
		err = d.client.postJSON(
			ctx,
			fmt.Sprintf("/api/v1/daemon/runtimes/%s/runs/%s/studio-project", runtimeID, runID),
			request,
			&result,
		)
		if err != nil {
			status, code := http.StatusServiceUnavailable, "studio_project_unavailable"
			var remote *requestError
			if errors.As(err, &remote) && remote.StatusCode >= 400 && remote.StatusCode < 500 {
				status, code = remote.StatusCode, remote.Code
			}
			writeLocalCapabilityError(
				w,
				status,
				code,
				"Studio project binding is unavailable for this Run. Retry after reconnecting Zenith Team.",
			)
			return
		}
		if authAction == "auth_wait" || authAction == "auth_ready" {
			if err := d.client.WaitForStudioAuth(ctx, runID, authAction == "auth_wait"); err != nil {
				writeLocalCapabilityError(
					w,
					http.StatusConflict,
					"studio_auth_wait_unavailable",
					"This Run can no longer wait for Studio sign-in.",
				)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}
}
