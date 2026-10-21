package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

// RunProofResolver resolves supplemental Run scope without introducing a
// second principal. The authenticated UserPrincipal remains the only actor.
type RunProofResolver interface {
	ResolveRunOrigin(context.Context, string) (execution.RunOrigin, error)
}

// The Run-proof refusals every surface that takes a proof shares: a proof
// that is not the member's live Run in this Company, and a read or create a
// Run may not make. The second is one fixed sentence that names no Task,
// Project, member or Agent; the docs and the Agent's skill quote it, so it
// is written once.
var (
	runTokenInvalidMapping = errorMapping{
		execution.ErrRunProofInvalid, http.StatusUnauthorized, "Run token is invalid", "run_token_invalid",
	}
	taskOutsideRunScopeMapping = errorMapping{
		task.ErrOutsideRunScope,
		http.StatusForbidden,
		"Permission denied. You can only access your own Tasks.",
		"task_outside_run_scope",
	}
)

// runProofErrorMappings render the refusals of the two checks below, which
// several surfaces share: a Run's own reads and the daemon's uploads.
var runProofErrorMappings = []errorMapping{runTokenInvalidMapping, taskOutsideRunScopeMapping}

func writeRunProofError(c *gin.Context, err error) {
	writeMappedError(c, err, runProofErrorMappings, internalErrorFallback)
}

// runOriginResolver hands RunProofPolicy the proof resolver, which the
// credential guards use to admit a daemon's own session as a Run. With
// no resolver no daemon session is admitted off the daemon plane.
func (h *Handler) runOriginResolver() middleware.RunOriginResolver {
	if h.RunProofResolver == nil {
		return nil
	}
	return h.RunProofResolver.ResolveRunOrigin
}

// resolveRunOrigin resolves the request's Run proof once. On a daemon's own
// session the credential guard already resolved it, and checked that the
// Run works for the session's person (middleware.ResolvedRunOrigin); on a
// person's session it is resolved here. Callers check RunProofResolver is
// set first.
func (h *Handler) resolveRunOrigin(c *gin.Context, token string) (execution.RunOrigin, error) {
	if origin, ok := middleware.ResolvedRunOrigin(c, token); ok {
		return origin, nil
	}
	return h.RunProofResolver.ResolveRunOrigin(c.Request.Context(), token)
}

// runOwnReadAdmitted fences a Run's own reads: its Run, its chat, that
// chat's messages and its transcript. A request without a proof is the
// member's own and is admitted. With one, the proof must be the member's
// live Run (runProofBearer) and the path must name that Run's own record
// or chat (names; a Run tagged in a channel or group DM owns no chat read,
// see runOwnsConversation). Anything else is refused before the read:
// run_token_invalid for a proof that is not this member's live Run,
// task_outside_run_scope for another Run or chat.
func (h *Handler) runOwnReadAdmitted(
	c *gin.Context,
	principal auth.UserPrincipal,
	names func(execution.RunOrigin) bool,
) bool {
	token := c.GetHeader(runTokenHeader)
	if token == "" {
		return true
	}
	if h.RunProofResolver == nil {
		writeRunProofError(c, errors.New("Run proof resolver is not configured"))
		return false
	}
	origin, err := h.resolveRunOrigin(c, token)
	if err != nil {
		writeRunProofError(c, err)
		return false
	}
	bearer := principal
	if bearer.CompanyID == uuid.Nil {
		// The messages route sits outside /companies, so no Company is
		// resolved on it. The chat it names must be the Run's own, which
		// fixes the Company; the session must still be the Run's authorizer.
		bearer.CompanyID = origin.CompanyID
	}
	if !runProofBearer(bearer, origin) {
		writeRunProofError(c, execution.ErrRunProofInvalid)
		return false
	}
	if !names(origin) {
		writeRunProofError(c, task.ErrOutsideRunScope)
		return false
	}
	return true
}

// runUploadAdmitted checks the Run proof an upload carries. The daemon
// uploads the files a Run presents on the member's session; with a proof,
// the Run must be that member's live Run in the Company the upload names
// (runProofBearer), or the upload is run_token_invalid before any byte is
// stored. It returns that Run, whose upload is then recorded as the Run's.
// Without a proof the upload is the member's own, as before, and the
// Run is nil.
func (h *Handler) runUploadAdmitted(
	c *gin.Context,
	principal auth.UserPrincipal,
	companyID uuid.UUID,
) (*execution.RunOrigin, bool) {
	token := c.GetHeader(runTokenHeader)
	if token == "" {
		return nil, true
	}
	principal.CompanyID = companyID
	origin, ok := h.runProofOrigin(c, principal, token, writeRunProofError)
	if !ok {
		return nil, false
	}
	return &origin, true
}

// runProofBearer reports whether a live Run's proof may ride on this
// session: the Run is in the session's Company and works for the session's
// member, its authorizer (ADR 0014, 0016). The claim fence already makes the
// daemon's session, the machine's owner and the authorizer one person;
// every route that takes a proof says so rather than trusting it, so a proof
// copied onto another member's session acts for nobody. A Run with no
// authorizer is never borne.
func runProofBearer(principal auth.UserPrincipal, origin execution.RunOrigin) bool {
	return origin.CompanyID == principal.CompanyID &&
		origin.AuthorizedByUserID != nil && *origin.AuthorizedByUserID == principal.UserID
}

