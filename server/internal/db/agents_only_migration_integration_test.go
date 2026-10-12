//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

// The agents-only cutover (000017) moves the appearance a department
// configuration carried onto the Agent, drops the Task's department owner
// pointer and the department tables, and renames the starter journey step —
// without touching a Task's assignee, description or dependencies.
func TestAgentsOnlyMigrationMovesAppearanceOntoAgents(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := runMigration(ctx, pool, "migrate to version 16", func(runner *migrate.Migrate) error {
		return runner.Migrate(16)
	}); err != nil {
		t.Fatalf("migrate to version 16: %v", err)
	}

	companyID, runtimeID := dbtest.NewUUID(t), dbtest.NewUUID(t)
	rootID, engineeringID := dbtest.NewUUID(t), dbtest.NewUUID(t)
	taskID, conversationID := dbtest.NewUUID(t), dbtest.NewUUID(t)
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO companies (id, name, slug, task_prefix)
			VALUES ($1, 'Existing Company', 'agents-only-cutover', 'EXT')`, companyID)
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO runtimes (id, company_id, daemon_id, name, provider)
			VALUES ($1, $2, 'existing-daemon', 'Existing Runtime', 'codex')`, runtimeID, companyID)
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO agents (
				id, company_id, runtime_id, name, title, reports_to_agent_id, department_key
			) VALUES
				($1, $3, $4, 'Andrew', 'CEO', NULL, NULL),
				($2, $3, $4, 'Eli', 'Engineering Agent', $1, 'engineering')`,
			rootID, engineeringID, companyID, runtimeID)
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO department_configs (
				agent_id, company_id, department_key, display_name, icon_key,
				accent_color, avatar_key, identity_style, communication_tone
			) VALUES ($1, $2, 'engineering', 'Engineering', 'code',
				'#58B8F2', 'engineering-male', 'studio', 'mixed')`, engineeringID, companyID)
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO conversations (id, company_id, kind, task_id, title)
			VALUES ($1, $2, 'task', $3, 'Existing Task')`, conversationID, companyID, taskID)
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO tasks (
				id, company_id, conversation_id, department_agent_id,
				milestone_type, number, key, title, description,
				assignee_agent_id, creator_agent_id
			) VALUES ($1, $2, $3, $4, 'product', 1, 'existing_task', 'Existing Task',
				'Preserve authored work', $4, $5)`,
			taskID, companyID, conversationID, engineeringID, rootID)
	})

	if err := runMigration(ctx, pool, "migrate to version 17", func(runner *migrate.Migrate) error { return runner.Migrate(17) }); err != nil {
		t.Fatalf("upgrade from version 16: %v", err)
	}
	for _, table := range []string{"department_configs", "department_capabilities"} {
		assertTableDoesNotExist(t, ctx, pool, table)
	}

	var avatarKey, accentColor *string
	if err := pool.QueryRow(ctx, `SELECT avatar_key, accent_color FROM agents WHERE id = $1`, engineeringID).
		Scan(&avatarKey, &accentColor); err != nil {
		t.Fatalf("read migrated appearance: %v", err)
	}
	if avatarKey == nil || *avatarKey != "engineering-male" ||
		accentColor == nil || *accentColor != "#58B8F2" {
		t.Errorf("Agent appearance = %v / %v, want the department configuration's", avatarKey, accentColor)
	}
	if err := pool.QueryRow(ctx, `SELECT avatar_key, accent_color FROM agents WHERE id = $1`, rootID).
		Scan(&avatarKey, &accentColor); err != nil {
		t.Fatalf("read root appearance: %v", err)
	}
	if avatarKey != nil || accentColor != nil {
		t.Errorf("root Agent appearance = %v / %v, want none", avatarKey, accentColor)
	}

	var assigneeID, creatorID uuid.UUID
	var description string
	if err := pool.QueryRow(ctx, `
		SELECT assignee_agent_id, creator_agent_id, description FROM tasks WHERE id = $1`, taskID).
		Scan(&assigneeID, &creatorID, &description); err != nil {
		t.Fatalf("read preserved Task: %v", err)
	}
	if assigneeID != engineeringID || creatorID != rootID || description != "Preserve authored work" {
		t.Errorf("Task changed during upgrade: assignee = %s, creator = %s, description = %q",
			assigneeID, creatorID, description)
	}
	var stateHasDepartment bool
	if err := pool.QueryRow(ctx, `
		SELECT state ? 'departmentAgentId' FROM activity_subject_states
		WHERE entity_type = 'task' AND entity_id = $1`, taskID).Scan(&stateHasDepartment); err != nil {
		t.Fatalf("read current Task activity state: %v", err)
	}
	if stateHasDepartment {
		t.Error("current Task activity state still carries departmentAgentId")
	}
}
