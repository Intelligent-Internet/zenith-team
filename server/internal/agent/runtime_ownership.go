package agent

// Runtime ownership (ADR 0014). A provider Runtime belongs to the member who
// registered it, and to nobody else: the registrant alone lists, reads,
// renames, removes and probes it, binds Agents to it, and runs work on it.
// There is no sharing and no Company role that overrides this — another
// member's machine is invisible to every other member, owner and admin
// included, so a management write judged on a locked row refuses exactly the
// rows the reads hide, and refuses them as an unknown id rather than
// confirming a registration exists. The same fact gates the roster and
// detail reads in SQL (ListRuntimesForCompany, GetRuntimeForCompany), the
// bind in binding.go, the writes in runtime_rename.go and
// runtime_removal.go, the machine RPCs in runtime_models.go and
// runtime_refresh.go, the Connector and Plugin catalog reads, and, in
// internal/execution, every enqueue and claim.

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// RuntimeAccess is the viewer's server-derived standing on an Agent through
// their own binding of it (see protocol.RuntimeAccess). Runtime rows carry
// none: a row a viewer may not use is never served.
type RuntimeAccess = protocol.RuntimeAccess

// registeredBy is the whole ownership rule: the row's stored registrant is
// this member. A runtime with no stored registrant belongs to nobody.
func registeredBy(ownerUserID uuid.NullUUID, userID uuid.UUID) bool {
	return ownerUserID.Valid && ownerUserID.UUID == userID
}

// catalogRuntimeMissing answers a Connector or Plugin catalog read whose
// runtime selection found nothing. A runtime_id that names no runtime the
// caller registered — another member's, another company's, removed or
// unknown — is ErrRuntimeNotFound, the answer every runtime route gives, so
// a known id is not an oracle. The caller's own runtime under another
// provider, or no runtime at all, is the catalog's own refusal, unavailable.
func catalogRuntimeMissing(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	runtimeID *uuid.UUID,
	unavailable error,
) error {
	if runtimeID == nil {
		return unavailable
	}
	_, err := q.GetRuntimeForCompany(ctx, generated.GetRuntimeForCompanyParams{
		CompanyID:    principal.CompanyID,
		ID:           *runtimeID,
		ViewerUserID: principal.UserID,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrRuntimeNotFound
	case err != nil:
		return fmt.Errorf("read selected runtime: %w", err)
	default:
		return unavailable
	}
}
