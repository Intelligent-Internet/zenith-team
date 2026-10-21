//go:build integration

package db

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errDependencyCycle = errors.New("dependency cycle")

func TestTaskDependencyQueriesTraverseBothDirectionsAndDetectPaths(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	taskB := insertDependencyTestTask(t, ctx, pool, fixture.CompanyA, fixture.UserA, 2, "task_b_a")
	taskC := insertDependencyTestTask(t, ctx, pool, fixture.CompanyA, fixture.UserA, 3, "task_c_a")

	insertDependencyQuery(t, ctx, store.Queries(), fixture.CompanyA, taskB, fixture.TaskA, fixture.UserA)
	insertDependencyQuery(t, ctx, store.Queries(), fixture.CompanyA, taskC, taskB, fixture.UserA)

	projections, err := store.Queries().ListTaskDependencyProjections(
		ctx,
		generated.ListTaskDependencyProjectionsParams{
			CompanyID:   fixture.CompanyA,
			TaskID:      taskB,
			ActorUserID: fixture.UserA,
		},
	)
	if err != nil || len(projections) != 2 {
		t.Fatalf("ListTaskDependencyProjections() = %+v, %v", projections, err)
	}
	relatedTaskByDirection := make(map[string]uuid.UUID, len(projections))
	for _, projection := range projections {
		relatedTaskByDirection[projection.Direction] = projection.TaskID
	}
	if relatedTaskByDirection["blocked_by"] != fixture.TaskA ||
		relatedTaskByDirection["blocks"] != taskC {
		t.Fatalf("ListTaskDependencyProjections() directions = %+v", relatedTaskByDirection)
	}

	pathExists, err := store.Queries().TaskDependencyPathExists(ctx, generated.TaskDependencyPathExistsParams{
		CompanyID: fixture.CompanyA, FromTaskID: fixture.TaskA, ToTaskID: taskC,
	})
	if err != nil || !pathExists {
		t.Fatalf("TaskDependencyPathExists(A, C) = %v, %v, want true", pathExists, err)
	}
	pathExists, err = store.Queries().TaskDependencyPathExists(ctx, generated.TaskDependencyPathExistsParams{
		CompanyID: fixture.CompanyA, FromTaskID: taskC, ToTaskID: fixture.TaskA,
	})
	if err != nil || pathExists {
		t.Fatalf("TaskDependencyPathExists(C, A) = %v, %v, want false", pathExists, err)
	}

	t.Run("transitive cycle is rejected inside the graph transaction", func(t *testing.T) {
		err := addDependencyEdgeForQueryTest(
			ctx, store, fixture.CompanyA, fixture.TaskA, taskC, fixture.UserA,
		)
		if !errors.Is(err, errDependencyCycle) {
			t.Fatalf("add C -> A edge error = %v, want dependency cycle", err)
		}
	})

	t.Run("cross Company endpoint is rejected before graph mutation", func(t *testing.T) {
		err := addDependencyEdgeForQueryTest(
			ctx, store, fixture.CompanyA, taskC, fixture.TaskB, fixture.UserA,
		)
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("add foreign prerequisite error = %v, want pgx.ErrNoRows", err)
		}
	})

	t.Run("delete requires the current edge version", func(t *testing.T) {
		_, err := store.Queries().DeleteTaskDependency(ctx, generated.DeleteTaskDependencyParams{
			CompanyID: fixture.CompanyA, DependentTaskID: taskC,
			PrerequisiteTaskID: taskB, ExpectedVersion: 2,
		})
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("DeleteTaskDependency(stale) error = %v, want pgx.ErrNoRows", err)
		}
		deleted, err := store.Queries().DeleteTaskDependency(ctx, generated.DeleteTaskDependencyParams{
			CompanyID: fixture.CompanyA, DependentTaskID: taskC,
			PrerequisiteTaskID: taskB, ExpectedVersion: 1,
		})
		if err != nil || deleted.Version != 1 {
			t.Fatalf("DeleteTaskDependency(current) = %+v, %v", deleted, err)
		}
	})
}

