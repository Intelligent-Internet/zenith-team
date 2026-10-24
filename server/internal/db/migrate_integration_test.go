//go:build integration

package db

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"

	migratedb "github.com/golang-migrate/migrate/v4/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

var expectedBusinessTables = []string{
	"activity_events",
	"agent_runtime_bindings",
	"agent_skills",
	"agents",
	"analytics_budget_alerts",
	"analytics_budgets",
	"approvals",
	"artifact_conversation_grants",
	"artifact_grants",
	"artifact_review_decisions",
	"artifact_snapshot_items",
	"artifact_snapshots",
	"artifacts",
	"asset_versions",
	"assets",
	"attachments",
	"auth_sessions",
	"board_changes",
	"board_edges",
	"board_files",
	"board_items",
	"board_notes",
	"board_operations",
	"board_strokes",
	"boards",
	"calendar_attendees",
	"calendar_events",
	"calendar_occurrence_states",
	"calendar_reminder_states",
	"calendar_tool_receipts",
	"calendars",
	"canvas_edges",
	"canvas_items",
	"canvas_personal_views",
	"canvas_templates",
	"channel_invocations",
	"companies",
	"company_group_members",
	"company_groups",
	"company_initializations",
	"company_invitation_tokens",
	"company_invitations",
	"company_mailboxes",
	"company_members",
	"company_milestone_attestations",
	"company_reference_documents",
	"company_resources",
	"company_share_links",
	"connector_catalog_items",
	"connector_discovery_generations",
	"connector_policies",
	"connector_runtime_observations",
	"conversation_agents",
	"conversation_channels",
	"conversation_human_directs",
	"conversation_items",
	"conversation_members",
	"conversation_message_pins",
	"conversation_message_reactions",
	"conversation_model_changes",
	"conversation_model_settings",
	"conversation_run_queue_attachments",
	"conversation_run_queue_items",
	"conversation_studio_projects",
	"conversation_thread_states",
	"conversation_user_states",
	"conversations",
	"crash_reports",
	"deliverables",
	"delivery_snapshots",
	"desktop_handoff_codes",
	"discussion_artifacts",
	"document_changes",
	"document_snapshots",
	"document_writing_requests",
	"email_verification_flows",
	"huddle_ai_budget_reservations",
	"huddle_ai_budgets",
	"huddle_ai_operations",
	"huddle_ai_sessions",
	"huddle_ai_states",
	"huddle_ai_usage_receipts",
	"huddle_connections",
	"huddle_invitations",
	"huddle_notes",
	"huddle_notes_actions",
	"huddle_notes_intervals",
	"huddle_notes_policies",
	"huddle_notes_recap_revisions",
	"huddle_notes_segments",
	"huddle_transcripts",
	"huddle_work_proposals",
	"huddles",
	"inbox_items",
	"mailbox_draft_deliveries",
	"mailbox_draft_groups",
	"mailbox_drafts",
	"mailbox_message_attachments",
	"mailbox_messages",
	"mailbox_received_receipts",
	"message_attachments",
	"message_connector_bindings",
	"message_connectors",
	"message_skills",
	"model_prices",
	"oauth_accounts",
	"oauth_login_flows",
	"output_conversation_grants",
	"output_documents",
	"output_grants",
	"output_items",
	"output_publications",
	"output_review_operations",
	"output_studio_canvases",
	"outputs",
	"page_review_operations",
	"page_suggestion_reviews",
	"password_reset_flows",
	"plugin_catalog_items",
	"plugin_discovery_generations",
	"plugin_runtime_observations",
	"project_canvas_operations",
	"project_canvases",
	"project_file_assets",
	"project_files",
	"project_grants",
	"project_group_grants",
	"project_page_changes",
	"project_page_managed_blocks",
	"project_page_source_previews",
	"project_presentation_publications",
	"project_presentation_sources",
	"project_presentations",
	"projects",
	"questions",
	"retained_snapshots",
	"review_comment_revisions",
	"review_comments",
	"review_thread_events",
	"review_threads",
	"run_connector_selections",
	"run_connector_snapshots",
	"run_skills",
	"run_tokens",
	"run_usage",
	"run_wiki_reads",
	"run_wiki_writes",
	"runs",
	"runtimes",
	"schedule_firings",
	"schedule_sources",
	"schedules",
	"skill_imports",
	"skill_versions",
	"social_connections",
	"social_oauth_flows",
	"social_posts",
	"social_targets",
	"task_dependencies",
	"task_merge_jobs",
	"task_report_batches",
	"task_report_events",
	"task_result_items",
	"task_result_sets",
	"task_schedules",
	"tasks",
	"users",
	"wiki_artifact_refs",
	"wiki_block_ids",
	"wiki_canvas_checkpoints",
	"wiki_canvas_drafts",
	"wiki_collections",
	"wiki_favorites",
	"wiki_links",
	"wiki_pages",
}

