package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/daemonws"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/usage"
	"boardly.dev/boardly/server/internal/util"
	provideragent "boardly.dev/boardly/server/pkg/agent"
	db "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
	"boardly.dev/boardly/server/pkg/runfailure"
)

// The daemon plane: the
// HTTP surface the local daemon uses to register runtimes, heartbeat, claim
// runs, and report run lifecycle transitions. Auth is a bearer user session
// token; company checks answer 404-not-403 so an unauthorized probe cannot
// distinguish "exists" from "denied".

const maxDaemonBodyBytes = 4 << 20

const claimBatchMaxRunsCap = 32

func (h *Handler) registerDaemonRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.RunService == nil || h.Queries == nil || h.Bus == nil {
		return
	}
	routes := group.Group("/daemon", middleware.RequireDaemonAuth(h.AuthService))
	routes.GET("/config", h.daemonConfig)
	routes.GET("/companies", h.daemonListCompanies)
	if h.DaemonWSHub != nil {
		routes.GET("/ws", h.daemonWebSocket)
	}

	routes.POST("/runs/claim", h.daemonClaimRunsBatch)
	routes.POST("/runtimes/:runtimeId/runs/:runId/prepare-lease", h.daemonExtendRunPrepareLease)
	routes.POST("/runtimes/:runtimeId/runs/:runId/skill-bundles/resolve", h.daemonResolveSkillBundles)
	routes.GET(
		"/runtimes/:runtimeId/runs/:runId/attachments/:attachmentId",
		h.daemonDownloadRunAttachment,
	)
	routes.POST("/runtimes/:runtimeId/tools", h.daemonReportTools)
	if h.AgentBindingService != nil {
		routes.GET("/runtimes/:runtimeId/agent-bindings", h.daemonRuntimeBindings)
	}
	routes.POST("/runtimes/:runtimeId/runs/:runId/studio-project", h.daemonStudioProject)
	routes.POST("/runtimes/:runtimeId/recover-orphans", h.daemonRecoverOrphans)
	// Registration, heartbeat, and their lifecycle logic live in
	// agent.RuntimeService; the routes exist only when that service is wired.
	if h.RuntimeService != nil {
		routes.POST("/register", h.daemonRegister)
		routes.POST("/deregister", h.daemonDeregister)
		routes.POST("/heartbeat", h.daemonHeartbeat)
		routes.POST(
			"/runtimes/:runtimeId/model-discoveries/:requestId/result",
			h.daemonReportRuntimeModelDiscovery,
		)
	}
	h.registerTaskMergeRoutes(routes)

	routes.GET("/runs/:runId/status", h.daemonGetRunStatus)
	routes.POST("/runs/:runId/start", h.daemonStartRun)
	routes.POST("/runs/:runId/wait-local-directory", h.daemonMarkRunWaitingLocalDirectory)
	routes.POST("/runs/:runId/wait-studio-auth", h.daemonWaitForStudioAuth)
	if h.UsageService != nil {
		routes.POST("/runs/:runId/usage", h.daemonReportRunUsage)
	}
	routes.POST("/runs/:runId/session", h.daemonPinRunSession)
	routes.POST("/runs/:runId/complete", h.daemonCompleteRun)
	routes.POST("/runs/:runId/fail", h.daemonFailRun)
	routes.POST("/runs/:runId/cancel-ack", h.daemonAckRunCancelled)
}

func (h *Handler) daemonConfig(c *gin.Context) {
	c.JSON(http.StatusOK, protocol.DaemonConfig{StudioAPIOrigin: h.StudioAPIOrigin})
}

type daemonModelDiscoveryResultRequest struct {
	Status agent.ModelDiscoveryStatus   `json:"status"          binding:"required,oneof=completed failed"`
	Models []provideragent.RuntimeModel `json:"models"          binding:"max=100"`
	Error  *agent.ModelDiscoveryError   `json:"error,omitempty"`
}

type daemonStatusResponse struct {
	Status string `json:"status"`
}

func (h *Handler) daemonReportRuntimeModelDiscovery(c *gin.Context) {
	runtime, ok := h.requireDaemonRuntimeAccess(c, c.Param("runtimeId"))
	if !ok {
		return
	}
	requestID, err := util.ParseGoUUID(c.Param("requestId"))
	if err != nil {
		writeRuntimeError(c, agent.ErrModelDiscoveryNotFound)
		return
	}
	request, err := bindRequest[daemonModelDiscoveryResultRequest](c, 1<<20)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	_, err = h.RuntimeService.FinishModelDiscovery(
		c.Request.Context(),
		runtime.ID,
		requestID,
		agent.ModelDiscoveryResult{
			Status: request.Status,
			Models: request.Models,
			Error:  request.Error,
		},
	)
	if err != nil {
		writeRuntimeError(c, err)
		return
	}
	c.JSON(http.StatusOK, daemonStatusResponse{Status: "ok"})
}

// daemonPrincipal reads the principal requireDaemonAuth installed on the
// request context; it verifies nothing itself.
func daemonPrincipal(c *gin.Context) (auth.UserPrincipal, bool) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return auth.UserPrincipal{}, false
	}
	return principal, true
}

// requireDaemonCompanyAccess verifies the caller is an active member of the
// company. Denials answer 404-not-403.
func (h *Handler) requireDaemonCompanyAccess(c *gin.Context, companyID uuid.UUID) bool {
	principal, ok := daemonPrincipal(c)
	if !ok {
		return false
	}
	if !h.verifyDaemonCompanyAccess(c.Request.Context(), principal, companyID) {
		writeBoundaryError(c, boundaryErrorCompanyNotFoundLowercase)
		return false
	}
	return true
}

// verifyDaemonCompanyAccess checks company access without writing an HTTP
// error. Used in loops where individual items are skipped silently, and by
// the transport-free claim core.
func (h *Handler) verifyDaemonCompanyAccess(
	ctx context.Context,
	principal auth.UserPrincipal,
	companyID uuid.UUID,
) bool {
	if companyID == uuid.Nil {
		return false
	}
	_, err := h.Queries.GetActiveCompanyMembership(ctx, db.GetActiveCompanyMembershipParams{
		CompanyID: companyID,
		UserID:    principal.UserID,
	})
	return err == nil
}

// requireDaemonID reads the machine identity the daemon sends on every
// request (X-Boardly-Daemon-ID, the `daemonId` it registered with) and
// applies the rollout stage to a missing one: admitted as the unfenced
// empty id with a warning log, or — when RequireDaemonID is on — refused
// with 400 invalid_request before any database access. The 400 is
// deliberate: a 404 on heartbeat is the daemon's runtime-gone signal and
// would send an old CLI into a prune / re-register loop, whereas a 400 only
// logs and lets the stale sweep take the runtime offline.
func (h *Handler) requireDaemonID(c *gin.Context) (string, bool) {
	daemonID := strings.TrimSpace(c.GetHeader(middleware.HeaderDaemonID))
	if daemonID != "" {
		return daemonID, true
	}
	if h.RequireDaemonID {
		writeBoundaryError(c, boundaryErrorDaemonIDRequired)
		return "", false
	}
	userID := ""
	if principal, ok := auth.UserPrincipalFromContext(c.Request.Context()); ok {
		userID = principal.UserID.String()
	}
	slog.Warn("daemon request without daemon id header",
		"route", c.FullPath(), "user_id", userID)
	return "", true
}

// requireDaemonRuntimeAccess looks up a runtime and verifies the calling
// daemon registered it and the caller may act for its company. The daemon
// check runs first: it costs no statement and its answer — the same 404 as
// a runtime that does not exist — discloses nothing either way. A daemon
// that sent no id (rollout, see requireDaemonID) skips that check and is
// gated by membership alone.
//
// Only pgx.ErrNoRows is a real "runtime gone" 404 — the daemon uses that
// response to drop the stale runtime from its in-memory map and re-register,
// so collapsing transient DB errors into the same 404 would force the daemon
// to self-cleanup on a hiccup. Other DB errors become 500.
func (h *Handler) requireDaemonRuntimeAccess(c *gin.Context, runtimeID string) (db.Runtime, bool) {
	runtimeUUID, ok := parseUUIDOrBadRequest(c, runtimeID, "runtime_id")
	if !ok {
		return db.Runtime{}, false
	}
	daemonID, ok := h.requireDaemonID(c)
	if !ok {
		return db.Runtime{}, false
	}
	rt, err := h.Queries.GetRuntime(c.Request.Context(), runtimeUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBoundaryError(c, boundaryErrorRuntimeNotFound)
			return db.Runtime{}, false
		}
		slog.Warn("get runtime failed", "runtime_id", runtimeID, "error", err)
		writeBoundaryError(c, boundaryErrorRuntimeLoadFailed)
		return db.Runtime{}, false
	}
	if daemonID != "" && rt.DaemonID != daemonID {
		slog.Warn("daemon runtime access: daemon mismatch", "runtime_id", runtimeID)
		writeBoundaryError(c, boundaryErrorRuntimeNotFound)
		return db.Runtime{}, false
	}
	// A runtime is its registrant's alone (ADR 0014): the daemon speaks for
	// it only under the account that registered it. The same 404 as a
	// foreign daemon, so a row registered under another account is not an
	// oracle. A row with no stored registrant belongs to nobody.
	if principal, ok := auth.UserPrincipalFromContext(c.Request.Context()); !ok ||
		!rt.OwnerUserID.Valid || rt.OwnerUserID.UUID != principal.UserID {
		slog.Warn("daemon runtime access: registrant mismatch", "runtime_id", runtimeID)
		writeBoundaryError(c, boundaryErrorRuntimeNotFound)
		return db.Runtime{}, false
	}
	if !h.requireDaemonCompanyAccess(c, rt.CompanyID) {
		return db.Runtime{}, false
	}
	return rt, true
}

