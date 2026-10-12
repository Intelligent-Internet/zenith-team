//go:build integration

package inbox

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

func TestAccountInboxIncludesPersonalItemsAndOnlyAccessibleCompanies(t *testing.T) {
	pool := startInboxPostgres(t)
	ctx := context.Background()
	owner, other := seedInboxCompany(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	var personal CreateResult
	err = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var pending []events.Event
		var err error
		personal, err = CreateInTx(
			ctx,
			q,
			&pending,
			CreateInput{
				RecipientUserID: owner.UserID,
				Type:            TypeCalendarReminder,
				Entity:          Entity{Type: protocol.EntityCalendar, ID: "personal-event"},
				Title:           "Calendar reminder",
				Body:            "Private review",
				DedupeKey:       "personal-review",
			},
		)
		if err != nil {
			return err
		}
		_, err = CreateInTx(
			ctx,
			q,
			&pending,
			CreateInput{
				CompanyID:       owner.CompanyID,
				RecipientUserID: owner.UserID,
				Type:            TypeTaskAssigned,
				Entity:          Entity{Type: protocol.EntityTask, ID: "task"},
				Title:           "Task assigned",
				Body:            "Review task",
				DedupeKey:       "company-task",
			},
		)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	account := auth.UserPrincipal{UserID: owner.UserID}
	page, err := s.List(ctx, account, ListOptions{})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("account list = %d, %v", len(page.Items), err)
	}
	company, err := s.List(ctx, owner, ListOptions{})
	if err != nil || len(company.Items) != 1 {
		t.Fatalf("company list = %d, %v", len(company.Items), err)
	}
	if _, err = s.Get(
		ctx,
		auth.UserPrincipal{UserID: other.UserID},
		personal.Item.ID,
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("cross-user read = %v", err)
	}
	result, err := s.Mutate(ctx, account, personal.Item.ID, MutationRead, personal.Item.Revision)
	if err != nil || result.Counts.UnreadCount != 1 {
		t.Fatalf("personal read/counts = %+v, %v", result, err)
	}
	bulk, err := s.ReadAll(ctx, account)
	if err != nil || len(bulk.Items) != 1 || bulk.Counts.UnreadCount != 0 {
		t.Fatalf("account bulk read=%+v, %v", bulk, err)
	}
	mustInboxExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		owner.CompanyID,
		owner.UserID,
	)
	if _, err := s.Mutate(
		ctx,
		account,
		company.Items[0].ID,
		MutationArchive,
		company.Items[0].Revision+1,
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("revoked company mutation=%v", err)
	}
	page, err = s.List(ctx, account, ListOptions{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != personal.Item.ID {
		t.Fatalf("account after revocation = %+v, %v", page, err)
	}
}

func TestAccountInboxPreservesPersonalRemindersAndArtifactAccess(t *testing.T) {
	t.Parallel()
	pool := startInboxPostgres(t)
	ctx := t.Context()
	fixture := dbtest.SeedFixture(t, ctx, pool)
	mustInboxExec(t, ctx, pool, `INSERT INTO company_members (company_id,user_id,role,status)
		VALUES ($1,$2,'member','active')`, fixture.CompanyA, fixture.UserB)
	var outputID, deliverableID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO outputs
		(company_id,run_id,owner_user_id,title,summary,presentation_fingerprint, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,$3,'Private result','Private result',repeat('a',64), origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`,
		fixture.CompanyA, fixture.RunA, fixture.UserA).Scan(&outputID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO deliverables (company_id,output_id,submitted_at)
		VALUES ($1,$2,statement_timestamp()) RETURNING id`, fixture.CompanyA, outputID).Scan(&deliverableID); err != nil {
		t.Fatal(err)
	}
	q := generated.New(pool)
	personal, err := q.CreateInboxItem(ctx, generated.CreateInboxItemParams{
		RecipientUserID: fixture.UserB, Kind: string(TypeCalendarReminder),
		EntityType: string(protocol.EntityCalendar), EntityID: "personal-reminder",
		Title: "Personal reminder", Body: "Personal reminder", DedupeKey: "personal-reminder",
	})
	if err != nil {
		t.Fatal(err)
	}
	private, err := q.CreateInboxItem(ctx, generated.CreateInboxItemParams{
		CompanyID: fixture.CompanyA, RecipientUserID: fixture.UserB, Kind: string(TypeApprovalRequest),
		EntityType: string(protocol.EntityDeliverable), EntityID: deliverableID.String(),
		Title: "Private result", Body: "Private result", DedupeKey: "private-result",
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	account := auth.UserPrincipal{UserID: fixture.UserB}
	for _, step := range []struct {
		name    string
		granted bool
	}{
		{name: "ungranted"}, {name: "granted", granted: true}, {name: "revoked"},
	} {
		if step.granted {
			mustInboxExec(t, ctx, pool, `INSERT INTO output_grants
				(company_id,output_id,user_id,permission,granted_by_user_id)
				VALUES ($1,$2,$3,'view',$4)`, fixture.CompanyA, outputID, fixture.UserB, fixture.UserA)
		} else {
			mustInboxExec(t, ctx, pool, `DELETE FROM output_grants WHERE output_id=$1`, outputID)
		}
		wantCount := 1
		if step.granted {
			wantCount = 2
		}
		page, err := service.List(ctx, account, ListOptions{})
		if err != nil || len(page.Items) != wantCount {
			t.Fatalf("%s list = %+v, %v, want %d items", step.name, page, err, wantCount)
		}
		counts, err := service.Counts(ctx, account)
		if err != nil || counts.UnreadCount != int64(wantCount) {
			t.Fatalf("%s counts = %+v, %v, want %d unread", step.name, counts, err, wantCount)
		}
		_, err = service.Get(ctx, account, private.ID)
		if step.granted && err != nil || !step.granted && !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s private item read = %v", step.name, err)
		}
	}
	if _, err := service.Mutate(
		ctx,
		account,
		private.ID,
		MutationRead,
		private.Version,
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("revoked artifact mutation = %v, want ErrNotFound", err)
	}
	read, err := service.ReadAll(ctx, account)
	if err != nil || len(read.Items) != 1 || read.Items[0].ID != personal.ID || read.Counts.UnreadCount != 0 {
		t.Fatalf("bulk read after revocation = %+v, %v", read, err)
	}
}