// messages and run_messages were the same conversation stored twice, one copy
// for the chat page and one for the run transcript. conversation_items is the
// single stream that replaced them.
var removedBusinessTables = []string{
	"output_access_history",
	"goals",
	"attachment_storage_authority",
	"messages",
	"run_messages",
	"usage_backfill_state",
	"usage_counted_runs",
	"usage_hourly",
	"usage_run_model_memberships",
	"usage_runs_hourly",
	"usage_tasks_hourly",
}

// Every retained application table records when its row was created. Mutable
// projections also record their latest write; immutable event, link, token,
// and usage-fact rows deliberately do not.
var retainedTableHasUpdatedAt = map[string]bool{
	"artifact_grants":                   true,
	"artifact_conversation_grants":      false,
	"artifact_snapshot_items":           false,
	"delivery_snapshots":                false,
	"page_review_operations":            false,
	"project_page_source_previews":      true,
	"project_presentation_publications": false,
	"retained_snapshots":                false,
	"board_files":                       false,
	"boards":                            true,
	"board_items":                       true,
	"board_notes":                       true,
	"board_strokes":                     false,
	"board_edges":                       true,
	"board_operations":                  false,
	"board_changes":                     false,
	"page_suggestion_reviews":           false,
	"task_result_sets":                  false,
	"task_result_items":                 false,
	"analytics_budget_alerts":           false,
	"analytics_budgets":                 true,
	"wiki_collections":                  true,
	"wiki_pages":                        true,
	"document_snapshots":                false,
	"wiki_links":                        false,
	"wiki_artifact_refs":                false,
	"run_wiki_reads":                    false,
	"run_wiki_writes":                   false,
	"wiki_favorites":                    false,
	"huddle_ai_budget_reservations":     true,
	"huddle_ai_budgets":                 true,
	"huddle_ai_states":                  true,
	"huddle_notes":                      false,
	"huddle_notes_actions":              false,
	"huddle_notes_intervals":            false,
	"huddle_notes_policies":             false,
	"huddle_notes_recap_revisions":      false,
	"huddle_notes_segments":             false,
	"huddle_transcripts":                true,
	"huddle_ai_sessions":                true,
	"huddle_ai_usage_receipts":          true,
	"huddle_ai_operations":              false,
	"huddle_work_proposals":             true,

	"huddle_connections":           true,
	"huddle_invitations":           true,
	"huddles":                      true,
	"output_documents":             false,
	"canvas_templates":             false,
	"canvas_personal_views":        false,
	"wiki_block_ids":               false,
	"project_canvases":             false,
	"artifacts":                    true,
	"project_file_assets":          false,
	"artifact_snapshots":           false,
	"canvas_items":                 false,
	"canvas_edges":                 false,
	"review_threads":               false,
	"review_comments":              false,
	"review_comment_revisions":     false,
	"review_thread_events":         false,
	"artifact_review_decisions":    false,
	"project_canvas_operations":    false,
	"document_changes":             false,
	"document_writing_requests":    false,
	"wiki_canvas_drafts":           true,
	"wiki_canvas_checkpoints":      false,
	"project_page_changes":         false,
	"project_page_managed_blocks":  false,
	"project_presentation_sources": false,
	"project_presentations":        false,

	"channel_invocations":                true,
	"calendars":                          false,
	"calendar_attendees":                 true,
	"calendar_reminder_states":           true,
	"calendar_tool_receipts":             false,
	"calendar_events":                    true,
	"calendar_occurrence_states":         true,
	"social_connections":                 true,
	"social_oauth_flows":                 false,
	"social_posts":                       true,
	"social_targets":                     false,
	"company_invitation_tokens":          true,
	"company_invitations":                true,
	"company_share_links":                true,
	"projects":                           true,
	"project_files":                      false,
	"project_grants":                     true,
	"activity_events":                    false,
	"agent_skills":                       true,
	"run_skills":                         false,
	"message_skills":                     false,
	"message_connectors":                 false,
	"message_connector_bindings":         false,
	"skill_imports":                      false,
	"skill_versions":                     false,
	"agent_runtime_bindings":             true,
	"agents":                             true,
	"approvals":                          true,
	"asset_versions":                     false,
	"assets":                             true,
	"attachments":                        true,
	"auth_sessions":                      true,
	"company_initializations":            true,
	"companies":                          true,
	"company_mailboxes":                  true,
	"company_members":                    true,
	"company_milestone_attestations":     false,
	"company_reference_documents":        false,
	"company_resources":                  true,
	"connector_catalog_items":            true,
	"connector_discovery_generations":    true,
	"connector_policies":                 true,
	"connector_runtime_observations":     true,
	"conversation_items":                 false,
	"conversation_message_pins":          false,
	"conversation_message_reactions":     false,
	"conversation_model_settings":        true,
	"conversation_model_changes":         false,
	"conversation_thread_states":         true,
	"conversation_human_directs":         false,
	"conversation_channels":              false,
	"conversation_members":               false,
	"conversation_agents":                false,
	"conversation_studio_projects":       false,
	"conversation_run_queue_attachments": true,
	"conversation_run_queue_items":       true,
	"conversations":                      true,
	"conversation_user_states":           true,
	"crash_reports":                      false,
	"deliverables":                       true,
	"desktop_handoff_codes":              false,
	"email_verification_flows":           true,
	"inbox_items":                        true,
	"mailbox_draft_deliveries":           false,
	"mailbox_draft_groups":               true,
	"mailbox_drafts":                     true,
	"mailbox_message_attachments":        false,
	"mailbox_messages":                   true,
	"mailbox_received_receipts":          false,
	"message_attachments":                false,
	"model_prices":                       false,
	"oauth_accounts":                     true,
	"oauth_login_flows":                  false,
	"output_conversation_grants":         false,
	"discussion_artifacts":               false,
	"output_grants":                      true,
	"output_items":                       false,
	"output_publications":                true,
	"output_review_operations":           false,
	"output_studio_canvases":             false,
	"outputs":                            false,
	"password_reset_flows":               true,
	"plugin_catalog_items":               true,
	"plugin_discovery_generations":       true,
	"plugin_runtime_observations":        true,
	"questions":                          true,
	"run_connector_selections":           false,
	"run_connector_snapshots":            true,
	"run_tokens":                         false,
	"run_usage":                          false,
	"runs":                               true,
	"runtimes":                           true,
	"schedule_firings":                   true,
	"schedule_sources":                   false,
	"schedules":                          true,
	"task_dependencies":                  false,
	"task_merge_jobs":                    true,
	"task_report_batches":                false,
	"task_report_events":                 false,
	"task_schedules":                     true,
	"tasks":                              true,
	"users":                              true,

	// A Group, its members (a membership is added or deleted, never
	// changed) and its grants.
	"company_groups":        true,
	"company_group_members": false,
	"project_group_grants":  true,
}

