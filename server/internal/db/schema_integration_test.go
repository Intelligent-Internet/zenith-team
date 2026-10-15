//go:build integration

package db

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestSchemaEnforcesRealtimeProjectionAndAccessGenerationContracts(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)

	// Resources whose mutation contracts use optimistic concurrency carry a
	// version. Execution state machines use status/lifecycle CAS predicates
	// instead, so runs and runtimes remain unversioned.
	wantVersioned := map[string]bool{
		"project_artifacts": true, "canvas_edges": true, "review_threads": true, "review_comments": true,
		"calendar_events":     true,
		"social_connections":  true,
		"company_invitations": true,
		"company_share_links": true,
		"projects":            true,
		"wiki_pages":          true,
		"wiki_collections":    true,
		"users":               true, "companies": true, "company_members": true,
		"agents": true, "company_resources": true, "company_mailboxes": true,
		"commerce_accounts": true, "commerce_operations": true,
		"commerce_sites": true, "commerce_manifests": true,
		"commerce_site_signing_keys": true,
		"commerce_offers":            true, "commerce_customers": true,
		"commerce_gateway_operations": true, "commerce_payments": true,
		"commerce_subscriptions": true, "commerce_entitlements": true,
		"commerce_webhook_receipts": true,
		"approvals":                 true,
		"tasks":                     true, "task_dependencies": true,
		"conversations":                true,
		"conversation_user_states":     true,
		"conversation_thread_states":   true,
		"conversation_members":         true,
		"conversation_agents":          true,
		"conversation_run_queue_items": true,
		"attachments":                  true,
		"assets":                       true,
		"deliverables":                 true,
		"deliverable_review_actions":   true,
		"inbox_items":                  true,
		"output_publications":          true,
		"questions":                    true,
		"connector_catalog_items":      true,
		"connector_policies":           true,
		"mailbox_messages":             true,
		"mailbox_drafts":               true,
		"schedules":                    true,
		"schedule_firings":             true,
		"task_schedules":               true,
		"company_groups":               true,
	}
	rows, err := pool.Query(ctx, `
		SELECT table_name
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name IN (SELECT tablename FROM pg_catalog.pg_tables WHERE schemaname = 'public')
		  AND table_name <> 'schema_migrations'
		  AND column_name = 'version'
		ORDER BY table_name`)
	if err != nil {
		t.Fatalf("query versioned projections: %v", err)
	}
	defer rows.Close()
	seenVersioned := make(map[string]bool, len(wantVersioned))
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("scan versioned projection: %v", err)
		}
		seenVersioned[table] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate versioned projections: %v", err)
	}
	for table := range wantVersioned {
		if !seenVersioned[table] {
			t.Errorf("mutable realtime projection %s has no version column", table)
		}
	}
	for table := range seenVersioned {
		if !wantVersioned[table] {
			t.Errorf("append-only or mechanical table %s unexpectedly has a version column", table)
		}
	}

	var memberStatus string
	var memberVersion int64
	if err := pool.QueryRow(ctx, `
		SELECT status, version
		FROM company_members
		WHERE company_id = $1 AND user_id = $2`, fixture.CompanyA, fixture.UserA).
		Scan(&memberStatus, &memberVersion); err != nil {
		t.Fatalf("read initial retained membership: %v", err)
	}
	if memberStatus != "active" || memberVersion != 1 {
		t.Fatalf("initial membership = status %q version %d, want active/1", memberStatus, memberVersion)
	}
	assertStatementRejected(t, ctx, pool, "23514", `
		UPDATE company_members
		SET status = 'suspended', version = version + 1
		WHERE company_id = $1 AND user_id = $2`, fixture.CompanyA, fixture.UserA)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE company_members
		SET status = 'revoked', revoked_at = now(), revoked_by_user_id = $2,
			version = version + 1, updated_at = now()
		WHERE company_id = $1 AND user_id = $2`, fixture.CompanyA, fixture.UserA)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE company_members
		SET status = 'active', reactivated_at = now(), reactivated_by_user_id = $2,
			version = version + 1, updated_at = now()
		WHERE company_id = $1 AND user_id = $2`, fixture.CompanyA, fixture.UserA)
	if err := pool.QueryRow(ctx, `
		SELECT status, version
		FROM company_members
		WHERE company_id = $1 AND user_id = $2`, fixture.CompanyA, fixture.UserA).
		Scan(&memberStatus, &memberVersion); err != nil {
		t.Fatalf("read reactivated membership: %v", err)
	}
	if memberStatus != "active" || memberVersion != 3 {
		t.Fatalf("reactivated membership = status %q version %d, want active/3", memberStatus, memberVersion)
	}
}

