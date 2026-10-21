//go:build integration

package company

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestProactiveModeOwnerCanEnableAndDisable(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	service, err := NewProactiveModeService(store)
	if err != nil {
		t.Fatalf("NewProactiveModeService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}

	initial, err := service.Get(ctx, principal)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if initial.CompanyID != fixture.CompanyA || initial.Enabled || initial.Revision != 1 ||
		initial.EnabledByUserID != nil || initial.ConversationID != nil || !initial.CanManage {
		t.Fatalf("Get() = %+v, want disabled owner-manageable setting at revision 1", initial)
	}

	enabled, err := service.Update(ctx, principal, UpdateProactiveModeInput{
		Enabled: true, ExpectedRevision: initial.Revision,
	})
	if err != nil {
		t.Fatalf("Update(enable) error = %v", err)
	}
	if !enabled.Enabled || enabled.Revision != 2 || enabled.EnabledByUserID == nil ||
		*enabled.EnabledByUserID != fixture.UserA || enabled.ConversationID == nil ||
		*enabled.ConversationID != fixture.DirectConversationA || !enabled.CanManage {
		t.Fatalf("Update(enable) = %+v", enabled)
	}

	_, err = service.Update(ctx, principal, UpdateProactiveModeInput{
		Enabled: false, ExpectedRevision: initial.Revision,
	})
	if !errors.Is(err, ErrProactiveModeConflict) {
		t.Fatalf("Update(stale) error = %v, want ErrProactiveModeConflict", err)
	}

	disabled, err := service.Update(ctx, principal, UpdateProactiveModeInput{
		Enabled: false, ExpectedRevision: enabled.Revision,
	})
	if err != nil {
		t.Fatalf("Update(disable) error = %v", err)
	}
	if disabled.Enabled || disabled.Revision != 3 || disabled.EnabledByUserID != nil ||
		disabled.ConversationID != nil || !disabled.CanManage {
		t.Fatalf("Update(disable) = %+v", disabled)
	}

	dbtest.MustExec(t, ctx, pool, `
		UPDATE conversations
		SET archived_at = clock_timestamp()
		WHERE id = $1`, fixture.DirectConversationA)
	_, err = service.Update(ctx, principal, UpdateProactiveModeInput{
		Enabled: true, ExpectedRevision: disabled.Revision,
	})
	if !errors.Is(err, ErrProactiveModeConversationRequired) {
		t.Fatalf(
			"Update(enable without CEO conversation) error = %v, want ErrProactiveModeConversationRequired",
			err,
		)
	}
}

func TestProactiveModeMemberCanReadButNotManage(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`, fixture.CompanyA, fixture.UserB)
	service, err := NewProactiveModeService(store)
	if err != nil {
		t.Fatalf("NewProactiveModeService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserB, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember,
	}

	result, err := service.Get(ctx, principal)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if result.CompanyID != fixture.CompanyA || result.CanManage {
		t.Fatalf("Get() = %+v, want member-readable setting with canManage false", result)
	}
	_, err = service.Update(ctx, principal, UpdateProactiveModeInput{
		Enabled: true, ExpectedRevision: result.Revision,
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("Update() error = %v, want ErrForbidden", err)
	}
}