// requireDaemonRunAccess looks up a run and verifies the calling daemon
// speaks for the runtime it runs on and the caller may act for its company.
// Only pgx.ErrNoRows is a real "run gone" 404 — the daemon uses this signal
// to interrupt the running agent, so a transient DB error must not be
// reported as a deletion.
//
// A path id the database-generated UUID cannot have produced takes that same 404 rather
// than the 422 the uuid parser used to answer: it names a run that cannot
// exist, which the daemon must not be able to tell apart from one that was
// deleted.
func (h *Handler) requireDaemonRunAccess(c *gin.Context, runID string) (db.Run, bool) {
	id, ok := parseRunPathID(runID)
	if !ok {
		writeBoundaryError(c, boundaryErrorRunNotFound)
		return db.Run{}, false
	}
	daemonID, ok := h.requireDaemonID(c)
	if !ok {
		return db.Run{}, false
	}
	run, err := h.Queries.GetRun(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBoundaryError(c, boundaryErrorRunNotFound)
			return db.Run{}, false
		}
		slog.Warn("get run failed", "run_id", id, "error", err)
		writeBoundaryError(c, boundaryErrorRunLoadFailed)
		return db.Run{}, false
	}
	// The runtime gate's fence, applied to the run's runtime (ADR 0014): a
	// daemon speaks for a run only from the machine that registered its
	// runtime and under the account that did. Anything else is the same 404
	// as a run that does not exist, so another member's run id is not an
	// oracle. The read keeps a removed runtime visible, because a Run keeps
	// its runtime through a removal and its daemon still reports on it.
	rt, err := h.Queries.GetRunRuntime(c.Request.Context(), db.GetRunRuntimeParams{
		CompanyID: run.CompanyID,
		ID:        run.RuntimeID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBoundaryError(c, boundaryErrorRunNotFound)
			return db.Run{}, false
		}
		slog.Warn("get run runtime failed", "run_id", id, "error", err)
		writeBoundaryError(c, boundaryErrorRunLoadFailed)
		return db.Run{}, false
	}
	if daemonID != "" && rt.DaemonID != daemonID {
		slog.Warn("daemon run access: daemon mismatch", "run_id", id)
		writeBoundaryError(c, boundaryErrorRunNotFound)
		return db.Run{}, false
	}
	if principal, ok := auth.UserPrincipalFromContext(c.Request.Context()); !ok ||
		!rt.OwnerUserID.Valid || rt.OwnerUserID.UUID != principal.UserID {
		slog.Warn("daemon run access: registrant mismatch", "run_id", id)
		writeBoundaryError(c, boundaryErrorRunNotFound)
		return db.Run{}, false
	}
	if !h.requireDaemonCompanyAccess(c, run.CompanyID) {
		return db.Run{}, false
	}
	return run, true
}

// ---------------------------------------------------------------------------
// Registration, deregistration, heartbeat
// ---------------------------------------------------------------------------

// daemonRuntimeUnusableReason is the wire form of protocol.RuntimeUnusableReason
// with the binding rules the boundary enforces: both fields present, the
// message bounded at protocol.RuntimeUnusableMessageMaxLen. The code's
// vocabulary is checked in daemonRegister next to the provider's.
type daemonRuntimeUnusableReason struct {
	Code    string `json:"code"    binding:"required"`
	Message string `json:"message" binding:"required,max=256"`
}

type daemonRuntimeSignIn struct {
	BillingMode string `json:"billingMode" binding:"omitempty,oneof=api subscription unknown"`
	State       string `json:"state"       binding:"required"`
	Detail      string `json:"detail"      binding:"max=256"`
	CheckedAt   string `json:"checkedAt"   binding:"required"`
}

type daemonRegisterRuntime struct {
	Name           string                       `json:"name"`
	Provider       string                       `json:"provider"       binding:"required"`
	Version        string                       `json:"version"`
	Status         string                       `json:"status"`
	UnusableReason *daemonRuntimeUnusableReason `json:"unusableReason"`
	SignIn         *daemonRuntimeSignIn         `json:"signIn"`
	// CredentialIsolation is the runtime's report (Access TECH B3d). It is
	// checked and never stored: no access decision reads it (D31).
	CredentialIsolation string `json:"credentialIsolation" binding:"omitempty,oneof=enforced not_enforced"`
}

type daemonRegisterRequest struct {
	CompanyID  string                  `json:"companyId"  binding:"required,uuid"`
	DaemonID   string                  `json:"daemonId"   binding:"required"`
	DeviceName string                  `json:"deviceName"`
	CLIVersion string                  `json:"cliVersion"`
	LaunchedBy string                  `json:"launchedBy"`
	Runtimes   []daemonRegisterRuntime `json:"runtimes"   binding:"required,min=1,dive"`
}

// runtimeResponse is the daemon-plane runtime object: exactly the fields the
// daemon decodes, nothing more (spec DaemonRuntime).
type runtimeResponse struct {
	ID        string `json:"id"`
	CompanyID string `json:"companyId"`
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	Status    string `json:"status"`
}

type daemonRuntimeCollectionResponse struct {
	Items []runtimeResponse `json:"items"`
}

func runtimeToResponse(rt agent.Runtime) runtimeResponse {
	return runtimeResponse{
		ID:        uuidToString(rt.ID),
		CompanyID: uuidToString(rt.CompanyID),
		Name:      rt.Name,
		Provider:  rt.Provider,
		Status:    rt.Status,
	}
}

