//go:build integration

package artifact

import (
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestArtifactSharingAndDiscovery(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s := NewArtifactService(store, bus)
	q := store.Queries()
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
	owner := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	member := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: auth.CompanyRoleMember}
	create := func(kind string, project, person uuid.NullUUID) uuid.UUID {
		t.Helper()
		id, err := q.CreateWikiPage(
			ctx,
			generated.CreateWikiPageParams{
				CompanyID:       f.CompanyA,
				Type:            kind,
				Title:           "Shared work",
				CreatedByUserID: f.UserA,
				ProjectID:       project,
				OwnerUserID:     person,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	page := create("page", uuid.NullUUID{}, uuid.NullUUID{UUID: f.UserA, Valid: true})
	project := create("page", uuid.NullUUID{UUID: f.ProjectA, Valid: true}, uuid.NullUUID{})
	knowledge := create("wiki", uuid.NullUUID{}, uuid.NullUUID{})
	check := func(p auth.UserPrincipal, want auth.ArtifactAccess, wantPages int) {
		t.Helper()
		got, err := auth.ResolveArtifactAccess(ctx, q, p, page)
		if err != nil || got != want {
			t.Fatalf("authority = %+v, %v; want %+v", got, err, want)
		}
		pages, err := s.Pages(ctx, p)
		if err != nil || len(pages) != wantPages {
			t.Fatalf("pages = %+v, %v; want %d", pages, err, wantPages)
		}
	}
	check(member, auth.ArtifactAccess{}, 0)
	access, err := s.Access(ctx, owner, page)
	if err != nil || !access.CanManage || access.Revision != 1 {
		t.Fatalf("access = %+v, %v", access, err)
	}
	if _, err := s.Access(ctx, member, page); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private access = %v", err)
	}
	visibility := "company"
	access, err = s.ChangeAccess(
		ctx,
		owner,
		AccessChange{ArtifactID: page, ExpectedRevision: access.Revision, Visibility: &visibility},
	)
	if err != nil {
		t.Fatal(err)
	}
	check(member, auth.ArtifactAccess{Read: true}, 1)
	if _, err := s.ChangeAccess(
		ctx,
		member,
		AccessChange{ArtifactID: page, ExpectedRevision: access.Revision, Visibility: &visibility},
	); !errors.Is(
		err,
		ErrForbidden,
	) {
		t.Fatalf("reader sharing = %v", err)
	}
	permission := "edit"
	grant := &GrantChange{UserID: f.UserB, Permission: &permission}
	access, err = s.ChangeAccess(
		ctx,
		owner,
		AccessChange{ArtifactID: page, ExpectedRevision: access.Revision, Grant: grant},
	)
	if err != nil || len(access.Grants) != 1 {
		t.Fatalf("grant = %+v, %v", access, err)
	}
	check(member, auth.ArtifactAccess{Read: true, Edit: true}, 1)
	visibility = "private"
	access, err = s.ChangeAccess(
		ctx,
		owner,
		AccessChange{ArtifactID: page, ExpectedRevision: access.Revision, Visibility: &visibility},
	)
	if err != nil {
		t.Fatal(err)
	}
	check(member, auth.ArtifactAccess{Read: true, Edit: true}, 1)
	if _, err := s.ChangeAccess(
		ctx,
		owner,
		AccessChange{ArtifactID: page, ExpectedRevision: 1, Grant: grant},
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale grant = %v", err)
	}
	permission = "manage"
	access, err = s.ChangeAccess(
		ctx,
		owner,
		AccessChange{ArtifactID: page, ExpectedRevision: access.Revision, Grant: grant},
	)
	if err != nil {
		t.Fatal(err)
	}
	check(member, auth.ArtifactAccess{Read: true, Edit: true, Manage: true}, 1)
	for _, id := range []uuid.UUID{f.UserA, f.UserB} {
		if _, err := s.ChangeAccess(
			ctx,
			member,
			AccessChange{
				ArtifactID:       page,
				ExpectedRevision: access.Revision,
				Grant:            &GrantChange{UserID: id},
			},
		); !errors.Is(
			err,
			ErrForbidden,
		) {
			t.Fatalf("manager removes owner/self = %v", err)
		}
	}
	access, err = s.ChangeAccess(
		ctx,
		owner,
		AccessChange{ArtifactID: page, ExpectedRevision: access.Revision, Grant: &GrantChange{UserID: f.UserB}},
	)
	if err != nil || len(access.Grants) != 0 {
		t.Fatalf("revoke = %+v, %v", access, err)
	}
	check(member, auth.ArtifactAccess{}, 0)
	for _, id := range []uuid.UUID{project, knowledge} {
		inherited, err := s.Access(ctx, owner, id)
		if err != nil || inherited.CanManage {
			t.Fatalf("inherited access = %+v, %v", inherited, err)
		}
		if _, err := s.ChangeAccess(
			ctx,
			owner,
			AccessChange{ArtifactID: id, ExpectedRevision: inherited.Revision, Grant: grant},
		); !errors.Is(
			err,
			ErrForbidden,
		) {
			t.Fatalf("inherited grant = %v", err)
		}
	}
	foreign := owner
	foreign.CompanyID = f.CompanyB
	if _, err := s.ChangeAccess(
		ctx,
		foreign,
		AccessChange{ArtifactID: page, ExpectedRevision: access.Revision, Grant: grant},
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("foreign grant = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE users SET disabled_at=clock_timestamp() WHERE id=$1`, f.UserB)
	if _, err := s.ChangeAccess(
		ctx,
		owner,
		AccessChange{ArtifactID: page, ExpectedRevision: access.Revision, Grant: grant},
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("disabled recipient = %v", err)
	}

	// Two managers editing the same access revision cannot silently overwrite it.
	var writes sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		writes.Go(func() {
			_, err := s.ChangeAccess(
				ctx,
				owner,
				AccessChange{
					ArtifactID:       page,
					ExpectedRevision: access.Revision,
					Visibility:       &visibility,
				},
			)
			results <- err
		})
	}
	writes.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrConflict):
			conflicted++
		default:
			t.Fatalf("concurrent access change = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent changes = %d successes, %d conflicts; want 1 each", succeeded, conflicted)
	}
}