func TestTaskDependencyGraphFiltersRequireBothEndpoints(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	marketingAgent := dbtest.NewUUID(t)
	dbtest.SeedBoundAgent(t, ctx, pool, marketingAgent, fixture.CompanyA,
		"Marketing", "codex", &fixture.AgentA, fixture.UserA, fixture.RuntimeA)
	insertTask := func(number int64, key string, agentID uuid.UUID) uuid.UUID {
		t.Helper()
		conversationID, taskID := dbtest.NewUUID(t), dbtest.NewUUID(t)
		dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
			dbtest.MustExecTx(t, ctx, tx, `
				INSERT INTO conversations (id, company_id, kind, task_id, title, project_id)
				VALUES ($1, $2, 'task', $3, $4, (SELECT id FROM projects WHERE company_id=$2 AND key='work'))`,
				conversationID, fixture.CompanyA, taskID, key)
			dbtest.MustExecTx(t, ctx, tx, `
								INSERT INTO tasks (project_id,
					id, company_id,
					milestone_type, conversation_id, number, key,
					title, executed_by_agent_id, assignee_user_id, creator_user_id
				) VALUES ((SELECT id FROM projects WHERE company_id=$2 AND key='work'), $1, $2, 'product', $3, $4, $5, $5, $6, $7, $7)`,
				taskID, fixture.CompanyA, conversationID, number, key, agentID, fixture.UserA)
		})
		return taskID
	}
	engineeringA := insertTask(2, "engineering_a", fixture.AgentA)
	engineeringB := insertTask(3, "engineering_b", fixture.AgentA)
	engineeringC := insertTask(4, "engineering_c", fixture.AgentA)
	otherAgent := insertTask(5, "other_agent", marketingAgent)
	insertDependencyQuery(t, ctx, store.Queries(), fixture.CompanyA, engineeringA, engineeringB, fixture.UserA)
	insertDependencyQuery(t, ctx, store.Queries(), fixture.CompanyA, engineeringC, engineeringB, fixture.UserA)
	insertDependencyQuery(t, ctx, store.Queries(), fixture.CompanyA, engineeringA, otherAgent, fixture.UserA)

	// The graph spans the Company's readable Projects: every edge there,
	// whichever Agents own the Tasks.
	rows, err := store.Queries().ListCompanyTaskDependencyGraph(ctx, generated.ListCompanyTaskDependencyGraphParams{
		CompanyID: fixture.CompanyA, ActorUserID: fixture.UserA,
	})
	if err != nil || len(rows) != 3 {
		t.Fatalf("ListCompanyTaskDependencyGraph() rows = %d, error = %v, want 3", len(rows), err)
	}
	other, err := store.Queries().
		ListCompanyTaskDependencyGraph(ctx, generated.ListCompanyTaskDependencyGraphParams{
			CompanyID: fixture.CompanyB, ActorUserID: fixture.UserA,
		})
	if err != nil || len(other) != 0 {
		t.Fatalf("ListCompanyTaskDependencyGraph(other Company) rows = %d, error = %v, want 0", len(other), err)
	}
	// A member who cannot read the Project sees none of its edges, and
	// neither endpoint of one is locked for them (ADR 0017).
	outsider := dbtest.NewUUID(t)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, 'graph-outsider@example.com', 'Outsider')`,
		outsider,
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		fixture.CompanyA,
		outsider,
	)
	hidden, err := store.Queries().
		ListCompanyTaskDependencyGraph(ctx, generated.ListCompanyTaskDependencyGraphParams{
			CompanyID: fixture.CompanyA, ActorUserID: outsider,
		})
	if err != nil || len(hidden) != 0 {
		t.Fatalf("ListCompanyTaskDependencyGraph(outsider) rows = %d, error = %v, want 0", len(hidden), err)
	}
	endpoints, err := store.Queries().LockTaskDependencyEndpoints(ctx, generated.LockTaskDependencyEndpointsParams{
		CompanyID:          fixture.CompanyA,
		DependentTaskID:    engineeringA,
		PrerequisiteTaskID: engineeringB,
		ActorUserID:        outsider,
	})
	if err != nil || len(endpoints) != 0 {
		t.Fatalf("LockTaskDependencyEndpoints(outsider) = %d rows, %v; want none", len(endpoints), err)
	}
}

func TestTaskDependencyGraphLockSerializesOppositeConcurrentEdges(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	taskB := insertDependencyTestTask(t, ctx, pool, fixture.CompanyA, fixture.UserA, 2, "opposite_b")

	start := make(chan struct{})
	errorsByWriter := make([]error, 2)
	edges := [][2]uuid.UUID{{fixture.TaskA, taskB}, {taskB, fixture.TaskA}}
	var wait sync.WaitGroup
	for index, edge := range edges {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errorsByWriter[index] = addDependencyEdgeForQueryTest(
				ctx, store, fixture.CompanyA, edge[0], edge[1], fixture.UserA,
			)
		}()
	}
	close(start)
	wait.Wait()

	cycleErrors := 0
	for _, err := range errorsByWriter {
		switch {
		case err == nil:
		case errors.Is(err, errDependencyCycle):
			cycleErrors++
		default:
			t.Fatalf("concurrent dependency mutation error = %v", err)
		}
	}
	if cycleErrors != 1 {
		t.Fatalf("cycle errors = %d, want 1; errors = %v", cycleErrors, errorsByWriter)
	}
	var edgeCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM task_dependencies WHERE company_id = $1`, fixture.CompanyA).
		Scan(&edgeCount); err != nil {
		t.Fatalf("count committed dependency edges: %v", err)
	}
	if edgeCount != 1 {
		t.Fatalf("committed dependency edges = %d, want 1", edgeCount)
	}
}