func (h *Handler) daemonRegister(c *gin.Context) {
	req, err := bindRequest[daemonRegisterRequest](c, maxDaemonBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	for index, runtime := range req.Runtimes {
		if !protocol.IsSupportedProvider(runtime.Provider) {
			writeBoundaryErrorDetail(
				c,
				boundaryErrorValidationFailed,
				fmt.Sprintf(
					"runtimes[%d].provider must be one of %s",
					index,
					strings.Join(protocol.SupportedProviders, ", "),
				),
			)
			return
		}
		if runtime.UnusableReason != nil && !protocol.IsRuntimeUnusableCode(runtime.UnusableReason.Code) {
			writeBoundaryErrorDetail(
				c,
				boundaryErrorValidationFailed,
				fmt.Sprintf(
					"runtimes[%d].unusableReason.code must be one of %s",
					index,
					strings.Join(protocol.RuntimeUnusableCodes, ", "),
				),
			)
			return
		}
		if runtime.SignIn != nil {
			if !protocol.IsRuntimeSignInState(runtime.SignIn.State) {
				writeBoundaryErrorDetail(
					c,
					boundaryErrorValidationFailed,
					fmt.Sprintf(
						"runtimes[%d].signIn.state must be one of %s",
						index,
						strings.Join(protocol.RuntimeSignInStates, ", "),
					),
				)
				return
			}
			if _, err := time.Parse(time.RFC3339, runtime.SignIn.CheckedAt); err != nil {
				// Parsed again below into the spec; this is the refusal.
				writeBoundaryErrorDetail(
					c,
					boundaryErrorValidationFailed,
					fmt.Sprintf(
						"runtimes[%d].signIn.checkedAt must be an RFC 3339 timestamp",
						index,
					),
				)
				return
			}
		}
	}
	req.DaemonID = strings.TrimSpace(req.DaemonID)
	req.DeviceName = strings.TrimSpace(req.DeviceName)
	companyID, ok := parseUUIDOrBadRequest(c, req.CompanyID, "company_id")
	if !ok {
		return
	}
	if !h.requireDaemonCompanyAccess(c, companyID) {
		return
	}
	principal, ok := daemonPrincipal(c)
	if !ok {
		return
	}

	specs := make([]agent.RuntimeRegistrationSpec, len(req.Runtimes))
	for i, runtime := range req.Runtimes {
		specs[i] = agent.RuntimeRegistrationSpec{
			Name:     runtime.Name,
			Provider: runtime.Provider,
			Version:  runtime.Version,
			Status:   runtime.Status,
		}
		if runtime.UnusableReason != nil {
			specs[i].UnusableReason = &protocol.RuntimeUnusableReason{
				Code:    runtime.UnusableReason.Code,
				Message: runtime.UnusableReason.Message,
			}
		}
		if runtime.SignIn != nil {
			checkedAt, _ := time.Parse(time.RFC3339, runtime.SignIn.CheckedAt) // validated above
			specs[i].SignIn = &agent.RuntimeSignIn{
				BillingMode: runtime.SignIn.BillingMode,
				State:       runtime.SignIn.State,
				Detail:      runtime.SignIn.Detail,
				CheckedAt:   checkedAt,
			}
		}
	}
	registered, err := h.RuntimeService.RegisterRuntimes(c.Request.Context(), agent.RuntimeRegistration{
		CompanyID:   companyID,
		OwnerUserID: principal.UserID,
		DaemonID:    req.DaemonID,
		DeviceName:  req.DeviceName,
		CLIVersion:  req.CLIVersion,
		LaunchedBy:  req.LaunchedBy,
		Runtimes:    specs,
	})
	if err != nil {
		slog.Warn(
			"register runtimes failed",
			"company_id",
			req.CompanyID,
			"daemon_id",
			req.DaemonID,
			"error",
			err,
		)
		writeBoundaryError(c, boundaryErrorRuntimeRegistrationFailed)
		return
	}

	resp := make([]runtimeResponse, 0, len(registered))
	for _, runtime := range registered {
		resp = append(resp, runtimeToResponse(runtime))
	}
	slog.Info(
		"daemon registered",
		"company_id",
		req.CompanyID,
		"daemon_id",
		req.DaemonID,
		"runtimes_count",
		len(resp),
	)
	c.JSON(http.StatusOK, daemonRuntimeCollectionResponse{Items: resp})
}

type daemonDeregisterRequest struct {
	RuntimeIDs []string `json:"runtimeIds" binding:"required,min=1"`
}

// daemonDeregister marks the caller's runtimes offline. Ids that name no
// runtime of the caller's — or of this daemon — are skipped: already-gone is
// the converged state. A failed write answers 500 with nothing confirmed, so
// the "ok" receipt always means the batch was applied.
func (h *Handler) daemonDeregister(c *gin.Context) {
	req, err := bindRequest[daemonDeregisterRequest](c, maxDaemonBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, ok := daemonPrincipal(c)
	if !ok {
		return
	}
	daemonID, ok := h.requireDaemonID(c)
	if !ok {
		return
	}

	if err := h.RuntimeService.DeregisterRuntimes(c.Request.Context(), principal, agent.RuntimeDeregistration{
		DaemonID:   daemonID,
		RuntimeIDs: req.RuntimeIDs,
	}); err != nil {
		slog.Warn("daemon deregister failed",
			"runtime_ids", req.RuntimeIDs, "daemon_id", daemonID, "error", err)
		writeBoundaryError(c, boundaryErrorRuntimeDeregistrationFailed)
		return
	}

	slog.Info("daemon deregistered", "runtime_ids", req.RuntimeIDs, "daemon_id", daemonID)
	c.JSON(http.StatusOK, daemonStatusResponse{Status: "ok"})
}

type daemonHeartbeatRequest struct {
	RuntimeID string `json:"runtimeId" binding:"required,uuid"`
	// CredentialIsolation is checked and never stored, as on registration;
	// the WebSocket seam checks the same vocabulary.
	CredentialIsolation string `json:"credentialIsolation" binding:"omitempty,oneof=enforced not_enforced"`
}

// Heartbeat-core sentinels: the HTTP handler maps both to a 404 "runtime not
// found" (404-not-403 secrecy), while the WS seam maps the gone case to a
// RuntimeGone ack and the denied case to a logged error.
var (
	errHeartbeatInvalidRuntimeID = errors.New("runtime_id is not a valid uuid")
	errHeartbeatRuntimeGone      = errors.New("runtime not found")
	errHeartbeatAccessDenied     = errors.New("runtime access denied")
)

// daemonHeartbeatCore is the transport-free heartbeat: RuntimeService owns
// the liveness write (daemon-id fence, membership check, last_seen_at
// touch, agent.status broadcast when the runtime was offline); this core
// maps its sentinels and builds the ack. Shared verbatim by
// POST /api/v1/daemon/heartbeat and the daemon WS seam so both transports
// keep identical semantics. daemonID is the machine identity each transport
// established — the request header, or the identity pinned at upgrade — and
// empty means unfenced (the rollout stage already admitted it).
func (h *Handler) daemonHeartbeatCore(
	ctx context.Context,
	principal auth.UserPrincipal,
	runtimeID string,
	daemonID string,
	clientCapabilities string,
) (*protocol.DaemonHeartbeatAckPayload, error) {
	runtimeUUID, err := util.ParseGoUUID(runtimeID)
	if err != nil {
		return nil, errHeartbeatInvalidRuntimeID
	}
	if err := h.RuntimeService.Heartbeat(ctx, principal, runtimeUUID, daemonID); err != nil {
		switch {
		case errors.Is(err, agent.ErrRuntimeNotFound):
			return nil, errHeartbeatRuntimeGone
		case errors.Is(err, agent.ErrRuntimeAccessDenied):
			return nil, errHeartbeatAccessDenied
		default:
			return nil, err
		}
	}
	serverCapabilities := []string{
		protocol.DaemonCapabilityRPCV1,
		protocol.DaemonCapabilityModelDiscoveryV1,
		protocol.DaemonCapabilityPluginPresentationV1,
	}
	var pendingModelList *protocol.DaemonHeartbeatPendingModelList
	if daemonHasCapability(clientCapabilities, protocol.DaemonCapabilityModelDiscoveryV1) {
		request, claimErr := h.RuntimeService.ClaimModelDiscovery(ctx, runtimeUUID)
		if claimErr != nil {
			return nil, fmt.Errorf("claim runtime model discovery: %w", claimErr)
		}
		if request != nil {
			pendingModelList = &protocol.DaemonHeartbeatPendingModelList{
				RequestID: request.ID.String(),
			}
		}
	}

	slog.Debug("daemon heartbeat", "runtime_id", runtimeID)
	return &protocol.DaemonHeartbeatAckPayload{
		RuntimeID:          runtimeID,
		Status:             "ok",
		ServerCapabilities: serverCapabilities,
		PendingModelList:   pendingModelList,
	}, nil
}

func daemonHasCapability(raw, wanted string) bool {
	for _, capability := range strings.Split(raw, ",") {
		if strings.TrimSpace(capability) == wanted {
			return true
		}
	}
	return false
}

// daemonHeartbeat records the runtime as alive. Boardly phase 1 has no Redis
// liveness lane: every beat writes last_seen_at directly and the sweeper
// trusts the DB stale window. The 404 on a missing runtime row is the
// runtime-gone signal — the daemon prunes the stale runtime and re-registers.
func (h *Handler) daemonHeartbeat(c *gin.Context) {
	req, err := bindRequest[daemonHeartbeatRequest](c, maxDaemonBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, ok := daemonPrincipal(c)
	if !ok {
		return
	}
	daemonID, ok := h.requireDaemonID(c)
	if !ok {
		return
	}

	ack, err := h.daemonHeartbeatCore(
		c.Request.Context(), principal, req.RuntimeID, daemonID, c.GetHeader("X-Client-Capabilities"),
	)
	switch {
	case errors.Is(err, errHeartbeatInvalidRuntimeID):
		writeBoundaryError(c, boundaryErrorRuntimeIDInvalid)
		return
	case errors.Is(err, errHeartbeatRuntimeGone), errors.Is(err, errHeartbeatAccessDenied):
		writeBoundaryError(c, boundaryErrorRuntimeNotFound)
		return
	case err != nil:
		writeBoundaryError(c, boundaryErrorHeartbeatFailed)
		return
	}

	c.JSON(http.StatusOK, daemonHeartbeatResponse(ack))
}

// daemonHeartbeatResponse is the HTTP heartbeat's reply body: the subset of
// the ack this transport returns, naming the runtime it acknowledges so the
// HTTP body and the WS daemon.heartbeat_ack carry the same payload. The WS
// seam returns the full protocol.DaemonHeartbeatAckPayload instead.
type daemonHeartbeatHTTPResponse struct {
	Status             string                                    `json:"status"`
	RuntimeID          string                                    `json:"runtimeId"`
	ServerCapabilities []string                                  `json:"serverCapabilities"`
	PendingModelList   *protocol.DaemonHeartbeatPendingModelList `json:"pendingModelList,omitempty"`
}

func daemonHeartbeatResponse(ack *protocol.DaemonHeartbeatAckPayload) daemonHeartbeatHTTPResponse {
	return daemonHeartbeatHTTPResponse{
		Status:             ack.Status,
		RuntimeID:          ack.RuntimeID,
		ServerCapabilities: ack.ServerCapabilities,
		PendingModelList:   ack.PendingModelList,
	}
}

// DaemonWSHeartbeat is the daemonws.HeartbeatHandler seam: same core as the
// HTTP heartbeat, with the runtime-gone case answered as a RuntimeGone ack so
// the daemon prunes and re-registers instead of tearing the socket down. The
// frame has no binding tags, so this seam checks the credential-isolation
// report the HTTP body's binding checks; an unknown value gets no ack.
func (h *Handler) DaemonWSHeartbeat(
	ctx context.Context,
	identity daemonws.ClientIdentity,
	beat protocol.DaemonHeartbeatRequestPayload,
) (*protocol.DaemonHeartbeatAckPayload, error) {
	if beat.CredentialIsolation != "" && !protocol.IsCredentialIsolation(beat.CredentialIsolation) {
		return nil, fmt.Errorf("credentialIsolation %q is not a known report", beat.CredentialIsolation)
	}
	runtimeID := beat.RuntimeID
	ack, err := h.daemonHeartbeatCore(
		ctx, identity.Principal, runtimeID, identity.DaemonID, identity.Capabilities,
	)
	if errors.Is(err, errHeartbeatRuntimeGone) {
		return &protocol.DaemonHeartbeatAckPayload{
			RuntimeID:   runtimeID,
			Status:      protocol.HeartbeatStatusRuntimeGone,
			RuntimeGone: true,
		}, nil
	}
	if err != nil {
		return nil, err
	}
	return ack, nil
}

// daemonWebSocket upgrades the daemon control connection. Authentication ran
// in requireDaemonAuth; the identity pins the daemon id (X-Boardly-Daemon-ID
// header, daemon_id query fallback — subject to the same rollout stage as
// the HTTP routes), the runtime_ids query scope narrowed to the caller's own
// runtimes, and the caller's company set so heartbeats and RPCs reuse the
// HTTP checks.
func (h *Handler) daemonWebSocket(c *gin.Context) {
	principal, ok := daemonPrincipal(c)
	if !ok {
		return
	}
	daemonID := strings.TrimSpace(c.GetHeader(middleware.HeaderDaemonID))
	if daemonID == "" {
		daemonID = strings.TrimSpace(c.Query("daemon_id"))
	}
	if daemonID == "" {
		if daemonID, ok = h.requireDaemonID(c); !ok {
			return
		}
	}
	runtimeIDs, err := parseDaemonRuntimeIDs(c.Query("runtime_ids"))
	if err != nil {
		writeBoundaryError(c, boundaryErrorRuntimeIDInvalid)
		return
	}
	var companyIDs []string
	rows, err := h.Queries.ListCompaniesForUser(c.Request.Context(), principal.UserID)
	if err != nil {
		writeBoundaryError(c, boundaryErrorCompanyResolutionFailed)
		return
	}
	for _, row := range rows {
		companyIDs = append(companyIDs, uuidToString(row.ID))
	}
	scope, err := h.daemonSocketRuntimeScope(c.Request.Context(), principal, daemonID, companyIDs, runtimeIDs)
	if err != nil {
		slog.Warn("daemon websocket: load runtimes failed", "daemon_id", daemonID, "error", err)
		writeBoundaryError(c, boundaryErrorRuntimeListFailed)
		return
	}

	h.DaemonWSHub.HandleWebSocket(c.Writer, c.Request, daemonws.ClientIdentity{
		DaemonID:      daemonID,
		UserID:        principal.UserID.String(),
		CompanyIDs:    companyIDs,
		RuntimeIDs:    scope,
		ClientVersion: c.GetHeader("X-Client-Version"),
		Capabilities:  c.GetHeader("X-Client-Capabilities"),
		Principal:     principal,
	})
}

func parseDaemonRuntimeIDs(rawValues string) ([]uuid.UUID, error) {
	var runtimeIDs []uuid.UUID
	for _, raw := range strings.Split(rawValues, ",") {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		id, err := util.ParseGoUUID(value)
		if err != nil {
			return nil, err
		}
		runtimeIDs = append(runtimeIDs, id)
	}
	return runtimeIDs, nil
}

// daemonSocketRuntimeScope keeps the requested runtime ids this socket may
// speak for (ADR 0014): registered under the caller's account, from this
// machine when the daemon named one, in one of the caller's companies. The
// hub indexes the socket under the result, so run.available and pending-work
// frames for another member's runtime never reach it, and the
// transcript-append RPC, which trusts this scope, cannot write into their
// Runs. Other ids are dropped rather than refused, as the claim skips them:
// a daemon may still hold a runtime that was just removed, and one stale id
// must not cost it the socket for the rest.
func (h *Handler) daemonSocketRuntimeScope(
	ctx context.Context,
	principal auth.UserPrincipal,
	daemonID string,
	companyIDs []string,
	requested []uuid.UUID,
) ([]string, error) {
	if len(requested) == 0 {
		return nil, nil
	}
	runtimes, err := h.Queries.GetRuntimes(ctx, requested)
	if err != nil {
		return nil, err
	}
	scope := make([]string, 0, len(runtimes))
	for _, rt := range runtimes {
		if !slices.Contains(companyIDs, uuidToString(rt.CompanyID)) ||
			(daemonID != "" && rt.DaemonID != daemonID) ||
			!rt.OwnerUserID.Valid || rt.OwnerUserID.UUID != principal.UserID {
			continue
		}
		scope = append(scope, uuidToString(rt.ID))
	}
	if len(scope) < len(requested) {
		slog.Warn("daemon websocket: runtime ids outside the caller's runtimes dropped",
			"daemon_id", daemonID, "requested", len(requested), "kept", len(scope))
	}
	return scope, nil
}

type daemonCompanyResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type daemonCompanyCollectionResponse struct {
	Items []daemonCompanyResponse `json:"items"`
}

func (h *Handler) daemonListCompanies(c *gin.Context) {
	principal, ok := daemonPrincipal(c)
	if !ok {
		return
	}
	rows, err := h.Queries.ListCompaniesForUser(c.Request.Context(), principal.UserID)
	if err != nil {
		writeBoundaryError(c, boundaryErrorCompanyListFailed)
		return
	}
	resp := make([]daemonCompanyResponse, 0, len(rows))
	for _, row := range rows {
		resp = append(resp, daemonCompanyResponse{
			ID:   uuidToString(row.ID),
			Name: row.Name,
			Slug: row.Slug,
		})
	}
	c.JSON(http.StatusOK, daemonCompanyCollectionResponse{Items: resp})
}

// ---------------------------------------------------------------------------
// Claim
// ---------------------------------------------------------------------------

// runResponse is the daemon-plane run object (DaemonRun): exactly what the
// daemon reads to execute — the routing ids, the enqueue-time context
// document, the resume pointer, and (on a claim only) the run's brt_ auth
// token. Lifecycle fields the daemon never reads — status, attempts,
// timestamps, failure text — are not served; the user plane's Run is where
// those live.
type runResponse struct {
	// ID is the PostgreSQL-generated Run UUID, serialized as a string.
	ID                uuid.UUID       `json:"id"`
	CompanyID         string          `json:"companyId"`
	AgentID           string          `json:"agentId"`
	TaskID            string          `json:"taskId,omitempty"`
	ConversationID    string          `json:"conversationId"`
	RuntimeID         string          `json:"runtimeId"`
	Context           json.RawMessage `json:"context,omitempty"`
	ForceFreshSession bool            `json:"forceFreshSession"`
	// Claim-only fields.
	AuthToken      string `json:"authToken,omitempty"`
	PriorSessionID string `json:"priorSessionId,omitempty"`
	// PriorSessionThroughSeq is the Conversation seq through which the prior
	// provider session provably received the chat. It travels with any
	// session that exists, resumable or withheld, so the daemon can inline
	// only what that session never saw, or say continuity was lost.
	PriorSessionThroughSeq int64 `json:"priorSessionThroughSeq,omitempty"`
}

type daemonClaimRunsResponse struct {
	Items []runResponse `json:"items"`
}

func runToResponse(run db.Run) runResponse {
	response := runResponse{
		ID:                run.ID,
		CompanyID:         uuidToString(run.CompanyID),
		AgentID:           uuidToString(run.AgentID),
		ConversationID:    uuidToString(run.ConversationID),
		RuntimeID:         uuidToString(run.RuntimeID),
		Context:           json.RawMessage(run.Context),
		ForceFreshSession: run.ForceFreshSession,
	}
	if run.TaskID.Valid {
		response.TaskID = run.TaskID.UUID.String()
	}
	return response
}

// mintRunToken mints a run-scoped brt_ credential (32 random bytes, hex) and
// its storage params. The daemon injects the token into the agent process
// instead of its own credential; the server stores only the sha256 hash.
// Tokens expire after the queue/runtime upper bound (24h) so they survive
// long-running runs but cannot outlive a forgotten one.
func mintRunToken(runID uuid.UUID) (string, db.CreateRunTokenParams, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", db.CreateRunTokenParams{}, fmt.Errorf("generate run token: %w", err)
	}
	token := "brt_" + hex.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	return token, db.CreateRunTokenParams{
		RunID:     runID,
		TokenHash: hash[:],
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true},
	}, nil
}

// buildClaimedRunResponse resolves the resume pointer for a freshly-claimed
// run: the last terminal run of the same (conversation, agent) supplies the
// provider session to resume, unless this run is pinned fresh.
func (h *Handler) buildClaimedRunResponse(ctx context.Context, run db.Run) runResponse {
	resp := runToResponse(run)
	if run.ForceFreshSession {
		return resp
	}
	if run.InvocationID.Valid {
		prior, err := h.Queries.GetChannelPriorSession(
			ctx,
			db.GetChannelPriorSessionParams{ConversationID: run.ConversationID, RunID: run.ID},
		)
		if err != nil {
			return resp
		}
		var previous, current struct {
			Agent struct {
				SkillSetHash string `json:"skillSetHash"`
			} `json:"agent"`
			ChannelInvocation struct {
				ContextRevision int64 `json:"contextRevision"`
			} `json:"channelInvocation"`
			Snapshot struct {
				ThroughSeq int64 `json:"throughSeq"`
			} `json:"conversationSnapshot"`
		}
		if json.Unmarshal(prior.Context, &previous) != nil || json.Unmarshal(run.Context, &current) != nil {
			return resp
		}
		resp.PriorSessionThroughSeq = previous.Snapshot.ThroughSeq
		if prior.RuntimeID != run.RuntimeID ||
			previous.ChannelInvocation.ContextRevision != current.ChannelInvocation.ContextRevision ||
			previous.Agent.SkillSetHash != current.Agent.SkillSetHash {
			return resp
		}
		resp.PriorSessionID = prior.SessionID.String
		return resp
	}
	prior, err := h.Queries.GetLastRunSessionForConversation(
		ctx,
		db.GetLastRunSessionForConversationParams{
			ConversationID: run.ConversationID,
			AgentID:        run.AgentID,
		},
	)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("resolve prior run session failed",
				"run_id", run.ID, "error", err)
		}
		return resp
	}
	if prior.SessionID.String == "" {
		return resp
	}
	// The watermark travels with any session that exists: a withheld one
	// still tells the daemon which messages the agent has already seen, so
	// it can say continuity was lost instead of silently starting over. A
	// session on another runtime is withheld like the invocation branch's:
	// it lives on that machine, and after a handoff that machine is another
	// member's.
	resp.PriorSessionThroughSeq = prior.ThroughSeq
	if prior.SessionRolloutMissing || prior.RuntimeID != run.RuntimeID ||
		prior.SkillSetHash != runSkillSetHash(run.Context) {
		return resp
	}
	resp.PriorSessionID = prior.SessionID.String
	return resp
}

