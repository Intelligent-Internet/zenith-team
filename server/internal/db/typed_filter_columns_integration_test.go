//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

// Each migration below moves a value that SQL used to read out of a jsonb
// document into a typed column. The upgrade reads the document once; these
// tests pin that one read.

func migrateTo(t *testing.T, ctx context.Context, pool *pgxpool.Pool, version uint) {
	t.Helper()
	runner, err := newMigrationRunner(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Migrate(version); err != nil {
		t.Fatalf("migrate to %d: %v", version, err)
	}
	if sourceErr, databaseErr := runner.Close(); sourceErr != nil || databaseErr != nil {
		t.Fatalf("close migration runner: %v, %v", sourceErr, databaseErr)
	}
}

func TestRuntimeDeviceNameMigrationMovesTheNameOutOfMetadata(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	migrateTo(t, ctx, pool, 48)
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runtimes SET metadata = '{"version":"1.2.3","deviceName":"Owner Laptop"}'
		WHERE id = $1`, fixture.RuntimeA)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runtimes SET metadata = '{"version":"1.2.3","deviceName":""}'
		WHERE id = $1`, fixture.RuntimeB)
	migrateTo(t, ctx, pool, 49)
	for _, want := range []struct {
		name     string
		id       any
		device   *string
		metadata string
	}{
		{name: "named", id: fixture.RuntimeA, device: ptr("Owner Laptop"), metadata: `{"version": "1.2.3"}`},
		{name: "unnamed", id: fixture.RuntimeB, device: nil, metadata: `{"version": "1.2.3"}`},
	} {
		var device *string
		var metadata string
		if err := pool.QueryRow(ctx,
			`SELECT device_name, metadata::text FROM runtimes WHERE id = $1`, want.id).Scan(&device, &metadata); err != nil {
			t.Fatal(err)
		}
		if (device == nil) != (want.device == nil) || (device != nil && *device != *want.device) ||
			metadata != want.metadata {
			t.Fatalf("%s runtime: device_name = %v, metadata = %s; want %v, %s",
				want.name, device, metadata, want.device, want.metadata)
		}
	}
	// Rolling back puts the name back into the document.
	migrateTo(t, ctx, pool, 48)
	var metadata string
	if err := pool.QueryRow(ctx,
		`SELECT metadata->>'deviceName' FROM runtimes WHERE id = $1`, fixture.RuntimeA).Scan(&metadata); err != nil ||
		metadata != "Owner Laptop" {
		t.Fatalf("rolled back deviceName = %q, %v", metadata, err)
	}
}