func TestSchemaEnforcesTenantAndExecutionInvariants(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)

	// A Run no longer owns its transcript: the rows live in conversation_items
	// and run_id labels them with no foreign key, so a deleted Run takes
	// nothing with it. See docs/adr/0005-one-conversation-stream.md.
	t.Run("deleting a Run leaves its transcript items standing", func(t *testing.T) {
		var runID uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO runs (
				company_id, agent_id, conversation_id, runtime_id, status
			, run_type) VALUES ($1, $2, $3, $4, 'completed', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
			RETURNING id`,
			fixture.CompanyA, fixture.AgentA,
			fixture.DirectConversationA, fixture.RuntimeA).Scan(&runID); err != nil {
			t.Fatalf("seed transcript owner run: %v", err)
		}
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO conversation_items (
				id, company_id, conversation_id, run_id, seq, type, author_kind,
				author_id, item_key, content, parts, content_sha256
			) VALUES (
				$1, $2, $3, $4, 1, 'agent_text', 'agent', $5, 'provider-item-1',
				'done', '[{"type":"text","text":"done"}]'::jsonb,
				decode(repeat('00', 32), 'hex')
			)`, dbtest.NewUUID(t), fixture.CompanyA, fixture.DirectConversationA,
			runID, fixture.AgentA)

		dbtest.MustExec(t, ctx, pool, `DELETE FROM runs WHERE id = $1`, runID)
		var count int
		if err := pool.QueryRow(
			ctx,
			`SELECT count(*) FROM conversation_items WHERE run_id = $1`,
			runID,
		).Scan(&count); err != nil {
			t.Fatalf("count items after deleting their Run: %v", err)
		}
		if count != 1 {
			t.Fatalf("item count after deleting the labeling Run = %d, want 1", count)
		}
	})

	t.Run("runtime identity is one provider per daemon per company", func(t *testing.T) {
		assertStatementRejected(t, ctx, pool, "23505", `
			INSERT INTO runtimes
				(id, company_id, daemon_id, name, provider, status, owner_user_id)
			VALUES ($1, $2, $3, 'Duplicate codex', 'codex', 'offline', $4)`,
			dbtest.NewUUID(t), fixture.CompanyA, fixture.DaemonA, fixture.UserA)
		// The same daemon registers its other provider in the same company.
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO runtimes
				(id, company_id, daemon_id, name, provider, status, owner_user_id)
			VALUES ($1, $2, $3, 'Claude A', 'claude', 'offline', $4)`,
			dbtest.NewUUID(t), fixture.CompanyA, fixture.DaemonA, fixture.UserA)
	})

	// runtime_mode is gone — every runtime is local now — so provider is the
	// other closed vocabulary this row carries.
	t.Run("runtime provider and status vocabularies are closed", func(t *testing.T) {
		assertStatementRejected(t, ctx, pool, "23514", `
			UPDATE runtimes SET status = 'draining' WHERE id = $1`, fixture.RuntimeA)
		assertStatementRejected(t, ctx, pool, "23514", `
			INSERT INTO runtimes
				(id, company_id, daemon_id, name, provider, status)
			VALUES ($1, $2, 'daemon-provider', 'Bad provider', 'gemini', 'offline')`,
			dbtest.NewUUID(t), fixture.CompanyA)
	})

	// An unusable reason is a closed vocabulary, always carries its message,
	// and only ever sits on an offline row — a heartbeat cannot flip an
	// unusable runtime online through the schema.
	t.Run("runtime unusable reason is a closed vocabulary bound to offline", func(t *testing.T) {
		assertStatementRejected(t, ctx, pool, "23514", `
			INSERT INTO runtimes
				(id, company_id, daemon_id, name, provider, status,
				 unusable_reason_code, unusable_reason_message)
			VALUES ($1, $2, 'daemon-unusable-1', 'Unknown reason', 'codex', 'offline',
				'gemini_broken', 'broken')`,
			dbtest.NewUUID(t), fixture.CompanyA)
		assertStatementRejected(t, ctx, pool, "23514", `
			INSERT INTO runtimes
				(id, company_id, daemon_id, name, provider, status,
				 unusable_reason_code, unusable_reason_message)
			VALUES ($1, $2, 'daemon-unusable-2', 'Online but unusable', 'codex', 'online',
				'below_minimum_version', 'Codex CLI 0.90.0 is below the minimum 0.100.0')`,
			dbtest.NewUUID(t), fixture.CompanyA)
		assertStatementRejected(t, ctx, pool, "23514", `
			INSERT INTO runtimes
				(id, company_id, daemon_id, name, provider, status, unusable_reason_code)
			VALUES ($1, $2, 'daemon-unusable-3', 'No message', 'codex', 'offline', 'not_executable')`,
			dbtest.NewUUID(t), fixture.CompanyA)
		unusableID := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO runtimes
				(id, company_id, daemon_id, name, provider, status,
				 unusable_reason_code, unusable_reason_message)
			VALUES ($1, $2, 'daemon-unusable-4', 'Unusable codex', 'codex', 'offline',
				'below_minimum_version', 'Codex CLI 0.90.0 is below the minimum 0.100.0')`,
			unusableID, fixture.CompanyA)
		assertStatementRejected(t, ctx, pool, "23514", `
			UPDATE runtimes SET status = 'online' WHERE id = $1`, unusableID)
	})

	// The custom name is the free-text label the user-plane rename writes;
	// NULL is "no custom name". Its length rule lives in the domain, so the
	// column carries no CHECK, and a seeded row starts without one.
	t.Run("runtime custom name is nullable and carries no constraint", func(t *testing.T) {
		var customName *string
		if err := pool.QueryRow(
			ctx,
			`SELECT custom_name FROM runtimes WHERE id = $1`, fixture.RuntimeA,
		).Scan(&customName); err != nil {
			t.Fatalf("read seeded custom_name: %v", err)
		}
		if customName != nil {
			t.Fatalf("seeded custom_name = %q, want NULL", *customName)
		}
		dbtest.MustExec(t, ctx, pool,
			`UPDATE runtimes SET custom_name = 'Office Mac' WHERE id = $1`, fixture.RuntimeA)
		dbtest.MustExec(t, ctx, pool,
			`UPDATE runtimes SET custom_name = NULL WHERE id = $1`, fixture.RuntimeA)
	})

	t.Run("runtime garbage collection preserves retained Run history", func(t *testing.T) {
		historyRuntimeID := dbtest.NewUUID(t)
		unusedRuntimeID := dbtest.NewUUID(t)
		// The owner's soft removal changes nothing here: a removed row with
		// history is kept by the same guard, and a removed row nothing
		// references is garbage after the TTL like any offline runtime.
		removedHistoryRuntimeID := dbtest.NewUUID(t)
		removedUnusedRuntimeID := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool, `
			UPDATE runtimes
			SET last_seen_at = now()
			WHERE company_id = $1`, fixture.CompanyA)
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO runtimes (
				id, company_id, daemon_id, name, provider,
				status, owner_user_id, last_seen_at
			) VALUES
			(
				$1, $2, 'daemon-run-history', 'Historical Runtime',
				'codex', 'offline', $3, now() - interval '8 days'
			), (
				$4, $2, 'daemon-unused-history', 'Unused Runtime',
				'claude', 'offline', $3, now() - interval '8 days'
			)`, historyRuntimeID, fixture.CompanyA, fixture.UserA, unusedRuntimeID)
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO runtimes (
				id, company_id, daemon_id, name, provider,
				status, owner_user_id, last_seen_at, removed_at
			) VALUES
			(
				$1, $2, 'daemon-removed-history', 'Removed Historical Runtime',
				'codex', 'offline', $3, now() - interval '8 days', now() - interval '8 days'
			), (
				$4, $2, 'daemon-removed-unused', 'Removed Unused Runtime',
				'claude', 'offline', $3, now() - interval '8 days', now() - interval '8 days'
			)`, removedHistoryRuntimeID, fixture.CompanyA, fixture.UserA, removedUnusedRuntimeID)
		var historyRunID uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO runs (
				company_id, agent_id, conversation_id, runtime_id,
				status
			, run_type) VALUES ($1, $2, $3, $4, 'completed', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
			RETURNING id`, fixture.CompanyA, fixture.AgentA,
			fixture.DirectConversationA, historyRuntimeID).Scan(&historyRunID); err != nil {
			t.Fatalf("seed retained Run history: %v", err)
		}
		var removedHistoryRunID uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO runs (
				company_id, agent_id, conversation_id, runtime_id,
				status
			, run_type) VALUES ($1, $2, $3, $4, 'completed', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
			RETURNING id`, fixture.CompanyA, fixture.AgentA,
			fixture.DirectConversationA, removedHistoryRuntimeID).Scan(&removedHistoryRunID); err != nil {
			t.Fatalf("seed retained Run history on the removed runtime: %v", err)
		}

		messageRuntimeID := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO runtimes (
				id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at
			) VALUES ($1, $2, 'daemon-message-history', 'Message Runtime',
				'codex', 'offline', $3, now() - interval '8 days')`,
			messageRuntimeID, fixture.CompanyA, fixture.UserA)
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO message_connector_bindings (
				company_id, conversation_id, request_key, selected_by_user_id, agent_id, runtime_id
			) VALUES ($1, $2, gen_random_uuid(), $3, $4, $5)`,
			fixture.CompanyA, fixture.DirectConversationA, fixture.UserA, fixture.AgentA, messageRuntimeID)

		deleted, err := generated.New(pool).DeleteStaleOfflineRuntimes(ctx, 7*24*60*60)
		if err != nil {
			t.Fatalf("DeleteStaleOfflineRuntimes() error = %v", err)
		}
		deletedIDs := map[uuid.UUID]bool{}
		for _, row := range deleted {
			deletedIDs[row.ID] = true
		}
		if len(deleted) != 2 || !deletedIDs[unusedRuntimeID] || !deletedIDs[removedUnusedRuntimeID] {
			t.Fatalf(
				"deleted Runtimes = %+v, want only %s and %s",
				deleted,
				unusedRuntimeID,
				removedUnusedRuntimeID,
			)
		}
		for runID, wantRuntimeID := range map[uuid.UUID]uuid.UUID{
			historyRunID:        historyRuntimeID,
			removedHistoryRunID: removedHistoryRuntimeID,
		} {
			var runRuntimeID uuid.UUID
			if err := pool.QueryRow(ctx, `SELECT runtime_id FROM runs WHERE id = $1`, runID).
				Scan(&runRuntimeID); err != nil {
				t.Fatalf("read retained Run runtime: %v", err)
			}
			if runRuntimeID != wantRuntimeID {
				t.Fatalf("retained Run %d runtime = %s, want %s", runID, runRuntimeID, wantRuntimeID)
			}
		}
	})

	deleteTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin company cascade check: %v", err)
	}
	defer func() { _ = deleteTx.Rollback(context.Background()) }()
	if _, err := deleteTx.Exec(ctx, `DELETE FROM companies WHERE id = $1`, fixture.CompanyA); err != nil {
		t.Fatalf("delete complete Company graph: %v", err)
	}
	if _, err := deleteTx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		t.Fatalf("validate complete company cascade: %v", err)
	}
}

