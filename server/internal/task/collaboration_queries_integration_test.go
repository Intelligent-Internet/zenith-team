//go:build integration

package task

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// Task-domain policy that only PostgreSQL enforces: number allocation,
// archived-association visibility, membership fencing on create, complete
// collection filters, and terminal completion. Moved here from internal/db,
// which owns no domain behavior.

func TestTaskUpdateRetainsButCannotSelectArchivedAssociations(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("db.MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("db.NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE agents SET archived_at=clock_timestamp() WHERE id=$1`, fixture.AgentA)

	current, err := store.Queries().GetTaskForActiveMember(ctx,
		generated.GetTaskForActiveMemberParams{
			ActorUserID: fixture.UserA, TaskID: fixture.TaskA, CompanyID: fixture.CompanyA,
		})
	if err != nil {
		t.Fatalf("GetTaskForActiveMember() error = %v", err)
	}
	retained, err := store.Queries().UpdateTask(ctx, generated.UpdateTaskParams{
		MilestoneType: current.MilestoneType, Title: "Unrelated title edit", Description: current.Description,
		Status: current.Status, Priority: current.Priority, AssigneeUserID: current.AssigneeUserID,
		ExecutedByAgentID: current.ExecutedByAgentID, TaskID: current.ID, CompanyID: current.CompanyID,
		ExpectedVersion: current.Version,
	})
	if err != nil || !retained.ExecutedByAgentID.Valid ||
		retained.ExecutedByAgentID.UUID != current.ExecutedByAgentID.UUID {
		t.Fatalf("UpdateTask(retain archived associations) = %+v, %v", retained, err)
	}

	archivedAgentID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO agents
			(id, company_id, name, title, provider, archived_at)
		VALUES ($1, $2, 'Archived replacement', 'Engineer', 'codex', clock_timestamp())`,
		archivedAgentID, fixture.CompanyA)
	t.Run("Agent", func(t *testing.T) {
		params := generated.UpdateTaskParams{
			MilestoneType:     retained.MilestoneType,
			Title:             retained.Title,
			Description:       retained.Description,
			Status:            retained.Status,
			Priority:          retained.Priority,
			AssigneeUserID:    retained.AssigneeUserID,
			ExecutedByAgentID: uuid.NullUUID{UUID: archivedAgentID, Valid: true},
			TaskID:            retained.ID,
			CompanyID:         retained.CompanyID,
			ExpectedVersion:   retained.Version,
		}
		if _, err := store.Queries().UpdateTask(ctx, params); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("UpdateTask(select archived Agent) error = %v, want pgx.ErrNoRows", err)
		}
	})

	t.Run("revoked user member", func(t *testing.T) {
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO company_members (company_id, user_id, role)
			VALUES ($1, $2, 'member')`, fixture.CompanyA, fixture.UserB)
		dbtest.MustExec(t, ctx, pool, `
			UPDATE tasks
			SET assignee_user_id=$2, executed_by_agent_id=NULL, updated_at=clock_timestamp(), version=version+1
			WHERE id=$1`, fixture.TaskA, fixture.UserB)
		dbtest.MustExec(t, ctx, pool, `
			UPDATE company_members
			SET status='revoked', revoked_at=clock_timestamp(),
			    revoked_by_user_id=$3, updated_at=clock_timestamp(), version=version+1
			WHERE company_id=$1 AND user_id=$2`,
			fixture.CompanyA, fixture.UserB, fixture.UserA)

		current, err := store.Queries().GetTaskForActiveMember(ctx,
			generated.GetTaskForActiveMemberParams{
				ActorUserID: fixture.UserA, TaskID: fixture.TaskA, CompanyID: fixture.CompanyA,
			})
		if err != nil {
			t.Fatalf("GetTaskForActiveMember(revoked assignee) error = %v", err)
		}
		// A Task whose assignee left keeps them until someone reassigns it
		// (ADR 0014): an unrelated edit does not have to move it first.
		retained, err := store.Queries().UpdateTask(ctx, generated.UpdateTaskParams{
			MilestoneType: current.MilestoneType, Title: "Edit with retained revoked assignee",
			Description: current.Description, Status: current.Status, Priority: current.Priority,
			AssigneeUserID: current.AssigneeUserID, ExecutedByAgentID: current.ExecutedByAgentID,
			TaskID: current.ID, CompanyID: current.CompanyID, ExpectedVersion: current.Version,
		})
		if err != nil || retained.AssigneeUserID != fixture.UserB {
			t.Fatalf("UpdateTask(retain revoked user assignee) = %+v, %v", retained, err)
		}

		reassigned, err := store.Queries().UpdateTask(ctx, generated.UpdateTaskParams{
			MilestoneType: retained.MilestoneType, Title: retained.Title, Description: retained.Description,
			Status: retained.Status, Priority: retained.Priority,
			AssigneeUserID: fixture.UserA, TaskID: retained.ID, CompanyID: retained.CompanyID,
			ExpectedVersion: retained.Version,
		})
		if err != nil || reassigned.AssigneeUserID != fixture.UserA {
			t.Fatalf("UpdateTask(reassign to active user) = %+v, %v", reassigned, err)
		}
		// Handing the Task BACK to the departed member is refused: they are
		// no longer the row's assignee, so the retention arm does not apply.
		if _, err := store.Queries().UpdateTask(ctx, generated.UpdateTaskParams{
			MilestoneType:   reassigned.MilestoneType,
			Title:           reassigned.Title,
			Description:     reassigned.Description,
			Status:          reassigned.Status,
			Priority:        reassigned.Priority,
			AssigneeUserID:  fixture.UserB,
			TaskID:          reassigned.ID,
			CompanyID:       reassigned.CompanyID,
			ExpectedVersion: reassigned.Version,
		}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("UpdateTask(assign to a revoked member) error = %v, want pgx.ErrNoRows", err)
		}
	})
}

func TestConcurrentCollaborationAllocatorsRemainGapFree(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("db.MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("db.NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)

	const writers = 12
	numbers := make([]int64, writers)
	messageIDs := make([]uuid.UUID, writers)
	messageSeqs := make([]int64, writers)
	errs := make([]error, writers*2)
	var wait sync.WaitGroup
	for index := range writers {
		wait.Add(2)
		go func() {
			defer wait.Done()
			var allocated generated.AllocateTaskNumberRow
			allocated, errs[index] = store.Queries().AllocateTaskNumber(ctx, fixture.CompanyA)
			numbers[index] = allocated.Number
		}()
		go func() {
			defer wait.Done()
			errs[writers+index] = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
				if _, lockErr := q.LockConversationForActiveUserMutation(
					ctx,
					generated.LockConversationForActiveUserMutationParams{
						ActorUserID:    fixture.UserA,
						ConversationID: fixture.ConversationA,
						CompanyID:      fixture.CompanyA,
					},
				); lockErr != nil {
					return lockErr
				}
				message, insertErr := q.InsertUserMessageAfterConversationLock(
					ctx,
					generated.InsertUserMessageAfterConversationLockParams{
						Content:        "concurrent",
						ActorUserID:    fixture.UserA,
						ConversationID: fixture.ConversationA,
						CompanyID:      fixture.CompanyA,
					},
				)
				messageIDs[index] = message.ID
				messageSeqs[index] = message.Seq
				return insertErr
			})
		}()
	}
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("concurrent operation %d error = %v", index, err)
		}
	}
	sort.Slice(numbers, func(left, right int) bool { return numbers[left] < numbers[right] })
	sort.Slice(messageSeqs, func(left, right int) bool { return messageSeqs[left] < messageSeqs[right] })
	generatedMessageIDs := make(map[uuid.UUID]struct{}, writers)
	for index := range writers {
		if numbers[index] != int64(index+1) {
			t.Fatalf("allocated Task numbers = %v, want gap-free 1..%d", numbers, writers)
		}
		if messageSeqs[index] != int64(index+2) {
			t.Fatalf("allocated Message sequences = %v, want gap-free 2..%d", messageSeqs, writers+1)
		}
		if messageIDs[index] == uuid.Nil {
			t.Fatalf("database-generated Message id %d is nil", index)
		}
		generatedMessageIDs[messageIDs[index]] = struct{}{}
	}
	if len(generatedMessageIDs) != writers {
		t.Fatalf("database-generated Message ids = %v, want %d unique ids", messageIDs, writers)
	}
}

func TestInsertTaskRejectsRevokedCreatorMembership(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("db.MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("db.NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE company_members
		SET status='revoked', revoked_at=clock_timestamp(), revoked_by_user_id=$2,
		    version=version+1, updated_at=clock_timestamp()
		WHERE company_id=$1 AND user_id=$2`, fixture.CompanyA, fixture.UserA)
	err = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		_, err := q.InsertTaskWithConversation(ctx, generated.InsertTaskWithConversationParams{
			ProjectID: fixture.ProjectA,
			Key:       "revoked_creator",
			Number:    2,
			Title:     "Revoked creator",

			MilestoneType:     string(MilestoneTypeProduct),
			Description:       "",
			Status:            "backlog",
			Priority:          "medium",
			AssigneeUserID:    fixture.UserA,
			ExecutedByAgentID: uuid.NullUUID{UUID: fixture.AgentA, Valid: true},
			CreatorUserID:     uuid.NullUUID{UUID: fixture.UserA, Valid: true},
			CompanyID:         fixture.CompanyA,
		})
		return err
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("InsertTask(revoked creator) error = %v, want pgx.ErrNoRows", err)
	}
}

