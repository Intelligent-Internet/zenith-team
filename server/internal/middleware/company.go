package middleware

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

// HeaderRunToken carries a Run's proof on requests the daemon's Run channel
// (or zenith-team-mcp) makes on behalf of an Agent. Its presence is what makes a
// request a machine actor's; RequireHumanActor refuses it.
const HeaderRunToken = "X-Zenith-Team-Run-Token"

// RequireCompanyMember admits any active member of the :companyId company
// and stamps the company scope onto the request principal.
func RequireCompanyMember(service company.CompanyService) gin.HandlerFunc {
	return requireCompanyAccess(service, "")
}

// RequireCompanyPermission admits an active member whose role holds the
// permission. The refusal is 403 company_permission_required: the caller can
// see the company, so the answer does not have to hide it.
func RequireCompanyPermission(service company.CompanyService, permission auth.Permission) gin.HandlerFunc {
	return requireCompanyAccess(service, permission)
}

// RouteActorType says who a route accepts: a person only, a Run (a request
// carrying its proof beside the member's session), or either.
type RouteActorType string

const (
	// RouteActorTypePerson refuses a Run proof. A route with no entry in the
	// table counts as this.
	RouteActorTypePerson RouteActorType = "person"
	// RouteActorTypeRun admits a Run proof; the handler resolves it and checks
	// its bearer.
	RouteActorTypeRun RouteActorType = "run"
	// RouteActorTypeAny admits a Run proof without reading it: the route holds
	// nothing a Run may not see.
	RouteActorTypeAny RouteActorType = "any"
)

// RunOriginResolver resolves a Run proof to its live Run; a proof that is
// not live is execution.ErrRunProofInvalid. The handler passes its Run
// proof resolver.
type RunOriginResolver func(context.Context, string) (execution.RunOrigin, error)

// runProofRoute is what RunProofPolicy learned about the matched route, kept
// on the request for the credential guards after it: whether the route
// accepts a Run, and how to resolve a daemon session's proof there.
type runProofRoute struct {
	actorType RouteActorType
	resolve   RunOriginResolver
}

// resolvedRunProof is a daemon session's proof, resolved once by the
// credential guard and read back by the handler (ResolvedRunOrigin).
type resolvedRunProof struct {
	token  string
	origin execution.RunOrigin
}

type runProofContextKey int

const (
	ctxKeyRunProofRoute runProofContextKey = iota
	ctxKeyResolvedRunProof
)

// RunProofPolicy refuses a Run proof on every route that does not say it
// accepts one. types is keyed by method and full route path, for example
// "POST /api/v1/companies/:companyId/approvals/:approvalId/approve". A
// request carrying the proof to a route with no entry, or a person entry,
// gets 403 human_actor_required before authentication and before the
// handler. A request without the proof is never touched, and a path no route
// matches passes, so NoRoute still answers route_not_found. Mount it as a
// global middleware: Gin sets the full path before global middleware runs.
//
// It also leaves the route's actor type and resolve on the request for the
// credential guards, which admit a daemon's own session off the daemon
// plane only as a Run (admitDaemonSession). A nil resolve admits no
// daemon session there.
func RunProofPolicy(types map[string]RouteActorType, resolve RunOriginResolver) gin.HandlerFunc {
	return func(c *gin.Context) {
		actorType := types[c.Request.Method+" "+c.FullPath()]
		c.Request = c.Request.WithContext(context.WithValue(
			c.Request.Context(), ctxKeyRunProofRoute, runProofRoute{actorType: actorType, resolve: resolve},
		))
		if !carriesRunProof(c) || c.FullPath() == "" {
			c.Next()
			return
		}
		switch actorType {
		case RouteActorTypeRun, RouteActorTypeAny:
			c.Next()
		default:
			RefuseRunProof(c)
		}
	}
}

// admitDaemonSession lets a daemon's own session past a route outside the
// daemon plane only as a Run. The route must accept a Run
// (run or any in RunProofPolicy's table), and the request must carry exactly
// one non-empty proof that resolves to a live Run whose Authorizer is the
// session's person. A blank or repeated header is refused here: a handler
// reads only the first value, and an empty one means "no Run". The proof is
// resolved once and kept on the request (ResolvedRunOrigin). Anything else
// is 403 run_proof_required: a daemon session is never its person.
func admitDaemonSession(c *gin.Context, principal auth.UserPrincipal) bool {
	route, _ := c.Request.Context().Value(ctxKeyRunProofRoute).(runProofRoute)
	values := c.Request.Header.Values(HeaderRunToken)
	if (route.actorType != RouteActorTypeRun && route.actorType != RouteActorTypeAny) ||
		route.resolve == nil || len(values) != 1 || values[0] == "" {
		refuseDaemonSession(c)
		return false
	}
	origin, err := route.resolve(c.Request.Context(), values[0])
	if err != nil && !errors.Is(err, execution.ErrRunProofInvalid) {
		util.WriteError(c, http.StatusInternalServerError, "internal server error", "internal_error")
		c.Abort()
		return false
	}
	if err != nil || origin.AuthorizedByUserID == nil || *origin.AuthorizedByUserID != principal.UserID {
		refuseDaemonSession(c)
		return false
	}
	c.Request = c.Request.WithContext(context.WithValue(
		c.Request.Context(), ctxKeyResolvedRunProof, resolvedRunProof{token: values[0], origin: origin},
	))
	return true
}

