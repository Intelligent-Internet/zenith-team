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

func TestCreateCompanyUsesDatabaseIDAndNaturalSlugUniqueness(t *testing.T) {
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
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewCompanyService(ServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatalf("NewCompanyService() error = %v", err)
	}

	firstUserID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO users (id, email, display_name)
		VALUES ($1, 'company-create-a@boardly.test', 'Creator A')`,
		firstUserID)
	input := CreateCompanyInput{
		Name:            " Northwind Robotics ",
		Slug:            " northwind-robotics ",
		TaskPrefix:      " NWR ",
		BusinessContext: " Warehouse automation ",
	}

	created, err := service.Create(ctx, auth.UserPrincipal{UserID: firstUserID}, input)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID == uuid.Nil {
		t.Fatalf("Create() = %+v, want a database-generated Company id", created)
	}

	var generalCount, founderCount, agentCount int
	if err := pool.QueryRow(ctx, `SELECT count(*),
        (SELECT count(*) FROM conversation_members m JOIN conversations c ON c.id=m.conversation_id
         WHERE c.company_id=$1 AND c.is_general AND m.user_id=$2 AND m.status='active'),
        (SELECT count(*) FROM conversation_agents a JOIN conversations c ON c.id=a.conversation_id
         WHERE c.company_id=$1 AND c.is_general)
        FROM conversations WHERE company_id=$1 AND is_general AND title='general' AND archived_at IS NULL`, created.ID, firstUserID).
		Scan(&generalCount, &founderCount, &agentCount); err != nil {
		t.Fatal(err)
	}
	if generalCount != 1 || founderCount != 1 || agentCount != 0 {
		t.Fatalf("general/founder/agents = %d/%d/%d, want 1/1/0", generalCount, founderCount, agentCount)
	}

	var budgetTicks, budgetRevision int64
	if err := pool.QueryRow(ctx, `SELECT monthly_limit_usd_ticks, revision FROM huddle_ai_budgets WHERE company_id=$1`, created.ID).
		Scan(&budgetTicks, &budgetRevision); err != nil || budgetTicks != 20_000_000_000 || budgetRevision != 1 {
		t.Fatalf("Huddle AI budget = %d ticks, revision %d, error %v; want the $2 default", budgetTicks, budgetRevision, err)
	}

	if _, err := service.Create(ctx, auth.UserPrincipal{UserID: firstUserID}, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("Create(duplicate slug) error = %v, want ErrConflict", err)
	}

	otherInput := input
	otherInput.Name = "Contoso Robotics"
	otherInput.Slug = "contoso-robotics"
	other, err := service.Create(ctx, auth.UserPrincipal{UserID: firstUserID}, otherInput)
	if err != nil {
		t.Fatalf("Create(distinct slug) error = %v", err)
	}
	if other.ID == uuid.Nil || other.ID == created.ID {
		t.Fatalf("Create(distinct slug) = %+v, want a distinct database-generated ID", other)
	}
}
