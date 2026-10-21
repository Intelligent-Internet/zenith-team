//go:build integration

package company

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func recordedCompanyBus(t *testing.T) (*events.Bus, *topicLog) {
	t.Helper()
	bus := companyTestBus(t)
	return bus, recordTopic(t, bus, Topic)
}

func TestUpdateCompanyProfileReturnsSnapshotsAndPrefersConflictOverNoChange(t *testing.T) {
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
	bus, eventLog := recordedCompanyBus(t)
	service, err := NewCompanyService(ServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatalf("NewCompanyService() error = %v", err)
	}
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA}

	updated, err := service.UpdateProfile(ctx, principal, UpdateProfileInput{
		ExpectedRevision: 1,
		Name:             " Northwind Robotics ",
		ContactEmail:     stringPointer(" OWNER@Example.COM "),
	})
	if err != nil {
		t.Fatalf("UpdateProfile() error = %v", err)
	}
	if updated.ID != fixture.CompanyA || updated.Name != "Northwind Robotics" ||
		updated.ContactEmail == nil || *updated.ContactEmail != "owner@example.com" ||
		updated.Revision != 2 {
		t.Fatalf("UpdateProfile() = %+v, want the reloaded post-commit snapshot", updated)
	}
	published := eventLog.snapshot()
	if len(published) != 1 || published[0].Name != "company.updated" {
		t.Fatalf("published Company events = %#v, want exactly the event update", published)
	}
	// The frame reaches every member, so it carries the Company without the
	// actor's own role and permissions.
	broadcast := updated
	broadcast.Role = ""
	broadcast.Permissions = nil
	if !reflect.DeepEqual(published[0].Payload, broadcast) {
		t.Fatalf("event payload = %#v, want the returned snapshot without viewer fields", published[0].Payload)
	}

	// A canonical-equal replay against the current revision is a no-op: the
	// current snapshot comes back and no event is event.
	noOp, err := service.UpdateProfile(ctx, principal, UpdateProfileInput{
		ExpectedRevision: 2,
		Name:             "Northwind Robotics",
		ContactEmail:     stringPointer("owner@example.com"),
	})
	if err != nil {
		t.Fatalf("no-op UpdateProfile() error = %v", err)
	}
	if !reflect.DeepEqual(noOp, updated) {
		t.Fatalf("no-op UpdateProfile() = %+v, want the unchanged snapshot %+v", noOp, updated)
	}

	// A stale revision conflicts even when the payload matches the current
	// row: the revision gate runs before the no-change gate.
	_, err = service.UpdateProfile(ctx, principal, UpdateProfileInput{
		ExpectedRevision: 1,
		Name:             "Northwind Robotics",
		ContactEmail:     stringPointer("owner@example.com"),
	})
	var conflict *ProfileRevisionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale UpdateProfile() error = %v, want ProfileRevisionConflictError", err)
	}
	if !reflect.DeepEqual(conflict.Current, updated) {
		t.Fatalf("conflict snapshot = %+v, want the current row %+v", conflict.Current, updated)
	}
	if published := eventLog.snapshot(); len(published) != 1 {
		t.Fatalf("published Company events = %d, want the first update only", len(published))
	}
}
