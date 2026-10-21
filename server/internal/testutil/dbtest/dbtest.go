// Package dbtest provides PostgreSQL scaffolding shared by integration suites.
// One parent-owned PostgreSQL container serves every test process; each dbtest
// allocation gets a separate database so fixtures remain isolated. Only tests
// and the test runner import this package, so it never reaches a shipped binary.
package dbtest

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SharedDatabaseURLEnv carries the parent-owned PostgreSQL server URL to test
// processes.
const SharedDatabaseURLEnv = "ZENITH_TEAM_TEST_DATABASE_URL"

// ErrSharedDatabaseUnavailable means the test process was not given a shared
// server URL.
var ErrSharedDatabaseUnavailable = errors.New("shared PostgreSQL test server is unavailable")

// Database is one isolated logical database on the shared PostgreSQL test server.
type Database struct {
	Pool *pgxpool.Pool
	URL  string

	adminURL string
	name     string
}

// NewUUID returns a fresh identifier for seeded rows, as the uuid.UUID the
// generated queries speak. Production ids are DB-generated
// (gen_random_uuid()); fixtures mint the same random-v4 shape up front so
// tests can reference rows before inserting them.
func NewUUID(t *testing.T) uuid.UUID {
	t.Helper()
	return uuid.MustParse(uuid.NewString())
}

// StartPostgres allocates an isolated database and ties its cleanup to t.
func StartPostgres(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	database, err := OpenDatabase(ctx)
	if err != nil {
		if errors.Is(err, ErrSharedDatabaseUnavailable) {
			requireSharedDatabase(t, err)
			return nil, ""
		}
		t.Fatalf("allocate isolated PostgreSQL database: %v", err)
		return nil, ""
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := database.Close(cleanupCtx); err != nil {
			t.Errorf("drop isolated PostgreSQL database: %v", err)
		}
	})
	return database.Pool, database.URL
}

// OpenDatabase allocates an isolated database with caller-owned cleanup.
func OpenDatabase(ctx context.Context) (*Database, error) {
	adminURL := os.Getenv(SharedDatabaseURLEnv)
	if adminURL == "" {
		return nil, fmt.Errorf(
			"%w: %s is unset; run tests through make go-test or make go-gate",
			ErrSharedDatabaseUnavailable,
			SharedDatabaseURLEnv,
		)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate isolated database name: %w", err)
	}
	databaseName := isolatedDatabaseName(id)
	databaseURL, err := databaseURLForName(adminURL, databaseName)
	if err != nil {
		return nil, err
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse isolated PostgreSQL pool config: %w", err)
	}
	poolConfig.MaxConns = 20
	poolConfig.MinConns = 1
	poolConfig.MaxConnLifetime = time.Hour
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	poolConfig.HealthCheckPeriod = time.Minute
	if err := createDatabase(ctx, adminURL, databaseName); err != nil {
		return nil, errors.Join(err, dropDatabaseAfterFailure(ctx, adminURL, databaseName))
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("create isolated PostgreSQL pool: %w", err),
			dropDatabaseAfterFailure(ctx, adminURL, databaseName),
		)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.Join(
			fmt.Errorf("ping isolated PostgreSQL database: %w", err),
			dropDatabaseAfterFailure(ctx, adminURL, databaseName),
		)
	}
	return &Database{Pool: pool, URL: databaseURL, adminURL: adminURL, name: databaseName}, nil
}

// Close drops the isolated database and closes its pool. The pool wait respects
// ctx even if a test leaked an acquired connection.
func (d *Database) Close(ctx context.Context) error {
	dropErr := dropDatabase(ctx, d.adminURL, d.name)
	return errors.Join(dropErr, closePool(ctx, d.Pool.Close))
}

func closePool(ctx context.Context, closePool func()) error {
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		closePool()
	}()
	select {
	case <-closed:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("close isolated PostgreSQL pool: %w", ctx.Err())
	}
}

