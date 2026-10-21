//go:build integration

package inbox

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// This test stays in-package because it pins authorizeActivePrincipal itself:
// the Company lock taken before the membership share lock is what serializes a
// concurrent revocation behind an open Inbox transaction, and that ordering is
// unobservable from outside the package. Every other Docker-backed Inbox
// assertion lives in server/tests/integration/inbox.
func TestInboxAuthorizationSerializesWithMembershipRevocation(t *testing.T) {
	pool := startInboxPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	owner, _ := seedInboxCompany(t, ctx, pool)

	inboxTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inboxTx.Rollback(context.Background()) }()
	if err := authorizeActivePrincipal(ctx, generated.New(inboxTx), owner); err != nil {
		t.Fatalf("authorizeActivePrincipal() error = %v", err)
	}

	revokeDone := make(chan error, 1)
	go func() {
		_, revokeErr := pool.Exec(ctx, `
			UPDATE company_members
			SET status='revoked', revoked_at=clock_timestamp(),
			    revoked_by_user_id=$2, version=version+1,
			    updated_at=clock_timestamp()
			WHERE company_id=$1 AND user_id=$2`,
			owner.CompanyID, owner.UserID)
		revokeDone <- revokeErr
	}()
	select {
	case revokeErr := <-revokeDone:
		t.Fatalf("membership revocation passed an open Inbox authorization fence: %v", revokeErr)
	case <-time.After(100 * time.Millisecond):
	}
	if err := inboxTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case revokeErr := <-revokeDone:
		if revokeErr != nil {
			t.Fatalf("membership revocation error = %v", revokeErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("membership revocation remained blocked after Inbox commit")
	}
}

func TestListPageAppliesStatusAndKindFilters(t *testing.T) {
	pool := startInboxPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	owner, _ := seedInboxCompany(t, ctx, pool)
	queries := generated.New(pool)

	create := func(notificationType Type, dedupeKey string) uuid.UUID {
		t.Helper()
		row, err := queries.CreateInboxItem(ctx, generated.CreateInboxItemParams{
			CompanyID: owner.CompanyID, RecipientUserID: owner.UserID,
			Kind:       string(notificationType),
			EntityType: string(protocol.EntityTask), EntityID: dedupeKey,
			Title: dedupeKey, Body: dedupeKey, DedupeKey: dedupeKey,
		})
		if err != nil {
			t.Fatalf("CreateInboxItem(%q) error = %v", dedupeKey, err)
		}
		if row.ID == uuid.Nil {
			t.Fatalf("CreateInboxItem(%q).ID is nil; want database-generated ID", dedupeKey)
		}
		return row.ID
	}
	unreadTask := create(TypeTaskAssigned, "unread-task")
	readBlocker := create(TypeBlocker, "read-blocker")
	archivedBlocker := create(TypeBlocker, "archived-blocker")
	mustInboxExec(t, ctx, pool, `
		UPDATE inbox_items
		SET read_at = statement_timestamp(), updated_at = statement_timestamp()
		WHERE id = $1`, readBlocker)
	mustInboxExec(t, ctx, pool, `
		UPDATE inbox_items
		SET read_at = statement_timestamp(), archived_at = statement_timestamp(),
		    updated_at = statement_timestamp()
		WHERE id = $1`, archivedBlocker)

	tests := []struct {
		name    string
		options ListOptions
		wantIDs map[uuid.UUID]bool
	}{
		{
			name: "active excludes archived", options: ListOptions{Status: StatusActive, Limit: 10},
			wantIDs: map[uuid.UUID]bool{unreadTask: true, readBlocker: true},
		},
		{
			name: "all is the true union", options: ListOptions{Status: StatusAll, Limit: 10},
			wantIDs: map[uuid.UUID]bool{unreadTask: true, readBlocker: true, archivedBlocker: true},
		},
		{
			name: "unread", options: ListOptions{Status: StatusUnread, Limit: 10},
			wantIDs: map[uuid.UUID]bool{unreadTask: true},
		},
		{
			name: "archived", options: ListOptions{Status: StatusArchived, Limit: 10},
			wantIDs: map[uuid.UUID]bool{archivedBlocker: true},
		},
		{
			name:    "kind combines with status",
			options: ListOptions{Status: StatusActive, Type: TypeBlocker, Limit: 10},
			wantIDs: map[uuid.UUID]bool{readBlocker: true},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			page, err := listPage(ctx, queries, owner.CompanyID, owner.UserID, test.options)
			if err != nil {
				t.Fatalf("listPage() error = %v", err)
			}
			if len(page.Items) != len(test.wantIDs) {
				t.Fatalf("len(items) = %d, want %d", len(page.Items), len(test.wantIDs))
			}
			for _, item := range page.Items {
				if !test.wantIDs[item.ID] {
					t.Fatalf("item %s was not expected", item.ID)
				}
			}
		})
	}
}