func refuseDaemonSession(c *gin.Context) {
	util.WriteError(
		c,
		http.StatusForbidden,
		"A daemon session can act here only for a live Run of its person",
		"run_proof_required",
	)
	c.Abort()
}

// ResolvedRunOrigin returns the Run the credential guard already resolved
// for this proof on a daemon session, so the handler does not resolve it a
// second time. On a person's session it reports false: the handler resolves
// the proof itself.
func ResolvedRunOrigin(c *gin.Context, token string) (execution.RunOrigin, bool) {
	resolved, ok := c.Request.Context().Value(ctxKeyResolvedRunProof).(resolvedRunProof)
	if !ok || token == "" || resolved.token != token {
		return execution.RunOrigin{}, false
	}
	return resolved.origin, true
}

// RequireHumanActor refuses a request that carries a Run proof. A Run acting
// through the zen channel authenticates as the member who authorized it, so
// without this guard an Agent could reshape the Company — invite, remove,
// promote, delete — with that member's role. RunProofPolicy already refuses
// a proof on every route that does not accept a Run; Team and Company
// lifecycle mutations keep this guard as a second check. Run-proven routes
// such as milestone attestations must not mount it.
func RequireHumanActor() gin.HandlerFunc {
	return func(c *gin.Context) {
		if RefuseRunProof(c) {
			return
		}
		c.Next()
	}
}

// RefuseRunProof answers 403 human_actor_required and aborts when the request
// carries a Run proof, and reports whether it did. RequireHumanActor is this
// check as a route guard; a handler whose query selects a person-only view
// (GET /projects?view=manage) calls it directly.
func RefuseRunProof(c *gin.Context) bool {
	if !carriesRunProof(c) {
		return false
	}
	util.WriteError(c, http.StatusForbidden, "This action must be taken by a person", "human_actor_required")
	c.Abort()
	return true
}

// carriesRunProof reports whether the request sent the Run proof header at
// all. An empty value still counts, so a blank or repeated header cannot slip
// past the refusal.
func carriesRunProof(c *gin.Context) bool {
	return len(c.Request.Header.Values(HeaderRunToken)) > 0
}

func requireCompanyAccess(service company.CompanyService, permission auth.Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		companyID, err := util.ParseGoUUID(c.Param("companyId"))
		if err != nil {
			util.WriteError(c, http.StatusNotFound, "Company not found", "company_not_found")
			c.Abort()
			return
		}
		principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
		if !ok {
			util.WriteError(
				c,
				http.StatusUnauthorized,
				"authentication is required",
				"authentication_required",
			)
			c.Abort()
			return
		}
		var access auth.UserPrincipal
		if permission != "" {
			access, err = service.RequirePermission(c.Request.Context(), principal, companyID, permission)
		} else {
			access, err = service.Authorize(c.Request.Context(), principal, companyID)
		}
		if err != nil {
			writeCompanyAccessError(c, err)
			c.Abort()
			return
		}
		principal.CompanyID = access.CompanyID
		principal.Role = access.Role
		c.Request = c.Request.WithContext(auth.WithUserPrincipal(c.Request.Context(), principal))
		c.Next()
	}
}

// writeCompanyAccessError renders the three refusals Authorize and
// RequirePermission can return. internal/handler's companyErrorMappings
// renders the same sentinels for the route handlers; the cross-company matrix
// test pins both, so a divergence fails the suite rather than leaking.
func writeCompanyAccessError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, company.ErrUnauthenticated):
		util.WriteError(c, http.StatusUnauthorized, "authentication is required", "authentication_required")
	case errors.Is(err, company.ErrForbidden):
		util.WriteError(
			c,
			http.StatusForbidden,
			"Your Company role does not allow this action",
			"company_permission_required",
		)
	case errors.Is(err, company.ErrNotFound):
		util.WriteError(c, http.StatusNotFound, "Company not found", "company_not_found")
	default:
		util.WriteError(c, http.StatusInternalServerError, "internal server error", "internal_error")
	}
}