func createDatabase(ctx context.Context, adminURL, databaseName string) error {
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return fmt.Errorf("connect to shared PostgreSQL test server: %w", err)
	}
	_, createErr := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{databaseName}.Sanitize())
	if createErr != nil {
		createErr = fmt.Errorf("create isolated PostgreSQL database: %w", createErr)
	}
	return errors.Join(createErr, closeAdminConnection(ctx, conn))
}

func dropDatabase(ctx context.Context, adminURL, databaseName string) error {
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return fmt.Errorf("connect to shared PostgreSQL test server for cleanup: %w", err)
	}
	statement := "DROP DATABASE IF EXISTS " + pgx.Identifier{databaseName}.Sanitize() + " WITH (FORCE)"
	_, dropErr := conn.Exec(ctx, statement)
	if dropErr != nil {
		dropErr = fmt.Errorf("drop isolated PostgreSQL database: %w", dropErr)
	}
	return errors.Join(dropErr, closeAdminConnection(ctx, conn))
}

func dropDatabaseAfterFailure(ctx context.Context, adminURL, databaseName string) error {
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cleanupCancel()
	return dropDatabase(cleanupCtx, adminURL, databaseName)
}

func closeAdminConnection(ctx context.Context, conn *pgx.Conn) error {
	closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer closeCancel()
	if err := conn.Close(closeCtx); err != nil {
		return fmt.Errorf("close shared PostgreSQL admin connection: %w", err)
	}
	return nil
}

func isolatedDatabaseName(id uuid.UUID) string {
	return "zenith_team_test_" + strings.ReplaceAll(id.String(), "-", "")
}