func addDependencyEdgeForQueryTest(
	ctx context.Context,
	store *Store,
	companyID uuid.UUID,
	dependentTaskID uuid.UUID,
	prerequisiteTaskID uuid.UUID,
	actorUserID uuid.UUID,
) error {
	return store.WithinTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite,
	}, func(queries *generated.Queries) error {
		if err := queries.LockTaskDependencyGraph(ctx, companyID); err != nil {
			return err
		}
		endpoints, err := queries.LockTaskDependencyEndpoints(ctx, generated.LockTaskDependencyEndpointsParams{
			CompanyID: companyID, DependentTaskID: dependentTaskID, PrerequisiteTaskID: prerequisiteTaskID,
			ActorUserID: actorUserID,
		})
		if err != nil {
			return err
		}
		if len(endpoints) != 2 {
			return pgx.ErrNoRows
		}
		cycle, err := queries.TaskDependencyPathExists(ctx, generated.TaskDependencyPathExistsParams{
			CompanyID: companyID, FromTaskID: dependentTaskID, ToTaskID: prerequisiteTaskID,
		})
		if err != nil {
			return err
		}
		if cycle {
			return errDependencyCycle
		}
		_, err = queries.InsertTaskDependency(ctx, generated.InsertTaskDependencyParams{
			CompanyID: companyID, DependentTaskID: dependentTaskID,
			PrerequisiteTaskID: prerequisiteTaskID,
			CreatedByUserID:    uuid.NullUUID{UUID: actorUserID, Valid: true},
		})
		return err
	})
}

func insertDependencyQuery(
	t *testing.T,
	ctx context.Context,
	queries *generated.Queries,
	companyID uuid.UUID,
	dependentTaskID uuid.UUID,
	prerequisiteTaskID uuid.UUID,
	actorUserID uuid.UUID,
) {
	t.Helper()
	if _, err := queries.InsertTaskDependency(ctx, generated.InsertTaskDependencyParams{
		CompanyID: companyID, DependentTaskID: dependentTaskID,
		PrerequisiteTaskID: prerequisiteTaskID,
		CreatedByUserID:    uuid.NullUUID{UUID: actorUserID, Valid: true},
	}); err != nil {
		t.Fatalf("InsertTaskDependency() error = %v", err)
	}
}

func insertDependencyTestTask(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID uuid.UUID,
	creatorUserID uuid.UUID,
	number int64,
	key string,
) uuid.UUID {
	t.Helper()
	conversationID := dbtest.NewUUID(t)
	taskID := dbtest.NewUUID(t)
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO conversations (id, company_id, kind, task_id, title, project_id)
			VALUES ($1, $2, 'task', $3, $4, (SELECT id FROM projects WHERE company_id=$2 AND key='work'))`, conversationID, companyID, taskID, key)
		dbtest.MustExecTx(t, ctx, tx, `
						INSERT INTO tasks (project_id,
				id, company_id, conversation_id,
				milestone_type, number, key, title,
				assignee_user_id, creator_user_id
			)
			VALUES ((SELECT id FROM projects WHERE company_id=$2 AND key='work'),
				$1, $2, $3,
				'product', $4, $5, $5, $6, $6
			)`,
			taskID, companyID, conversationID, number, key, creatorUserID)
	})
	return taskID
}
