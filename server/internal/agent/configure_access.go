package agent

// Who may change an Agent (ADR 0013). Agents are the Company's, and any
// active member may create one on a runtime they may use; changing one
// afterwards — configuration, binding, archive, restore — is for the member
// who created it and for the holders of agents.manage (the Company's owners
// and admins, ADR 0012). The rule is "only the agent's owner, or an admin,
// can manage this agent", with the creator
// standing in for an owner the Company-owned Agent does not have. The root
// Agent is the exception both ways: creating and configuring it need
// agents.manage, so an onboarding owner who is later demoted does not keep
// the CEO through the creator record. Reads are every active member's.

import (
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// Access is the viewer's server-derived standing on one Agent, sent on
// every Agent projection so a client renders controls from it instead of
// reconstructing permission from a role. It is the viewer's, not the row's:
// a lifecycle event carries the acting member's verdict, and a client
// applying such a frame keeps its own.
type Access struct {
	// Configure: the viewer may update, archive or restore the Agent.
	Configure bool `json:"configure"`
	// Execute: the viewer has the Agent set up on one of their own runtimes
	// and that runtime can run work right now (ADR 0014) — the same fact
	// runtimeAvailable carries.
	Execute bool `json:"execute"`
}

// canConfigureAgent is the whole rule: agents.manage always; the creator of
// the Agent otherwise, except on the root Agent. An Agent with no recorded
// creator — rows from before ADR 0013 — is configurable by agents.manage
// holders alone.
func canConfigureAgent(role auth.CompanyRole, row generated.Agent, userID uuid.UUID) bool {
	if role.Can(auth.PermissionAgentsManage) {
		return true
	}
	if !row.ReportsToAgentID.Valid {
		return false
	}
	return row.CreatedByUserID.Valid && row.CreatedByUserID.UUID == userID
}