func runSkillSetHash(data []byte) string {
	var doc struct {
		Agent struct {
			SkillSetHash string `json:"skillSetHash"`
		} `json:"agent"`
	}
	_ = json.Unmarshal(data, &doc)
	return doc.Agent.SkillSetHash
}

// finalizeClaimedRun mints and persists the run token; on failure the exact
// claim is requeued and the error is returned so the batch reports the
// mid-batch failure instead of silently serving a partial claim.
func (h *Handler) finalizeClaimedRun(ctx context.Context, run db.Run) (runResponse, error) {
	token, params, err := mintRunToken(run.ID)
	if err != nil {
		slog.Error("run claim: generate run token failed; requeueing claim",
			"run_id", run.ID, "error", err)
		h.requeueFailedClaim(ctx, run, "token_generation")
		return runResponse{}, err
	}
	if err := h.RunService.FinalizeRunClaim(ctx, params); err != nil {
		slog.Error("run claim: finalize run claim failed; requeueing claim",
			"run_id", run.ID, "error", err)
		h.requeueFailedClaim(ctx, run, "token_persist")
		return runResponse{}, err
	}
	resp := h.buildClaimedRunResponse(ctx, run)
	resp.AuthToken = token
	return resp, nil
}

func (h *Handler) requeueFailedClaim(ctx context.Context, run db.Run, reason string) {
	if _, err := h.RunService.RequeueRunAfterClaimFailure(ctx, run); err != nil {
		slog.Error(
			"run claim: failed to requeue after finalization error",
			"run_id", run.ID,
			"reason", reason,
			"error", err,
		)
	}
}

