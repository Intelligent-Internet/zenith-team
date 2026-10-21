package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

const (
	localConnectorConnectPath = "/v1/connectors/connect"
	localConnectorRefreshPath = "/v1/connectors/refresh"
	localConnectorStatusPath  = "/v1/connectors/status"
	localPluginInstallPath    = "/v1/plugins/install"
	localPluginUninstallPath  = "/v1/plugins/uninstall"
	localPluginRefreshPath    = "/v1/plugins/refresh"
	localPluginIconPath       = "/v1/plugins/icon"
	localPluginReadPath       = "/v1/plugins/read"
	maximumLocalOperationBody = 4 << 10
	maximumPluginIconBytes    = 4 << 20
)

type localCapabilityRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	CompanyID     string `json:"companyId"`
	RuntimeID     string `json:"runtimeId"`
	Provider      string `json:"provider"`
	Source        string `json:"source,omitempty"`
	NativeID      string `json:"nativeId,omitempty"`
	// Force asks a refresh to rediscover from the provider. Absent means
	// true, so an older desktop keeps its forced refresh.
	Force *bool `json:"force,omitempty"`
}

// forced reports whether a refresh request rediscovers from the provider.
func (r localCapabilityRequest) forced() bool { return r.Force == nil || *r.Force }

type localCapabilityResponse struct {
	OK    bool `json:"ok"`
	Value any  `json:"value"`
}

type localCapabilityError struct {
	OK     bool   `json:"ok"`
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

func (d *Daemon) localConnectorConnectHandler() http.HandlerFunc {
	return d.localCapabilityOperationHandler(func(r *http.Request, request localCapabilityRequest) (any, error) {
		if !validLocalConnectorSource(request.Provider, request.Source) ||
			!validLocalNativeID(request.NativeID) {
			return nil, errInvalidLocalCapabilityRequest
		}
		return d.connectors.connect(
			r.Context(), request.CompanyID, request.RuntimeID,
			request.Provider, request.Source, request.NativeID,
		)
	})
}

func (d *Daemon) localConnectorStatusHandler() http.HandlerFunc {
	return d.localCapabilityOperationHandler(func(r *http.Request, request localCapabilityRequest) (any, error) {
		if !validLocalConnectorSource(request.Provider, request.Source) ||
			!validLocalNativeID(request.NativeID) {
			return nil, errInvalidLocalCapabilityRequest
		}
		return d.connectors.status(
			r.Context(), request.CompanyID, request.RuntimeID,
			request.Provider, request.Source, request.NativeID,
		)
	})
}

func (d *Daemon) localConnectorRefreshHandler() http.HandlerFunc {
	return d.localCapabilityOperationHandler(func(r *http.Request, request localCapabilityRequest) (any, error) {
		return d.connectors.refreshRuntime(
			r.Context(), request.CompanyID, request.RuntimeID, request.Provider, request.forced(),
		)
	})
}

func (d *Daemon) localPluginActionHandler(install bool) http.HandlerFunc {
	return d.localCapabilityOperationHandler(func(r *http.Request, request localCapabilityRequest) (any, error) {
		if !validLocalNativeID(request.NativeID) {
			return nil, errInvalidLocalCapabilityRequest
		}
		if install {
			return d.plugins.install(
				r.Context(), request.CompanyID, request.RuntimeID, request.Provider, request.NativeID,
			)
		}
		return d.plugins.uninstall(
			r.Context(), request.CompanyID, request.RuntimeID, request.Provider, request.NativeID,
		)
	})
}

func (d *Daemon) localPluginRefreshHandler() http.HandlerFunc {
	return d.localCapabilityOperationHandler(func(r *http.Request, request localCapabilityRequest) (any, error) {
		return d.plugins.refreshRuntime(
			r.Context(), request.CompanyID, request.RuntimeID, request.Provider, request.forced(),
		)
	})
}

// localPluginReadHandler answers the desktop plugin page with a live read.
// Codex apps carry the logo the daemon's own app catalog observed for them
// when it loads outside a ChatGPT session; a session-only one is left out so
// the page keeps the loadable logo the server chose for that app.
func (d *Daemon) localPluginReadHandler() http.HandlerFunc {
	return d.localCapabilityOperationHandler(func(r *http.Request, request localCapabilityRequest) (any, error) {
		if !validLocalNativeID(request.NativeID) {
			return nil, errInvalidLocalCapabilityRequest
		}
		detail, err := d.plugins.readDetail(
			r.Context(), request.CompanyID, request.RuntimeID, request.Provider, request.NativeID,
		)
		if err != nil {
			return nil, err
		}
		if request.Provider == "codex" && len(detail.Apps) != 0 && d.connectors != nil {
			icons := d.connectors.iconURLs(request.Provider, string(CodexConnectorSourceHostedApp))
			for index := range detail.Apps {
				icon := icons[detail.Apps[index].ID]
				if detail.Apps[index].IconURL == "" && !sessionOnlyCodexIconURL(icon) {
					detail.Apps[index].IconURL = icon
				}
			}
		}
		return detail, nil
	})
}

func (d *Daemon) localPluginIconHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		request, ok := d.decodeLocalCapabilityRequest(w, r)
		if !ok || !validLocalNativeID(request.NativeID) {
			writeLocalCapabilityError(
				w,
				http.StatusBadRequest,
				"invalid_request",
				"Plugin icon request is invalid.",
			)
			return
		}
		icon, err := d.plugins.readIcon(
			r.Context(), request.CompanyID, request.RuntimeID, request.Provider, request.NativeID,
		)
		if err != nil {
			writeLocalCapabilityServiceError(w, err)
			return
		}
		if !validLocalPluginIcon(icon) {
			writeLocalCapabilityError(
				w,
				http.StatusInternalServerError,
				"invalid_response",
				"Plugin icon is invalid.",
			)
			return
		}
		w.Header().Set("Cache-Control", "private, max-age=300")
		w.Header().Set("Content-Type", icon.ContentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if icon.ContentType == "image/svg+xml" {
			w.Header().Set(
				"Content-Security-Policy",
				"default-src 'none'; style-src 'unsafe-inline'; sandbox",
			)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(icon.Bytes)
	}
}

var errInvalidLocalCapabilityRequest = errors.New("local capability request is invalid")

func (d *Daemon) localCapabilityOperationHandler(
	operation func(*http.Request, localCapabilityRequest) (any, error),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		request, ok := d.decodeLocalCapabilityRequest(w, r)
		if !ok {
			return
		}
		result, err := operation(r, request)
		if errors.Is(err, errInvalidLocalCapabilityRequest) {
			writeLocalCapabilityError(
				w,
				http.StatusBadRequest,
				"invalid_request",
				"Capability request is invalid.",
			)
			return
		}
		if err != nil {
			writeLocalCapabilityServiceError(w, err)
			return
		}
		writeLocalCapabilityJSON(w, http.StatusOK, localCapabilityResponse{OK: true, Value: result})
	}
}