func TestDeliverableReviewActionInboxForeignKeyPinsCompany(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}

	var definition string
	if err := pool.QueryRow(
		ctx, `
		SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conrelid = 'deliverable_review_actions'::regclass
		  AND conname = 'deliverable_review_actions_inbox_item_fkey'`,
	).Scan(&definition); err != nil {
		t.Fatalf("read Deliverable review Inbox foreign key: %v", err)
	}
	if !strings.Contains(definition, "FOREIGN KEY (company_id, inbox_item_id)") ||
		!strings.Contains(definition, "REFERENCES inbox_items(company_id, id)") {
		t.Fatalf("Deliverable review Inbox foreign key = %q, want company-pinned composite", definition)
	}
}

func TestSchemaAllowsMultipleDistinctOutputsPerRun(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)

	for presentationNumber, fingerprint := range []string{"first", "second"} {
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO outputs (
				company_id, run_id, title, summary,
				presentation_fingerprint, presentation_number
			, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1, $2, $3, 'Saved Artifact', $4, $5, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2`,
			fixture.CompanyA,
			fixture.RunA,
			fingerprint,
			fingerprint,
			presentationNumber+1,
		)
	}
	var count int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM outputs WHERE run_id = $1`,
		fixture.RunA,
	).Scan(&count); err != nil {
		t.Fatalf("count same-Run Outputs: %v", err)
	}
	if count != 2 {
		t.Fatalf("same-Run Output count = %d, want 2", count)
	}

	assertStatementRejected(t, ctx, pool, "23505", `
		INSERT INTO outputs (
			company_id, run_id, title, summary,
			presentation_fingerprint, presentation_number
		, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1, $2, 'Duplicate number', 'Rejected', 'third', 2, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2`,
		fixture.CompanyA, fixture.RunA)
	assertStatementRejected(t, ctx, pool, "23505", `
		INSERT INTO outputs (
			company_id, run_id, title, summary,
			presentation_fingerprint, presentation_number
		, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1, $2, 'Duplicate fingerprint', 'Rejected', 'first', 3, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2`,
		fixture.CompanyA, fixture.RunA)
	assertStatementRejected(t, ctx, pool, "23514", `
		INSERT INTO outputs (
			company_id, run_id, title, summary,
			presentation_fingerprint, presentation_number
		, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1, $2, 'Invalid number', 'Rejected', 'fourth', 0, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2`,
		fixture.CompanyA, fixture.RunA)
}