func TestMigrationUpBuildsTheSchemaAndDownRemovesIt(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	assertBusinessTables(t, ctx, pool, expectedBusinessTables)
	t.Run("removed tables", func(t *testing.T) {
		for _, table := range removedBusinessTables {
			t.Run(table, func(t *testing.T) {
				assertTableDoesNotExist(t, ctx, pool, table)
			})
		}
	})
	assertRequiredIndexes(t, ctx, pool, []string{
		"wiki_artifact_refs_output_idx",
		"company_invitations_pending_email_idx",
		"company_invitations_company_idx",
		"company_invitation_delivery_idx",
		"company_members_active_user_idx",
		"crash_reports_retention_idx",
		"activity_events_company_created_idx",
		"activity_events_entity_created_idx",
		"activity_events_agent_created_idx",
		"inbox_items_recipient_conversation_unread_idx",
		"email_verification_flows_email_ci_key",
		"email_verification_flows_expiry_idx",
		"email_verification_flows_user_id_key",
		// The five conversation_items indexes: the two replay fences, the
		// timeline page, one run's transcript, and search.
		"conversation_items_run_item_key_idx",
		"conversation_items_request_key_idx",
		"conversation_items_timeline_idx",
		"conversation_items_run_seq_idx",
		"conversation_items_content_search_idx",
		// Wiki search reads the page row's current-content vector.
		"wiki_pages_search_idx",
		"oauth_login_flows_expiry_idx",
		"company_resources_company_daemon_key",
		"company_resources_tenant_order_idx",
		"runs_claim_candidates_idx",
		// runs_pending_idx and runs_conversation_pending_idx were deleted as
		// redundant; these two are the survivors their queries fell back to, so
		// pin them or the fallback disappears unnoticed.
		"runs_agent_status_idx",
		"runs_conversation_live_idx",
		"runs_dispatched_prepare_idx",
		"runs_deferred_fire_idx",
		"runs_one_pending_per_task_agent_idx",
		"runs_conversation_created_idx",
		"runs_parent_retry_idx",
		"runs_company_created_idx",
		"run_tokens_run_idx",
		"run_tokens_expiry_idx",
		"approvals_company_created_idx",
		"approvals_company_task_created_idx",
		"approvals_pending_agent_idx",
		"approvals_expiry_idx",
		"approvals_continuation_run_key",
		"approvals_continuation_pending_idx",
		"questions_company_created_idx",
		"questions_company_task_created_idx",
		"questions_company_origin_user_created_idx",
		"questions_expiry_idx",
		"questions_continuation_run_key",
		"questions_continuation_pending_idx",
		"runtimes_company_status_idx",
		"runtimes_last_seen_idx",
		"tasks_milestone_idx",
		"task_dependencies_prerequisite_idx",
		"connector_catalog_items_company_provider_status_idx",
		"connector_runtime_observations_company_provider_idx",
		"plugin_runtime_observations_company_provider_runtime_idx",
		// The Plugins list's duplicate probe and logo borrowing.
		"plugin_runtime_observations_company_provider_name_key_idx",
		"plugin_runtime_observations_company_provider_website_host_idx",
		"plugin_runtime_observations_company_provider_developer_idx",
		"schedules_due_idx",
		"schedule_firings_schedule_history_idx",
		"task_report_events_pending_recipient_idx",
		"task_report_events_batch_idx",
		"task_report_batches_manual_request_key",
		"task_report_batches_proactive_request_key",
		"task_merge_jobs_due_idx",
		"task_merge_jobs_company_task_created_idx",
		"mailbox_messages_company_list_idx",
		"mailbox_messages_retention_idx",
		"mailbox_drafts_send_queue_idx",
		"assets_company_created_idx",
		"asset_versions_company_asset_version_idx",
		"asset_versions_source_run_idx",
		"outputs_company_created_idx",
		"outputs_owner_created_idx",
		"outputs_company_visible_created_idx",
		"output_grants_recipient_idx",
		"outputs_run_presentation_fingerprint_key",
		"outputs_run_presentation_number_key",
		"output_items_asset_version_idx",
		"output_items_one_primary_idx",
		"output_publications_company_created_idx",
		"output_publications_work_idx",
		"deliverables_company_status_submitted_idx",
		"project_grants_user_idx",
		// A Group name is unique per Company ignoring case; every
		// access read probes a person's Groups, and a Group change lists the
		// Projects a Group reaches.
		"company_groups_name_key",
		"company_group_members_user_idx",
		"project_group_grants_group_idx",
		"conversations_project_idx",
		"inbox_items_recipient_project_idx",
		"activity_events_project_created_idx",
		// Collection deletes unfile revisions and drafts through these.
		"wiki_revisions_collection_idx",
		"wiki_canvas_drafts_collection_idx",
		"wiki_canvas_checkpoints_page_seq_idx",
		"run_wiki_writes_company_created_idx",
	})
	t.Run("retained table timestamps", func(t *testing.T) {
		if len(retainedTableHasUpdatedAt) != len(expectedBusinessTables) {
			t.Fatalf(
				"timestamp policy classifies %d tables, want %d",
				len(retainedTableHasUpdatedAt),
				len(expectedBusinessTables),
			)
		}
		for _, table := range expectedBusinessTables {
			wantUpdatedAt, classified := retainedTableHasUpdatedAt[table]
			if !classified {
				t.Errorf("retained table %s has no timestamp policy classification", table)
				continue
			}
			t.Run(table, func(t *testing.T) {
				assertGenericTimestampColumns(t, ctx, pool, table, wantUpdatedAt)
			})
		}
	})
	// There is deliberately no visibility column here: what a reader sees is
	// derived from type, so no writer can set a flag wrong and leak an agent's
	// tool output into the user's chat.
	t.Run("conversation item columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "conversation_items", []string{
			"id", "company_id", "conversation_id", "run_id", "seq", "type",
			"author_kind", "author_id", "item_key", "request_key", "content",
			"parts", "content_sha256", "metadata", "created_at", "body", "revision", "send_fingerprint",
			"thread_root_id", "edited_at", "deleted_at",
			// A channel answer's invocation, a column so SQL never reads metadata.
			"invocation_id",
		})
	})
	t.Run("inbox item columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "inbox_items", []string{
			"id", "company_id", "recipient_user_id", "conversation_id", "kind",
			"entity_type", "entity_id", "title", "body", "deep_link", "dedupe_key",
			"discussion_grant_generation", "read_at", "archived_at", "created_at", "updated_at", "version",
			// The Task chat's Project, so a non-reader never sees the row.
			"project_id",
		})
	})
	t.Run("approval columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "approvals", []string{
			"id", "company_id", "type", "status", "rule_key", "title", "summary",
			"preview_markdown", "presentation", "requested_by_agent_id", "origin_run_id", "request_key",
			"task_id", "conversation_id", "origin_user_id",
			"canonical_action", "request_sha256", "action_sha256", "expires_at", "decision_note",
			"decided_by_user_id", "execution_status", "execution_result",
			"skipped_reason", "continuation_run_id", "continuation_prompt",
			"continuation_status", "continuation_next_attempt_at",
			"continuation_abandon_reason", "continuation_attempts", "created_at",
			"updated_at", "version",
		})
	})
	t.Run("question columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "questions", []string{
			"id", "company_id", "status", "title", "asked_by_agent_id", "origin_run_id",
			"request_key", "task_id", "conversation_id", "origin_user_id", "prompts",
			"request_sha256", "expires_at", "answers", "resolved_by_user_id",
			"continuation_run_id", "continuation_prompt",
			"continuation_status", "continuation_next_attempt_at",
			"continuation_abandon_reason", "continuation_attempts", "created_at",
			"updated_at", "version",
		})
	})
	t.Run("company onboarding columns", func(t *testing.T) {
		assertTableOmitsColumns(t, ctx, pool, "companies", []string{
			"first_goal", "company_plan_activated_at", "delete_attempts",
			"delete_next_attempt_at", "delete_last_error",
		})
	})
	t.Run("removed public sharing policy columns", func(t *testing.T) {
		assertTableOmitsColumns(t, ctx, pool, "companies", []string{
			"artifact_public_enabled", "artifact_policy_version",
		})
	})
	t.Run("company proactive mode columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "companies", []string{
			"id",
			"name",
			"slug",
			"task_prefix",
			"next_task_number",
			"deleting_at",
			"created_at",
			"updated_at",
			"version",
			"business_context",
			"contact_email",
			"phone_country_iso2",
			"phone_number",
			"proactive_mode_enabled",
			"proactive_mode_version",
			"proactive_mode_enabled_by_user_id",
			"proactive_mode_conversation_id",
			"agent_setup_completed_at",
			"first_plan_applied_at",
			"website_url",
			"avatar_attachment_id",
		})
	})
	t.Run("attachment storage columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "attachments", []string{
			"id", "company_id", "uploaded_by_user_id", "request_key", "object_key", "filename",
			"content_type", "size_bytes", "status", "upload_lease_expires_at",
			"created_at", "updated_at", "version",
		})
	})
	t.Run("Run result and Deliverable columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "assets", []string{
			"id", "company_id", "title", "media_class", "status",
			"created_by_user_id", "created_at", "updated_at", "version",
		})
		assertTableColumns(t, ctx, pool, "asset_versions", []string{
			"id", "company_id", "asset_id", "version_number", "attachment_id",
			"source_run_id", "filename", "content_type", "size_bytes",
			"integrity_status", "created_at", "source_type", "created_by_user_id",
		})
		assertTableColumns(t, ctx, pool, "outputs", []string{
			"id",
			"company_id",
			"run_id",
			"title",
			"summary",
			"submission_notes",
			"presentation_fingerprint",
			"created_at",
			"type",
			"preview_url",
			"payload",
			"presentation_number",
			"owner_user_id",
			"access_version",
			"visibility",
			"public_token",
			"conversation_id",
			"task_id",
			"project_id",
			"task_execution_generation",
			"actor_type",
			"actor_user_id",
			"actor_agent_id",
			"source_run_id",
			"on_behalf_of_user_id",
			"source_run_completed",
		})
		assertTableColumns(t, ctx, pool, "output_grants", []string{
			"company_id", "output_id", "user_id", "permission", "granted_by_user_id",
			"created_at", "updated_at",
		})
		assertTableColumns(t, ctx, pool, "output_items", []string{
			"id", "company_id", "output_id", "ordinal", "role", "label",
			"asset_version_id", "created_at",
		})
		assertTableColumns(t, ctx, pool, "deliverables", []string{
			"id", "company_id", "output_id", "status", "submitted_at",
			"reviewed_by_user_id", "reviewed_at", "review_feedback", "created_at",
			"updated_at", "version",
		})
		assertTableColumns(t, ctx, pool, "output_publications", []string{
			"custom_hostname", "route_binding_id", "route_pending",
			"id", "company_id", "output_id", "output_item_id", "requested_by_user_id",
			"status", "service_name", "provider_type", "provider_binding_id",
			"provider_resources_may_exist", "source_ref", "source_template", "build_ref",
			"artifact_ref", "artifact_digest", "service_ref", "public_url", "failure_code",
			"failure_detail", "attempt_count", "next_attempt_at", "lease_id", "lease_expires_at",
			"published_at", "unpublished_at", "created_at", "updated_at", "version",
		})
	})
	t.Run("run usage columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "run_usage", []string{
			"run_id", "provider", "model", "input_tokens", "output_tokens",
			"cache_read_tokens", "cache_write_tokens", "usage_basis",
			"provider_cost_usd", "raw_json", "created_at", "reasoning_tokens", "turns",
			"provider_duration_ms", "billing_mode",
		})
		assertJSONBObjectColumn(t, ctx, pool, "run_usage", "raw_json")
	})
	t.Run("agent columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "agents", []string{
			"id", "company_id", "name", "title", "instructions",
			"reports_to_agent_id", "model", "thinking_level",
			// The task-lane concurrency cap the claim path reads under the
			// agent-row lock; DEFAULT 3, bounds 1..10 owned by the binder.
			"max_concurrent_runs",
			"settings", "archived_at", "created_at", "updated_at", "version",
			// The Agent's own appearance: the client renders initials and its
			// default palette when either is NULL.
			"avatar_key", "accent_color",
			// The member who created the Agent; with the agents.manage
			// holders, the one who may configure it (ADR 0013).
			"created_by_user_id",
			// The Plugins the Agent turned off, the column its Run brief filters on.
			"disabled_plugin_ids",
			// The Agent is a template with a fixed provider, and no
			// machine of its own — a member binds it to one of theirs
			// (agent_runtime_bindings, ADR 0014).
			"provider",
		})
	})
	t.Run("agent binding columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "agent_runtime_bindings", []string{
			"id", "company_id", "agent_id", "user_id", "runtime_id",
			// provider is carried, not derived: the composite foreign keys
			// to agents and runtimes make a cross-provider binding
			// unrepresentable rather than merely refused in Go.
			"provider",
			"created_at", "updated_at",
		})
	})
	t.Run("run columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "runs", []string{
			"id", "company_id", "agent_id", "task_id", "run_type", "revision", "invocation_id",
			// No lane column: the capacity lane is the run's conversation
			// kind, which survives the task delete that clears task_id.
			"conversation_id", "runtime_id",
			// No per-transition clocks either. updated_at is when the run
			// entered its current status; the history is the activity
			// stream's.
			"status", "priority", "created_at", "updated_at",
			"error", "failure_reason", "context",
			"session_id", "session_rollout_missing", "retired_session_id",
			"attempt", "parent_run_id", "force_fresh_session",
			"wait_reason", "prepare_lease_expires_at", "fire_at", "task_scheduled",
			"task_execution_generation",
			// The Connector scope; its rows live in run_connector_selections
			// and run_connector_snapshots.
			"connector_selection_mode", "connector_snapshot_frozen_at", "schedule_firing_id",
			"transcript_through_seq",
			"queued_at", "claimed_at", "started_at", "finished_at",
			// The member whose permitted action asked for the Run,
			// rechecked by the claim against the runtime's current consent.
			"execution_authorized_by_user_id",
			// Facts SQL filters on, frozen with the Run: its source
			// Task, its proven final answer item and its Studio access.
			"source_task_id", "final_answer_item_key", "studio_enabled",
		})
	})
	t.Run("runtime columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "runtimes", []string{
			// The daemon-registered row.
			"id", "company_id", "daemon_id", "name", "provider", "status",
			"device_info", "metadata", "last_seen_at", "owner_user_id",
			"created_at", "updated_at",
			// The user-facing lifecycle. Typed unusable-reason columns
			// (not metadata keys), the user's machine name, and the owner's
			// soft removal — NULL means live, and registration clears it.
			"unusable_reason_code", "unusable_reason_message", "custom_name",
			"removed_at",
			// The provider sign-in verdict.
			"provider_sign_in_state", "provider_sign_in_detail", "provider_sign_in_checked_at",
			// The announced device name, out of metadata because SQL reads it.
			"device_name",
			// No visibility column: a runtime is always private, and
			// only its registrant can see or use it (ADR 0014).
			// When another person took the machine over.
			"handed_over_at",
		})
	})
	t.Run("approval mode columns are absent", func(t *testing.T) {
		assertTableOmitsColumns(t, ctx, pool, "company_members", []string{
			"approval_mode", "approval_mode_version",
		})
		assertTableOmitsColumns(t, ctx, pool, "conversations", []string{"approval_mode"})
		assertTableOmitsColumns(t, ctx, pool, "runs", []string{"approval_mode"})
	})
	t.Run("crash report timestamps", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "crash_reports", []string{
			"id", "kind", "platform", "architecture", "app_version", "reason", "created_at",
		})
	})
	t.Run("task report event columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "task_report_events", []string{
			"id", "company_id", "task_id", "task_version", "task_key", "task_title",
			"source_agent_id", "recipient_agent_id", "completion_source",
			"completed_at", "result_summary", "source_run_id", "request_key",
			"batch_id", "superseded_at", "created_at",
			// The reported Task's Project (ADR 0017).
			"project_id",
		})
	})
	t.Run("task report batch columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "task_report_batches", []string{
			"id", "company_id", "recipient_agent_id", "conversation_id",
			"confirmed_by_user_id", "delivery_type", "automation_user_id", "request_key",
			"message_id", "run_id", "event_count", "created_at",
		})
	})
	t.Run("task merge job columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "task_merge_jobs", []string{
			"id", "company_id", "task_id", "run_id", "task_execution_generation",
			"authority_user_id", "request_type", "status", "attempt_count", "next_attempt_at",
			"lease_expires_at", "result_code", "merged_commit", "created_at",
			"updated_at", "completed_at",
		})
	})
	t.Run("auth session columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "auth_sessions", []string{
			"id", "user_id", "token_prefix", "token_hash", "expires_at", "last_used_at",
			"revoked_at", "revocation_reason", "revoked_by_user_id", "rotated_from_session_id",
			"client_metadata", "created_at", "updated_at", "rotation_id",
			// A session is a person's or a daemon's.
			"client",
		})
	})
	t.Run("activity columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "activity_events", []string{
			"id", "company_id", "actor_type", "actor_id", "action", "entity_type", "entity_id",
			"agent_id", "task_id", "conversation_id", "details", "created_at",
			"revision", "visibility", "visibility_user_id", "schema_version", "project_id",
			// An Agent's row in a Run names the person it
			// acted for and the Run.
			"on_behalf_of_user_id", "run_id",
		})
	})
	t.Run("task columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "tasks", []string{
			"id",
			"company_id",
			"project_id",
			"conversation_id",
			"number",
			"title",
			"description",
			// The assignee is always a member and never NULL; the
			// Agent that does the work is executed_by_agent_id, NULL when
			// the assignee does it themself (ADR 0014).
			"status",
			"priority",
			"assignee_user_id",
			"executed_by_agent_id",
			"creator_user_id",
			"creator_agent_id",
			"created_at",
			"updated_at",
			"completed_at",
			"version",
			"milestone_type",
			"execution_generation",
			"key",
			"result_proposal_only",
			"result_review_required",
			"labels",
			"start_date",
			"due_date",
			"assignment_status",
			"scheduled_at",
			"schedule_fired_at",
			"task_schedule_id",
			"task_schedule_generation",
			"schedule_occurrence_at",
			"schedule_disposition",
			// The workspace identity a confirmed handoff advances, so the
			// next assignee's daemon prepares a fresh root.
			"workspace_generation",
		})
	})
	// The baseline rolls all the way back and leaves no business table.
	if err := MigrateDown(ctx, pool); err != nil {
		t.Fatalf("MigrateDown() error = %v", err)
	}
	assertBusinessTables(t, ctx, pool, nil)
}