type daemonClaimRunsBatchRequest struct {
	DaemonID   string   `json:"daemonId"   binding:"required"`
	RuntimeIDs []string `json:"runtimeIds"`
	MaxRuns    int      `json:"maxRuns"`
}

// Claim-core sentinels: the HTTP handler maps each to its boundary error;
// the WS adapter maps the malformed case to 400 and the rest to 500. The
// infrastructure sentinels wrap the cause via errors.Join.
var (
	errClaimMaxRunsNegative   = errors.New("max_runs must not be negative")
	errClaimRuntimeListFailed = errors.New("failed to load claim runtimes")
	errClaimFailed            = errors.New("failed to claim runs")
)

// daemonClaimRunsCore is the transport-free machine-level batch claim: the
// daemon posts every runtime_id it hosts plus its free execution-slot count
// and receives up to max_runs already-claimed runs in one round trip, each
// carrying its runtime_id so the daemon routes it locally. Unknown or
// unauthorized runtime_ids are skipped silently (a daemon may send a
// just-deleted runtime; it self-heals via the heartbeat path). Shared
// verbatim by POST /api/v1/daemon/runs/claim and the daemon WS runs.claim RPC
// so both transports keep identical semantics.
func (h *Handler) daemonClaimRunsCore(
	ctx context.Context,
	principal auth.UserPrincipal,
	req daemonClaimRunsBatchRequest,
) (daemonClaimRunsResponse, error) {
	empty := daemonClaimRunsResponse{Items: []runResponse{}}

	// max_runs semantics: a negative count is malformed; zero is a valid "no
	// free slots" poll that must claim nothing — never coerce to 1, which
	// would dispatch a run the daemon cannot host and strand it until stale
	// reclaim. Positive counts are capped so one request can't build an
	// unbounded payload.
	if req.MaxRuns < 0 {
		return empty, errClaimMaxRunsNegative
	}
	if req.MaxRuns == 0 {
		return empty, nil
	}
	maxRuns := req.MaxRuns
	if maxRuns > claimBatchMaxRunsCap {
		maxRuns = claimBatchMaxRunsCap
	}

	// Parse + de-dup requested ids with the non-panicking parser; invalid ids
	// are skipped, matching this endpoint's "unknown id skipped" semantics.
	idByKey := make(map[string]uuid.UUID, len(req.RuntimeIDs))
	for _, rid := range req.RuntimeIDs {
		ruid, err := util.ParseGoUUID(rid)
		if err != nil {
			continue
		}
		idByKey[uuidToString(ruid)] = ruid
	}
	if len(idByKey) == 0 {
		return empty, nil
	}
	ids := make([]uuid.UUID, 0, len(idByKey))
	for _, id := range idByKey {
		ids = append(ids, id)
	}

	// Resolve all requested runtimes in one query, then authorize each; skip
	// (don't fail) unknown/unauthorized ids so a single stale runtime can't
	// sink the whole batch. The daemon_id guard rejects any runtime bound to
	// a different machine so a stale/crossed runtime set cannot claim another
	// daemon's machine-local work.
	runtimes, err := h.Queries.GetRuntimes(ctx, ids)
	if err != nil {
		return empty, errors.Join(errClaimRuntimeListFailed, err)
	}
	authorized := make([]uuid.UUID, 0, len(runtimes))
	for _, rt := range runtimes {
		if !h.verifyDaemonCompanyAccess(ctx, principal, rt.CompanyID) {
			continue
		}
		// Every runtime names the machine that registered it, so a daemon
		// only ever speaks for its own. The nullable form of this check let a
		// runtime with no daemon answer for any of them.
		if rt.DaemonID != req.DaemonID {
			continue
		}
		// And the account that registered it (ADR 0014): a runtime executes
		// only its registrant's work, so a daemon signed in as anyone else
		// claims nothing for it. The claim SQL rechecks the same fact
		// against the Run's authorizer; this keeps a crossed set from even
		// asking.
		if !rt.OwnerUserID.Valid || rt.OwnerUserID.UUID != principal.UserID {
			continue
		}
		authorized = append(authorized, rt.ID)
	}
	if len(authorized) == 0 {
		return empty, nil
	}

	claimed, err := h.RunService.ClaimRunsForRuntimes(ctx, authorized, maxRuns)
	if err != nil {
		return empty, errors.Join(errClaimFailed, err)
	}

	out := make([]runResponse, 0, len(claimed))
	for i := range claimed {
		// A finalization failure is a mid-batch failure: the failed claim is
		// requeued, runs already finalized stay claimed (the stale-claim
		// reclaim re-serves them), and the daemon hears a 5xx so it retries
		// the claim instead of guessing what it holds.
		resp, err := h.finalizeClaimedRun(ctx, claimed[i])
		if err != nil {
			return empty, errors.Join(errClaimFailed, err)
		}
		out = append(out, resp)
	}

	if len(out) > 0 {
		slog.Info("runs claimed by runtime batch",
			"runtimes", len(authorized), "requested_max", maxRuns, "claimed", len(out))
	}
	return daemonClaimRunsResponse{Items: out}, nil
}