// runProofOrigin resolves the Run proof a request carries beside the
// member's session. It admits only a Run that is live in this Company and
// works for that member (runProofBearer); anything else is written through
// writeErr as run_token_invalid.
func (h *Handler) runProofOrigin(
	c *gin.Context,
	principal auth.UserPrincipal,
	token string,
	writeErr func(*gin.Context, error),
) (execution.RunOrigin, bool) {
	if h.RunProofResolver == nil {
		writeErr(c, errors.New("Run proof resolver is not configured"))
		return execution.RunOrigin{}, false
	}
	origin, err := h.resolveRunOrigin(c, token)
	if err == nil && !runProofBearer(principal, origin) {
		err = execution.ErrRunProofInvalid
	}
	if err != nil {
		writeErr(c, err)
		return execution.RunOrigin{}, false
	}
	return origin, true
}

// runReadScope is the scope a Task or Project read or create is made
// under. `zen task` and `zen project` reach these routes through a Run's
// channel, with the Run's proof beside the member's session. A proof that
// is not the member's live Run in this Company is written through writeErr
// as run_token_invalid. Cancelling a Run (a handoff, a user cancel, a lost
// grant) deletes its tokens, so a cancelled Run cannot read or file work in
// the seconds before its daemon notices. A member's own request carries no
// proof and gets a nil scope.
func (h *Handler) runReadScope(
	c *gin.Context,
	principal auth.UserPrincipal,
	writeErr func(*gin.Context, error),
) (*task.RunScope, bool) {
	token := c.GetHeader(runTokenHeader)
	if token == "" {
		return nil, true
	}
	origin, ok := h.runProofOrigin(c, principal, token, writeErr)
	if !ok {
		return nil, false
	}
	scope, err := runProofScope(origin)
	if err != nil {
		writeErr(c, err)
		return nil, false
	}
	return scope, true
}

// runProofScope turns a borne Run's origin into the scope its reads are
// made under (task.RunScope). The reads are made with the authorizer's
// session and so with their Project access; the scope narrows them by the
// delegate rule, which the database reads from the Run's own row: the
// Authorizer's access, and a Task Run only in its Task chat's Project. A
// tagged Run is bounded by its Authorizer's access alone. A Task chat with
// no Project admits no Run (the claim fence), so a proof for one is
// run_token_invalid. The planner (runIsPlanner) is not narrowed to its own
// Tasks.
func runProofScope(origin execution.RunOrigin) (*task.RunScope, error) {
	scope := &task.RunScope{
		AgentID: origin.AgentID, UserID: *origin.AuthorizedByUserID, RunID: origin.RunID,
		TaskID: origin.TaskID, Planner: runIsPlanner(origin),
	}
	if origin.ConversationKind == "task" {
		if origin.ProjectID == nil {
			return nil, execution.ErrRunProofInvalid
		}
		scope.ProjectID = origin.ProjectID
	}
	return scope, nil
}

// runIsPlanner reports whether a borne Run reads as the Company's planner
// (task.RunScope.Planner): the root Agent's Run, with its Company
// Plan tools on, in its own person's direct chat, with no Task and no
// tagging channel. Its answer reaches only that person, so it reads every
// Task that person reads, which its Company Plan export needs. The server
// decides this from the proof alone; what the daemon offers the Run is not
// authority. Every other Run reads only its own Tasks.
func runIsPlanner(origin execution.RunOrigin) bool {
	return origin.RootAgent && !origin.PlanningDisabled &&
		origin.ConversationKind == "direct" &&
		origin.OriginUserID != nil && origin.AuthorizedByUserID != nil &&
		*origin.OriginUserID == *origin.AuthorizedByUserID &&
		origin.TaskID == nil && origin.InvocationChannelID == nil
}

// contentRunPrincipal proves the request comes from the Run in the path: a Run
// token that resolves to this Company and Run, in a Conversation that is not a
// Schedule's, borne by the Run's authorizer (runProofBearer, ADR 0017). Anything
// else is run_token_invalid, so a probe cannot tell a wrong Run from a bad
// token. Each content service further narrows access under its transaction locks.
func (h *Handler) contentRunPrincipal(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	runID, err := util.ParseGoUUID(c.Param("runId"))
	token := c.GetHeader(runTokenHeader)
	if err != nil || h.RunProofResolver == nil || token == "" {
		writeContentRunError(c, execution.ErrRunProofInvalid)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	origin, err := h.resolveRunOrigin(c, token)
	if err != nil {
		writeContentRunError(c, err)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	if !runProofBearer(principal, origin) || origin.RunID != runID || origin.ConversationKind == "schedule" {
		writeContentRunError(c, execution.ErrRunProofInvalid)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	return principal, runID, true
}

func writeContentRunError(c *gin.Context, err error) {
	writeMappedError(c, err, []errorMapping{runTokenInvalidMapping}, internalErrorFallback)
}
