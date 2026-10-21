//go:build integration

package task

import (
	"context"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestTaskInboxAssignmentAndBlockerRecipientsAreDeduplicated(t *testing.T) {
	pool := startTaskInboxPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	companyID := uuid.Must(uuid.NewV7())
	creatorID := uuid.Must(uuid.NewV7())
	assigneeID := uuid.Must(uuid.NewV7())
	taskID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	mustTaskInboxExec(t, ctx, pool, `
		INSERT INTO users (id,email,display_name) VALUES
			($1,'task-inbox-creator@zenith-team.test','Creator'),
			($2,'task-inbox-assignee@zenith-team.test','Assignee')`,
		creatorID, assigneeID)
	mustTaskInboxExec(t, ctx, pool, `
		INSERT INTO companies (id,name,slug,task_prefix)
		VALUES ($1,'Task Inbox Company',$2,'TIN')`,
		companyID, "task-inbox-"+companyID.String())
	mustTaskInboxExec(t, ctx, pool, `
		INSERT INTO company_members (company_id,user_id,role) VALUES
			($1,$2,'owner'),($1,$3,'member')`,
		companyID, creatorID, assigneeID)
	// Both recipients read a Company-visible Project; an Inbox item about a
	// Project reaches only its readers (ADR 0017).
	mustTaskInboxExec(t, ctx, pool, `
		WITH project AS (
			INSERT INTO projects (company_id, key, title, visibility) VALUES ($2, 'work', 'Work', 'company') RETURNING id
		)
		INSERT INTO conversations (id, company_id, kind, project_id)
		SELECT $1, $2, 'task', project.id FROM project`,
		conversationID, companyID)

	previous := Response{
		ID: taskID, CompanyID: companyID, ConversationID: conversationID, Title: "Prepare release",
		Status: StatusTodo, Revision: 1,
		Creator:  ActorRef{Kind: ActorUser, ID: creatorID},
		Assignee: ActorRef{Kind: ActorAgent, ID: agentID},
	}
	current := previous
	current.Status = StatusBlocked
	current.Revision = 2
	current.Assignee = ActorRef{Kind: ActorUser, ID: assigneeID}

	var event []events.Event
	create := func() []uuid.UUID {
		t.Helper()
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var pending []events.Event
		results, err := createTaskInboxItems(
			ctx, generated.New(tx), &pending, &previous, current, creatorID, handoffOutcome{},
		)
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		event = append(event, pending...)
		ids := make([]uuid.UUID, 0, len(results))
		for _, result := range results {
			ids = append(ids, result.Item.ID)
		}
		return ids
	}

	// This replay is also the only proof that inbox_items still carries the
	// unique (company_id, recipient_user_id, dedupe_key) constraint: the
	// production INSERT infers it with ON CONFLICT, so moving or dropping it
	// fails here with SQLSTATE 42P10 rather than silently duplicating items.
	if created := create(); len(created) != 3 {
		t.Fatalf("created Task Inbox items = %d, want 3", len(created))
	}
	if replayed := create(); len(replayed) != 0 {
		t.Fatalf("replayed Task Inbox items = %d, want 0", len(replayed))
	}

	var assignedToAssignee, blockedForAssignee, blockedForCreator int
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (
				WHERE kind='task_assigned' AND recipient_user_id=$2
			)::integer,
			count(*) FILTER (
				WHERE kind='blocker' AND recipient_user_id=$2
			)::integer,
			count(*) FILTER (
				WHERE kind='blocker' AND recipient_user_id=$3
			)::integer
		FROM inbox_items WHERE company_id=$1`,
		companyID, assigneeID, creatorID,
	).Scan(&assignedToAssignee, &blockedForAssignee, &blockedForCreator); err != nil {
		t.Fatalf("count Task Inbox recipients: %v", err)
	}
	var missingConversationID int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::integer
		FROM inbox_items
		WHERE company_id=$1 AND conversation_id IS NULL`,
		companyID,
	).Scan(&missingConversationID); err != nil {
		t.Fatalf("count Task Inbox conversation associations: %v", err)
	}
	// Unread counts are derived from inbox_items now (inbox_user_state is
	// gone): COUNT(*) of unarchived unread items per recipient.
	var assigneeUnread, creatorUnread int
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE recipient_user_id=$2)::integer,
			count(*) FILTER (WHERE recipient_user_id=$3)::integer
		FROM inbox_items
		WHERE company_id=$1 AND read_at IS NULL AND archived_at IS NULL`,
		companyID, assigneeID, creatorID,
	).Scan(&assigneeUnread, &creatorUnread); err != nil {
		t.Fatalf("count Task Inbox unread projection: %v", err)
	}
	// One inbox.new frame per created item; the recipient's counts ride each
	// frame — there is no counts-only event any more.
	// Staged is opaque, so the frames are counted where a consumer sees
	// them: published on a bus, delivered to a typed subscription.
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	var createdEvents int
	if err := bus.Subscribe(inbox.Topic,
		func(_ context.Context, event events.Event) error {
			if event.Name != inbox.EventItemCreated {
				return nil
			}
			item, err := events.PayloadOf[inbox.ItemWithCounts](event)
			if err != nil {
				return err
			}
			if item.CompanyID != nil && *item.CompanyID == companyID {
				createdEvents++
			}
			return nil
		}); err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	for _, group := range events.GroupByTopic(event) {
		bus.Publish(ctx, group.Topic, group.Events...)
	}
	if assignedToAssignee != 1 || blockedForAssignee != 1 || blockedForCreator != 1 ||
		assigneeUnread != 2 || creatorUnread != 1 || missingConversationID != 0 ||
		createdEvents != 3 {
		t.Fatalf(
			"Task Inbox assignment=%d assignee blocker=%d creator blocker=%d unread=%d/%d missing conversation=%d events=%d",
			assignedToAssignee,
			blockedForAssignee,
			blockedForCreator,
			assigneeUnread,
			creatorUnread,
			missingConversationID,
			createdEvents,
		)
	}
}

func startTaskInboxPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func mustTaskInboxExec(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	query string,
	args ...any,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, query, args...); err != nil {
		t.Fatalf("execute Task Inbox fixture SQL: %v", err)
	}
}
