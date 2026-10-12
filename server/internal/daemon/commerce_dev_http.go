package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"boardly.dev/boardly/server/internal/runchannel"
)

const (
	commerceDevLeaseCreatePath = "/v1/commerce/dev/leases"
	commerceDevLeaseRenewPath  = "/v1/commerce/dev/leases/renew"
	commerceDevLeaseRevokePath = "/v1/commerce/dev/leases/revoke"
	commerceDevSignPath        = "/v1/commerce/dev/sign"
)

var errCommerceDevControlUnavailable = errors.New(
	"local Commerce signing is available only inside a Zenith Team Task run",
)

// CommerceDevControlConfig is the run-scoped bootstrap document written only
// into a Task's isolated Boardly home and removed with that run.
type CommerceDevControlConfig struct {
	SchemaVersion int    `json:"schemaVersion"`
	Endpoint      string `json:"endpoint"`
	Capability    string `json:"capability"`
}

type commerceDevLeaseRequest struct {
	SchemaVersion int                     `json:"schemaVersion"`
	CompanyID     string                  `json:"companyId"`
	Site          CommerceDevManifestSite `json:"site"`
	OwnerPID      int                     `json:"ownerPid"`
}

type commerceDevLeaseLifecycleRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	LeaseID       string `json:"leaseId"`
	OwnerPID      int    `json:"ownerPid"`
}