func databaseURLForName(adminURL, databaseName string) (string, error) {
	parsed, err := url.Parse(adminURL)
	if err != nil {
		return "", fmt.Errorf("parse shared PostgreSQL test URL: %w", err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return "", fmt.Errorf("parse shared PostgreSQL test URL: unsupported scheme %q", parsed.Scheme)
	}
	parsed.Path = "/" + databaseName
	parsed.RawPath = ""
	return parsed.String(), nil
}

func requireSharedDatabase(t *testing.T, err error) {
	t.Helper()
	if os.Getenv("CI") != "" || os.Getenv("TEST_REQUIRE_DOCKER") == "1" {
		t.Fatalf("shared Docker-backed PostgreSQL is mandatory in CI: %v", err)
	}
	t.Skipf("shared Docker-backed PostgreSQL unavailable locally: %v", err)
}

type Fixture struct {
	UserA, UserB                              uuid.UUID
	CompanyA, CompanyB                        uuid.UUID
	ProjectA, ProjectB                        uuid.UUID
	AgentA, AgentB                            uuid.UUID
	ConversationA, ConversationB              uuid.UUID
	DirectConversationA, UnownedConversationB uuid.UUID
	TaskA, TaskB                              uuid.UUID
	RuntimeA, RuntimeB                        uuid.UUID
	DaemonA, DaemonB                          string
	// MessageA/MessageB are conversation_items of type 'user_message' — the
	// one stream absorbed the messages table.
	MessageA, MessageB uuid.UUID
	// RunA/RunB are database-generated UUID run ids, allocated by the sequence rather than
	// minted here: seeding an explicit id would leave runs_id_seq behind it
	// and the next CreateRun in the same database would collide.
	RunA, RunB uuid.UUID
}

// SeedFixture seeds the shared two-company graph in one transaction, in FK
// dependency order. The transaction matters because the circular
// task <-> conversation edge is DEFERRABLE and only validates at commit.
//
// The Run rows are inserted BEFORE their conversation items, which is the
// production order now: runs.id is a database-generated UUID that needs nothing from the
// stream, and the item is what carries run_id.
func SeedFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) Fixture {
	t.Helper()
	f := Fixture{
		UserA:    NewUUID(t),
		UserB:    NewUUID(t),
		ProjectA: NewUUID(t), ProjectB: NewUUID(t),
		CompanyA:             NewUUID(t),
		CompanyB:             NewUUID(t),
		AgentA:               NewUUID(t),
		AgentB:               NewUUID(t),
		ConversationA:        NewUUID(t),
		ConversationB:        NewUUID(t),
		DirectConversationA:  NewUUID(t),
		UnownedConversationB: NewUUID(t),
		TaskA:                NewUUID(t),
		TaskB:                NewUUID(t),
		RuntimeA:             NewUUID(t),
		RuntimeB:             NewUUID(t),
		DaemonA:              "daemon-a",
		DaemonB:              "daemon-b",
		MessageA:             NewUUID(t),
		MessageB:             NewUUID(t),
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin schema fixture transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	mustExecTx := func(statement string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed schema fixture: %v", err)
		}
	}
	mustExecTx(`
		INSERT INTO users (id, email, display_name) VALUES
			($1, 'schema-a@example.com', 'Schema A'),
			($2, 'schema-b@example.com', 'Schema B')`, f.UserA, f.UserB)
	mustExecTx(`
		INSERT INTO companies (id, name, slug, task_prefix) VALUES
			($1, 'Schema A', 'schema-a', 'SCA'),
			($2, 'Schema B', 'schema-b', 'SCA')`, f.CompanyA, f.CompanyB)
	mustExecTx(
		`INSERT INTO projects(id,company_id,key,title) VALUES ($1,$2,'work','Work'),($3,$4,'work','Work')`,
		f.ProjectA,
		f.CompanyA,
		f.ProjectB,
		f.CompanyB,
	)
	mustExecTx(`
		INSERT INTO company_members (company_id, user_id, role) VALUES
			($1, $2, 'owner'), ($3, $4, 'owner')`, f.CompanyA, f.UserA, f.CompanyB, f.UserB)
	mustExecTx(`
		INSERT INTO runtimes
			(id, company_id, daemon_id, name, provider,
			 status, owner_user_id, last_seen_at)
		VALUES
			($1, $2, $3, 'Runtime A', 'codex', 'online', $4, now()),
			($5, $6, $7, 'Runtime B', 'claude', 'online', $8, now())`,
		f.RuntimeA, f.CompanyA, f.DaemonA, f.UserA,
		f.RuntimeB, f.CompanyB, f.DaemonB, f.UserB)
	// Agents are templates of a provider; each fixture owner runs their
	// Company's Agent on their own runtime through a binding (ADR 0014).
	mustExecTx(`
			INSERT INTO agents
				(id, company_id, name, title, model, provider, created_by_user_id)
			VALUES
				($1, $2, 'Agent A', 'CEO', 'test', 'codex', $5),
				($3, $4, 'Agent B', 'CEO', 'test', 'claude', $6)`,
		f.AgentA, f.CompanyA, f.AgentB, f.CompanyB, f.UserA, f.UserB)
	mustExecTx(`
			INSERT INTO agent_runtime_bindings
				(company_id, agent_id, user_id, runtime_id, provider)
			VALUES
				($1, $2, $3, $4, 'codex'),
				($5, $6, $7, $8, 'claude')`,
		f.CompanyA, f.AgentA, f.UserA, f.RuntimeA,
		f.CompanyB, f.AgentB, f.UserB, f.RuntimeB)
	// A Task chat names its Task's Project (ADR 0017), and the fixture owners
	// hold every Project permission, as Project creation gives them. The
	// catalog is read from the CHECK so the fixture never restates it.
	mustExecTx(`
		INSERT INTO conversations (id, company_id, kind, task_id, agent_id, created_by_user_id, title, project_id)
		SELECT id, company_id, kind, task_id, agent_id, created_by_user_id, title, project_id FROM (VALUES
			($1::uuid, $2::uuid, 'task', $9::uuid, NULL::uuid, NULL::uuid, 'Task A', $13::uuid),
			($3, $4, 'task', $10, NULL, NULL, 'Task B', $14::uuid),
			($5, $2, 'direct', NULL, $6, $7, 'Direct A', NULL),
			($8, $4, 'direct', NULL, $11, $12, 'Direct B', NULL)
		) AS seed(id, company_id, kind, task_id, agent_id, created_by_user_id, title, project_id)`,
		f.ConversationA, f.CompanyA,
		f.ConversationB, f.CompanyB,
		f.DirectConversationA, f.AgentA, f.UserA, f.UnownedConversationB,
		f.TaskA, f.TaskB, f.AgentB, f.UserB, f.ProjectA, f.ProjectB)
	mustExecTx(`
			INSERT INTO project_grants (company_id, project_id, user_id, permission, granted_by_user_id)
			SELECT project.company_id, project.id, member.user_id, catalog.permission, member.user_id
			FROM projects AS project
			JOIN company_members AS member ON member.company_id = project.company_id AND member.role = 'owner'
			CROSS JOIN (
				SELECT (regexp_matches(pg_get_constraintdef(oid), '''([^'']+)''', 'g'))[1] AS permission
				FROM pg_constraint WHERE conname = 'project_grants_permission_check'
			) AS catalog
			WHERE project.id IN ($1, $2)`,
		f.ProjectA, f.ProjectB)
	// Each Task is assigned to its Company's owner, who runs it through the
	// Agent they bound above.
	mustExecTx(`
			INSERT INTO tasks
				(id, company_id, conversation_id, project_id,
				 milestone_type, number, key, title,
				 assignee_user_id, executed_by_agent_id, creator_user_id)
			VALUES
				($1, $2, $3, $11, 'product', 1, 'task_a', 'Task A', $5, $4, $5),
				($6, $7, $8, $12, 'product', 1, 'task_b', 'Task B', $10, $9, $10)`,
		f.TaskA, f.CompanyA, f.ConversationA, f.AgentA, f.UserA,
		f.TaskB, f.CompanyB, f.ConversationB, f.AgentB, f.UserB, f.ProjectA, f.ProjectB)
	// The fixture runs anchor the seeded conversation items and the usage
	// facts; they are seeded terminal so the shared graph never occupies the
	// runtime-scoped claim queues a test exercises with its own runs.
	//
	// The capacity lane comes from the conversation these name, not from a
	// column here: a run in a kind 'task' conversation holds a slot, and one
	// in a direct conversation does not.
	mustQueryRunID := func(
		companyID, agentID, taskID, conversationID, runtimeID uuid.UUID,
	) uuid.UUID {
		t.Helper()
		var runID uuid.UUID
		err := tx.QueryRow(
			ctx, `
			INSERT INTO runs
				(company_id, agent_id, task_id, conversation_id, runtime_id,
				 status, updated_at, run_type, revision)
			VALUES ($1, $2, $3, $4, $5, 'completed', now(), 'task', 1)
			RETURNING id`,
			companyID, agentID, taskID, conversationID, runtimeID,
		).Scan(&runID)
		if err != nil {
			t.Fatalf("seed schema fixture run: %v", err)
		}
		return runID
	}
	f.RunA = mustQueryRunID(f.CompanyA, f.AgentA, f.TaskA, f.ConversationA, f.RuntimeA)
	f.RunB = mustQueryRunID(f.CompanyB, f.AgentB, f.TaskB, f.ConversationB, f.RuntimeB)
	// The user's words, in the one stream, carrying the run that consumed
	// them. seq is conversation-wide, so each of these is its conversation's
	// first item.
	mustExecTx(`
		INSERT INTO conversation_items
			(id, company_id, conversation_id, run_id, seq, type, author_kind,
			 author_id, content)
		VALUES
			($1, $2, $3, $4, 1, 'user_message', 'user', $5, 'A'),
			($6, $7, $8, $9, 1, 'user_message', 'user', $10, 'B')`,
		f.MessageA, f.CompanyA, f.ConversationA, f.RunA, f.UserA,
		f.MessageB, f.CompanyB, f.ConversationB, f.RunB, f.UserB)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit schema fixture: %v", err)
	}
	return f
}