func TestMigrationUpSerializesConcurrentReplicas(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	const replicas = 8
	errorsByReplica := make(chan error, replicas)
	var wg sync.WaitGroup
	for range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errorsByReplica <- MigrateUp(ctx, pool)
		}()
	}
	wg.Wait()
	close(errorsByReplica)
	for err := range errorsByReplica {
		if err != nil {
			t.Errorf("concurrent MigrateUp() error = %v", err)
		}
	}
	assertBusinessTables(t, ctx, pool, expectedBusinessTables)
}

func TestMigrationUpWaitsForDriverAdvisoryLock(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	holder, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer holder.Release()
	lockKey := migrationLockKey(t, pool)
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		t.Fatalf("acquire advisory lock: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- MigrateUp(ctx, pool) }()
	select {
	case err := <-done:
		t.Fatalf("MigrateUp() returned while advisory lock was held: %v", err)
	case <-time.After(250 * time.Millisecond):
	}

	if _, err := holder.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockKey); err != nil {
		t.Fatalf("release advisory lock: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("MigrateUp() after lock release error = %v", err)
	}
}

func TestMigrationUpBoundsDriverAdvisoryLockWait(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	holderCtx, holderCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer holderCancel()
	holder, err := pool.Acquire(holderCtx)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer holder.Release()
	lockKey := migrationLockKey(t, pool)
	if _, err := holder.Exec(holderCtx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		t.Fatalf("acquire advisory lock: %v", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := holder.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", lockKey); err != nil {
			t.Errorf("release advisory lock: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := MigrateUp(ctx, pool); err == nil {
		t.Fatal("MigrateUp() error = nil while driver advisory lock remained held")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("MigrateUp() advisory lock wait = %s, want bounded by context deadline", elapsed)
	}
}

func migrationLockKey(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	lockID, err := migratedb.GenerateAdvisoryLockId(
		pool.Config().ConnConfig.Database,
		"public",
		"schema_migrations",
	)
	if err != nil {
		t.Fatalf("GenerateAdvisoryLockId() error = %v", err)
	}
	lockKey, err := strconv.ParseInt(lockID, 10, 64)
	if err != nil {
		t.Fatalf("parse advisory lock ID: %v", err)
	}
	return lockKey
}

func assertBusinessTables(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want []string) {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT tablename
		FROM pg_catalog.pg_tables
		WHERE schemaname = 'public' AND tablename <> 'schema_migrations'
		ORDER BY tablename`)
	if err != nil {
		t.Fatalf("list business tables: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("scan business table: %v", err)
		}
		got = append(got, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate business tables: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("business tables = %v, want %v", got, want)
	}
}

func assertRequiredIndexes(t *testing.T, ctx context.Context, pool *pgxpool.Pool, names []string) {
	t.Helper()
	for _, name := range names {
		var exists bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_catalog.pg_indexes
				WHERE schemaname = 'public' AND indexname = $1
			)`, name).Scan(&exists)
		if err != nil {
			t.Fatalf("check index %s: %v", name, err)
		}
		if !exists {
			t.Errorf("required index %s does not exist", name)
		}
	}
}

func assertTableColumns(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	table string,
	want []string,
) {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1
		ORDER BY ordinal_position`, table)
	if err != nil {
		t.Fatalf("list %s columns: %v", table, err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatalf("scan %s column: %v", table, err)
		}
		got = append(got, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s columns: %v", table, err)
	}
	got = slices.Clone(got)
	want = slices.Clone(want)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("%s columns = %v, want %v", table, got, want)
	}
}

func assertTableOmitsColumns(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	table string,
	columns []string,
) {
	t.Helper()
	for _, column := range columns {
		var exists bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2
			)`, table, column).Scan(&exists); err != nil {
			t.Fatalf("check %s.%s: %v", table, column, err)
		}
		if exists {
			t.Errorf("obsolete column %s.%s still exists", table, column)
		}
	}
}

