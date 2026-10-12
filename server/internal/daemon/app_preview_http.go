package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
)

const (
	localAppPreviewInspectPath       = "/v2/app-previews/inspect"
	localAppPreviewAttachPath        = "/v2/app-previews/attach"
	localAppPreviewEnsurePath        = "/v2/app-previews/ensure"
	localAppPreviewRenewPath         = "/v2/app-previews/renew"
	localAppPreviewReleasePath       = "/v2/app-previews/release"
	localAppPreviewRestartPath       = "/v2/app-previews/restart"
	localAppPreviewStopPath          = "/v2/app-previews/stop"
	localAppPreviewLegacyEnsurePath  = "/v1/app-previews/ensure"
	localAppPreviewLegacyRestartPath = "/v1/app-previews/restart"
)

func (d *Daemon) localAppPreviewUnsupportedHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.authorizeLocalWorkspaceRequest(w, r) {
			return
		}
		writeLocalWorkspaceError(
			w,
			http.StatusUpgradeRequired,
			"app_preview_protocol_unsupported",
			"This app preview protocol is no longer supported. Update Zenith Team Desktop.",
		)
	}
}

type localAppPreviewControlRequest struct {
	SchemaVersion int       `json:"schemaVersion"`
	CompanyID     uuid.UUID `json:"companyId"`
	OutputID      uuid.UUID `json:"outputId"`
	RunID         uuid.UUID `json:"runId"`
	RuntimeID     uuid.UUID `json:"runtimeId"`
	StopToken     string    `json:"stopToken,omitempty"`
	LeaseID       uuid.UUID `json:"leaseId,omitempty"`
	ActivationID  uuid.UUID `json:"activationId,omitempty"`
}

func (request localAppPreviewControlRequest) identity() appPreviewIdentity {
	return appPreviewIdentity{
		CompanyID: request.CompanyID, OutputID: request.OutputID,
		RunID: request.RunID, RuntimeID: request.RuntimeID,
	}
}

type localAppPreviewResponse struct {
	SchemaVersion  int                    `json:"schemaVersion"`
	CompanyID      string                 `json:"companyId,omitempty"`
	OutputID       string                 `json:"outputId,omitempty"`
	RunID          string                 `json:"runId,omitempty"`
	RuntimeID      string                 `json:"runtimeId,omitempty"`
	ActivationID   string                 `json:"activationId,omitempty"`
	Lifecycle      appPreviewLifecycle    `json:"lifecycle,omitempty"`
	Availability   appPreviewAvailability `json:"availability,omitempty"`
	Reason         string                 `json:"reason,omitempty"`
	URL            string                 `json:"url,omitempty"`
	Ports          []int                  `json:"ports,omitempty"`
	LastObservedAt string                 `json:"lastObservedAt,omitempty"`
	StopToken      string                 `json:"stopToken,omitempty"`
	LeaseID        string                 `json:"leaseId,omitempty"`
	LeaseExpiresAt string                 `json:"leaseExpiresAt,omitempty"`
	LeaseTTLMS     int64                  `json:"leaseTtlMs,omitempty"`
	RenewAfterMS   int64                  `json:"renewAfterMs,omitempty"`
	Code           string                 `json:"code,omitempty"`
	Detail         string                 `json:"detail,omitempty"`
}

type appPreviewOperation string

const (
	appPreviewOperationInspect appPreviewOperation = "inspect"
	appPreviewOperationAttach  appPreviewOperation = "attach"
	appPreviewOperationEnsure  appPreviewOperation = "ensure"
	appPreviewOperationRenew   appPreviewOperation = "renew"
	appPreviewOperationRelease appPreviewOperation = "release"
	appPreviewOperationRestart appPreviewOperation = "restart"
	appPreviewOperationStop    appPreviewOperation = "stop"
)

func (d *Daemon) localAppPreviewControlHandler(operation appPreviewOperation) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.authorizeLocalWorkspaceRequest(w, r) {
			return
		}
		var request localAppPreviewControlRequest
		if !decodeLocalWorkspaceJSON(w, r, &request) || request.SchemaVersion != 2 ||
			request.CompanyID == uuid.Nil || request.OutputID == uuid.Nil || request.RunID == uuid.Nil ||
			request.RuntimeID == uuid.Nil || !validAppPreviewOperationRequest(operation, request) {
			writeLocalWorkspaceError(
				w,
				http.StatusBadRequest,
				"invalid_request",
				"App preview request is invalid.",
			)
			return
		}
		runtimeValue := d.findRuntime(request.RuntimeID.String())
		if d.appPreviews == nil || runtimeValue == nil || runtimeValue.CompanyID != request.CompanyID.String() {
			writeLocalWorkspaceError(
				w,
				http.StatusNotFound,
				"runtime_unavailable",
				"The originating runtime is not available on this device.",
			)
			return
		}

		var result appPreviewOperationResult
		var err error
		switch operation {
		case appPreviewOperationInspect:
			result, err = d.appPreviews.Inspect(request.identity())
		case appPreviewOperationAttach:
			result, err = d.appPreviews.Attach(request.StopToken, request.identity())
		case appPreviewOperationEnsure:
			result, err = d.appPreviews.Ensure(request.LeaseID, request.identity())
		case appPreviewOperationRenew:
			result, err = d.appPreviews.Renew(request.LeaseID, request.identity())
		case appPreviewOperationRelease:
			result, err = d.appPreviews.Release(request.LeaseID, request.identity())
		case appPreviewOperationRestart:
			result, err = d.appPreviews.Restart(request.LeaseID, request.ActivationID, request.identity())
		case appPreviewOperationStop:
			result, err = d.appPreviews.Stop(request.StopToken, request.identity())
		default:
			err = errAppPreviewInvalid
		}
		if err != nil {
			status, code, detail := appPreviewHTTPError(err)
			writeLocalAppPreviewResponse(w, status, result, code, detail)
			return
		}
		writeLocalAppPreviewResponse(w, http.StatusOK, result, "", "")
	}
}