// WithinSeedTx runs fn inside one transaction and commits it. Seeds that
// create a task-kind conversation and its task must use this: the circular
// task <-> conversation FK is DEFERRABLE and only validates at commit.
func WithinSeedTx(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fn func(tx pgx.Tx),
) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	fn(tx)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed transaction: %v", err)
	}
}

// MustExecTx is MustExec against an open seed transaction.
func MustExecTx(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	statement string,
	args ...any,
) {
	t.Helper()
	if _, err := tx.Exec(ctx, statement, args...); err != nil {
		t.Fatalf("seed schema fixture: %v", err)
	}
}

func MustExec(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	statement string,
	args ...any,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, statement, args...); err != nil {
		t.Fatalf("seed schema fixture: %v", err)
	}
}

// SeedAgent inserts a template Agent (ADR 0014): a provider and a reporting
// line, no machine. reportsTo is nil for a root. The Agent runs for nobody
// until a member binds it; SeedAgentBinding does that.
func SeedAgent(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	agentID, companyID uuid.UUID,
	name, provider string,
	reportsTo *uuid.UUID,
) {
	t.Helper()
	MustExec(t, ctx, pool, `
		INSERT INTO agents (id, company_id, name, title, model, provider, reports_to_agent_id)
		VALUES ($1, $2, $3, 'Agent', 'test', $4, $5)`,
		agentID, companyID, name, provider, reportsTo)
}