func TestRunSourceTaskMigrationKeepsTheTaskIdentity(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	migrateTo(t, ctx, pool, 49)
	fixture := dbtest.SeedFixture(t, ctx, pool)
	insertRun := func(companyID, agentID, runtimeID, conversationID, taskID any, runType, runContext string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO runs (company_id, agent_id, runtime_id, conversation_id, task_id, status, run_type, context)
			VALUES ($1, $2, $3, $4, $5, 'completed', $6, $7::jsonb) RETURNING id`,
			companyID, agentID, runtimeID, conversationID, taskID, runType, runContext).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	frozen := insertRun(fixture.CompanyA, fixture.AgentA, fixture.RuntimeA, fixture.ConversationA, fixture.TaskA,
		"task", `{"source":{"entityType":"task","entityId":"`+fixture.TaskA.String()+`"}}`)
	legacy := insertRun(fixture.CompanyB, fixture.AgentB, fixture.RuntimeB, fixture.ConversationB, fixture.TaskB, "task", `{}`)
	direct := insertRun(fixture.CompanyA, fixture.AgentA, fixture.RuntimeA, fixture.DirectConversationA, nil,
		"direct_chat", `{"source":{"entityType":"conversation","entityId":"`+fixture.DirectConversationA.String()+`"}}`)
	// The deleted Task survives only in the frozen source.
	dbtest.MustExec(t, ctx, pool, `DELETE FROM tasks WHERE id = $1`, fixture.TaskA)
	migrateTo(t, ctx, pool, 50)
	for _, want := range []struct {
		name string
		id   uuid.UUID
		task uuid.NullUUID
	}{
		{name: "frozen source", id: frozen, task: uuid.NullUUID{UUID: fixture.TaskA, Valid: true}},
		{name: "legacy live Task", id: legacy, task: uuid.NullUUID{UUID: fixture.TaskB, Valid: true}},
		{name: "direct", id: direct},
	} {
		var got uuid.NullUUID
		if err := pool.QueryRow(ctx, `SELECT source_task_id FROM runs WHERE id = $1`, want.id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want.task {
			t.Fatalf("%s run source_task_id = %v, want %v", want.name, got, want.task)
		}
	}
}

func TestRunFinalAnswerMigrationRecordsTheLatestProvenAnswer(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	migrateTo(t, ctx, pool, 50)
	fixture := dbtest.SeedFixture(t, ctx, pool)
	insertRun := func() uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO runs (company_id, agent_id, runtime_id, conversation_id, status, run_type, context)
			VALUES ($1, $2, $3, $4, 'completed', 'direct_chat', '{}') RETURNING id`,
			fixture.CompanyA, fixture.AgentA, fixture.RuntimeA, fixture.DirectConversationA).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	appendText := func(runID uuid.UUID, itemKey string, final bool) {
		t.Helper()
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO conversation_items (company_id, conversation_id, run_id, seq, type, author_kind, author_id, content, parts, item_key)
			SELECT $1, $2, $3, COALESCE(max(seq), 0) + 1, 'agent_text', 'agent', $4, 'text',
				jsonb_build_array(jsonb_build_object('type', 'text', 'text', 'text', 'final', $5::boolean)), $6
			FROM conversation_items WHERE conversation_id = $2`,
			fixture.CompanyA, fixture.DirectConversationA, runID, fixture.AgentA, final, itemKey)
	}
	answered := insertRun()
	appendText(answered, "first", true)
	appendText(answered, "second", true)
	appendText(answered, "trailing commentary", false)
	unproven := insertRun()
	appendText(unproven, "commentary", false)
	migrateTo(t, ctx, pool, 51)
	for _, want := range []struct {
		name string
		id   uuid.UUID
		key  *string
	}{
		{name: "answered", id: answered, key: ptr("second")},
		{name: "unproven", id: unproven},
	} {
		var got *string
		if err := pool.QueryRow(ctx, `SELECT final_answer_item_key FROM runs WHERE id = $1`, want.id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if (got == nil) != (want.key == nil) || (got != nil && *got != *want.key) {
			t.Fatalf("%s run final_answer_item_key = %v, want %v", want.name, got, want.key)
		}
	}
}

func TestAgentToolMigrationCopiesDisabledPluginsAndStudioAccess(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	migrateTo(t, ctx, pool, 51)
	fixture := dbtest.SeedFixture(t, ctx, pool)
	pluginID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE agents SET settings = jsonb_build_object('capabilities', jsonb_build_object(
			'runtimeId', runtime_id, 'enabledTools', jsonb_build_array('zenith-studio'),
			'disabledPlugins', jsonb_build_array($2::text)))
		WHERE id = $1`, fixture.AgentA, pluginID.String())
	insertRun := func(runContext string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO runs (company_id, agent_id, runtime_id, conversation_id, status, run_type, context)
			VALUES ($1, $2, $3, $4, 'completed', 'direct_chat', $5::jsonb) RETURNING id`,
			fixture.CompanyA, fixture.AgentA, fixture.RuntimeA, fixture.DirectConversationA, runContext).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	withStudio := insertRun(`{"agent":{"capabilities":{"enabledTools":["zenith-studio"]}}}`)
	withoutStudio := insertRun(`{"agent":{"capabilities":{"enabledTools":[]}}}`)
	migrateTo(t, ctx, pool, 52)
	var disabled, untouched []uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT disabled_plugin_ids FROM agents WHERE id = $1`, fixture.AgentA).Scan(&disabled); err != nil ||
		len(disabled) != 1 || disabled[0] != pluginID {
		t.Fatalf("configured Agent disabled_plugin_ids = %v, %v", disabled, err)
	}
	if err := pool.QueryRow(ctx, `SELECT disabled_plugin_ids FROM agents WHERE id = $1`, fixture.AgentB).Scan(&untouched); err != nil ||
		len(untouched) != 0 {
		t.Fatalf("unconfigured Agent disabled_plugin_ids = %v, %v", untouched, err)
	}
	for id, want := range map[uuid.UUID]bool{withStudio: true, withoutStudio: false} {
		var got bool
		if err := pool.QueryRow(ctx, `SELECT studio_enabled FROM runs WHERE id = $1`, id).Scan(&got); err != nil || got != want {
			t.Fatalf("run %s studio_enabled = %v, %v; want %v", id, got, err, want)
		}
	}
}