func TestPreservationForeignKeysAllowOwnedGraphCascades(t *testing.T) {
	for _, test := range []struct {
		name       string
		deleteRoot func(context.Context, *pgxpool.Pool, dbtest.Fixture) error
	}{
		{
			name: "Run",
			deleteRoot: func(ctx context.Context, pool *pgxpool.Pool, fixture dbtest.Fixture) error {
				_, err := pool.Exec(ctx, `DELETE FROM runs WHERE id = $1`, fixture.RunA)
				return err
			},
		},
		{
			name: "Company",
			deleteRoot: func(ctx context.Context, pool *pgxpool.Pool, fixture dbtest.Fixture) error {
				_, err := pool.Exec(ctx, `DELETE FROM companies WHERE id = $1`, fixture.CompanyA)
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool, _ := dbtest.StartPostgres(t)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if err := MigrateUp(ctx, pool); err != nil {
				t.Fatalf("MigrateUp() error = %v", err)
			}
			fixture := dbtest.SeedFixture(t, ctx, pool)
			outputID := seedPreservedOutputGraph(t, ctx, pool, fixture)
			for _, constraint := range []string{
				"asset_versions_attachment_fkey",
				"output_items_asset_version_fkey",
				"output_publications_output_item_fkey",
				"message_connectors_connector_id_fkey",
				"message_connector_bindings_agent_id_fkey",
				"message_connector_bindings_runtime_id_fkey",
			} {
				var deleteAction string
				var deferrable, initiallyDeferred bool
				if err := pool.QueryRow(
					ctx, `
					SELECT confdeltype::text, condeferrable, condeferred
					FROM pg_constraint
					WHERE conname = $1`, constraint,
				).Scan(&deleteAction, &deferrable, &initiallyDeferred); err != nil {
					t.Fatalf("read preservation constraint %s: %v", constraint, err)
				}
				if deleteAction != "a" || !deferrable || !initiallyDeferred {
					t.Fatalf(
						"preservation constraint %s = delete %q deferrable %t deferred %t",
						constraint,
						deleteAction,
						deferrable,
						initiallyDeferred,
					)
				}
			}

			if err := test.deleteRoot(ctx, pool, fixture); err != nil {
				t.Fatalf("delete %s with preserved Output graph: %v", test.name, err)
			}
			for _, check := range []struct {
				table string
				query string
			}{
				{"asset_versions", `SELECT count(*) FROM asset_versions WHERE company_id = $1`},
				{"output_items", `SELECT count(*) FROM output_items WHERE company_id = $1`},
				{"output_publications", `SELECT count(*) FROM output_publications WHERE company_id = $1`},
			} {
				var count int
				if err := pool.QueryRow(
					ctx,
					check.query,
					fixture.CompanyA,
				).Scan(&count); err != nil {
					t.Fatalf("count %s after deleting %s: %v", check.table, test.name, err)
				}
				want := 0
				if test.name == "Run" {
					want = 1
				}
				if count != want {
					t.Fatalf("%s rows after deleting %s = %d, want %d (Output %s)",
						check.table, test.name, count, want, outputID)
				}
			}
		})
	}
}

