//go:build integration

package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func principalFor(fixture dbtest.Fixture, userID uuid.UUID, role auth.CompanyRole) auth.UserPrincipal {
	return auth.UserPrincipal{
		UserID: userID, SessionID: uuid.Must(uuid.NewV7()), AuthKind: auth.TokenKindBearer,
		CompanyID: fixture.CompanyA, Role: role,
	}
}

// Any active member creates Agent templates and may set one up on a
// machine of their own at creation (ADR 0013 with ADR 0014). The binding is
// the creator's alone: another member sees the template with no binding
// and no execution until they set it up themselves, and cannot create on
// the creator's machine. Configuration afterwards is the creator's and the
// Company owners'; another member is refused. The root Agent alone stays
// the Company owner's to create.
func TestMembersCreateAgentsAndConfigureTheirOwn(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	agents, err := NewAgentService(ServiceOptions{Store: store, Bus: mustAgentTestBus(t)})
	if err != nil {
		t.Fatalf("NewAgentService() error = %v", err)
	}
	quy := seedMember(t, ctx, pool, fixture, "quy@boardly.test", "member")
	eli := seedMember(t, ctx, pool, fixture, "eli@boardly.test", "member")
	quysRuntime := seedRuntime(t, ctx, pool, fixture, "daemon-quy", "codex", quy)
	quyPrincipal := principalFor(fixture, quy, auth.CompanyRoleMember)
	eliPrincipal := principalFor(fixture, eli, auth.CompanyRoleMember)
	owner := principalFor(fixture, fixture.UserA, auth.CompanyRoleOwner)

	// Quy creates a codex Agent and sets it up on their own machine at once.
	input := validCreateInput()
	input.ReportsToAgentID = &fixture.AgentA
	input.Provider = "codex"
	input.RuntimeID = &quysRuntime
	created, err := agents.Create(ctx, quyPrincipal, input)
	if err != nil {
		t.Fatalf("member Create() bound to their own runtime error = %v", err)
	}
	if created.CreatedBy == nil || created.CreatedBy.ID != quy || !created.Access.Configure {
		t.Fatalf("created Agent = createdBy %+v access %+v, want Quy with configure", created.CreatedBy, created.Access)
	}
	if created.Binding == nil || created.Binding.Runtime.ID != quysRuntime || !created.Access.Execute ||
		created.Provider != "codex" {
		t.Fatalf("creator's view = binding %+v access %+v provider %q, want their own binding with execute", created.Binding, created.Access, created.Provider)
	}
	var creator string
	if err := pool.QueryRow(ctx,
		`SELECT created_by_user_id::text FROM agents WHERE id = $1`, created.ID,
	).Scan(&creator); err != nil || creator != quy.String() {
		t.Fatalf("creator = %s, %v; want Quy", creator, err)
	}

	// Eli sees the template, not Quy's machine: no binding, no execution,
	// and no configure, until Eli sets it up on a computer of their own.
	seen, err := agents.Get(ctx, eliPrincipal, created.ID)
	if err != nil {
		t.Fatalf("member Get() error = %v", err)
	}
	if seen.Binding != nil || seen.Access.Execute || seen.Access.Configure ||
		seen.UnavailableReason == nil || *seen.UnavailableReason != "not_bound" {
		t.Fatalf("another member's view = binding %+v access %+v reason %v, want a bare template", seen.Binding, seen.Access, seen.UnavailableReason)
	}
	// Eli may not create an Agent bound to Quy's machine: it is invisible.
	if _, err := agents.Create(ctx, eliPrincipal, input); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("member Create() on another member's runtime error = %v, want ErrRuntimeNotFound (not an oracle)", err)
	}
	// A template of the other provider cannot be set up on a codex machine.
	mismatched := validCreateInput()
	mismatched.ReportsToAgentID = &fixture.AgentA
	mismatched.Provider = "claude"
	mismatched.RuntimeID = &quysRuntime
	if _, err := agents.Create(ctx, quyPrincipal, mismatched); !errors.Is(err, ErrBindingProviderMismatch) {
		t.Fatalf("Create(claude template on a codex runtime) error = %v, want ErrBindingProviderMismatch", err)
	}
	// And a template needs no machine at all.
	unbound := validCreateInput()
	unbound.ReportsToAgentID = &fixture.AgentA
	unbound.Provider = "claude"
	unbound.RuntimeID = nil
	if template, err := agents.Create(ctx, eliPrincipal, unbound); err != nil || template.Binding != nil || template.Access.Execute {
		t.Fatalf("Create(template) = %+v, %v; want an unbound template", template, err)
	}

	// Configuration: Eli is refused, Quy and the Company owner are not.
	title := "Renamed by a bystander"
	if _, err := agents.Update(ctx, eliPrincipal, created.ID, UpdateInput{
		ExpectedRevision: created.Revision, Title: &title,
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("another member Update() error = %v, want ErrForbidden", err)
	}
	title = "Renamed by its creator"
	updated, err := agents.Update(ctx, quyPrincipal, created.ID, UpdateInput{
		ExpectedRevision: created.Revision, Title: &title,
	})
	if err != nil || updated.Title != title {
		t.Fatalf("creator Update() = %+v, %v", updated, err)
	}
	title = "Renamed by the owner"
	updated, err = agents.Update(ctx, owner, created.ID, UpdateInput{
		ExpectedRevision: updated.Revision, Title: &title,
	})
	if err != nil || updated.Title != title || !updated.Access.Configure {
		t.Fatalf("owner Update() = %+v, %v", updated, err)
	}
	if _, err := agents.Archive(ctx, eliPrincipal, created.ID, RevisionInput{ExpectedRevision: updated.Revision}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("another member Archive() error = %v, want ErrForbidden", err)
	}

	// The owner's pre-existing Agent has no recorded creator: owners configure
	// it, members do not.
	if _, err := agents.Update(ctx, quyPrincipal, fixture.AgentA, UpdateInput{
		ExpectedRevision: 1, Title: &title,
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member Update(owner's Agent) error = %v, want ErrForbidden", err)
	}

	// The root Agent stays the owner's to create.
	root := validCreateInput()
	root.ReportsToAgentID = nil
	root.Provider = "codex"
	root.RuntimeID = &quysRuntime
	if _, err := agents.Create(ctx, quyPrincipal, root); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member Create(root) error = %v, want ErrForbidden", err)
	}

	// An admin holds agents.manage and configures any Agent, Quy's included;
	// the creator's standing does not reach the root Agent, so an onboarding
	// owner demoted to member does not keep the CEO through the creator record.
	admin := principalFor(fixture, seedMember(t, ctx, pool, fixture, "ada@boardly.test", "admin"), auth.CompanyRoleAdmin)
	seen, err = agents.Get(ctx, admin, created.ID)
	if err != nil || !seen.Access.Configure {
		t.Fatalf("admin view of a member's Agent = %+v, %v; want configure", seen.Access, err)
	}
	title = "Renamed by an admin"
	if _, err := agents.Update(ctx, admin, created.ID, UpdateInput{ExpectedRevision: updated.Revision, Title: &title}); err != nil {
		t.Fatalf("admin Update() error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE agents SET created_by_user_id = $1 WHERE id = $2`, quy, fixture.AgentA)
	rootView, err := agents.Get(ctx, quyPrincipal, fixture.AgentA)
	if err != nil || rootView.Access.Configure {
		t.Fatalf("member creator's view of the root Agent = %+v, %v; want no configure", rootView.Access, err)
	}
	if _, err := agents.Update(ctx, quyPrincipal, fixture.AgentA, UpdateInput{ExpectedRevision: 1, Title: &title}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member creator Update(root Agent) error = %v, want ErrForbidden", err)
	}

	// Every member reads every Agent.
	all, err := agents.List(ctx, eliPrincipal, ListOptions{})
	if err != nil || len(all) < 2 {
		t.Fatalf("member List() = %d agents, %v; want the roster", len(all), err)
	}
}
