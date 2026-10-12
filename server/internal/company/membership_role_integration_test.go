//go:build integration

package company

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

// roleFixture seeds Company A with its founder (owner), one admin and one
// member, and returns a service plus a principal per role.
type roleFixture struct {
	service *DefaultCompanyService
	events  *[]events.Event
	owner   auth.UserPrincipal
	admin   auth.UserPrincipal
	member  auth.UserPrincipal
}

func seedRoleFixture(t *testing.T) roleFixture {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	adminID, memberID := dbtest.NewUUID(t), dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO users (id, email, display_name, email_verified_at) VALUES
			($1, 'admin@example.com', 'Admin', now()),
			($2, 'member@example.com', 'Member', now())`, adminID, memberID)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role) VALUES
			($1, $2, 'admin'), ($1, $3, 'member')`, fixture.CompanyA, adminID, memberID)
	bus := companyTestBus(t)
	var seen []events.Event
	if err := bus.SubscribeAll(func(_ context.Context, event events.Event) error {
		seen = append(seen, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewCompanyService(ServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	principal := func(userID uuid.UUID, role auth.CompanyRole) auth.UserPrincipal {
		return auth.UserPrincipal{
			UserID:    userID,
			SessionID: dbtest.NewUUID(t),
			AuthKind:  auth.TokenKindSession,
			CompanyID: fixture.CompanyA,
			Role:      role,
		}
	}
	return roleFixture{
		service: service,
		events:  &seen,
		owner:   principal(fixture.UserA, auth.CompanyRoleOwner),
		admin:   principal(adminID, auth.CompanyRoleAdmin),
		member:  principal(memberID, auth.CompanyRoleMember),
	}
}

func (f roleFixture) revisionOf(t *testing.T, userID uuid.UUID) int64 {
	t.Helper()
	members, err := f.service.ListMembers(context.Background(), f.owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range members {
		if member.Actor.ID == userID {
			return member.Revision
		}
	}
	t.Fatalf("member %s not in roster", userID)
	return 0
}

func TestRequirePermissionFollowsTheCatalogue(t *testing.T) {
	t.Parallel()
	f := seedRoleFixture(t)
	ctx := context.Background()
	for _, testCase := range []struct {
		name       string
		principal  auth.UserPrincipal
		permission auth.Permission
		want       error
	}{
		{"owner deletes", f.owner, auth.PermissionCompanyDelete, nil},
		{"admin cannot delete", f.admin, auth.PermissionCompanyDelete, ErrForbidden},
		{"admin manages agents", f.admin, auth.PermissionAgentsManage, nil},
		{"member cannot manage agents", f.member, auth.PermissionAgentsManage, ErrForbidden},
		// An empty permission is refused: membership alone is Authorize's job.
		{"empty permission fails closed", f.member, "", ErrForbidden},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			access, err := f.service.RequirePermission(
				ctx,
				testCase.principal,
				f.owner.CompanyID,
				testCase.permission,
			)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("RequirePermission() error = %v, want %v", err, testCase.want)
			}
			if err == nil &&
				(access.CompanyID != f.owner.CompanyID || access.Role != testCase.principal.Role) {
				t.Fatalf("RequirePermission() = %+v", access)
			}
		})
	}
	stranger := f.member
	stranger.UserID = dbtest.NewUUID(t)
	if _, err := f.service.RequirePermission(
		ctx,
		stranger,
		f.owner.CompanyID,
		auth.PermissionAgentsManage,
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("non-member RequirePermission() error = %v, want ErrNotFound (never 403)", err)
	}
}

func TestChangeRoleEnforcesTheMembershipRules(t *testing.T) {
	t.Parallel()
	f := seedRoleFixture(t)
	ctx := context.Background()
	memberRevision := f.revisionOf(t, f.member.UserID)

	if _, err := f.service.ChangeRole(ctx, f.member, ChangeRoleInput{
		UserID: f.admin.UserID, Role: auth.CompanyRoleMember, ExpectedRevision: 1,
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member changing a role = %v, want ErrForbidden", err)
	}
	if _, err := f.service.ChangeRole(ctx, f.admin, ChangeRoleInput{
		UserID: f.admin.UserID, Role: auth.CompanyRoleOwner, ExpectedRevision: 1,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("self role change = %v, want ErrInvalidInput", err)
	}
	if _, err := f.service.ChangeRole(ctx, f.admin, ChangeRoleInput{
		UserID: f.member.UserID, Role: auth.CompanyRoleOwner, ExpectedRevision: memberRevision,
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin granting owner = %v, want ErrForbidden", err)
	}
	if _, err := f.service.ChangeRole(ctx, f.admin, ChangeRoleInput{
		UserID: f.owner.UserID, Role: auth.CompanyRoleAdmin, ExpectedRevision: 1,
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin demoting an owner = %v, want ErrForbidden", err)
	}
	if _, err := f.service.ChangeRole(ctx, f.admin, ChangeRoleInput{
		UserID: f.member.UserID, Role: auth.CompanyRole("viewer"), ExpectedRevision: memberRevision,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown role = %v, want ErrInvalidInput", err)
	}
	if _, err := f.service.ChangeRole(ctx, f.admin, ChangeRoleInput{
		UserID: f.member.UserID, Role: auth.CompanyRoleAdmin, ExpectedRevision: memberRevision + 7,
	}); !errors.Is(err, ErrMemberRevisionConflict) {
		t.Fatalf("stale revision = %v, want ErrMemberRevisionConflict", err)
	}
	if _, err := f.service.ChangeRole(ctx, f.owner, ChangeRoleInput{
		UserID: f.owner.UserID, Role: auth.CompanyRoleAdmin, ExpectedRevision: 1,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("owner demoting themselves = %v, want ErrInvalidInput (use transfer)", err)
	}

	// The same role is a no-op that neither bumps the revision nor emits.
	before := len(*f.events)
	unchanged, err := f.service.ChangeRole(ctx, f.admin, ChangeRoleInput{
		UserID: f.member.UserID, Role: auth.CompanyRoleMember, ExpectedRevision: memberRevision,
	})
	if err != nil || unchanged.Revision != memberRevision || unchanged.Role != auth.CompanyRoleMember {
		t.Fatalf("unchanged role = %+v, %v", unchanged, err)
	}
	if len(*f.events) != before {
		t.Fatalf("unchanged role emitted %d events", len(*f.events)-before)
	}

	// An admin promotes a member to admin: the projection, the frames and
	// the Activity row all agree.
	promoted, err := f.service.ChangeRole(ctx, f.admin, ChangeRoleInput{
		UserID: f.member.UserID, Role: auth.CompanyRoleAdmin, ExpectedRevision: memberRevision,
	})
	if err != nil {
		t.Fatalf("ChangeRole() error = %v", err)
	}
	if promoted.Role != auth.CompanyRoleAdmin || promoted.Revision != memberRevision+1 ||
		promoted.Actor.ID != f.member.UserID || promoted.Email != "member@example.com" {
		t.Fatalf("promoted = %+v", promoted)
	}
	var sawMemberUpdated, sawAccessChanged bool
	for _, event := range (*f.events)[before:] {
		switch event.Name {
		case EventMemberUpdated:
			sawMemberUpdated = true
		case EventAuthCompanyAccessChanged:
			receipt, err := events.PayloadOf[AccessChangedReceipt](event)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.UserID != f.member.UserID || receipt.Role != auth.CompanyRoleAdmin ||
				receipt.Reason != AccessReasonRoleChanged ||
				len(receipt.Permissions) != len(auth.PermissionsOf(auth.CompanyRoleAdmin)) {
				t.Fatalf("access receipt = %+v", receipt)
			}
			sawAccessChanged = true
		}
	}
	if !sawMemberUpdated || !sawAccessChanged {
		t.Fatalf("role change frames: member.updated=%t access_changed=%t", sawMemberUpdated, sawAccessChanged)
	}

	// The last owner cannot be demoted, even by an owner.
	if _, err := f.service.ChangeRole(ctx, f.owner, ChangeRoleInput{
		UserID: f.owner.UserID, Role: auth.CompanyRoleMember, ExpectedRevision: 1,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("self demotion = %v", err)
	}
	// Promote a second owner, then demoting the founder is allowed and the
	// last-owner rule applies to whoever is left.
	if _, err := f.service.ChangeRole(ctx, f.owner, ChangeRoleInput{
		UserID: f.admin.UserID, Role: auth.CompanyRoleOwner, ExpectedRevision: f.revisionOf(t, f.admin.UserID),
	}); err != nil {
		t.Fatalf("owner promoting admin to owner = %v", err)
	}
	secondOwner := f.admin
	secondOwner.Role = auth.CompanyRoleOwner
	if _, err := f.service.ChangeRole(ctx, secondOwner, ChangeRoleInput{
		UserID: f.owner.UserID, Role: auth.CompanyRoleAdmin, ExpectedRevision: f.revisionOf(t, f.owner.UserID),
	}); err != nil {
		t.Fatalf("second owner demoting the founder = %v", err)
	}
	if _, err := f.service.ChangeRole(ctx, f.owner, ChangeRoleInput{
		UserID:           secondOwner.UserID,
		Role:             auth.CompanyRoleAdmin,
		ExpectedRevision: f.revisionOf(t, secondOwner.UserID),
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("demoted founder touching an owner = %v, want ErrForbidden", err)
	}
	if _, err := f.service.ChangeRole(ctx, secondOwner, ChangeRoleInput{
		UserID: f.owner.UserID, Role: auth.CompanyRoleOwner, ExpectedRevision: f.revisionOf(t, f.owner.UserID),
	}); err != nil {
		t.Fatalf("restoring the founder = %v", err)
	}
	if _, err := f.service.ChangeRole(ctx, f.owner, ChangeRoleInput{
		UserID:           secondOwner.UserID,
		Role:             auth.CompanyRoleMember,
		ExpectedRevision: f.revisionOf(t, secondOwner.UserID),
	}); err != nil {
		t.Fatalf("demoting the second owner with another owner present = %v", err)
	}
}

func TestTransferOwnershipSwapsBothRowsAtomically(t *testing.T) {
	t.Parallel()
	f := seedRoleFixture(t)
	ctx := context.Background()

	if _, err := f.service.TransferOwnership(ctx, f.admin, MembershipMutation{
		UserID: f.member.UserID, ExpectedRevision: 1,
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin transferring = %v, want ErrForbidden", err)
	}
	if _, err := f.service.TransferOwnership(ctx, f.owner, MembershipMutation{
		UserID: f.owner.UserID, ExpectedRevision: 1,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("self transfer = %v, want ErrInvalidInput", err)
	}
	if _, err := f.service.TransferOwnership(ctx, f.owner, MembershipMutation{
		UserID: f.member.UserID, ExpectedRevision: 9,
	}); !errors.Is(err, ErrMemberRevisionConflict) {
		t.Fatalf("stale transfer = %v, want ErrMemberRevisionConflict", err)
	}

	before := len(*f.events)
	items, err := f.service.TransferOwnership(ctx, f.owner, MembershipMutation{
		UserID: f.member.UserID, ExpectedRevision: f.revisionOf(t, f.member.UserID),
	})
	if err != nil {
		t.Fatalf("TransferOwnership() error = %v", err)
	}
	if len(items) != 2 || items[0].Actor.ID != f.member.UserID || items[0].Role != auth.CompanyRoleOwner ||
		items[1].Actor.ID != f.owner.UserID || items[1].Role != auth.CompanyRoleAdmin {
		t.Fatalf("TransferOwnership() = %+v", items)
	}
	updated, accessChanged := 0, 0
	for _, event := range (*f.events)[before:] {
		switch event.Name {
		case EventMemberUpdated:
			updated++
		case EventAuthCompanyAccessChanged:
			accessChanged++
		}
	}
	if updated != 2 || accessChanged != 2 {
		t.Fatalf("transfer frames: member.updated=%d access_changed=%d, want 2 and 2", updated, accessChanged)
	}
	// The former owner is now an admin: they may still manage but not delete.
	demoted := f.owner
	demoted.Role = auth.CompanyRoleAdmin
	if _, err := f.service.RequirePermission(
		ctx,
		demoted,
		f.owner.CompanyID,
		auth.PermissionCompanyDelete,
	); !errors.Is(
		err,
		ErrForbidden,
	) {
		t.Fatalf("former owner deleting = %v, want ErrForbidden", err)
	}
	if _, err := f.service.RequirePermission(
		ctx,
		demoted,
		f.owner.CompanyID,
		auth.PermissionAgentsManage,
	); err != nil {
		t.Fatalf("former owner managing agents = %v", err)
	}
	// And the new owner is the only owner: they cannot leave.
	newOwner := f.member
	newOwner.Role = auth.CompanyRoleOwner
	if err := f.service.Leave(ctx, newOwner, f.revisionOf(t, newOwner.UserID)); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("new sole owner leaving = %v, want ErrLastOwner", err)
	}
}