func validAppPreviewOperationRequest(
	operation appPreviewOperation,
	request localAppPreviewControlRequest,
) bool {
	switch operation {
	case appPreviewOperationInspect:
		return request.StopToken == "" && request.LeaseID == uuid.Nil && request.ActivationID == uuid.Nil
	case appPreviewOperationAttach, appPreviewOperationStop:
		return request.StopToken != "" && len(request.StopToken) <= 128 &&
			request.LeaseID == uuid.Nil && request.ActivationID == uuid.Nil
	case appPreviewOperationEnsure, appPreviewOperationRenew, appPreviewOperationRelease:
		return request.StopToken == "" && request.LeaseID != uuid.Nil && request.ActivationID == uuid.Nil
	case appPreviewOperationRestart:
		return request.StopToken == "" && request.LeaseID != uuid.Nil && request.ActivationID != uuid.Nil
	default:
		return false
	}
}

func appPreviewHTTPError(err error) (int, string, string) {
	switch {
	case errors.Is(err, errAppPreviewNotFound), errors.Is(err, errAppPreviewRuntimeMismatch):
		return http.StatusNotFound, "app_preview_not_found", "This app preview is not available on the originating device."
	case errors.Is(err, errAppPreviewChanged):
		return http.StatusConflict, "app_preview_changed", "The app preview changed before this action was applied."
	case errors.Is(err, errAppPreviewLeaseInvalid):
		return http.StatusConflict, "app_preview_lease_invalid", "This preview viewer is no longer attached."
	case errors.Is(err, errAppPreviewPortUnavailable):
		return http.StatusConflict, "app_preview_port_unavailable", "A required port is already used by another preview."
	case errors.Is(err, errAppPreviewBusy):
		return http.StatusConflict, "app_preview_busy", "The app preview is still stopping."
	case errors.Is(err, errAppPreviewCleanupBlocked):
		return http.StatusServiceUnavailable, "app_preview_cleanup_blocked", "Zenith Team could not finish stopping the app and is retrying."
	case errors.Is(err, errAppPreviewPlatformUnsupported):
		return http.StatusNotImplemented, "app_preview_platform_unsupported", "App preview supervision is not supported on this device."
	case errors.Is(err, errAppPreviewInvalid):
		return http.StatusBadRequest, "invalid_request", "App preview request is invalid."
	default:
		return http.StatusServiceUnavailable, "app_preview_unavailable", "The app preview operation could not be completed."
	}
}

func writeLocalAppPreviewResponse(
	w http.ResponseWriter,
	status int,
	result appPreviewOperationResult,
	code, detail string,
) {
	snapshot := result.Snapshot
	response := localAppPreviewResponse{
		SchemaVersion: 2,
		Lifecycle:     snapshot.Lifecycle, Availability: snapshot.Availability,
		Reason: snapshot.Reason, URL: snapshot.URL, Ports: snapshot.Ports,
		StopToken:  result.StopToken,
		LeaseTTLMS: result.LeaseTTL.Milliseconds(), RenewAfterMS: result.RenewAfter.Milliseconds(),
		Code: code, Detail: detail,
	}
	if snapshot.CompanyID != uuid.Nil {
		response.CompanyID = snapshot.CompanyID.String()
		response.OutputID = snapshot.OutputID.String()
		response.RunID = snapshot.RunID.String()
		response.RuntimeID = snapshot.RuntimeID.String()
	}
	if snapshot.ActivationID != uuid.Nil {
		response.ActivationID = snapshot.ActivationID.String()
	}
	if !snapshot.LastObservedAt.IsZero() {
		response.LastObservedAt = snapshot.LastObservedAt.UTC().Format(time.RFC3339Nano)
	}
	if result.LeaseID != uuid.Nil {
		response.LeaseID = result.LeaseID.String()
	}
	if !result.LeaseExpires.IsZero() {
		response.LeaseExpiresAt = result.LeaseExpires.UTC().Format(time.RFC3339Nano)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}