func TestRunConnectorScopeMigrationMovesTheDocumentsIntoRows(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	migrateTo(t, ctx, pool, 53)
	fixture := dbtest.SeedFixture(t, ctx, pool)
	connectorID := dbtest.NewUUID(t)
	entry := `{"id":"` + connectorID.String() + `","policyRevision":3,"source":"codex_app","nativeId":"github","contractDigest":"digest-1"}`
	insertRun := func(selection, snapshot, runContext string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO runs (company_id, agent_id, runtime_id, conversation_id, status, run_type,
				connector_selection, connector_snapshot, context)
			VALUES ($1, $2, $3, $4, 'running', 'direct_chat', $5::jsonb, $6::jsonb, $7::jsonb) RETURNING id`,
			fixture.CompanyA, fixture.AgentA, fixture.RuntimeA, fixture.DirectConversationA,
			selection, snapshot, runContext).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	frozenSnapshot := `{"schemaVersion":1,"connectors":[` + entry + `]}`
	exact := insertRun(`{"schemaVersion":1,"mode":"exact","connectors":[`+entry+`]}`, frozenSnapshot,
		`{"connectorSnapshot":`+frozenSnapshot+`}`)
	defaults := insertRun(`{"schemaVersion":1,"mode":"all","connectors":[]}`, `{"schemaVersion":1,"connectors":[]}`, `{}`)
	migrateTo(t, ctx, pool, 54)
	for _, want := range []struct {
		name               string
		id                 uuid.UUID
		mode               string
		selected, snapshot int
		frozen             bool
	}{
		{name: "exact", id: exact, mode: "exact", selected: 1, snapshot: 1, frozen: true},
		{name: "default", id: defaults, mode: "all"},
	} {
		var mode string
		var selected, snapshot int
		var frozen bool
		if err := pool.QueryRow(ctx, `
			SELECT connector_selection_mode, connector_snapshot_frozen_at IS NOT NULL,
				(SELECT count(*) FROM run_connector_selections WHERE run_id = runs.id AND connector_id = $2
					AND policy_revision = 3 AND source = 'codex_app' AND native_id = 'github' AND contract_digest = 'digest-1'),
				(SELECT count(*) FROM run_connector_snapshots WHERE run_id = runs.id AND connector_id = $2
					AND policy_revision = 3 AND contract_digest = 'digest-1')
			FROM runs WHERE id = $1`, want.id, connectorID).Scan(&mode, &frozen, &selected, &snapshot); err != nil {
			t.Fatal(err)
		}
		if mode != want.mode || selected != want.selected || snapshot != want.snapshot || frozen != want.frozen {
			t.Fatalf("%s run: mode %q, selected %d, snapshot %d, frozen %v; want %+v",
				want.name, mode, selected, snapshot, frozen, want)
		}
	}
	// Rolling back rebuilds both documents.
	migrateTo(t, ctx, pool, 53)
	var selectionMode, selectedNativeID, snapshotNativeID string
	if err := pool.QueryRow(ctx, `
		SELECT connector_selection->>'mode', connector_selection->'connectors'->0->>'nativeId',
			connector_snapshot->'connectors'->0->>'nativeId'
		FROM runs WHERE id = $1`, exact).Scan(&selectionMode, &selectedNativeID, &snapshotNativeID); err != nil ||
		selectionMode != "exact" || selectedNativeID != "github" || snapshotNativeID != "github" {
		t.Fatalf("rolled back documents = %q %q %q, %v", selectionMode, selectedNativeID, snapshotNativeID, err)
	}
}

func ptr[T any](value T) *T { return &value }