func seedPreservedOutputGraph(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
) uuid.UUID {
	t.Helper()
	attachmentID := dbtest.NewUUID(t)
	assetID := dbtest.NewUUID(t)
	assetVersionID := dbtest.NewUUID(t)
	outputID := dbtest.NewUUID(t)
	outputItemID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO attachments (
			id, company_id, uploaded_by_user_id, request_key,
			filename, content_type, size_bytes, status
		) VALUES ($1, $2, $3, $4, 'application.zip', 'application/zip', 4, 'ready')`,
		attachmentID, fixture.CompanyA, fixture.UserA, dbtest.NewUUID(t))
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO assets (id, company_id, title, media_class, created_by_user_id)
		VALUES ($1, $2, 'Application', 'file', $3)`,
		assetID, fixture.CompanyA, fixture.UserA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO asset_versions (
			id, company_id, asset_id, version_number, attachment_id, source_run_id,
			filename, content_type, size_bytes, integrity_status
		) VALUES ($1, $2, $3, 1, $4, $5, 'application.zip', 'application/zip', 4, 'verified')`,
		assetVersionID, fixture.CompanyA, assetID, attachmentID, fixture.RunA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO outputs (id, company_id, run_id, title, summary, presentation_fingerprint, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1, $2, $3, 'Application', 'Ready', $4, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$2 AND origin.id=$3`,
		outputID, fixture.CompanyA, fixture.RunA,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO output_items (
			id, company_id, output_id, ordinal, role, label, asset_version_id
		) VALUES ($1, $2, $3, 0, 'primary', 'Application source', $4)`,
		outputItemID, fixture.CompanyA, outputID, assetVersionID)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO output_publications (
			company_id, output_id, output_item_id, requested_by_user_id,
			service_name, provider_type, provider_binding_id
		) VALUES ($1, $2, $3, $4, 'boardly-application', 'cloud_run', 'binding')`,
		fixture.CompanyA, outputID, outputItemID, fixture.UserA)
	return outputID
}

