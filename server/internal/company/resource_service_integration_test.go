//go:build integration

package company

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestCreateResourceUsesDatabaseIDAndDaemonNaturalKey(t *testing.T) {
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
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewResourceService(ResourceServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatalf("NewResourceService() error = %v", err)
	}
	input := CreateLocalDirectoryResourceInput{
		DaemonID: fixture.DaemonA,
		Path:     "/workspace/boardly",
		Label:    "Boardly",
		Position: 3,
	}

	created, err := service.CreateLocalDirectoryResource(ctx, fixture.UserA, fixture.CompanyA, input)
	if err != nil {
		t.Fatalf("CreateLocalDirectoryResource() error = %v", err)
	}
	if !created.Created || created.Resource.ID == uuid.Nil {
		t.Fatalf("CreateLocalDirectoryResource() = %+v, want a database-generated id", created)
	}
	replayed, err := service.CreateLocalDirectoryResource(ctx, fixture.UserA, fixture.CompanyA, input)
	if err != nil {
		t.Fatalf("CreateLocalDirectoryResource(replay) error = %v", err)
	}
	if replayed.Created || replayed.Resource.ID != created.Resource.ID {
		t.Fatalf("CreateLocalDirectoryResource(replay) = %+v, want %s", replayed, created.Resource.ID)
	}

	for name, mutate := range map[string]func(*CreateLocalDirectoryResourceInput){
		"path":     func(candidate *CreateLocalDirectoryResourceInput) { candidate.Path = "/workspace/other" },
		"label":    func(candidate *CreateLocalDirectoryResourceInput) { candidate.Label = "Other" },
		"position": func(candidate *CreateLocalDirectoryResourceInput) { candidate.Position++ },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := input
			mutate(&candidate)
			if _, err := service.CreateLocalDirectoryResource(
				ctx, fixture.UserA, fixture.CompanyA, candidate,
			); !errors.Is(err, ErrResourceConflict) {
				t.Fatalf(
					"CreateLocalDirectoryResource(conflict) error = %v, want ErrResourceConflict",
					err,
				)
			}
		})
	}

	concurrentInput := CreateLocalDirectoryResourceInput{
		DaemonID: "concurrent-daemon",
		Path:     "/workspace/concurrent",
		Label:    "Concurrent",
		Position: 4,
	}
	const contenders = 4
	results := make(chan ResourceCreateResult, contenders)
	errors := make(chan error, contenders)
	start := make(chan struct{})
	for range contenders {
		go func() {
			<-start
			result, createErr := service.CreateLocalDirectoryResource(
				ctx, fixture.UserA, fixture.CompanyA, concurrentInput,
			)
			results <- result
			errors <- createErr
		}()
	}
	close(start)
	createdCount := 0
	var concurrentID uuid.UUID
	for range contenders {
		if err := <-errors; err != nil {
			t.Errorf("concurrent CreateLocalDirectoryResource() error = %v", err)
		}
		result := <-results
		if result.Created {
			createdCount++
		}
		if concurrentID == uuid.Nil {
			concurrentID = result.Resource.ID
		} else if result.Resource.ID != concurrentID {
			t.Errorf("concurrent resource id = %s, want %s", result.Resource.ID, concurrentID)
		}
	}
	if createdCount != 1 || concurrentID == uuid.Nil {
		t.Fatalf("concurrent resource creates = %d / %s, want one database row", createdCount, concurrentID)
	}

	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM company_resources
		WHERE company_id = $1 AND daemon_id = $2`, fixture.CompanyA, fixture.DaemonA).Scan(&count); err != nil {
		t.Fatalf("count Company resources: %v", err)
	}
	if count != 1 {
		t.Fatalf("Company resources = %d, want 1", count)
	}
}

func TestResourceReadsReturnCreatorAndDaemonProjection(t *testing.T) {
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
		UPDATE users SET avatar_url = 'https://cdn.example.com/owner.png'
		WHERE id = $1`, fixture.UserA)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runtimes SET device_name = 'Owner Laptop' WHERE id = $1`, fixture.RuntimeA)
	onlineResourceID := dbtest.NewUUID(t)
	unregisteredResourceID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_resources (
			id, company_id, resource_type, daemon_id, local_path, label,
			position, metadata, created_by_user_id
		) VALUES
			($1, $2, 'local_directory', $3, '/workspace/online', 'Online', 1, '{}', $4),
			($5, $2, 'local_directory', 'retired-daemon', '/workspace/retired',
				'Retired', 2, '{}', $4)`,
		onlineResourceID, fixture.CompanyA, fixture.DaemonA, fixture.UserA,
		unregisteredResourceID)

	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewResourceService(ResourceServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatalf("NewResourceService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	resources, err := service.ListResources(ctx, principal, fixture.CompanyA, ResourceListOptions{})
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	if len(resources) != 2 {
		t.Fatalf("ListResources() count = %d, want 2", len(resources))
	}
	online := resources[0]
	if online.ID != onlineResourceID || online.ResourceRef.DaemonID != fixture.DaemonA ||
		online.ResourceRef.DaemonName != "Owner Laptop" || online.DaemonStatus != DaemonStatusOnline ||
		online.DaemonLastSeenAt == nil || online.CreatedBy.ID != fixture.UserA ||
		online.CreatedBy.Kind != ActorTypeUser || online.CreatedBy.DisplayName != "Schema A" ||
		online.CreatedBy.AvatarURL == nil ||
		*online.CreatedBy.AvatarURL != "https://cdn.example.com/owner.png" ||
		string(online.Metadata) != "{}" {
		t.Fatalf("online resource projection = %+v", online)
	}
	unregistered := resources[1]
	if unregistered.ID != unregisteredResourceID ||
		unregistered.ResourceRef.DaemonName != "retired-daemon" ||
		unregistered.DaemonStatus != DaemonStatusUnregistered ||
		unregistered.DaemonLastSeenAt != nil {
		t.Fatalf("unregistered resource projection = %+v", unregistered)
	}

	fetched, err := service.FetchResource(ctx, principal, fixture.CompanyA, onlineResourceID)
	if err != nil {
		t.Fatalf("FetchResource() error = %v", err)
	}
	if fetched.ResourceRef != online.ResourceRef || fetched.DaemonStatus != online.DaemonStatus ||
		fetched.CreatedBy.ID != online.CreatedBy.ID ||
		fetched.CreatedBy.DisplayName != online.CreatedBy.DisplayName {
		t.Fatalf("FetchResource() = %+v, list projection = %+v", fetched, online)
	}
}