func TestTaskListReturnsCompleteCollectionWithClosedFilters(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("db.MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("db.NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	conversation2, conversation3 := dbtest.NewUUID(t), dbtest.NewUUID(t)
	task2, task3 := dbtest.NewUUID(t), dbtest.NewUUID(t)
	otherAgentID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
				INSERT INTO agents (
			id, company_id, name, title, reports_to_agent_id, provider
		) VALUES ($1, $2, 'Product Agent', 'Product Lead', $3, 'codex')`,
		otherAgentID, fixture.CompanyA, fixture.AgentA)
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO conversations (id,company_id,kind,task_id,title,project_id) VALUES
				($1,$3,'task',$4,'Second',(SELECT id FROM projects WHERE company_id=$3 AND key='work')),
				($2,$3,'task',$5,'Third',(SELECT id FROM projects WHERE company_id=$3 AND key='work'))`,
			conversation2, conversation3, fixture.CompanyA, task2, task3)
		dbtest.MustExecTx(t, ctx, tx, `
						INSERT INTO tasks (project_id,
				id,company_id,conversation_id,
				milestone_type,number,key,title,status,priority,
				assignee_user_id,creator_user_id
			) VALUES ((SELECT id FROM projects WHERE company_id=$2 AND key='work'),
				$1,$2,$3,'product',2,'second','Second','todo','high',$4,$4
			)`,
			task2, fixture.CompanyA, conversation2, fixture.UserA)
		dbtest.MustExecTx(t, ctx, tx, `
						INSERT INTO tasks (project_id,
				id,company_id,conversation_id,
				milestone_type,number,key,title,status,priority,
				executed_by_agent_id,assignee_user_id,creator_user_id
			) VALUES ((SELECT id FROM projects WHERE company_id=$2 AND key='work'),
				$1,$2,$3,'product',3,'third','Third','backlog','medium',$4,$5,$5
			)`,
			task3, fixture.CompanyA, conversation3, otherAgentID, fixture.UserA)
	})
	dbtest.MustExec(t, ctx, pool, `
		WITH source AS MATERIALIZED (
			SELECT
				gen_random_uuid() AS task_id,
				gen_random_uuid() AS conversation_id,
				number
			FROM generate_series(4, 104) AS number
		), inserted_conversations AS (
			INSERT INTO conversations (id, company_id, kind, task_id, title, project_id)
			SELECT
				conversation_id,
				$1,
				'task',
				task_id,
				'Complete collection ' || number,
				(SELECT id FROM projects WHERE company_id=$1 AND key='work')
			FROM source
			RETURNING id
		)
				INSERT INTO tasks (project_id,
			id, company_id, conversation_id,
			milestone_type, number, key, title,
			assignee_user_id, creator_user_id
		)
		SELECT
 (SELECT id FROM projects WHERE company_id=$1 AND key='work'),
			task_id,
			$1,
			conversation_id,
			'product',
			number,
			'complete_collection_' || number,
			'Complete collection ' || number,
			$2,
			$2
		FROM source
				CROSS JOIN (SELECT count(*) FROM inserted_conversations) AS inserted`,
		fixture.CompanyA, fixture.UserA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO task_dependencies (project_id,
			company_id, dependent_task_id, prerequisite_task_id, created_by_user_id
		) VALUES ((SELECT project_id FROM tasks WHERE company_id=$1 AND id=$2), $1, $2, $3, $4)`,
		fixture.CompanyA, task3, fixture.TaskA, fixture.UserA)

	base := generated.ListTasksForActiveMemberParams{
		ActorUserID: fixture.UserA, CompanyID: fixture.CompanyA,
	}
	listed, err := store.Queries().ListTasksForActiveMember(ctx, base)
	if err != nil || len(listed) != 104 || listed[0].Number != 104 ||
		listed[102].Number != 2 || listed[103].Number != 1 {
		t.Fatalf("ListTasksForActiveMember() = %+v, %v", listed, err)
	}
	unresolved := base
	unresolved.HasDependenciesResolved = true
	listed, err = store.Queries().ListTasksForActiveMember(ctx, unresolved)
	if err != nil || len(listed) != 1 || listed[0].ID != task3 {
		t.Fatalf("ListTasksForActiveMember(unresolved dependencies) = %+v, %v", listed, err)
	}
	resolved := base
	resolved.HasDependenciesResolved, resolved.DependenciesResolved = true, true
	listed, err = store.Queries().ListTasksForActiveMember(ctx, resolved)
	if err != nil || len(listed) != 103 {
		t.Fatalf("ListTasksForActiveMember(resolved dependencies) = %d rows, %v", len(listed), err)
	}
	base.HasStatuses, base.Statuses = true, []string{"todo"}
	base.HasPriorities, base.Priorities = true, []string{"high"}
	base.HasAssignee, base.AssigneeID = true, fixture.UserA
	listed, err = store.Queries().ListTasksForActiveMember(ctx, base)
	if err != nil || len(listed) != 1 || listed[0].ID != task2 {
		t.Fatalf("ListTasksForActiveMember(filtered) = %+v, %v", listed, err)
	}
	// The executedBy filter is the other half (ADR 0014): the third Task is
	// the only one the Product Agent does, and it is neither todo nor high.
	base.HasExecutedBy, base.ExecutedByAgentID = true, otherAgentID
	listed, err = store.Queries().ListTasksForActiveMember(ctx, base)
	if err != nil || len(listed) != 0 {
		t.Fatalf("ListTasksForActiveMember(mismatched filters) = %+v, %v", listed, err)
	}
	base.HasStatuses, base.HasPriorities = false, false
	listed, err = store.Queries().ListTasksForActiveMember(ctx, base)
	if err != nil || len(listed) != 1 || listed[0].ID != task3 {
		t.Fatalf("ListTasksForActiveMember(executedBy) = %+v, %v", listed, err)
	}
}

func TestTaskQueriesPersistTerminalCompletionLifecycle(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("db.MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("db.NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	var created generated.InsertTaskWithConversationRow
	err = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		row, err := q.InsertTaskWithConversation(ctx, generated.InsertTaskWithConversationParams{
			ProjectID: fixture.ProjectA,
			Key:       "cancelled",

			MilestoneType:  string(MilestoneTypeProduct),
			Number:         2,
			Title:          "Cancelled",
			Description:    "",
			Status:         "cancelled",
			Priority:       "medium",
			AssigneeUserID: fixture.UserA,
			CreatorUserID:  uuid.NullUUID{UUID: fixture.UserA, Valid: true},
			CompanyID:      fixture.CompanyA,
		})
		created = row
		return err
	})
	if err != nil {
		t.Fatalf("InsertTask(cancelled) error = %v", err)
	}
	if created.UpdatedAt.Time.Before(created.CreatedAt.Time) {
		t.Fatalf("InsertTask(cancelled) lifecycle = created %v updated %v",
			created.CreatedAt, created.UpdatedAt)
	}
	updated, err := store.Queries().UpdateTask(ctx, generated.UpdateTaskParams{
		MilestoneType:     string(MilestoneTypeProduct),
		Title:             "Task A",
		Description:       "",
		Status:            "cancelled",
		Priority:          "medium",
		AssigneeUserID:    fixture.UserA,
		ExecutedByAgentID: uuid.NullUUID{UUID: fixture.AgentA, Valid: true},
		TaskID:            fixture.TaskA,
		CompanyID:         fixture.CompanyA,
		ExpectedVersion:   1,
	})
	if err != nil {
		t.Fatalf("UpdateTask(cancelled) error = %v", err)
	}
	if !updated.UpdatedAt.Time.After(created.UpdatedAt.Time) {
		t.Fatalf("UpdateTask(cancelled) lifecycle = created %v updated %v",
			updated.CreatedAt, updated.UpdatedAt)
	}
}

func TestTaskCreationUsesDatabaseGeneratedPairAndUniqueKey(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("db.MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("db.NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `UPDATE companies SET next_task_number=2 WHERE id=$1`, fixture.CompanyA)

	var created generated.InsertTaskWithConversationRow
	err = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		allocated, err := q.AllocateTaskNumber(ctx, fixture.CompanyA)
		if err != nil {
			return err
		}
		created, err = q.InsertTaskWithConversation(ctx, generated.InsertTaskWithConversationParams{
			ProjectID: fixture.ProjectA,
			Key:       "stable_task",

			MilestoneType:     string(MilestoneTypeProduct),
			Number:            allocated.Number,
			Title:             "Stable task",
			Description:       "",
			Status:            "backlog",
			Priority:          "medium",
			AssigneeUserID:    fixture.UserA,
			ExecutedByAgentID: uuid.NullUUID{UUID: fixture.AgentA, Valid: true},
			CreatorUserID:     uuid.NullUUID{UUID: fixture.UserA, Valid: true},
			CompanyID:         fixture.CompanyA,
		})
		return err
	})
	if err != nil {
		t.Fatalf("InsertTaskWithConversation() error = %v", err)
	}
	var linkedTaskID uuid.UUID
	if err := pool.QueryRow(
		ctx,
		`SELECT task_id FROM conversations WHERE id=$1`,
		created.ConversationID,
	).Scan(&linkedTaskID); err != nil {
		t.Fatalf("read generated Task Conversation link: %v", err)
	}
	if created.ID == uuid.Nil || created.ConversationID == uuid.Nil ||
		created.ID == created.ConversationID || linkedTaskID != created.ID {
		t.Fatalf("created Task pair = %+v", created)
	}
	if created.Number != 2 {
		t.Fatalf("created Task number = %d, want 2", created.Number)
	}
	_, err = store.Queries().InsertTaskWithConversation(ctx, generated.InsertTaskWithConversationParams{
		ProjectID: fixture.ProjectA,
		Key:       "stable_task", Number: 3, Title: "Duplicate stable task", Description: "",
		MilestoneType: string(MilestoneTypeProduct), Status: "backlog", Priority: "medium",
		AssigneeUserID:    fixture.UserA,
		ExecutedByAgentID: uuid.NullUUID{UUID: fixture.AgentA, Valid: true},
		CreatorUserID:     uuid.NullUUID{UUID: fixture.UserA, Valid: true},
		CompanyID:         fixture.CompanyA,
	})
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "23505" ||
		postgresError.ConstraintName != "tasks_company_key_key" {
		t.Fatalf("duplicate Task key error = %v, want tasks_company_key_key 23505", err)
	}
}