func TestSchemaEnforcesQueuedRunAndTaskConversationContracts(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)

	// An item is scoped by its Conversation, never by its Run. The composite FK
	// that tied a message to runs(company_id, conversation_id, id) is gone —
	// run_id is a label with no referential edge — so nothing here refuses an
	// item naming a Run from another Conversation. What still refuses a bad row
	// is the tenant edge. The same-Conversation guarantee is now structural in
	// the writer: EnqueueRunInTx returns the Run it just inserted for that
	// conversation, and the caller writes the item with that id.
	t.Run("an item is scoped to its Conversation, not to its Run", func(t *testing.T) {
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO conversation_items
				(id, company_id, conversation_id, run_id, seq, type, author_kind,
				 author_id, content)
			VALUES ($1, $2, $3, $4, 1, 'agent_text', 'agent', $5,
				'cross-conversation output')`,
			dbtest.NewUUID(t), fixture.CompanyA, fixture.DirectConversationA,
			fixture.RunA, fixture.AgentA)

		dbtest.AssertStatementRejected(t, ctx, pool, "23503", `
			INSERT INTO conversation_items
				(id, company_id, conversation_id, run_id, seq, type, author_kind,
				 author_id, content)
			VALUES ($1, $2, $3, $4, 9, 'agent_text', 'agent', $5,
				'cross-tenant output')`,
			dbtest.NewUUID(t), fixture.CompanyA, fixture.ConversationB,
			fixture.RunA, fixture.AgentA)
	})

	// The user's words outlive the Run that consumed them: run_id carries no
	// foreign key, so deleting run history reaches nothing in the stream.
	t.Run("deleting a Run leaves the user's words standing", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin Run delete transaction: %v", err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(ctx, `DELETE FROM runs WHERE id = $1`, fixture.RunA); err != nil {
			t.Fatalf("delete Run: %v", err)
		}
		var count int
		if err := tx.QueryRow(
			ctx,
			`SELECT count(*) FROM conversation_items WHERE id = $1`, fixture.MessageA,
		).Scan(&count); err != nil {
			t.Fatalf("count the user's words after deleting their Run: %v", err)
		}
		if count != 1 {
			t.Fatalf("user message count after deleting its Run = %d, want 1", count)
		}
	})

	// The fixture's own runs are terminal; the pending-slot invariants are
	// probed with a run this test queues itself (and the claim subtest then
	// claims).
	// runs.id is a UUID, so every writer here lets PostgreSQL allocate
	// and reads the id back.
	var pendingRun uuid.UUID
	t.Run("one pending run per task and agent", func(t *testing.T) {
		if err := pool.QueryRow(ctx, `
			INSERT INTO runs (company_id, agent_id, task_id, conversation_id, runtime_id, run_type)
			VALUES ($1, $2, $3, $4, $5, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))
			RETURNING id`,
			fixture.CompanyA, fixture.AgentA,
			fixture.TaskA, fixture.ConversationA, fixture.RuntimeA).Scan(&pendingRun); err != nil {
			t.Fatalf("queue task run: %v", err)
		}
		assertStatementRejected(t, ctx, pool, "23505", `
			INSERT INTO runs (company_id, agent_id, task_id, conversation_id, runtime_id, run_type)
			VALUES ($1, $2, $3, $4, $5, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))`,
			fixture.CompanyA, fixture.AgentA,
			fixture.TaskA, fixture.ConversationA, fixture.RuntimeA)
		// Direct-conversation runs carry no task; NULLs are distinct, so two
		// pending task-less runs for the same agent never collide.
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO runs (company_id, agent_id, task_id, conversation_id, runtime_id, run_type)
			VALUES ($1, $2, NULL, $3, $4, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 )), ($1, $2, NULL, $3, $4, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))`,
			fixture.CompanyA, fixture.AgentA,
			fixture.DirectConversationA, fixture.RuntimeA)
	})

	// trigger_message_id is gone: a send is fenced by its own request_key on
	// the conversation item, so a retried send resolves to the row it already
	// wrote instead of enqueuing a second Run.
	t.Run("a send is idempotent per request key", func(t *testing.T) {
		requestKey := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO conversation_items
				(id, company_id, conversation_id, run_id, seq, type, author_kind,
				 author_id, request_key, content)
			VALUES ($1, $2, $3, $4, 2, 'user_message', 'user', $5, $6, 'first send')`,
			dbtest.NewUUID(t), fixture.CompanyA, fixture.DirectConversationA,
			pendingRun, fixture.UserA, requestKey)
		assertStatementRejected(t, ctx, pool, "23505", `
			INSERT INTO conversation_items
				(id, company_id, conversation_id, run_id, seq, type, author_kind,
				 author_id, request_key, content)
			VALUES ($1, $2, $3, $4, 3, 'user_message', 'user', $5, $6, 'replayed send')`,
			dbtest.NewUUID(t), fixture.CompanyA, fixture.DirectConversationA,
			pendingRun, fixture.UserA, requestKey)
	})

	t.Run("claim stamps the dispatch fence and prepare lease", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin claim transaction: %v", err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		var claimed uuid.UUID
		if err := tx.QueryRow(ctx, `
			UPDATE runs
			SET status = 'dispatched', runtime_id = $1, updated_at = now(),
				prepare_lease_expires_at = now() + interval '45 seconds'
			WHERE id = (
				SELECT id FROM runs
				WHERE agent_id = $2 AND status = 'queued' AND task_id IS NOT NULL
				ORDER BY priority DESC, created_at, id
				LIMIT 1
				FOR UPDATE SKIP LOCKED)
			RETURNING id`, fixture.RuntimeA, fixture.AgentA).
			Scan(&claimed); err != nil {
			t.Fatalf("claim queued run: %v", err)
		}
		if claimed != pendingRun {
			t.Fatalf("claimed run = %d, want %d", claimed, pendingRun)
		}
	})

	// seq is the Conversation's counter now, so a conflicting delivery collides
	// with whatever already holds that number in the stream, whichever Run wrote
	// it.
	t.Run("the stream rejects conflicting duplicate sequence numbers", func(t *testing.T) {
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO conversation_items (
				id, company_id, conversation_id, run_id, seq, type, author_kind,
				author_id, item_key, content, parts, content_sha256
			) VALUES (
				$1, $2, $3, $4, 2, 'agent_text', 'agent', $5, 'first-delivery',
				'first delivery',
				'[{"type":"text","text":"first delivery"}]'::jsonb,
				decode(repeat('00', 32), 'hex')
			)`, dbtest.NewUUID(t), fixture.CompanyA, fixture.ConversationA,
			fixture.RunA, fixture.AgentA)
		assertStatementRejected(t, ctx, pool, "23505", `
			INSERT INTO conversation_items (
				id, company_id, conversation_id, run_id, seq, type, author_kind,
				author_id, item_key, content, parts, content_sha256
			) VALUES (
				$1, $2, $3, $4, 2, 'agent_text', 'agent', $5, 'conflicting-delivery',
				'conflicting delivery',
				'[{"type":"text","text":"conflicting delivery"}]'::jsonb,
				decode(repeat('01', 32), 'hex')
			)`, dbtest.NewUUID(t), fixture.CompanyA, fixture.ConversationA,
			fixture.RunA, fixture.AgentA)
	})

	t.Run("run token hashes are unique", func(t *testing.T) {
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO run_tokens (id, run_id, token_hash, expires_at)
			VALUES ($1, $2, $3, now() + interval '24 hours')`,
			dbtest.NewUUID(t), fixture.RunA, bytes.Repeat([]byte{1}, 32))
		assertStatementRejected(t, ctx, pool, "23505", `
			INSERT INTO run_tokens (id, run_id, token_hash, expires_at)
			VALUES ($1, $2, $3, now() + interval '24 hours')`,
			dbtest.NewUUID(t), fixture.RunB, bytes.Repeat([]byte{1}, 32))
	})
}

func TestSchemaEnforcesTerminalAndShapeConstraints(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)

	assertStatementRejected(t, ctx, pool, "23514", `
		UPDATE runs SET status = 'paused' WHERE id = $1`, fixture.RunA)
	assertStatementRejected(t, ctx, pool, "23514", `
		INSERT INTO activity_events (id, company_id, actor_type, action, entity_type, entity_id)
		VALUES ($1, $2, 'robot', 'created', 'task', $3::text)`,
		dbtest.NewUUID(t), fixture.CompanyA, fixture.TaskA)
	// Defaults: details is {}, and an insert that names no actor lands as
	// 'system' via the column default.
	var activityDetails []byte
	var activityActorType string
	activityID := dbtest.NewUUID(t)
	if err := pool.QueryRow(ctx, `
		INSERT INTO activity_events (id, company_id, action, entity_type, entity_id, agent_id)
		VALUES ($1, $2, 'status_changed', 'task', $3::text, $4)
		RETURNING details, actor_type`, activityID, fixture.CompanyA, fixture.TaskA, fixture.AgentA).
		Scan(&activityDetails, &activityActorType); err != nil {
		t.Fatalf("insert activity row: %v", err)
	}
	if string(activityDetails) != "{}" {
		t.Fatalf("activity details default = %s, want {}", activityDetails)
	}
	if activityActorType != "system" {
		t.Fatalf("activity actor_type default = %s, want system", activityActorType)
	}

	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversation_items
			(id, company_id, conversation_id, run_id, seq, type, author_kind,
			 author_id, content)
		VALUES
			($1, $2, $3, $4, 2, 'agent_text', 'agent', $5, 'ordinary one'),
			($6, $2, $3, $4, 3, 'agent_text', 'agent', $5, 'ordinary two')`,
		dbtest.NewUUID(t), fixture.CompanyA, fixture.ConversationA,
		fixture.RunA, fixture.AgentA, dbtest.NewUUID(t))
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runs
		SET status = 'completed',
			runtime_id = $2,
			updated_at = now(),
			session_id = 'session-a'
		WHERE id = $1`, fixture.RunA, fixture.RuntimeA)
	// The one-terminal-output seal is gone: a Run's answer is just its newest
	// 'agent_text', so nothing stops it writing another. What still refuses a
	// second copy is the provider replay fence on (run_id, item_key).
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversation_items
			(id, company_id, conversation_id, run_id, seq, type, author_kind,
			 author_id, item_key, content)
		VALUES ($1, $2, $3, $4, 4, 'agent_text', 'agent', $5, 'terminal-item',
			'terminal output')`,
		dbtest.NewUUID(t), fixture.CompanyA, fixture.ConversationA,
		fixture.RunA, fixture.AgentA)
	assertStatementRejected(t, ctx, pool, "23505", `
		INSERT INTO conversation_items
			(id, company_id, conversation_id, run_id, seq, type, author_kind,
			 author_id, item_key, content)
		VALUES ($1, $2, $3, $4, 5, 'agent_text', 'agent', $5, 'terminal-item',
			'replayed output')`,
		dbtest.NewUUID(t), fixture.CompanyA, fixture.ConversationA,
		fixture.RunA, fixture.AgentA)

	deleteTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin populated company cascade: %v", err)
	}
	defer func() { _ = deleteTx.Rollback(context.Background()) }()
	if _, err := deleteTx.Exec(ctx, `DELETE FROM companies WHERE id = $1`, fixture.CompanyA); err != nil {
		t.Fatalf("delete populated Company graph: %v", err)
	}
	if _, err := deleteTx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		t.Fatalf("validate populated company cascade: %v", err)
	}
}

func TestRunUsageSchemaEnforcesRawJSONEvidenceBounds(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}

	const insertUsage = `
		INSERT INTO run_usage (
			run_id, provider, model, input_tokens, output_tokens,
			cache_read_tokens, cache_write_tokens, raw_json
		) VALUES ($1, 'codex', 'gpt-test', 0, 0, 0, 0, $2)`
	// provider is the vocabulary that survives here. The raw_json shape and
	// size bounds, and the model-name length, are the usage service's to
	// enforce on the way in — they describe a payload, and a CHECK cannot
	// tell the caller which of them it broke.
	assertStatementRejected(
		t,
		ctx,
		pool,
		"23514",
		strings.Replace(insertUsage, "'codex'", "'unsupported'", 1),
		uuid.New(),
		[]byte(`{}`),
	)
}