func TestReadConversationFencesVisibilityAndUpdatesOnlyMatchingUnreadItems(t *testing.T) {
	pool := startInboxPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	owner, member := seedInboxCompany(t, ctx, pool)

	ownerDirectID := uuid.Must(uuid.NewV7())
	memberDirectID := uuid.Must(uuid.NewV7())
	taskConversationID := uuid.Must(uuid.NewV7())
	// A Task chat names its Project (ADR 0017).
	projectID := uuid.Must(uuid.NewV7())
	mustInboxExec(
		t,
		ctx,
		pool,
		`INSERT INTO projects (id, company_id, key, title) VALUES ($1, $2, $3, 'Inbox fence')`,
		projectID,
		owner.CompanyID,
		"inbox-fence-"+projectID.String(),
	)
	// The owner reads it, so its chat is theirs to read.
	mustInboxExec(t, ctx, pool, `
		INSERT INTO project_grants (company_id, project_id, user_id, permission, granted_by_user_id)
		VALUES ($1, $2, $3, 'project.read', $3)`, owner.CompanyID, projectID, owner.UserID)
	for _, conversation := range []struct {
		id      uuid.UUID
		kind    string
		creator uuid.UUID
		project uuid.NullUUID
	}{
		{ownerDirectID, "direct", owner.UserID, uuid.NullUUID{}},
		{memberDirectID, "direct", member.UserID, uuid.NullUUID{}},
		{taskConversationID, "task", uuid.Nil, uuid.NullUUID{UUID: projectID, Valid: true}},
	} {
		mustInboxExec(t, ctx, pool, `
			INSERT INTO conversations (id, company_id, kind, created_by_user_id, project_id)
			VALUES ($1, $2, $3, NULLIF($4, '00000000-0000-0000-0000-000000000000'::uuid), $5)`,
			conversation.id, owner.CompanyID, conversation.kind, conversation.creator, conversation.project)
	}

	createItem := func(conversationID uuid.UUID, key, state string) uuid.UUID {
		t.Helper()
		id := uuid.Must(uuid.NewV7())
		mustInboxExec(t, ctx, pool, `
			INSERT INTO inbox_items (
				id, company_id, recipient_user_id, conversation_id,
				kind, entity_type, entity_id, title, body, dedupe_key,
				read_at, archived_at
			) VALUES (
				$1, $2, $3, $4::uuid, 'blocker', 'conversation', $4::uuid::text,
				$5, $5, $5,
				CASE WHEN $6 = 'read' THEN statement_timestamp() END,
				CASE WHEN $6 = 'archived' THEN statement_timestamp() END
			)`, id, owner.CompanyID, owner.UserID, conversationID, key, state)
		return id
	}
	ownerUnreadID := createItem(ownerDirectID, "owner-unread", "unread")
	createItem(ownerDirectID, "owner-read", "read")
	createItem(ownerDirectID, "owner-archived", "archived")
	memberConversationItemID := createItem(memberDirectID, "member-private", "unread")
	taskItemID := createItem(taskConversationID, "task-unread", "unread")
	otherCompanyID := uuid.Must(uuid.NewV7())
	otherConversationID := uuid.Must(uuid.NewV7())
	mustInboxExec(t, ctx, pool, `
		INSERT INTO companies (id, name, slug, task_prefix)
		VALUES ($1, 'Other Inbox Company', $2, 'OIN')`,
		otherCompanyID, "other-inbox-"+otherCompanyID.String())
	mustInboxExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'owner')`,
		otherCompanyID, owner.UserID)
	mustInboxExec(t, ctx, pool, `
		INSERT INTO conversations (id, company_id, kind, created_by_user_id)
		VALUES ($1, $2, 'direct', $3)`,
		otherConversationID, otherCompanyID, owner.UserID)

	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	// One subscription per domain, switching on the name: re-typing the
	// payload here reports a wrong shape as an error rather than skipping it.
	var published []publishedItemEvent
	if err := bus.Subscribe(Topic, func(_ context.Context, event events.Event) error {
		switch event.Name {
		case EventItemCreated, EventItemUpdated:
			payload, err := events.PayloadOf[ItemWithCounts](event)
			if err != nil {
				return err
			}
			published = append(published, publishedItemEvent{Name: event.Name, Payload: payload})
		}
		return nil
	}); err != nil {
		t.Fatalf("subscribe Inbox events: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	service, err := NewService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	if _, err := service.ReadConversation(ctx, owner, memberDirectID); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("ReadConversation(other member direct) error = %v, want ErrConversationNotFound", err)
	}
	if _, err := service.ReadConversation(
		ctx,
		owner,
		otherConversationID,
	); !errors.Is(
		err,
		ErrConversationNotFound,
	) {
		t.Fatalf("ReadConversation(path-company mismatch) error = %v, want ErrConversationNotFound", err)
	}
	result, err := service.ReadConversation(ctx, owner, ownerDirectID)
	if err != nil {
		t.Fatalf("ReadConversation(owner direct) error = %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != ownerUnreadID {
		t.Fatalf("owner direct result items = %+v", result.Items)
	}
	if result.Items[0].ReadAt == nil || result.Items[0].Unread {
		t.Fatalf(
			"updated owner item read state = readAt %v unread %t",
			result.Items[0].ReadAt,
			result.Items[0].Unread,
		)
	}

	taskResult, err := service.ReadConversation(ctx, owner, taskConversationID)
	if err != nil {
		t.Fatalf("ReadConversation(task) error = %v", err)
	}
	if len(taskResult.Items) != 1 || taskResult.Items[0].ID != taskItemID {
		t.Fatalf("task result items = %+v", taskResult.Items)
	}
	beforeReplayEvents := len(published)
	replay, err := service.ReadConversation(ctx, owner, ownerDirectID)
	if err != nil {
		t.Fatalf("ReadConversation(replay) error = %v", err)
	}
	if len(replay.Items) != 0 || len(published) != beforeReplayEvents {
		t.Fatalf("replay = %+v, events %d -> %d", replay, beforeReplayEvents, len(published))
	}

	var privateStillUnread bool
	if err := pool.QueryRow(ctx,
		`SELECT read_at IS NULL FROM inbox_items WHERE id = $1`,
		memberConversationItemID,
	).Scan(&privateStillUnread); err != nil {
		t.Fatalf("read private item state: %v", err)
	}
	if !privateStillUnread {
		t.Fatal("another member's direct-conversation item was marked read")
	}

	updatedEvents := 0
	for _, event := range published {
		updatedEvents++
		payload := event.Payload
		// The counts ride each data-bearing frame; there is no counts-only
		// event any more.
		if payload.Counts.CountsVersion < 1 {
			t.Fatalf("inbox.updated counts version = %d, want >= 1", payload.Counts.CountsVersion)
		}
		// Routing rides the payload: this frame is for one recipient in one
		// company, and the bridge reads both off the item itself.
		if (payload.CompanyID == nil || *payload.CompanyID != owner.CompanyID) ||
			payload.RecipientUserID != owner.UserID {
			t.Fatalf("Inbox event routing = company %v recipient %v, want %v / %v",
				payload.CompanyID, payload.RecipientUserID, owner.CompanyID, owner.UserID)
		}
	}
	if updatedEvents != 2 {
		t.Fatalf("published inbox.updated events = %d, want 2", updatedEvents)
	}
}

// publishedItemEvent is one inbox item frame as a subscriber saw it.
type publishedItemEvent struct {
	Name    string
	Payload ItemWithCounts
}

func seedInboxCompany(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) (auth.UserPrincipal, auth.UserPrincipal) {
	t.Helper()
	companyID := uuid.Must(uuid.NewV7())
	ownerID, memberID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for index, userID := range []uuid.UUID{ownerID, memberID} {
		mustInboxExec(
			t,
			ctx,
			pool,
			`INSERT INTO users (id, email, display_name) VALUES ($1, $2, $3)`,
			userID,
			fmt.Sprintf("inbox-%d-%s@zenith-team.test", index, userID),
			fmt.Sprintf("Inbox User %d", index),
		)
	}
	mustInboxExec(t, ctx, pool,
		`INSERT INTO companies (id, name, slug, task_prefix) VALUES ($1, 'Inbox Company', $2, 'INB')`,
		companyID, "inbox-"+companyID.String())
	mustInboxExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'owner'), ($1, $3, 'member')`,
		companyID, ownerID, memberID)
	principal := func(userID uuid.UUID, role auth.CompanyRole) auth.UserPrincipal {
		return auth.UserPrincipal{
			UserID: userID, SessionID: uuid.Must(uuid.NewV7()), AuthKind: auth.TokenKindBearer,
			CompanyID: companyID, Role: role,
		}
	}
	return principal(ownerID, auth.CompanyRoleOwner), principal(memberID, auth.CompanyRoleMember)
}

func startInboxPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func mustInboxExec(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	statement string,
	arguments ...any,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, statement, arguments...); err != nil {
		t.Fatalf("execute Inbox fixture SQL: %v", err)
	}
}