type commerceDevLeaseRenewResponse struct {
	SchemaVersion int       `json:"schemaVersion"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

type commerceDevErrorResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	Code          string `json:"code"`
	Reason        string `json:"reason"`
}

func (d *Daemon) commerceDevLeaseCreateHandler(localBaseURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeCommerceDevError(
				w,
				http.StatusMethodNotAllowed,
				"method_not_allowed",
				"Method not allowed.",
			)
			return
		}
		var request commerceDevLeaseRequest
		if !decodeCommerceDevJSON(w, r, &request) {
			return
		}
		if request.SchemaVersion != 1 {
			writeCommerceDevError(w, http.StatusBadRequest, "invalid_request", "Request body is invalid.")
			return
		}
		authority, releaseAuthorization, authorized := d.commerceDevBootstrapAuthorization(
			r.Header.Get("Authorization"),
		)
		if !authorized {
			writeCommerceDevError(
				w,
				http.StatusUnauthorized,
				"unauthorized",
				"Local capability is invalid.",
			)
			return
		}
		defer releaseAuthorization()
		if authority.scoped && authority.companyID != request.CompanyID {
			writeCommerceDevError(
				w,
				http.StatusUnauthorized,
				"unauthorized",
				"Local capability is invalid.",
			)
			return
		}
		if d.commerceDev == nil {
			writeCommerceDevError(
				w,
				http.StatusServiceUnavailable,
				"commerce_unavailable",
				"Local Commerce is unavailable.",
			)
			return
		}
		requestContext := r.Context()
		cancelRequest := func() {}
		if authority.scoped {
			var cancel context.CancelFunc
			requestContext, cancel = context.WithCancel(requestContext)
			stop := context.AfterFunc(authority.ctx, cancel)
			cancelRequest = func() {
				stop()
				cancel()
			}
		}
		defer cancelRequest()
		lease, err := d.commerceDev.createLeaseForAuthority(
			requestContext,
			request.CompanyID,
			request.Site,
			request.OwnerPID,
			localBaseURL,
			authority,
		)
		if err != nil {
			d.logger.Debug("create local Commerce lease failed", "error", err)
			status := http.StatusBadGateway
			code := "lease_failed"
			if errors.Is(err, errCommerceDevLeaseInvalid) {
				status = http.StatusBadRequest
				code = "invalid_request"
			}
			writeCommerceDevError(w, status, code, "Unable to create a local Commerce signing lease.")
			return
		}
		writeCommerceDevJSON(w, http.StatusCreated, lease)
	}
}

func (d *Daemon) commerceDevLeaseRenewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeCommerceDevError(
				w,
				http.StatusMethodNotAllowed,
				"method_not_allowed",
				"Method not allowed.",
			)
			return
		}
		leaseCapability, authorized := d.commerceDevLifecycleAuthorization(
			r.Header.Get("Authorization"),
		)
		if !authorized {
			writeCommerceDevError(
				w,
				http.StatusUnauthorized,
				"unauthorized",
				"Local capability is invalid.",
			)
			return
		}
		if d.commerceDev == nil {
			writeCommerceDevError(
				w,
				http.StatusServiceUnavailable,
				"commerce_unavailable",
				"Local Commerce is unavailable.",
			)
			return
		}
		var request commerceDevLeaseLifecycleRequest
		if !decodeCommerceDevJSON(w, r, &request) {
			return
		}
		if request.SchemaVersion != 1 {
			writeCommerceDevError(w, http.StatusBadRequest, "invalid_request", "Request body is invalid.")
			return
		}
		expiresAt, err := d.commerceDev.renewWithCapability(
			leaseCapability,
			request.LeaseID,
			request.OwnerPID,
		)
		if err != nil {
			writeCommerceDevError(
				w,
				http.StatusUnauthorized,
				"lease_invalid",
				"Local Commerce signing lease is invalid.",
			)
			return
		}
		writeCommerceDevJSON(w, http.StatusOK, commerceDevLeaseRenewResponse{
			SchemaVersion: 1,
			ExpiresAt:     expiresAt,
		})
	}
}

func (d *Daemon) commerceDevLeaseRevokeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeCommerceDevError(
				w,
				http.StatusMethodNotAllowed,
				"method_not_allowed",
				"Method not allowed.",
			)
			return
		}
		leaseCapability, authorized := d.commerceDevLifecycleAuthorization(
			r.Header.Get("Authorization"),
		)
		if !authorized {
			writeCommerceDevError(
				w,
				http.StatusUnauthorized,
				"unauthorized",
				"Local capability is invalid.",
			)
			return
		}
		if d.commerceDev == nil {
			writeCommerceDevError(
				w,
				http.StatusServiceUnavailable,
				"commerce_unavailable",
				"Local Commerce is unavailable.",
			)
			return
		}
		var request commerceDevLeaseLifecycleRequest
		if !decodeCommerceDevJSON(w, r, &request) {
			return
		}
		if request.SchemaVersion != 1 {
			writeCommerceDevError(w, http.StatusBadRequest, "invalid_request", "Request body is invalid.")
			return
		}
		err := d.commerceDev.revokeWithCapability(
			leaseCapability,
			request.LeaseID,
			request.OwnerPID,
		)
		if err != nil {
			writeCommerceDevError(
				w,
				http.StatusUnauthorized,
				"lease_invalid",
				"Local Commerce signing lease is invalid.",
			)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d *Daemon) commerceDevSignHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		capability, ok := parseCommerceDevAuthorization(
			r.Header.Get("Authorization"), "BoardlyCommerceLease",
		)
		if !ok || d.commerceDev == nil {
			writeCommerceDevError(
				w,
				http.StatusUnauthorized,
				"lease_invalid",
				"Local Commerce signing lease is invalid.",
			)
			return
		}
		switch r.Method {
		case http.MethodGet:
			metadata, err := d.commerceDev.metadata(capability)
			if err != nil {
				writeCommerceDevError(
					w,
					http.StatusUnauthorized,
					"lease_invalid",
					"Local Commerce signing lease is invalid.",
				)
				return
			}
			writeCommerceDevJSON(w, http.StatusOK, metadata)
		case http.MethodPost:
			var request CommerceDevSignRequest
			if !decodeCommerceDevJSON(w, r, &request) {
				return
			}
			response, err := d.commerceDev.sign(capability, request)
			if err != nil {
				status := http.StatusBadRequest
				code := "signing_request_invalid"
				if errors.Is(err, errCommerceDevLeaseInvalid) {
					status = http.StatusUnauthorized
					code = "lease_invalid"
				}
				writeCommerceDevError(w, status, code, "Local Commerce signing request was rejected.")
				return
			}
			writeCommerceDevJSON(w, http.StatusOK, response)
		default:
			writeCommerceDevError(
				w,
				http.StatusMethodNotAllowed,
				"method_not_allowed",
				"Method not allowed.",
			)
		}
	}
}

func decodeCommerceDevJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeCommerceDevError(
			w,
			http.StatusUnsupportedMediaType,
			"invalid_content_type",
			"Content-Type must be application/json.",
		)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		writeCommerceDevError(w, http.StatusBadRequest, "invalid_request", "Request body is invalid.")
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeCommerceDevError(w, http.StatusBadRequest, "invalid_request", "Request body is invalid.")
		return false
	}
	return true
}

func writeCommerceDevJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeCommerceDevError(w http.ResponseWriter, status int, code, reason string) {
	writeCommerceDevJSON(w, status, commerceDevErrorResponse{
		SchemaVersion: 1,
		Code:          code,
		Reason:        reason,
	})
}

func (d *Daemon) commerceDevLifecycleAuthorization(header string) (string, bool) {
	capability, ok := parseCommerceDevAuthorization(header, "BoardlyCommerceLease")
	if !ok || !validCommerceDevCapability(capability) {
		return "", false
	}
	return capability, true
}

func parseCommerceDevAuthorization(header, scheme string) (string, bool) {
	prefix := scheme + " "
	if !strings.HasPrefix(header, prefix) || strings.Count(header, " ") != 1 {
		return "", false
	}
	capability := strings.TrimPrefix(header, prefix)
	return capability, capability != ""
}

func writeCommerceDevControlConfig(directory, endpoint, capability string) (string, error) {
	if !runchannel.ValidEndpoint(endpoint) || !validCommerceDevCapability(capability) {
		return "", errors.New("local Commerce control configuration is invalid")
	}
	encoded, err := json.MarshalIndent(CommerceDevControlConfig{
		SchemaVersion: 1,
		Endpoint:      endpoint,
		Capability:    capability,
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode local Commerce control configuration: %w", err)
	}
	path := filepath.Join(directory, "commerce-dev.json")
	if err := writePrivateFile(path, append(encoded, '\n')); err != nil {
		return "", err
	}
	return path, nil
}

// LoadCommerceDevControlConfig reads the current Task's scoped local Commerce
// endpoint and bootstrap capability. A normal operator home carries no such
// document, so standalone invocations fail closed.
func LoadCommerceDevControlConfig() (CommerceDevControlConfig, error) {
	home, err := BoardlyHome()
	if err != nil {
		return CommerceDevControlConfig{}, err
	}
	path := filepath.Join(home, "commerce-dev.json")
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return CommerceDevControlConfig{}, errCommerceDevControlUnavailable
	}
	if err != nil {
		return CommerceDevControlConfig{}, fmt.Errorf("open local Commerce control configuration: %w", err)
	}
	defer file.Close()
	if runtime.GOOS != "windows" {
		info, err := file.Stat()
		if err != nil {
			return CommerceDevControlConfig{}, fmt.Errorf(
				"stat local Commerce control configuration: %w",
				err,
			)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return CommerceDevControlConfig{}, errors.New(
				"local Commerce control configuration permissions are too open",
			)
		}
	}
	var config CommerceDevControlConfig
	decoder := json.NewDecoder(io.LimitReader(file, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return CommerceDevControlConfig{}, fmt.Errorf("decode local Commerce control configuration: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return CommerceDevControlConfig{}, errors.New("local Commerce control configuration has trailing data")
	}
	if config.SchemaVersion != 1 || !runchannel.ValidEndpoint(config.Endpoint) ||
		!validCommerceDevCapability(config.Capability) {
		return CommerceDevControlConfig{}, errors.New("local Commerce control configuration is invalid")
	}
	return config, nil
}

func (d *Daemon) prepareCommerceDev(listener net.Listener) (func(), error) {
	home, err := BoardlyHome()
	if err != nil {
		return func() {}, err
	}
	if err := removeLegacyCommerceDevAuthority(home); err != nil {
		return func() {}, err
	}
	d.commerceDevEndpoint = "http://" + listener.Addr().String()
	d.commerceDev = newCommerceDevService(d.client, d.cfg.ServerBaseURL)
	service := d.commerceDev
	return func() {
		service.clear()
	}, nil
}

func removeLegacyCommerceDevAuthority(home string) error {
	legacyControl := filepath.Join(home, "commerce-dev.json")
	if err := os.Remove(legacyControl); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove legacy local Commerce control capability: %w", err)
	}
	legacyKeys := filepath.Join(home, "commerce", "dev-signing")
	if err := os.RemoveAll(legacyKeys); err != nil {
		return fmt.Errorf("remove legacy persistent Commerce signing keys: %w", err)
	}
	return nil
}

// CommerceDevLease is the CLI-side handle supervising one child process.
type CommerceDevLease struct {
	CommerceDevLeaseResponse
	control  CommerceDevControlConfig
	ownerPID int
	client   *http.Client
}

// OpenCommerceDevLease asks the running daemon to bind its in-memory public
// key and exchange this Task's bootstrap for a short-lived signer capability.
func OpenCommerceDevLease(
	ctx context.Context,
	companyID string,
	site CommerceDevManifestSite,
	ownerPID int,
) (*CommerceDevLease, error) {
	control, err := LoadCommerceDevControlConfig()
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	var response CommerceDevLeaseResponse
	if err := doCommerceDevControlRequest(ctx, client, control, "BoardlyCommerceTask", commerceDevLeaseCreatePath,
		commerceDevLeaseRequest{
			SchemaVersion: 1,
			CompanyID:     companyID,
			Site:          site,
			OwnerPID:      ownerPID,
		}, &response); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if response.SchemaVersion != 1 || !validCommerceOperationID(response.LeaseID) ||
		!validCommerceOperationID(response.SiteID) ||
		!validCommerceDevCapability(response.SignerCapability) ||
		!response.ExpiresAt.After(now) ||
		response.ExpiresAt.After(now.Add(commerceDevLeaseLifetime+5*time.Second)) ||
		!validCommerceDevSignerURL(response.SignerURL, control.Endpoint) ||
		!validCommerceAPIBaseURL(response.APIBaseURL) || response.SiteOrigin != site.Origin ||
		response.Environment != "sandbox" || response.KeyID == "" {
		return nil, errors.New("daemon returned an invalid local Commerce signing lease")
	}
	fingerprint, err := commercePublicJWKFingerprint(response.PublicJWK)
	if err != nil || fingerprint != response.KeyID {
		return nil, errors.New("daemon returned an invalid local Commerce public key")
	}
	return &CommerceDevLease{
		CommerceDevLeaseResponse: response,
		control: CommerceDevControlConfig{
			SchemaVersion: control.SchemaVersion,
			Endpoint:      control.Endpoint,
		},
		ownerPID: ownerPID,
		client:   client,
	}, nil
}

// Renew extends the lease while the supervising CLI process remains alive.
func (l *CommerceDevLease) Renew(ctx context.Context) error {
	var response commerceDevLeaseRenewResponse
	lifecycleControl := l.control
	lifecycleControl.Capability = l.SignerCapability
	if err := doCommerceDevControlRequest(
		ctx,
		l.client,
		lifecycleControl,
		"BoardlyCommerceLease",
		commerceDevLeaseRenewPath,
		commerceDevLeaseLifecycleRequest{
			SchemaVersion: 1,
			LeaseID:       l.LeaseID,
			OwnerPID:      l.ownerPID,
		}, &response); err != nil {
		return err
	}
	now := time.Now().UTC()
	if response.SchemaVersion != 1 || !response.ExpiresAt.After(now) ||
		response.ExpiresAt.After(now.Add(commerceDevLeaseLifetime+5*time.Second)) {
		return errors.New("daemon returned an invalid local Commerce lease renewal")
	}
	l.ExpiresAt = response.ExpiresAt
	return nil
}

// Close revokes the signer capability. It is idempotent server-side.
func (l *CommerceDevLease) Close(ctx context.Context) error {
	lifecycleControl := l.control
	lifecycleControl.Capability = l.SignerCapability
	return doCommerceDevControlRequest(
		ctx,
		l.client,
		lifecycleControl,
		"BoardlyCommerceLease",
		commerceDevLeaseRevokePath,
		commerceDevLeaseLifecycleRequest{
			SchemaVersion: 1,
			LeaseID:       l.LeaseID,
			OwnerPID:      l.ownerPID,
		}, nil)
}

// EnvironmentVariables returns exactly the configuration inherited by the
// child. The private key and daemon control capability are deliberately absent.
func (l *CommerceDevLease) EnvironmentVariables() []string {
	return []string{
		"BOARDLY_COMMERCE_API_URL=" + l.APIBaseURL,
		"BOARDLY_COMMERCE_SITE_ORIGIN=" + l.SiteOrigin,
		"BOARDLY_COMMERCE_SITE_ID=" + l.SiteID,
		"BOARDLY_COMMERCE_ENVIRONMENT=" + l.Environment,
		"BOARDLY_COMMERCE_SIGNER_URL=" + l.SignerURL,
		"BOARDLY_COMMERCE_SIGNER_CAPABILITY=" + l.SignerCapability,
		"BOARDLY_COMMERCE_SIGNER_KEY_ID=" + l.KeyID,
	}
}

func doCommerceDevControlRequest(
	ctx context.Context,
	client *http.Client,
	control CommerceDevControlConfig,
	authorizationScheme string,
	path string,
	requestBody, responseBody any,
) error {
	encoded, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("encode local Commerce request: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		strings.TrimRight(control.Endpoint, "/")+path,
		bytes.NewReader(encoded),
	)
	if err != nil {
		return fmt.Errorf("create local Commerce request: %w", err)
	}
	request.Header.Set("Authorization", authorizationScheme+" "+control.Capability)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("reach the local Zenith Team daemon: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure commerceDevErrorResponse
		_ = json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&failure)
		reason := strings.TrimSpace(failure.Reason)
		if reason == "" {
			reason = http.StatusText(response.StatusCode)
		}
		return fmt.Errorf("local Commerce daemon request failed: %s", reason)
	}
	if responseBody == nil || response.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 16<<10))
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(responseBody)
}

func validCommerceDevSignerURL(value, endpoint string) bool {
	return value == strings.TrimRight(endpoint, "/")+commerceDevSignPath
}