// daemonClaimRunsBatch is the HTTP transport for the batch claim core.
func (h *Handler) daemonClaimRunsBatch(c *gin.Context) {
	req, err := bindRequest[daemonClaimRunsBatchRequest](c, maxDaemonBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, ok := daemonPrincipal(c)
	if !ok {
		return
	}

	resp, err := h.daemonClaimRunsCore(c.Request.Context(), principal, req)
	switch {
	case errors.Is(err, errClaimMaxRunsNegative):
		writeBoundaryError(c, boundaryErrorMaxRunsNegative)
		return
	case errors.Is(err, errClaimRuntimeListFailed):
		writeBoundaryError(c, boundaryErrorRuntimeListFailed)
		return
	case err != nil:
		writeBoundaryError(c, boundaryErrorRunClaimFailed)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// DaemonWSClaimRuns is the daemonws RPC adapter for runs.claim: the same
// transport-free core as POST /api/v1/daemon/runs/claim, bound from the RPC
// frame body instead of a synthesized HTTP request. The connection pinned its
// principal at upgrade, so the core's daemon_id + company membership checks
// behave exactly like the HTTP path.
func (h *Handler) DaemonWSClaimRuns(
	ctx context.Context,
	identity daemonws.ClientIdentity,
	_ string,
	body json.RawMessage,
) (int, json.RawMessage, error) {
	if identity.Principal.UserID == uuid.Nil {
		return http.StatusUnauthorized, nil, errors.New("authentication is required")
	}
	// An empty RPC body reads as an empty JSON object so the shared binder
	// sees a well-formed request.
	if len(body) == 0 {
		body = json.RawMessage("{}")
	}
	var req daemonClaimRunsBatchRequest
	if err := util.BindStrictJSON(bytes.NewReader(body), &req); err != nil {
		return http.StatusBadRequest, nil, errors.New("claim request is invalid")
	}
	if strings.TrimSpace(req.DaemonID) == "" {
		return http.StatusUnprocessableEntity, nil, errors.New("daemonId is required")
	}

	resp, err := h.daemonClaimRunsCore(ctx, identity.Principal, req)
	switch {
	case errors.Is(err, errClaimMaxRunsNegative):
		// Matches the HTTP boundary's 400 invalid_request for a negative count.
		return http.StatusBadRequest, nil, err
	case err != nil:
		return http.StatusInternalServerError, nil, errors.New("failed to claim runs")
	}
	payload, err := json.Marshal(resp)
	if err != nil {
		return http.StatusInternalServerError, nil, errors.New("claim response encoding failed")
	}
	return http.StatusOK, payload, nil
}

// daemonExtendRunPrepareLease keeps a dispatched run protected while the
// daemon resolves startup inputs and prepares the execution environment.
func (h *Handler) daemonExtendRunPrepareLease(c *gin.Context) {
	runtimeID := c.Param("runtimeId")

	runtime, ok := h.requireDaemonRuntimeAccess(c, runtimeID)
	if !ok {
		return
	}
	run, ok := h.requireDaemonRunAccess(c, c.Param("runId"))
	if !ok {
		return
	}
	if run.CompanyID != runtime.CompanyID || run.RuntimeID != runtime.ID {
		writeBoundaryError(c, boundaryErrorRunNotFound)
		return
	}

	if err := h.RunService.ExtendRunPrepareLease(c.Request.Context(), run.ID, runtime.ID); err != nil {
		slog.Warn("extend run prepare lease failed",
			"run_id", run.ID, "runtime_id", runtimeID, "error", err)
		writeBoundaryError(c, boundaryErrorPrepareLeaseExpired)
		return
	}
	c.JSON(http.StatusOK, daemonStatusResponse{Status: "ok"})
}

type daemonSkillRef struct {
	ID     string `json:"id"     binding:"required"`
	Source string `json:"source" binding:"required"`
}

type daemonResolveSkillBundlesRequest struct {
	ContractVersion int              `json:"contractVersion"`
	Skills          []daemonSkillRef `json:"skills"          binding:"required,min=1,max=32,dive"`
}

type daemonSkillBundleCollectionResponse struct {
	Items []json.RawMessage `json:"items"`
}

// daemonResolveSkillBundles serves skill bundles for a claimed run. The
// allowlist is keyed (source, id) over the run context's agent skill set —
// a request outside it answers 404 — and the run must still be in its
// startup window (dispatched / waiting_local_directory), else 409: a bundle
// download for a running or terminal run is always stale daemon state.
func (h *Handler) daemonResolveSkillBundles(c *gin.Context) {
	runtimeID := c.Param("runtimeId")

	runtime, ok := h.requireDaemonRuntimeAccess(c, runtimeID)
	if !ok {
		return
	}
	run, ok := h.requireDaemonRunAccess(c, c.Param("runId"))
	if !ok {
		return
	}
	if run.CompanyID != runtime.CompanyID || run.RuntimeID != runtime.ID {
		writeBoundaryError(c, boundaryErrorRunNotFound)
		return
	}
	if run.Status != "dispatched" && run.Status != "waiting_local_directory" {
		writeBoundaryError(c, boundaryErrorRunNotPreparing)
		return
	}
	req, err := bindRequest[daemonResolveSkillBundlesRequest](c, maxDaemonBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if req.ContractVersion != 1 {
		writeMappedError(c, agent.ErrSkillDaemonUpgrade, skillErrorMappings, internalErrorFallback)
		return
	}

	bundles := make([]json.RawMessage, 0, len(req.Skills))
	for _, ref := range req.Skills {
		versionID, err := util.ParseGoUUID(ref.ID)
		if err != nil || ref.Source != "company" {
			writeBoundaryError(c, boundaryErrorSkillNotFound)
			return
		}
		bundle, err := h.SkillService.ResolveForRun(c.Request.Context(), run.CompanyID, run.ID, versionID)
		if err != nil {
			writeSkillError(c, err)
			return
		}
		encoded, err := json.Marshal(bundle)
		if err != nil {
			writeSkillError(c, err)
			return
		}
		bundles = append(bundles, encoded)
	}
	c.JSON(http.StatusOK, daemonSkillBundleCollectionResponse{Items: bundles})
}

// daemonDownloadRunAttachment serves a claimed Run one of the files its
// context froze: a Company reference document, a Project document, a file
// on the channel messages it answers, or a file attached to its Task. The
// allowlist is exactly what the run context froze — an id outside it
// answers 404 — and the run must still be in its startup window, because a
// download for a running or terminal run is always stale daemon state. The
// allowlist says what the Run may ask for; the Run's authorizer must also
// still read the file (attachment.DownloadForRun, ADR 0017), which under
// ADR 0014 is also the owner of the machine asking.
func (h *Handler) daemonDownloadRunAttachment(c *gin.Context) {
	runtime, ok := h.requireDaemonRuntimeAccess(c, c.Param("runtimeId"))
	if !ok {
		return
	}
	run, ok := h.requireDaemonRunAccess(c, c.Param("runId"))
	if !ok {
		return
	}
	if run.CompanyID != runtime.CompanyID || run.RuntimeID != runtime.ID {
		writeBoundaryError(c, boundaryErrorRunNotFound)
		return
	}
	if run.Status != "dispatched" && run.Status != "waiting_local_directory" {
		writeBoundaryError(c, boundaryErrorRunNotPreparing)
		return
	}
	attachmentID, err := util.ParseGoUUID(c.Param("attachmentId"))
	if err != nil {
		writeAttachmentError(c, attachment.ErrNotFound)
		return
	}

	type frozenAttachment struct {
		ID string `json:"id"`
	}
	var doc struct {
		ProjectDocuments   []frozenAttachment `json:"projectDocuments"`
		CompanyReferences  []frozenAttachment `json:"companyReferences"`
		ChannelAttachments []frozenAttachment `json:"channelAttachments"`
		TaskAttachments    []frozenAttachment `json:"taskAttachments"`
	}
	if len(run.Context) > 0 {
		if err := json.Unmarshal(run.Context, &doc); err != nil {
			slog.Warn("run context unparseable for attachment download", "run_id", run.ID, "error", err)
		}
	}
	listed := func(frozen []frozenAttachment) bool {
		for _, file := range frozen {
			if file.ID == attachmentID.String() {
				return true
			}
		}
		return false
	}
	projectDocument := listed(doc.ProjectDocuments)
	if !projectDocument && !listed(doc.CompanyReferences) && !listed(doc.ChannelAttachments) &&
		!listed(doc.TaskAttachments) {
		writeAttachmentError(c, attachment.ErrNotFound)
		return
	}

	// The frozen list says which files the Run may ask for; the Run's
	// authorizer must still be allowed to read each one now (ADR 0017).
	result, err := h.AttachmentService.DownloadForRun(
		c.Request.Context(),
		attachment.RunDownloadInput{
			ID: attachmentID, CompanyID: run.CompanyID, RunID: run.ID,
			AuthorizedByUserID:    run.ExecutionAuthorizedByUserID.UUID,
			FrozenProjectDocument: projectDocument,
		},
	)
	if err != nil {
		writeAttachmentError(c, err)
		return
	}
	streamAttachmentDownload(c, result)
}

type daemonRecoverOrphansResponse struct {
	Orphaned int `json:"orphaned"`
	Retried  int `json:"retried"`
}

// daemonRecoverOrphans fails every in-flight run of the runtime after a
// daemon restart, funneled through the shared post-failure pipeline so the
// run.failed events and auto-retry behavior match the runtime sweeper.
func (h *Handler) daemonRecoverOrphans(c *gin.Context) {
	runtimeID := c.Param("runtimeId")
	runtime, ok := h.requireDaemonRuntimeAccess(c, runtimeID)
	if !ok {
		return
	}

	batch, err := h.RunService.RecoverOrphanedRunsForRuntime(c.Request.Context(), runtime.ID)
	if err != nil {
		slog.Warn("recover-orphans failed", "runtime_id", runtimeID, "error", err)
		writeBoundaryError(c, boundaryErrorOrphanRecoveryFailed)
		return
	}

	if len(batch.Runs) > 0 {
		slog.Info(
			"recover-orphans completed",
			"runtime_id", runtimeID,
			"orphaned", len(batch.Runs),
			"retried", batch.RetryCount,
		)
	}
	c.JSON(http.StatusOK, daemonRecoverOrphansResponse{
		Orphaned: len(batch.Runs),
		Retried:  batch.RetryCount,
	})
}

// ---------------------------------------------------------------------------
// Run lifecycle (called by the daemon)
// ---------------------------------------------------------------------------

// daemonStartRunRequest names the runtime the daemon believes it is starting
// this run under. It is required because the start transition is fenced on it
// (bug R2): the UPDATE matches runs.runtime_id, so a run reassigned since the
// claim refuses to start under its old owner. Reading the runtime off the row
// about to be updated would make that fence compare a value to itself.
type daemonStartRunRequest struct {
	RuntimeID string `json:"runtimeId" binding:"required,uuid"`
}

func (h *Handler) daemonStartRun(c *gin.Context) {
	claimed, ok := h.requireDaemonRunAccess(c, c.Param("runId"))
	if !ok {
		return
	}
	req, err := bindRequest[daemonStartRunRequest](c, maxDaemonBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	// The body's runtime is not re-read here: requireDaemonRunAccess already
	// proved the run's runtime is the caller's, and the UPDATE's runtime_id
	// match against this body value is what refuses a run reassigned since
	// the claim.
	runtimeID, ok := parseUUIDOrBadRequest(c, req.RuntimeID, "runtime_id")
	if !ok {
		return
	}

	run, err := h.RunService.StartRun(c.Request.Context(), claimed.ID, runtimeID)
	if err != nil {
		slog.Warn("start run failed",
			"run_id", claimed.ID, "runtime_id", runtimeID, "error", err)
		writeBoundaryError(c, boundaryErrorRunNotStartable)
		return
	}

	slog.Info("run started", "run_id", run.ID, "agent_id", uuidToString(run.AgentID))
	c.JSON(http.StatusOK, runToResponse(*run))
}

// daemonMarkRunWaitingLocalDirectory parks a dispatched run while the daemon
// waits for another in-flight run to release the local-directory path lock.
func (h *Handler) daemonMarkRunWaitingLocalDirectory(c *gin.Context) {
	parked, ok := h.requireDaemonRunAccess(c, c.Param("runId"))
	if !ok {
		return
	}
	run, err := h.RunService.MarkRunWaitingLocalDirectory(c.Request.Context(), parked.ID)
	if err != nil {
		slog.Warn("mark run waiting_local_directory failed", "run_id", parked.ID, "error", err)
		writeBoundaryError(c, boundaryErrorRunLocalDirectoryWait)
		return
	}
	c.JSON(http.StatusOK, runToResponse(*run))
}

func (h *Handler) daemonWaitForStudioAuth(c *gin.Context) {
	claimed, ok := h.requireDaemonRunAccess(c, c.Param("runId"))
	if !ok {
		return
	}
	if _, ok := h.requireDaemonRuntimeAccess(c, claimed.RuntimeID.String()); !ok {
		return
	}
	request, err := bindRequest[struct {
		Waiting *bool `json:"waiting" binding:"required"`
	}](c, 1024)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	run, err := h.RunService.WaitForStudioAuth(c.Request.Context(), claimed.ID, *request.Waiting)
	if err != nil {
		writeBoundaryError(c, boundaryErrorRunStudioAuthWait)
		return
	}
	c.JSON(http.StatusOK, runToResponse(*run))
}

type runUsagePayload struct {
	Provider string `json:"provider" binding:"required"`
	Model    string `json:"model"    binding:"required,max=255"`
	// UsageBasis names what the counters measure. Omitted means per_run;
	// the codex path reports session_cumulative because the provider counts
	// the whole resumed session.
	UsageBasis       string `json:"usageBasis"       binding:"omitempty,oneof=per_run session_cumulative"`
	InputTokens      *int64 `json:"inputTokens"      binding:"required,min=0"`
	OutputTokens     *int64 `json:"outputTokens"     binding:"required,min=0"`
	CacheReadTokens  *int64 `json:"cacheReadTokens"  binding:"required,min=0"`
	CacheWriteTokens *int64 `json:"cacheWriteTokens" binding:"required,min=0"`
	// CostUSDTicks is the provider's own price for this usage in 1e-10 USD.
	// Nil means unreported; a pointer to zero is an exact reported zero.
	CostUSDTicks       *int64          `json:"costUsdTicks"       binding:"omitempty,min=0"`
	ReasoningTokens    *int64          `json:"reasoningTokens"    binding:"omitempty,min=0"`
	Turns              *int32          `json:"turns"              binding:"omitempty,min=0"`
	ProviderDurationMs *int64          `json:"providerDurationMs" binding:"omitempty,min=0"`
	BillingMode        string          `json:"billingMode"        binding:"omitempty,oneof=api subscription unknown"`
	RawJSON            json.RawMessage `json:"rawJson"            binding:"required"`
}

type runUsageBatchRequest struct {
	Usage []runUsagePayload `json:"usage" binding:"required,dive"`
}

func (h *Handler) daemonReportRunUsage(c *gin.Context) {
	run, ok := h.requireDaemonRunAccess(c, c.Param("runId"))
	if !ok {
		return
	}
	req, err := bindRequest[runUsageBatchRequest](c, maxDaemonBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}

	facts := make([]usage.RunUsageInput, len(req.Usage))
	for i, u := range req.Usage {
		facts[i] = runUsageInputFromPayload(u)
	}
	if err := h.UsageService.ReportRunUsage(c.Request.Context(), run.ID, facts); err != nil {
		writeRunUsageError(c, err)
		return
	}

	c.JSON(http.StatusOK, daemonStatusResponse{Status: "ok"})
}

// runUsageInputFromPayload passes the provider through verbatim: the usage
// service validates it exactly, case-sensitively, against
// protocol.SupportedProviders — the same rule registration applies. An
// omitted usageBasis is the wire default, per_run.
func runUsageInputFromPayload(payload runUsagePayload) usage.RunUsageInput {
	basis := payload.UsageBasis
	if basis == "" {
		basis = usage.UsageBasisPerRun
	}
	return usage.RunUsageInput{
		Provider:           payload.Provider,
		Model:              payload.Model,
		UsageBasis:         basis,
		InputTokens:        *payload.InputTokens,
		OutputTokens:       *payload.OutputTokens,
		CacheReadTokens:    *payload.CacheReadTokens,
		CacheWriteTokens:   *payload.CacheWriteTokens,
		CostUSDTicks:       payload.CostUSDTicks,
		ReasoningTokens:    payload.ReasoningTokens,
		Turns:              payload.Turns,
		ProviderDurationMs: payload.ProviderDurationMs,
		BillingMode:        payload.BillingMode,
		RawJSON:            payload.RawJSON,
	}
}

var runUsageErrorMappings = []errorMapping{
	{
		target: usage.ErrInvalidInput,
		status: http.StatusUnprocessableEntity,
		detail: "request validation failed",
		code:   "validation_failed",
	},
	{
		target: usage.ErrRunUsageConflict,
		status: http.StatusConflict,
		detail: "run usage conflicts with the stored immutable fact",
		code:   "run_usage_conflict",
	},
}

func writeRunUsageError(c *gin.Context, err error) {
	writeMappedError(c, err, runUsageErrorMappings, internalErrorFallback)
}

type pinRunSessionRequest struct {
	SessionID string `json:"sessionId,omitempty"`
}

// daemonPinRunSession lets the daemon persist the provider session id as soon
// as it is known. Run workspaces remain daemon-local and reconstructible.
func (h *Handler) daemonPinRunSession(c *gin.Context) {
	run, ok := h.requireDaemonRunAccess(c, c.Param("runId"))
	if !ok {
		return
	}
	req, err := bindRequest[pinRunSessionRequest](c, maxDaemonBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}

	if err := h.Queries.PinRunSession(c.Request.Context(), db.PinRunSessionParams{
		ID:        run.ID,
		SessionID: req.SessionID,
	}); err != nil {
		slog.Warn("pin run session failed", "run_id", run.ID, "error", err)
		writeBoundaryError(c, boundaryErrorRunSessionPinFailed)
		return
	}
	c.JSON(http.StatusOK, daemonStatusResponse{Status: "ok"})
}

// daemonRunStatusResponse is the daemon's mid-flight view of a run. Reason
// is the cancellation reason of a cancelled run; a confirmed handoff's
// task_reassigned tells the daemon the Task's workspace generation moved on,
// so it supersedes the run's worktree instead of completing it (ADR 0014).
type daemonRunStatusResponse struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// daemonGetRunStatus returns the current status of a run. Used by the daemon
// to detect terminal/interruption signals (cancelled, failed, completed)
// while a run is executing mid-flight.
func (h *Handler) daemonGetRunStatus(c *gin.Context) {
	run, ok := h.requireDaemonRunAccess(c, c.Param("runId"))
	if !ok {
		return
	}
	c.JSON(http.StatusOK, newDaemonRunStatusResponse(run))
}

// newDaemonRunStatusResponse reads runs.error as a reason only for a
// cancelled run, where it holds a cancellation reason; on a failed run it is
// provider failure text, which the poll has no use for.
func newDaemonRunStatusResponse(run db.Run) daemonRunStatusResponse {
	response := daemonRunStatusResponse{Status: run.Status}
	if run.Status == "cancelled" {
		response.Reason = run.Error.String
	}
	return response
}

type runCompleteRequest struct {
	// ThroughSeq is int64: seq counts the whole Conversation now, not one
	// run's items, so a busy conversation runs it past what an int32 holds.
	ThroughSeq *int64 `json:"throughSeq" binding:"required,min=0"`
	// FinalItemKey: empty means the successful turn produced no final agent
	// text — the provider contract's explicit no-output signal — and the Run
	// completes without a canonical Conversation Message.
	FinalItemKey string `json:"finalItemKey,omitempty" binding:"omitempty,max=512"`
	SessionID    string `json:"sessionId"`
	// SessionRolloutMissing: the daemon withheld this run's session because
	// its rollout was missing. Store NULL session_id and flag the continuity
	// gap for the next claim.
	SessionRolloutMissing bool `json:"sessionRolloutMissing,omitempty"`
	// RetiredSessionID: a session this run proved unresumable and abandoned.
	// Distinct from an empty SessionID, which only means "nothing to report"
	// — this says "never hand this id to a later run".
	RetiredSessionID string `json:"retiredSessionId,omitempty"`
}

func (h *Handler) daemonCompleteRun(c *gin.Context) {
	completing, ok := h.requireDaemonRunAccess(c, c.Param("runId"))
	if !ok {
		return
	}
	runID := completing.ID
	req, err := bindRequest[runCompleteRequest](c, maxDaemonBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}

	proof := execution.TerminalTranscriptProof{
		ThroughSeq:   *req.ThroughSeq,
		FinalItemKey: req.FinalItemKey,
	}

	run, err := h.RunService.CompleteRun(
		c.Request.Context(),
		runID,
		proof,
		req.SessionID,
		req.SessionRolloutMissing,
		req.RetiredSessionID,
	)
	if errors.Is(err, execution.ErrRunContextExhausted) {
		// The provider reported "context window is full" as a successful
		// answer. Recording that as a completion would keep the poisoned
		// session pointer, so the same terminal observation lands as a
		// failure instead — with the reported session retired, never stored.
		retiredSessionID := req.RetiredSessionID
		if req.SessionID != "" {
			retiredSessionID = req.SessionID
		}
		slog.Warn("complete run refused: context-exhausted output; failing the run instead",
			"run_id", runID)
		run, err = h.RunService.FailRun(
			c.Request.Context(),
			runID,
			proof,
			"provider reported the context window is exhausted",
			"",
			string(runfailure.ReasonAgentContextOverflow),
			req.SessionRolloutMissing,
			retiredSessionID,
		)
	}
	if err != nil {
		// A CompleteRun error is an infrastructure failure, not a bad request:
		// an already-finalized callback is treated as idempotent success and
		// returns no error. Return 5xx so the daemon retries the terminal
		// callback and the completion lands exactly once.
		slog.Warn("complete run failed", "run_id", runID, "error", err)
		writeTerminalRunError(c, err, boundaryErrorRunCompletionFailed)
		return
	}

	// The terminal transition is committed. Wake the owning runtime now so
	// queued work that was blocked by this run's serialization key is
	// re-claimed immediately.
	h.RunService.NotifyRunFinished(*run)

	// Best-effort revoke of the brt_ token minted at claim. The token would
	// naturally expire at the 24h watermark; eager deletion shrinks the
	// window where a compromised agent process can keep calling the API.
	if err := h.Queries.DeleteRunTokensByRun(c.Request.Context(), run.ID); err != nil {
		slog.Warn("complete run: failed to revoke run tokens", "run_id", run.ID, "error", err)
	}

	slog.Info("run completed", "run_id", runID, "agent_id", uuidToString(run.AgentID))
	c.JSON(http.StatusOK, runToResponse(*run))
}

type runFailRequest struct {
	// ThroughSeq: see runCompleteRequest.
	ThroughSeq    *int64 `json:"throughSeq"              binding:"required,min=0"`
	FinalItemKey  string `json:"finalItemKey,omitempty"  binding:"omitempty,max=512"`
	Error         string `json:"error"`
	SessionID     string `json:"sessionId,omitempty"`
	FailureReason string `json:"failureReason,omitempty"`
	// SessionRolloutMissing / RetiredSessionID: see runCompleteRequest.
	SessionRolloutMissing bool   `json:"sessionRolloutMissing,omitempty"`
	RetiredSessionID      string `json:"retiredSessionId,omitempty"`
}

func (h *Handler) daemonFailRun(c *gin.Context) {
	failing, ok := h.requireDaemonRunAccess(c, c.Param("runId"))
	if !ok {
		return
	}
	req, err := bindRequest[runFailRequest](c, maxDaemonBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	h.failRun(c, failing.ID, req)
}

// failRun records a terminal failure and writes the response.
func (h *Handler) failRun(c *gin.Context, runID uuid.UUID, req runFailRequest) {
	proof := execution.TerminalTranscriptProof{
		ThroughSeq:   *req.ThroughSeq,
		FinalItemKey: req.FinalItemKey,
	}
	run, err := h.RunService.FailRun(
		c.Request.Context(),
		runID,
		proof,
		req.Error,
		req.SessionID,
		req.FailureReason,
		req.SessionRolloutMissing,
		req.RetiredSessionID,
	)
	if err != nil {
		// A FailRun error is an infrastructure failure (the terminal
		// transaction that also creates the auto-retry rolled back), not a
		// bad request. Return 5xx so the daemon's terminal callback retries
		// and the fail + retry land exactly once.
		slog.Warn("fail run failed", "run_id", runID, "error", err)
		writeTerminalRunError(c, err, boundaryErrorRunFailureFailed)
		return
	}
	h.RunService.NotifyRunFinished(*run)

	// Best-effort revoke of the brt_ token minted at claim — same rationale
	// as daemonCompleteRun.
	if err := h.Queries.DeleteRunTokensByRun(c.Request.Context(), run.ID); err != nil {
		slog.Warn("fail run: failed to revoke run tokens", "run_id", run.ID, "error", err)
	}

	slog.Info(
		"run failed",
		"run_id",
		runID,
		"agent_id",
		uuidToString(run.AgentID),
		"run_error",
		run.Error.String,
		"failure_reason",
		req.FailureReason,
	)
	c.JSON(http.StatusOK, runToResponse(*run))
}

// terminalRunHeadMismatchMapping is named because the boundary serves this
// one row with a body the table cannot express. It stays inside
// terminalRunErrorMappings so the error-code registry still sees it.
var terminalRunHeadMismatchMapping = errorMapping{
	execution.ErrTranscriptHeadMismatch,
	http.StatusConflict,
	"terminal transcript head does not match the committed run transcript",
	"run_transcript_head_mismatch",
}

var terminalRunErrorMappings = []errorMapping{
	terminalRunHeadMismatchMapping,
	{
		execution.ErrTerminalFinalItemInvalid,
		http.StatusConflict,
		"terminal transcript final item is not the latest committed agent text item",
		"run_terminal_item_invalid",
	},
}

func writeTerminalRunError(c *gin.Context, err error, fallback boundaryErrorKey) {
	// The head mismatch is the one refusal that must carry a payload, so it
	// cannot go through the table: writeMappedError serves a fixed body and
	// would drop the head. That drop IS bug R1 — with nothing to reconcile
	// against the daemon re-sent the identical stale proof, was refused
	// again, and gave up, leaving the Run non-terminal while the agent's
	// answer sat durable in the Conversation.
	var mismatch *execution.TranscriptHeadMismatchError
	if errors.As(err, &mismatch) {
		writeTerminalHeadMismatch(c, mismatch.Head())
		return
	}
	for _, mapping := range terminalRunErrorMappings {
		if errors.Is(err, mapping.target) {
			writeMappedError(c, err, terminalRunErrorMappings, internalErrorFallback)
			return
		}
	}
	writeBoundaryError(c, fallback)
}

// writeTerminalHeadMismatch renders the reconciliation 409: the standard error
// envelope with the server's OWN committed head flat beside it, which is the
// shape protocol.RunTerminalHeadMismatch declares and internal/daemon's
// terminalHeadMismatch reads back.
//
// It composes the envelope here instead of calling util.WriteError because
// that helper writes a closed three-key body. The three keys and the
// null-when-absent requestId are reproduced exactly so the two bodies cannot
// drift apart.
func writeTerminalHeadMismatch(c *gin.Context, head protocol.RunTerminalHeadMismatch) {
	var requestID any
	if id := util.RequestIDFromContext(c); id != "" {
		requestID = id
	}
	c.JSON(terminalRunHeadMismatchMapping.status, gin.H{
		"detail":     terminalRunHeadMismatchMapping.detail,
		"code":       terminalRunHeadMismatchMapping.code,
		"requestId":  requestID,
		"runId":      head.RunID,
		"throughSeq": head.ThroughSeq,
	})
}

// daemonAckRunCancelled receives the daemon's acknowledgement that it
// observed a run cancellation and finished flushing the transcript. Boardly
// has no deferred finalization to settle — cancelled runs just cancel — so
// the ack is a plain 200 that also wakes the runtime for queued successors.
func (h *Handler) daemonAckRunCancelled(c *gin.Context) {
	run, ok := h.requireDaemonRunAccess(c, c.Param("runId"))
	if !ok {
		return
	}
	h.RunService.NotifyRunFinished(run)
	c.JSON(http.StatusOK, daemonStatusResponse{Status: "ok"})
}