func assertTableDoesNotExist(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	table string,
) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_catalog.pg_tables
			WHERE schemaname = 'public' AND tablename = $1
		)`, table).Scan(&exists); err != nil {
		t.Fatalf("check removed table %s: %v", table, err)
	}
	if exists {
		t.Errorf("obsolete table %s still exists", table)
	}
}

func assertGenericTimestampColumns(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	table string,
	wantUpdatedAt bool,
) {
	t.Helper()
	var hasCreatedAt, hasUpdatedAt bool
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE column_name = 'created_at') = 1,
			count(*) FILTER (WHERE column_name = 'updated_at') = 1
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1
		  AND column_name IN ('created_at', 'updated_at')`, table).
		Scan(&hasCreatedAt, &hasUpdatedAt); err != nil {
		t.Fatalf("read %s generic timestamp columns: %v", table, err)
	}
	if !hasCreatedAt {
		t.Errorf("retained table %s has no created_at column", table)
	}
	if hasUpdatedAt != wantUpdatedAt {
		t.Errorf("retained table %s updated_at presence = %t, want %t", table, hasUpdatedAt, wantUpdatedAt)
	}
}

func assertJSONBObjectColumn(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	table string,
	column string,
) {
	t.Helper()
	var dataType string
	var nullable string
	if err := pool.QueryRow(ctx, `
		SELECT data_type, is_nullable
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2`, table, column).
		Scan(&dataType, &nullable); err != nil {
		t.Fatalf("read %s.%s definition: %v", table, column, err)
	}
	if dataType != "jsonb" || nullable != "NO" {
		t.Errorf(
			"%s.%s definition = type %s nullable %s, want jsonb NOT NULL",
			table,
			column,
			dataType,
			nullable,
		)
	}
}

// The readiness fence derives its expected schema version from the embedded
// migration set; this proves a fresh MigrateUp actually lands on it, so a
// new migration can never leave /readyz permanently unready again.
func TestMigrateUpMatchesReadinessSchemaVersion(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	var version int64
	var dirty bool
	if err := pool.QueryRow(
		ctx,
		"SELECT version, dirty FROM schema_migrations",
	).Scan(&version, &dirty); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if dirty {
		t.Fatal("fresh migration set is dirty")
	}
	if version != SchemaVersion {
		t.Fatalf(
			"migrated version %d != SchemaVersion %d",
			version, SchemaVersion,
		)
	}
}