// SeedAgentBinding writes a member's binding of an Agent to one of their
// runtimes, the row the Agent domain's Bind writes, moving an existing one.
func SeedAgentBinding(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID, agentID, userID, runtimeID uuid.UUID,
	provider string,
) {
	t.Helper()
	MustExec(t, ctx, pool, `
		INSERT INTO agent_runtime_bindings (company_id, agent_id, user_id, runtime_id, provider)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (agent_id, user_id) DO UPDATE SET runtime_id = EXCLUDED.runtime_id, updated_at = now()`,
		companyID, agentID, userID, runtimeID, provider)
}

// SeedBoundAgent is SeedAgent plus the creator's binding on runtimeID: the
// shape the fixture Agents have, for a test that needs one more of them.
func SeedBoundAgent(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	agentID, companyID uuid.UUID,
	name, provider string,
	reportsTo *uuid.UUID,
	userID, runtimeID uuid.UUID,
) {
	t.Helper()
	SeedAgent(t, ctx, pool, agentID, companyID, name, provider, reportsTo)
	SeedAgentBinding(t, ctx, pool, companyID, agentID, userID, runtimeID, provider)
}

// GrantProject stores a person's Project grant rows (ADR 0017), as a grant
// change would: project.read is always written with any other permission,
// since project_grants_read_fkey refuses a row without it. Existing rows are
// refreshed, so a grant after a Company rejoin passes the rejoin fence. With
// no permissions it grants read, write and execute: what a member needs to
// work on the Project's Tasks.
func GrantProject(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID, projectID, userID uuid.UUID,
	permissions ...string,
) {
	t.Helper()
	if len(permissions) == 0 {
		permissions = []string{"project.tasks.write", "project.tasks.execute"}
	}
	permissions = append([]string{"project.read"}, permissions...)
	MustExec(t, ctx, pool, `
		INSERT INTO project_grants (company_id, project_id, user_id, permission, granted_by_user_id)
		SELECT $1, $2, $3, requested.permission, $3
		FROM (SELECT DISTINCT permission FROM unnest($4::text[]) AS listed(permission)) AS requested
		ON CONFLICT (company_id, project_id, user_id, permission) DO UPDATE SET updated_at = clock_timestamp()`,
		companyID, projectID, userID, permissions)
}

// AssertStatementRejected runs statement inside a throwaway transaction and
// requires PostgreSQL to reject it with wantCode. Deferred constraints are
// forced immediate so a DEFERRABLE violation surfaces here rather than at
// commit. Shared so a schema invariant can be asserted from the package that
// owns the behavior rather than from internal/db.
func AssertStatementRejected(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	wantCode string,
	statement string,
	args ...any,
) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin rejected-statement transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = tx.Exec(ctx, statement, args...)
	if err == nil {
		_, err = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("statement error = %v, want PostgreSQL SQLSTATE %s", err, wantCode)
	}
	if pgErr.Code != wantCode {
		t.Fatalf("statement SQLSTATE = %s (%s), want %s", pgErr.Code, pgErr.Message, wantCode)
	}
}