func (d *Daemon) decodeLocalCapabilityRequest(
	w http.ResponseWriter,
	r *http.Request,
) (localCapabilityRequest, bool) {
	if r.Method != http.MethodPost {
		writeLocalCapabilityError(
			w,
			http.StatusMethodNotAllowed,
			"method_not_allowed",
			"Method is not allowed.",
		)
		return localCapabilityRequest{}, false
	}
	if d.localPreviewToken == "" ||
		!validLocalPreviewAuthorization(r.Header.Get("Authorization"), d.localPreviewToken) {
		w.Header().Set("Cache-Control", "no-store")
		writeLocalCapabilityError(w, http.StatusUnauthorized, "unauthorized", "Local capability is invalid.")
		return localCapabilityRequest{}, false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maximumLocalOperationBody))
	decoder.DisallowUnknownFields()
	var request localCapabilityRequest
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		request.SchemaVersion != 1 || !validLocalRuntimeBinding(request.CompanyID, request.RuntimeID) ||
		(request.Provider != "claude" && request.Provider != "codex") {
		writeLocalCapabilityError(w, http.StatusBadRequest, "invalid_request", "Capability request is invalid.")
		return localCapabilityRequest{}, false
	}
	return request, true
}

func validLocalRuntimeBinding(companyID, runtimeID string) bool {
	_, companyErr := util.ParseGoUUID(companyID)
	_, runtimeErr := util.ParseGoUUID(runtimeID)
	return companyErr == nil && runtimeErr == nil
}

func validLocalConnectorSource(provider, source string) bool {
	switch provider {
	case "codex":
		return source == "codex_app" || source == "codex_mcp"
	case "claude":
		return source == "claude_plugin" || source == "claude_mcp" || source == "claude_cloud"
	default:
		return false
	}
}

func validLocalNativeID(value string) bool {
	if value == "" || len(value) > 240 || !utf8.ValidString(value) || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validLocalPluginIcon(icon pluginIcon) bool {
	if len(icon.Bytes) == 0 || len(icon.Bytes) > maximumPluginIconBytes {
		return false
	}
	switch icon.ContentType {
	case "image/png":
		return bytes.HasPrefix(icon.Bytes, []byte("\x89PNG\r\n\x1a\n"))
	case "image/jpeg":
		return len(icon.Bytes) >= 3 && icon.Bytes[0] == 0xff && icon.Bytes[1] == 0xd8 && icon.Bytes[2] == 0xff
	case "image/webp":
		return len(icon.Bytes) >= 12 && string(icon.Bytes[:4]) == "RIFF" && string(icon.Bytes[8:12]) == "WEBP"
	case "image/svg+xml":
		return validPluginSVGIcon(icon.Bytes)
	default:
		return false
	}
}

func writeLocalCapabilityServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errConnectorsUnavailable), errors.Is(err, errPluginsUnavailable):
		writeLocalCapabilityError(
			w,
			http.StatusServiceUnavailable,
			"daemon_unavailable",
			"Capability management is unavailable.",
		)
	case errors.Is(err, errConnectorNotDiscovered), errors.Is(err, errPluginNotDiscovered):
		writeLocalCapabilityError(
			w,
			http.StatusNotFound,
			"capability_not_discovered",
			"Refresh the catalog and try again.",
		)
	case errors.Is(err, errPluginActionInvalid):
		writeLocalCapabilityError(
			w,
			http.StatusConflict,
			"plugin_action_invalid",
			"Plugin action is not currently available.",
		)
	case errors.Is(err, errPluginProviderMismatch):
		writeLocalCapabilityError(
			w,
			http.StatusConflict,
			"plugin_provider_mismatch",
			"The runtime uses a different provider.",
		)
	case errors.Is(err, errPluginIconUnavailable):
		writeLocalCapabilityError(
			w,
			http.StatusNotFound,
			"plugin_icon_unavailable",
			"Plugin icon is unavailable.",
		)
	default:
		writeLocalCapabilityError(
			w,
			http.StatusInternalServerError,
			"operation_failed",
			"Capability operation failed.",
		)
	}
}

func writeLocalCapabilityError(w http.ResponseWriter, status int, code, reason string) {
	writeLocalCapabilityJSON(w, status, localCapabilityError{Code: code, Reason: reason})
}

func writeLocalCapabilityJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
