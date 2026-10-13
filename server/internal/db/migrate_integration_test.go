//go:build integration

package db

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/testutil/dbtest"

	"github.com/golang-migrate/migrate/v4"
	migratedb "github.com/golang-migrate/migrate/v4/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	"artifact_review_decisions",
	"asset_versions",
	"assets",
	"attachments",
	"auth_sessions",
	"board_agent_access",
	"board_changes",
	"board_create_receipts",
	"board_edges",
	"board_favorites",
	"board_files",
	"board_items",
	"board_member_access",
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
	"commerce_accounts",
	"commerce_customers",
	"commerce_entitlements",
	"commerce_gateway_operations",
	"commerce_manifests",
	"commerce_offers",
	"commerce_operations",
	"commerce_payments",
	"commerce_rate_limits",
	"commerce_site_challenges",
	"commerce_site_signing_keys",
	"commerce_sites",
	"commerce_subscriptions",
	"commerce_terms_acceptances",
	"commerce_terms_catalog",
	"commerce_webhook_account_leases",
	"commerce_webhook_receipts",
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
	"deliverable_review_actions",
	"deliverables",
	"desktop_handoff_codes",
	"discussion_artifacts",
	"document_changes",
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
	"page_suggestion_reviews",
	"password_reset_flows",
	"plugin_catalog_items",
	"plugin_discovery_generations",
	"plugin_runtime_observations",
	"project_artifact_versions",
	"project_artifacts",
	"project_canvas_operations",
	"project_canvases",
	"project_file_assets",
	"project_files",
	"project_grants",
	"project_group_grants",
	"project_page_changes",
	"project_page_managed_blocks",
	"project_presentation_sources",
	"project_presentations",
	"projects",
	"questions",
	"review_comment_revisions",
	"review_comments",
	"review_thread_events",
	"review_threads",
	"run_connector_selections",
	"run_connector_snapshots",
	"run_identity_mappings",
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
	"wiki_revisions",
}

// messages and run_messages were the same conversation stored twice, one copy
// for the chat page and one for the run transcript. conversation_items is the
// single stream that replaced them.
var removedBusinessTables = []string{
	"output_access_history",
	"goals",
	"attachment_storage_authority",
	"commerce_sandbox_authorization_codes",
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
	"board_agent_access":            false,
	"board_files":                   false,
	"boards":                        true,
	"board_items":                   true,
	"board_notes":                   true,
	"board_strokes":                 false,
	"board_edges":                   true,
	"board_operations":              false,
	"board_changes":                 false,
	"board_create_receipts":         false,
	"board_favorites":               false,
	"page_suggestion_reviews":       false,
	"task_result_sets":              false,
	"task_result_items":             false,
	"analytics_budget_alerts":       false,
	"analytics_budgets":             true,
	"wiki_collections":              true,
	"wiki_pages":                    true,
	"wiki_revisions":                false,
	"wiki_links":                    false,
	"wiki_artifact_refs":            false,
	"run_wiki_reads":                false,
	"run_wiki_writes":               false,
	"wiki_favorites":                false,
	"huddle_ai_budget_reservations": true,
	"huddle_ai_budgets":             true,
	"huddle_ai_states":              true,
	"huddle_transcripts":            true,
	"huddle_ai_sessions":            true,
	"huddle_ai_usage_receipts":      true,
	"huddle_ai_operations":          false,
	"huddle_work_proposals":         true,

	"huddle_connections":           true,
	"huddle_invitations":           true,
	"huddles":                      true,
	"output_documents":             false,
	"canvas_templates":             false,
	"canvas_personal_views":        false,
	"wiki_block_ids":               false,
	"project_canvases":             false,
	"board_member_access":          false,
	"project_artifacts":            true,
	"project_file_assets":          false,
	"project_artifact_versions":    false,
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
	"run_identity_mappings":              false,
	"activity_events":                    false,
	"agent_skills":                       true,
	"run_skills":                         false,
	"skill_imports":                      false,
	"skill_versions":                     false,
	"agent_runtime_bindings":             true,
	"agents":                             true,
	"approvals":                          true,
	"asset_versions":                     false,
	"assets":                             true,
	"attachments":                        true,
	"auth_sessions":                      true,
	"commerce_accounts":                  true,
	"commerce_customers":                 true,
	"commerce_entitlements":              true,
	"commerce_gateway_operations":        true,
	"commerce_manifests":                 true,
	"commerce_offers":                    true,
	"commerce_operations":                true,
	"commerce_payments":                  true,
	"commerce_rate_limits":               true,
	"commerce_site_challenges":           false,
	"commerce_site_signing_keys":         true,
	"commerce_sites":                     true,
	"commerce_subscriptions":             true,
	"commerce_terms_acceptances":         false,
	"commerce_terms_catalog":             false,
	"commerce_webhook_account_leases":    true,
	"commerce_webhook_receipts":          true,
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
	"deliverable_review_actions":         true,
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

	// 000101: a Group, its members (a membership is added or deleted, never
	// changed) and its grants.
	"company_groups":        true,
	"company_group_members": false,
	"project_group_grants":  true,
}

func TestMigrationUpAndCutoverDowngradeRefusal(t *testing.T) {
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
		"commerce_webhook_account_leases_expiry_idx",
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
		// 000063: the Plugins list's duplicate probe and logo borrowing (D26).
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
		"commerce_rate_limits_updated_idx",
		"commerce_site_challenges_expiry_idx",
		"commerce_site_challenges_consumed_idx",
		"commerce_site_signing_keys_verification_idx",
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
		"deliverable_review_actions_company_status_idx",
		"project_grants_user_idx",
		// 000101: a Group name is unique per Company ignoring case; every
		// access read probes a person's Groups, and a Group change lists the
		// Projects a Group reaches.
		"company_groups_name_key",
		"company_group_members_user_idx",
		"project_group_grants_group_idx",
		"conversations_project_idx",
		"inbox_items_recipient_project_idx",
		"activity_events_project_created_idx",
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
			// 000053: a channel answer's invocation, a column so SQL never reads metadata.
			"invocation_id",
		})
	})
	t.Run("inbox item columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "inbox_items", []string{
			"id", "company_id", "recipient_user_id", "conversation_id", "kind",
			"entity_type", "entity_id", "title", "body", "deep_link", "dedupe_key",
			"discussion_grant_generation", "read_at", "archived_at", "created_at", "updated_at", "version",
			// 000086: the Task chat's Project, so a non-reader never sees the row.
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
			"updated_at", "version", "commerce_manifest_id",
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
		assertTableColumns(t, ctx, pool, "deliverable_review_actions", []string{
			"id", "company_id", "deliverable_id", "inbox_item_id",
			"recipient_user_id", "status", "resolved_at", "created_at",
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
			// 000029: the member who created the Agent; with the agents.manage
			// holders, the one who may configure it (ADR 0013).
			"created_by_user_id",
			// 000052: the Plugins the Agent turned off, the column its Run brief filters on.
			"disabled_plugin_ids",
			// 000059: the Agent is a template with a fixed provider, and no
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
			// 000054: the Connector scope; its rows live in run_connector_selections
			// and run_connector_snapshots.
			"connector_selection_mode", "connector_snapshot_frozen_at", "schedule_firing_id",
			"transcript_through_seq",
			"queued_at", "claimed_at", "started_at", "finished_at",
			// 000026: the member whose permitted action asked for the Run,
			// rechecked by the claim against the runtime's current consent.
			"execution_authorized_by_user_id",
			// 000050-000052: facts SQL filters on, frozen with the Run: its source
			// Task, its proven final answer item and its Studio access.
			"source_task_id", "final_answer_item_key", "studio_enabled",
		})
	})
	t.Run("runtime columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "runtimes", []string{
			// 000005: the daemon-registered row.
			"id", "company_id", "daemon_id", "name", "provider", "status",
			"device_info", "metadata", "last_seen_at", "owner_user_id",
			"created_at", "updated_at",
			// 000012: the user-facing lifecycle. Typed unusable-reason columns
			// (not metadata keys), the user's machine name, and the owner's
			// soft removal — NULL means live, and registration clears it.
			"unusable_reason_code", "unusable_reason_message", "custom_name",
			"removed_at",
			// 000022: the provider sign-in verdict.
			"provider_sign_in_state", "provider_sign_in_detail", "provider_sign_in_checked_at",
			// 000049: the announced device name, out of metadata because SQL reads it.
			"device_name",
			// 000059 dropped visibility: a runtime is always private, and
			// only its registrant can see or use it (ADR 0014).
			// 000103: when another person took the machine over (D33).
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
			// 000086: the reported Task's Project (ADR 0017).
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
			// 000098 (D31): a session is a person's or a daemon's.
			"client",
		})
	})
	t.Run("activity columns", func(t *testing.T) {
		assertTableColumns(t, ctx, pool, "activity_events", []string{
			"id", "company_id", "actor_type", "actor_id", "action", "entity_type", "entity_id",
			"agent_id", "task_id", "conversation_id", "details", "created_at",
			"revision", "visibility", "visibility_user_id", "schema_version", "project_id",
			// 000097 (D31): an Agent's row in a Run names the person it
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
			// 000059: the assignee is always a member and never NULL; the
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
	seedPasswordResetRevokedSession(t, ctx, pool)

	if err := MigrateDown(ctx, pool); err == nil || !strings.Contains(err.Error(), "backup") {
		t.Fatalf("cutover downgrade = %v, want explicit backup restoration requirement", err)
	}
	remaining := slices.DeleteFunc(
		slices.Clone(expectedBusinessTables),
		func(table string) bool {
			// 000057 (project files), 000058 (Studio), 000059 (bindings) and
			// 000086 (project access)
			// come down with the rest of the ladder above the cutover
			// refusal, taking their tables with them.
			return table == "boards" || strings.HasPrefix(table, "board_") || table == "task_result_sets" ||
				table == "task_result_items" ||
				table == "page_suggestion_reviews" ||
				table == "document_changes" || table == "document_writing_requests" || table == "output_documents" ||
				strings.HasPrefix(table, "output_review_") ||
				strings.HasPrefix(table, "canvas_") ||
				strings.HasPrefix(table, "project_") ||
				strings.HasPrefix(table, "review_") ||
				table == "artifact_review_decisions" ||
				strings.HasPrefix(table, "huddle") ||
				strings.HasPrefix(table, "wiki_") ||
				table == "channel_invocations" ||
				table == "projects" ||
				table == "project_files" ||
				table == "company_invitations" ||
				table == "agent_runtime_bindings" ||
				table == "company_initializations" ||
				table == "agent_skills" ||
				table == "run_skills" ||
				table == "skill_imports" ||
				table == "skill_versions" ||
				table == "company_groups" ||
				table == "company_group_members" ||
				table == "company_invitation_tokens" ||
				table == "company_share_links" ||
				table == "conversation_studio_projects" ||
				table == "output_studio_canvases" ||
				(table == "calendars" || strings.HasPrefix(table, "calendar_")) ||
				table == "conversation_agents" ||
				table == "conversation_channels" ||
				table == "model_prices" ||
				table == "task_schedules" ||
				strings.HasPrefix(table, "analytics_") ||
				table == "conversation_human_directs" ||
				table == "conversation_members" ||
				table == "conversation_message_pins" ||
				table == "conversation_message_reactions" ||
				table == "conversation_thread_states" ||
				table == "conversation_model_settings" ||
				table == "conversation_model_changes" ||
				strings.HasPrefix(table, "social_") ||
				(table == "output_grants" || table == "output_conversation_grants" || table == "discussion_artifacts") ||
				table == "run_connector_selections" ||
				table == "run_connector_snapshots" ||
				table == "run_wiki_reads" ||
				table == "run_wiki_writes"
		},
	)
	assertBusinessTables(t, ctx, pool, remaining)
}

// The greenfield ladder has no legacy usage shape to restore: rolling the
// usage migration back simply removes the immutable usage-fact table. This
// proves a partial rollback to a pinned version and a subsequent MigrateUp both
// preserve the clean-slate ladder.
func TestUsageDownMigrationRemovesRunUsage(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	if err := runMigration(
		ctx,
		pool,
		"migrate to usage schema",
		func(runner *migrate.Migrate) error { return runner.Migrate(13) },
	); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}

	runner, err := newMigrationRunner(ctx, pool)
	if err != nil {
		t.Fatalf("newMigrationRunner() error = %v", err)
	}
	// Target the schema immediately before the migration that creates
	// run_usage. A relative Step would silently roll back only the newest
	// unrelated migration as the migration ladder grows.
	migrationErr := runner.Migrate(6)
	sourceErr, databaseErr := runner.Close()
	if err := errors.Join(migrationErr, sourceErr, databaseErr); err != nil {
		t.Fatalf("roll back usage accounting migration: %v", err)
	}

	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_catalog.pg_tables
			WHERE schemaname='public' AND tablename='run_usage'
		)`).Scan(&exists); err != nil {
		t.Fatalf("check rolled-back run_usage table: %v", err)
	}
	if exists {
		t.Error("table run_usage survived the usage rollback")
	}

	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() after usage rollback error = %v", err)
	}
	assertBusinessTables(t, ctx, pool, expectedBusinessTables)
	var rebuiltRows int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM run_usage`).Scan(&rebuiltRows); err != nil {
		t.Fatalf("read rebuilt run_usage: %v", err)
	}
	if rebuiltRows != 0 {
		t.Fatalf("rebuilt run_usage rows = %d, want 0", rebuiltRows)
	}
}

func TestOutputMigrationsUpgradeAndRollbackFromVersionEight(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	toBaseline := func(runner *migrate.Migrate) error { return runner.Migrate(8) }
	if err := runMigration(ctx, pool, "migrate to version 8", toBaseline); err != nil {
		t.Fatalf("migrate to version 8: %v", err)
	}
	assertTableDoesNotExist(t, ctx, pool, "outputs")
	assertTableDoesNotExist(t, ctx, pool, "output_publications")

	var inboxID string
	if err := pool.QueryRow(ctx, `
		WITH owner AS (
			INSERT INTO users (email, display_name)
			VALUES ('output-migration@boardly.test', 'Migration Owner') RETURNING id
		), company AS (
			INSERT INTO companies (name, slug, task_prefix)
			VALUES ('Existing Company', 'output-migration', 'OUT') RETURNING id
		)
		INSERT INTO inbox_items (
			company_id, recipient_user_id, kind, entity_type, entity_id, title, body, dedupe_key
		)
		SELECT company.id, owner.id, 'run_completed', 'company', company.id::text,
			'Existing notification', 'Preserve this notification', 'migration-notification'
		FROM company CROSS JOIN owner
		RETURNING id::text`).Scan(&inboxID); err != nil {
		t.Fatalf("seed version 8 inbox: %v", err)
	}

	for _, step := range []struct {
		name      string
		direction func(*migrate.Migrate) error
		baseline  bool
	}{
		{name: "upgrade", direction: func(runner *migrate.Migrate) error { return runner.Migrate(13) }},
		{name: "rollback", direction: toBaseline, baseline: true},
		{name: "upgrade again", direction: func(runner *migrate.Migrate) error { return runner.Migrate(13) }},
	} {
		if err := runMigration(ctx, pool, step.name, step.direction); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if step.baseline {
			assertTableDoesNotExist(t, ctx, pool, "outputs")
			assertTableDoesNotExist(t, ctx, pool, "output_publications")
		} else {
			assertBusinessTables(t, ctx, pool, businessTablesBeforeRunHistory())
			var routePending bool
			if err := pool.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM output_publications
					WHERE custom_hostname IS NOT NULL OR route_binding_id IS NOT NULL OR route_pending
				)
			`).Scan(&routePending); err != nil {
				t.Fatalf("%s custom publication schema: %v", step.name, err)
			}
			if routePending {
				t.Fatalf("%s created unexpected publication routes", step.name)
			}
		}
		var body string
		if err := pool.QueryRow(ctx, `SELECT body FROM inbox_items WHERE id = $1`, inboxID).
			Scan(&body); err != nil {
			t.Fatalf("%s existing inbox: %v", step.name, err)
		}
		if body != "Preserve this notification" {
			t.Fatalf("%s inbox body = %q, want preserved notification", step.name, body)
		}
	}
}

func seedPasswordResetRevokedSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	const userID = "01990d25-3d7a-7000-8000-000000000001"
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, display_name)
		VALUES ($1, 'migration-password-reset@boardly.test', 'Migration Password Reset')`, userID); err != nil {
		t.Fatalf("insert migration password-reset User: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth_sessions (
			id, user_id, token_prefix, token_hash, expires_at, revoked_at, revocation_reason
		) VALUES (
			'01990d25-3d7a-7000-8000-000000000002', $1, 'migration-password-reset',
			decode(repeat('01', 32), 'hex'), clock_timestamp() + interval '1 hour',
			clock_timestamp(), 'password_reset'
		)`, userID); err != nil {
		t.Fatalf("insert password-reset-revoked auth session: %v", err)
	}
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

func TestWebAppMigrationPreservesExistingOutputsAcrossUpgradeAndRollback(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	toVersionTen := func(runner *migrate.Migrate) error { return runner.Migrate(10) }
	if err := runMigration(ctx, pool, "migrate to version 10", toVersionTen); err != nil {
		t.Fatal(err)
	}
	fixture := seedLegacyMigrationRun(t, ctx, pool)
	var outputID string
	if err := pool.QueryRow(ctx, `INSERT INTO outputs (company_id, run_id, title, summary, presentation_fingerprint)
        VALUES ($1, $2, 'Existing output', 'Preserve this result', repeat('a', 64)) RETURNING id::text`, fixture.CompanyA, fixture.RunA).Scan(&outputID); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		name      string
		direction func(*migrate.Migrate) error
		rollback  bool
	}{
		{name: "upgrade", direction: func(runner *migrate.Migrate) error { return runner.Migrate(13) }},
		{name: "rollback", direction: toVersionTen, rollback: true},
		{name: "upgrade again", direction: func(runner *migrate.Migrate) error { return runner.Migrate(13) }},
	} {
		if err := runMigration(ctx, pool, step.name, step.direction); err != nil {
			t.Fatal(err)
		}
		var summary string
		if err := pool.QueryRow(ctx, `SELECT summary FROM outputs WHERE id = $1`, outputID).
			Scan(&summary); err != nil ||
			summary != "Preserve this result" {
			t.Fatalf("%s preserved output = %q, %v", step.name, summary, err)
		}
		if step.rollback {
			continue
		}
		var outputType string
		if err := pool.QueryRow(ctx, `SELECT type FROM outputs WHERE id = $1`, outputID).
			Scan(&outputType); err != nil ||
			outputType != "files" {
			t.Fatalf("%s output type = %q, %v", step.name, outputType, err)
		}
		if _, err := pool.Exec(ctx, `UPDATE outputs SET type = 'unknown' WHERE id = $1`, outputID); err == nil {
			t.Fatal("invalid output type accepted")
		}
	}
}

// TestRuntimeOfflineInboxRollbackDropsOnlyItsOwnNotices pins the down
// file that deletes inbox rows. 000013 widens
// `inbox_items_kind_check`, so rolling it back over a database that ran a
// stale sweep would fail on the ADD CONSTRAINT unless the new rows go first —
// and only those rows: every other notification survives.
func TestRuntimeOfflineInboxRollbackDropsOnlyItsOwnNotices(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	toVersionTwelve := func(runner *migrate.Migrate) error { return runner.Migrate(12) }
	if err := runMigration(ctx, pool, "migrate to version 12", toVersionTwelve); err != nil {
		t.Fatal(err)
	}
	fixture := seedLegacyMigrationRun(t, ctx, pool)
	insertItem := func(kind, dedupeKey string) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO inbox_items (
				company_id, recipient_user_id, kind, entity_type, entity_id,
				title, body, dedupe_key
			) VALUES ($1, $2, $3, 'runtime', $4::text, 'Notice', 'Body', $5)`,
			fixture.CompanyA, fixture.UserA, kind, fixture.RuntimeA, dedupeKey)
		return err
	}
	// Before 000013 the kind is not in the vocabulary at all.
	if err := insertItem("runtime_offline", "runtime:before"); err == nil {
		t.Fatal("inbox_items accepted runtime_offline at version 12")
	}
	if err := insertItem("run_failed", "run:kept"); err != nil {
		t.Fatalf("seed the notice that must survive: %v", err)
	}

	if err := runMigration(
		ctx,
		pool,
		"upgrade",
		func(runner *migrate.Migrate) error { return runner.Migrate(13) },
	); err != nil {
		t.Fatal(err)
	}
	if err := insertItem("runtime_offline", "runtime:dropped"); err != nil {
		t.Fatalf("insert runtime_offline after 000013: %v", err)
	}

	if err := runMigration(ctx, pool, "rollback", toVersionTwelve); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `SELECT kind FROM inbox_items ORDER BY dedupe_key`)
	if err != nil {
		t.Fatalf("read inbox items after the rollback: %v", err)
	}
	var kinds []string
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			rows.Close()
			t.Fatalf("scan inbox kind: %v", err)
		}
		kinds = append(kinds, kind)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("read inbox items after the rollback: %v", err)
	}
	if len(kinds) != 1 || kinds[0] != "run_failed" {
		t.Fatalf("inbox kinds after the rollback = %v, want only run_failed", kinds)
	}

	if err := runMigration(ctx, pool, "upgrade again", (*migrate.Migrate).Up); err != nil {
		t.Fatal(err)
	}
	if err := insertItem("runtime_offline", "runtime:again"); err != nil {
		t.Fatalf("insert runtime_offline after the second upgrade: %v", err)
	}
}

// businessTablesBeforeRunHistory lists the tables at version 13, the last
// version before the Run history migrations. Migrations up to 13 never change,
// so the list is written out instead of being derived from
// expectedBusinessTables: a new migration never has to edit it. It still holds
// goals and the department tables, which 000016 and 000017 remove.
func businessTablesBeforeRunHistory() []string {
	return []string{
		"activity_events",
		"agents",
		"approvals",
		"asset_versions",
		"assets",
		"attachments",
		"auth_sessions",
		"commerce_accounts",
		"commerce_customers",
		"commerce_entitlements",
		"commerce_gateway_operations",
		"commerce_manifests",
		"commerce_offers",
		"commerce_operations",
		"commerce_payments",
		"commerce_rate_limits",
		"commerce_site_challenges",
		"commerce_site_signing_keys",
		"commerce_sites",
		"commerce_subscriptions",
		"commerce_terms_acceptances",
		"commerce_terms_catalog",
		"commerce_webhook_account_leases",
		"commerce_webhook_receipts",
		"companies",
		"company_mailboxes",
		"company_members",
		"company_milestone_attestations",
		"company_reference_documents",
		"company_resources",
		"connector_catalog_items",
		"connector_discovery_generations",
		"connector_policies",
		"connector_runtime_observations",
		"conversation_items",
		"conversation_run_queue_attachments",
		"conversation_run_queue_items",
		"conversation_user_states",
		"conversations",
		"crash_reports",
		"deliverable_review_actions",
		"deliverables",
		"department_capabilities",
		"department_configs",
		"desktop_handoff_codes",
		"email_verification_flows",
		"goals",
		"inbox_items",
		"mailbox_draft_deliveries",
		"mailbox_draft_groups",
		"mailbox_drafts",
		"mailbox_message_attachments",
		"mailbox_messages",
		"mailbox_received_receipts",
		"message_attachments",
		"oauth_accounts",
		"oauth_login_flows",
		"output_items",
		"output_publications",
		"outputs",
		"password_reset_flows",
		"plugin_catalog_items",
		"plugin_discovery_generations",
		"plugin_runtime_observations",
		"questions",
		"run_tokens",
		"run_usage",
		"runs",
		"runtimes",
		"schedule_firings",
		"schedule_sources",
		"schedules",
		"task_dependencies",
		"task_merge_jobs",
		"task_report_batches",
		"task_report_events",
		"tasks",
		"users",
	}
}

// The historical output and Inbox migrations predate UUID Run identities.
func seedLegacyMigrationRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool) struct {
	CompanyA, UserA, RuntimeA uuid.UUID
	RunA                      int64
} {
	t.Helper()
	var fixture struct {
		CompanyA, UserA, RuntimeA uuid.UUID
		RunA                      int64
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, display_name) VALUES ('migration@example.test','Migration Owner') RETURNING id`).
		Scan(&fixture.UserA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO companies (name,slug,task_prefix) VALUES ('Migration Company','migration-company','MIG') RETURNING id`).
		Scan(&fixture.CompanyA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO runtimes (company_id,daemon_id,name,provider,owner_user_id) VALUES ($1,'migration-daemon','Migration Runtime','codex',$2) RETURNING id`, fixture.CompanyA, fixture.UserA).
		Scan(&fixture.RuntimeA); err != nil {
		t.Fatal(err)
	}
	var agentID, conversationID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO agents (company_id,name,title,runtime_id) VALUES ($1,'Migration Agent','Agent',$2) RETURNING id`, fixture.CompanyA, fixture.RuntimeA).
		Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO conversations (company_id,kind,created_by_user_id,agent_id) VALUES ($1,'direct',$2,$3) RETURNING id`, fixture.CompanyA, fixture.UserA, agentID).
		Scan(&conversationID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO runs (company_id,agent_id,conversation_id,runtime_id,status) VALUES ($1,$2,$3,$4,'completed') RETURNING id`, fixture.CompanyA, agentID, conversationID, fixture.RuntimeA).
		Scan(&fixture.RunA); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestStudioMigrationUpDownPreservesExistingConversations(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	// Exercise Studio alone; later migrations retain Company-wide chat state.
	toVersionTwentyThree := func(runner *migrate.Migrate) error { return runner.Migrate(23) }
	toVersionTwentyTwo := func(runner *migrate.Migrate) error { return runner.Migrate(22) }
	if err := runMigration(ctx, pool, "migrate to version 22", toVersionTwentyTwo); err != nil {
		t.Fatal(err)
	}
	// Seeded by hand in the version-22 shape: the shared fixture writes what
	// the head schema holds, and this database is deliberately behind it.
	var companyID, conversationID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO companies (name, slug, task_prefix)
		VALUES ('Studio', 'studio-migration', 'STU') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("seed Studio migration Company: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO conversations (company_id, kind, title)
		VALUES ($1, 'task', 'Existing conversation') RETURNING id`, companyID).
		Scan(&conversationID); err != nil {
		t.Fatalf("seed Studio migration Conversation: %v", err)
	}
	for _, step := range []struct {
		name       string
		direction  func(*migrate.Migrate) error
		wantStudio bool
	}{
		{"upgrade", toVersionTwentyThree, true},
		{"rollback", toVersionTwentyTwo, false},
		{"upgrade again", toVersionTwentyThree, true},
	} {
		if err := runMigration(ctx, pool, step.name, step.direction); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		var hasCanvas, hasProject, hasFinal, keptConversation bool
		if err := pool.QueryRow(ctx, `SELECT
			to_regclass('output_studio_canvases') IS NOT NULL,
			to_regclass('conversation_studio_projects') IS NOT NULL,
			(SELECT pg_get_constraintdef(oid) LIKE '%final%' FROM pg_constraint
			 WHERE conrelid = 'output_items'::regclass AND conname = 'output_items_role_check'),
			EXISTS(SELECT 1 FROM conversations WHERE id=$1)`, conversationID).
			Scan(&hasCanvas, &hasProject, &hasFinal, &keptConversation); err != nil {
			t.Fatal(err)
		}
		if hasCanvas != step.wantStudio || hasProject != step.wantStudio || hasFinal != step.wantStudio ||
			!keptConversation {
			t.Fatalf(
				"%s: canvas=%v project=%v final=%v conversation=%v, want Studio=%v with existing conversation retained",
				step.name,
				hasCanvas,
				hasProject,
				hasFinal,
				keptConversation,
				step.wantStudio,
			)
		}
		if step.wantStudio {
			dbtest.MustExec(
				t,
				ctx,
				pool,
				`INSERT INTO conversation_studio_projects(company_id,conversation_id,title)
				VALUES($1,$2,'Campaign')`,
				companyID,
				conversationID,
			)
		}
	}
}

func TestOutputAccessMigrationUpgradeAndRollback(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	toVersionThirtyFive := func(runner *migrate.Migrate) error { return runner.Migrate(35) }
	if err := runMigration(ctx, pool, "migrate to version 35", toVersionThirtyFive); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	var outputID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO outputs
		(company_id, run_id, title, summary, presentation_fingerprint)
		VALUES ($1, $2, 'Keep artifact', 'Keep files and access', repeat('a', 64)) RETURNING id`,
		fixture.CompanyA, fixture.RunA).Scan(&outputID); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `INSERT INTO company_members (company_id, user_id, role, status)
		VALUES ($1, $2, 'member', 'active')`, fixture.CompanyA, fixture.UserB)
	for _, step := range []struct {
		name       string
		direction  func(*migrate.Migrate) error
		wantAccess bool
	}{
		{name: "upgrade", direction: func(runner *migrate.Migrate) error { return runner.Migrate(36) }, wantAccess: true},
		{name: "rollback", direction: toVersionThirtyFive},
		{name: "upgrade again", direction: func(runner *migrate.Migrate) error { return runner.Migrate(36) }, wantAccess: true},
	} {
		if err := runMigration(ctx, pool, step.name, step.direction); err != nil {
			t.Fatal(err)
		}
		assertTableDoesNotExist(t, ctx, pool, "output_comments")
		assertTableDoesNotExist(t, ctx, pool, "output_access_history")
		var title string
		if err := pool.QueryRow(ctx, `SELECT title FROM outputs WHERE id=$1`, outputID).
			Scan(&title); err != nil || title != "Keep artifact" {
			t.Fatalf("%s artifact title = %q, error %v", step.name, title, err)
		}
		if !step.wantAccess {
			assertTableDoesNotExist(t, ctx, pool, "output_grants")
			assertTableDoesNotExist(t, ctx, pool, "output_conversation_grants")
			assertTableDoesNotExist(t, ctx, pool, "discussion_artifacts")
			continue
		}
		var ownerID uuid.UUID
		var revision int64
		var ownerLevel int
		if err := pool.QueryRow(ctx, `SELECT outputs.owner_user_id, outputs.access_version, access.access_level
			FROM outputs JOIN output_access AS access ON access.output_id=outputs.id
			WHERE outputs.id=$1 AND access.user_id=$2`, outputID, fixture.UserA).
			Scan(&ownerID, &revision, &ownerLevel); err != nil || ownerID != fixture.UserA || revision != 1 || ownerLevel != 3 {
			t.Fatalf(
				"%s owner = %v, revision = %d, level = %d, error %v",
				step.name,
				ownerID,
				revision,
				ownerLevel,
				err,
			)
		}
		for _, test := range []struct {
			permission string
			level      int
		}{
			{permission: "view", level: 1},
			{permission: "manage", level: 3},
		} {
			dbtest.MustExec(t, ctx, pool, `INSERT INTO output_grants
				(company_id, output_id, user_id, permission, granted_by_user_id)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (company_id, output_id, user_id) DO UPDATE SET permission=EXCLUDED.permission`,
				fixture.CompanyA, outputID, fixture.UserB, test.permission, fixture.UserA)
			var level int
			if err := pool.QueryRow(ctx, `SELECT access_level FROM output_access WHERE output_id=$1 AND user_id=$2`,
				outputID, fixture.UserB).
				Scan(&level); err != nil ||
				level != test.level {
				t.Fatalf("%s %s level = %d, error %v", step.name, test.permission, level, err)
			}
		}
		if _, err := pool.Exec(
			ctx,
			`UPDATE output_grants SET permission='comment' WHERE output_id=$1`,
			outputID,
		); err == nil {
			t.Fatal("unsupported permission accepted")
		}
	}
}

func TestHumanProjectPagePreventsDowngrade(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	var page uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO wiki_pages(company_id,project_id,slug,title,created_by_user_id)
	 VALUES($1,$2,'human-project-page','Human working Page',$3) RETURNING id`, fixture.CompanyA, fixture.ProjectA, fixture.UserA).Scan(&page); err != nil {
		t.Fatal(err)
	}
	// A human Page has no Overview designation, Run attribution or contribution.
	err := runMigration(
		ctx,
		pool,
		"downgrade project Pages",
		func(runner *migrate.Migrate) error { return runner.Migrate(88) },
	)
	if err == nil || !strings.Contains(err.Error(), "restore a verified backup") {
		t.Fatalf("downgrade = %v, want refusal preserving Project Page ownership", err)
	}
	var retained bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wiki_pages WHERE id=$1 AND project_id=$2)
	 AND NOT EXISTS(SELECT 1 FROM company_wiki_pages WHERE id=$1)`, page, fixture.ProjectA).Scan(&retained); err != nil || !retained {
		t.Fatalf("Project Page retains scope outside Company Wiki = %v, %v", retained, err)
	}
}

func TestArtifactPolicyRemovalPreservesPublicLinks(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := runMigration(
		ctx,
		pool,
		"previous schema",
		func(m *migrate.Migrate) error { return m.Migrate(91) },
	); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	var outputID, token uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id,visibility,public_token)
 VALUES($1,$2,'Shared report','Summary','public-policy-removal',$3,'public',gen_random_uuid()) RETURNING id,public_token`,
		f.CompanyA, f.RunA, f.UserA).
		Scan(&outputID, &token); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		name    string
		version uint
	}{
		{"upgrade", 92},
		{"rollback", 91},
		{"reapply", 92},
	} {
		if err := runMigration(
			ctx,
			pool,
			step.name,
			func(m *migrate.Migrate) error { return m.Migrate(step.version) },
		); err != nil {
			t.Fatal(err)
		}
		var gotToken uuid.UUID
		var visibility string
		if err := pool.QueryRow(ctx, `SELECT public_token,visibility FROM outputs WHERE id=$1`, outputID).
			Scan(&gotToken, &visibility); err != nil || gotToken != token || visibility != "public" {
			t.Fatalf(
				"%s public link = %s, %q, %v; want %s, public",
				step.name,
				gotToken,
				visibility,
				err,
				token,
			)
		}
		if step.version == 92 {
			assertTableOmitsColumns(
				t,
				ctx,
				pool,
				"companies",
				[]string{"artifact_public_enabled", "artifact_policy_version"},
			)
		}
	}
}

func TestPageMigrationPreservesPublishedWikiAcrossUpgradeAndRollback(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := runMigration(
		ctx,
		pool,
		"main schema",
		func(m *migrate.Migrate) error { return m.Migrate(88) },
	); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	var pageID, revisionID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO wiki_pages(company_id,slug,title,created_by_user_id)
 VALUES($1,'existing-knowledge','Existing knowledge',$2) RETURNING id`, f.CompanyA, f.UserA).Scan(&pageID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO wiki_revisions(company_id,page_id,number,title,body,author_user_id)
 VALUES($1,$2,3,'Existing knowledge','Keep this published content',$3) RETURNING id`, f.CompanyA, pageID, f.UserA).Scan(&revisionID); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE wiki_pages SET current_revision_id=$2 WHERE id=$1`, pageID, revisionID)
	for _, step := range []struct {
		name      string
		direction func(*migrate.Migrate) error
		version   int
	}{
		{"upgrade", func(m *migrate.Migrate) error { return m.Migrate(89) }, 89},
		{"rollback", func(m *migrate.Migrate) error { return m.Migrate(88) }, 88},
		{"reapply", func(m *migrate.Migrate) error { return m.Migrate(89) }, 89},
	} {
		if err := runMigration(ctx, pool, step.name, step.direction); err != nil {
			t.Fatal(err)
		}
		var version int
		var dirty bool
		if err := pool.QueryRow(ctx, `SELECT version,dirty FROM schema_migrations`).
			Scan(&version, &dirty); err != nil || version != step.version ||
			dirty {
			t.Fatalf(
				"%s schema = %d, dirty %v, error %v; want %d clean",
				step.name,
				version,
				dirty,
				err,
				step.version,
			)
		}
		var preserved bool
		if err := pool.QueryRow(ctx, `SELECT p.title='Existing knowledge' AND r.body='Keep this published content' AND r.number=3
  FROM wiki_pages p JOIN wiki_revisions r ON r.id=p.current_revision_id WHERE p.id=$1 AND r.id=$2`, pageID, revisionID).
			Scan(&preserved); err != nil ||
			!preserved {
			t.Fatalf("%s published Wiki preserved = %v, error %v", step.name, preserved, err)
		}
		if step.version == 88 {
			assertTableDoesNotExist(t, ctx, pool, "review_threads")
			assertTableDoesNotExist(t, ctx, pool, "project_presentations")
			continue
		}
		if err := pool.QueryRow(ctx, `SELECT p.next_revision_number=4 AND r.published_at=r.created_at AND r.block_content='{}'::jsonb
  FROM company_wiki_pages p JOIN wiki_revisions r ON r.id=p.current_revision_id WHERE p.id=$1`, pageID).
			Scan(&preserved); err != nil ||
			!preserved {
			t.Fatalf(
				"%s publication and revision counter preserved = %v, error %v",
				step.name,
				preserved,
				err,
			)
		}
		// Each part of the feature must arrive in this one upgrade.
		if _, err := pool.Exec(ctx, `SELECT page_id FROM review_threads LIMIT 0;
  SELECT title,summary,collection_id,restored_revision_id FROM wiki_canvas_drafts LIMIT 0;
  SELECT overview_page_id FROM project_canvases LIMIT 0`); err != nil {
			t.Fatalf("%s complete Page schema: %v", step.name, err)
		}
	}
}

func TestHuddleDefaultBudgetMigrationKeepsConfiguredBudgets(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := runMigration(
		ctx,
		pool,
		"main schema",
		func(m *migrate.Migrate) error { return m.Migrate(89) },
	); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	// An owner's explicit zero is a choice, not a missing configuration.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO huddle_ai_budgets(company_id,monthly_limit_usd_ticks) VALUES($1,0)`,
		f.CompanyB,
	)
	budget := func(step string, company uuid.UUID) (int64, bool) {
		t.Helper()
		var ticks int64
		err := pool.QueryRow(ctx, `SELECT monthly_limit_usd_ticks FROM huddle_ai_budgets WHERE company_id=$1`, company).
			Scan(&ticks)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false
		}
		if err != nil {
			t.Fatalf("%s budget: %v", step, err)
		}
		return ticks, true
	}
	for _, step := range []struct {
		name      string
		direction func(*migrate.Migrate) error
		seeded    bool
	}{
		{"upgrade", func(m *migrate.Migrate) error { return m.Migrate(90) }, true},
		{"rollback", func(m *migrate.Migrate) error { return m.Migrate(89) }, false},
		{"reapply", func(m *migrate.Migrate) error { return m.Migrate(90) }, true},
	} {
		if err := runMigration(ctx, pool, step.name, step.direction); err != nil {
			t.Fatal(err)
		}
		if ticks, ok := budget(step.name, f.CompanyA); ok != step.seeded || ok && ticks != 20_000_000_000 {
			t.Fatalf(
				"%s default budget = %d, present %v; want present %v at $2",
				step.name,
				ticks,
				ok,
				step.seeded,
			)
		}
		if ticks, ok := budget(step.name, f.CompanyB); !ok || ticks != 0 {
			t.Fatalf("%s configured zero budget = %d, present %v; want kept", step.name, ticks, ok)
		}
	}
}

// 000103 makes the person part of a runtime's identity (D33), so one machine
// can hold several people's rows. Rolling back must restore the old
// (company, daemon, provider) key without deleting anyone's row, since a
// delete would cascade into its Run history: the most recently seen row keeps
// the daemon id and every other one moves to a daemon id of its own.
func TestRuntimesPerPersonMigrationRollsBackWithoutLosingRows(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := runMigration(ctx, pool, "main schema",
		func(m *migrate.Migrate) error { return m.Migrate(103) }); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	mia := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, 'mia@example.com', 'Mia')`, mia)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`, f.CompanyA, mia)
	miasCodex := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at, handed_over_at)
		VALUES ($1, $2, $3, 'codex', 'codex', 'offline', $4, now() - interval '1 hour', now())`,
		miasCodex, f.CompanyA, f.DaemonA, mia)

	for _, step := range []struct {
		name    string
		version uint
	}{{"rollback", 102}, {"reapply", 103}} {
		if err := runMigration(ctx, pool, step.name,
			func(m *migrate.Migrate) error { return m.Migrate(step.version) }); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		daemons := map[uuid.UUID]string{}
		rows, err := pool.Query(ctx, `SELECT id, daemon_id FROM runtimes WHERE id = ANY($1)`,
			[]uuid.UUID{f.RuntimeA, miasCodex})
		if err != nil {
			t.Fatalf("%s: read runtimes: %v", step.name, err)
		}
		for rows.Next() {
			var id uuid.UUID
			var daemonID string
			if err := rows.Scan(&id, &daemonID); err != nil {
				t.Fatalf("%s: scan runtime: %v", step.name, err)
			}
			daemons[id] = daemonID
		}
		rows.Close()
		if daemons[f.RuntimeA] != f.DaemonA || daemons[miasCodex] != f.DaemonA+":"+miasCodex.String() {
			t.Fatalf("%s: daemon ids = %v; want the latest row on %s and the other moved aside",
				step.name, daemons, f.DaemonA)
		}
		if step.version == 102 {
			assertTableOmitsColumns(t, ctx, pool, "runtimes", []string{"handed_over_at"})
			continue
		}
		// Reapplied, the identity takes a second person on the same machine.
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO runtimes (company_id, daemon_id, name, provider, status, owner_user_id)
			VALUES ($1, $2, 'codex', 'codex', 'offline', $3)`, f.CompanyA, f.DaemonA, mia)
	}
}

func TestArtifactSuggestionsMigrationBackfillsExistingOutputs(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := runMigration(ctx, pool, "previous schema",
		func(m *migrate.Migrate) error { return m.Migrate(103) }); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	// The delivery predates a handoff; its frozen generation must not follow
	// the current Task when the new columns are populated.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE runs SET task_execution_generation=3,execution_authorized_by_user_id=$2 WHERE id=$1`,
		f.RunA,
		f.UserA,
	)
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET execution_generation=4 WHERE id=$1`, f.TaskA)
	dbtest.MustExec(t, ctx, pool, `UPDATE projects SET visibility='restricted' WHERE id=$1`, f.ProjectA)
	dbtest.MustExec(t, ctx, pool, `INSERT INTO company_members(company_id,user_id,role)
		VALUES($1,$2,'member')`, f.CompanyA, f.UserB)
	var directRun uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO runs
		(company_id,agent_id,conversation_id,runtime_id,status,run_type)
		VALUES($1,$2,$3,$4,'completed','direct_chat') RETURNING id`,
		f.CompanyA, f.AgentA, f.DirectConversationA, f.RuntimeA).Scan(&directRun); err != nil {
		t.Fatal(err)
	}
	for _, run := range []uuid.UUID{f.RunA, f.RunB, directRun} {
		dbtest.MustExec(t, ctx, pool, `INSERT INTO outputs
			(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id,visibility)
			SELECT company_id,id,'Retained delivery','Original summary',repeat('a',64),
			CASE WHEN company_id=$2 THEN $3::uuid ELSE $4::uuid END,'company'
			FROM runs WHERE id=$1`, run, f.CompanyA, f.UserA, f.UserB)
	}
	for _, step := range []struct {
		name    string
		version uint
	}{{"upgrade", 104}, {"rollback", 103}, {"reapply", 104}} {
		if err := runMigration(ctx, pool, step.name,
			func(m *migrate.Migrate) error { return m.Migrate(step.version) }); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM outputs
			WHERE title='Retained delivery' AND summary='Original summary'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 3 {
			t.Fatalf("%s retained outputs = %d, want 3", step.name, count)
		}
		if step.version == 103 {
			continue
		}
		var correct bool
		if err := pool.QueryRow(ctx, `SELECT bool_and(
			o.conversation_id IS NOT DISTINCT FROM r.conversation_id
			AND o.task_id IS NOT DISTINCT FROM r.task_id
			AND o.project_id IS NOT DISTINCT FROM c.project_id
			AND o.task_execution_generation IS NOT DISTINCT FROM r.task_execution_generation
			AND o.actor_type='agent' AND o.actor_agent_id IS NOT DISTINCT FROM r.agent_id
			AND o.source_run_id IS NOT DISTINCT FROM r.id
			AND o.on_behalf_of_user_id IS NOT DISTINCT FROM r.execution_authorized_by_user_id
			AND o.source_run_completed=(r.status='completed')
			AND o.actor_user_id IS NULL)
			FROM outputs o JOIN runs r ON r.company_id=o.company_id AND r.id=o.run_id
			JOIN conversations c ON c.company_id=r.company_id AND c.id=r.conversation_id`).Scan(&correct); err != nil {
			t.Fatal(err)
		}
		if !correct {
			t.Fatalf("%s frozen scope and attribution match source = false, want true", step.name)
		}
		var visible bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM output_access a
			JOIN outputs o ON o.company_id=a.company_id AND o.id=a.output_id
			WHERE o.run_id=$1 AND a.user_id=$2)`, f.RunA, f.UserB).Scan(&visible); err != nil {
			t.Fatal(err)
		}
		if visible {
			t.Fatalf("%s restricted Project output visible to non-reader = true, want false", step.name)
		}
	}
}

func TestDocumentAgentsMigrationPreservesDocumentHistory(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := runMigration(ctx, pool, "previous schema",
		func(m *migrate.Migrate) error { return m.Migrate(105) }); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	var pageID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO wiki_pages
		(company_id,slug,title,created_by_user_id) VALUES($1,'migration-history','Existing document',$2)
		RETURNING id`, f.CompanyA, f.UserA).Scan(&pageID); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `INSERT INTO document_changes
		(company_id,page_id,sequence,actor_type,change_type,delta,checkpoint,label)
		VALUES($1,$2,1,'system','initial',''::bytea,'existing snapshot'::bytea,'Before upgrade')`, f.CompanyA, pageID)
	var originalReaders string
	if err := pool.QueryRow(ctx, `SELECT pg_get_viewdef('conversation_readers'::regclass)`).
		Scan(&originalReaders); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		name    string
		version uint
	}{{"upgrade", 106}, {"rollback", 105}, {"reapply", 106}} {
		if err := runMigration(ctx, pool, step.name,
			func(m *migrate.Migrate) error { return m.Migrate(step.version) }); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		var checkpoint, label string
		if err := pool.QueryRow(ctx, `SELECT convert_from(checkpoint,'UTF8'),label
			FROM document_changes WHERE company_id=$1 AND page_id=$2 AND sequence=1`,
			f.CompanyA, pageID).Scan(&checkpoint, &label); err != nil {
			t.Fatal(err)
		}
		if checkpoint != "existing snapshot" || label != "Before upgrade" {
			t.Fatalf("%s history = %q, %q; want original snapshot and label", step.name, checkpoint, label)
		}
		if step.version == 105 {
			assertTableDoesNotExist(t, ctx, pool, "document_writing_requests")
			assertTableOmitsColumns(t, ctx, pool, "conversations", []string{"document_thread_id"})
			var readers string
			if err := pool.QueryRow(ctx, `SELECT pg_get_viewdef('conversation_readers'::regclass)`).
				Scan(&readers); err != nil {
				t.Fatal(err)
			}
			if readers != originalReaders {
				t.Fatal("rollback changed the original conversation access view")
			}
			continue
		}
		var timestampPresent bool
		if err := pool.QueryRow(ctx, `INSERT INTO document_writing_requests
			(company_id,run_id,page_id,request_key,target,before_content,sequence)
			VALUES($1,$2,$3,gen_random_uuid(),'{}','{}',0) RETURNING created_at IS NOT NULL`,
			f.CompanyA, f.RunA, pageID).Scan(&timestampPresent); err != nil {
			t.Fatalf("%s writing request: %v", step.name, err)
		}
		if !timestampPresent {
			t.Fatalf("%s writing request timestamp = NULL, want database default", step.name)
		}
	}
}

func TestWhiteboardMigrationsPreserveExistingContent(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := runMigration(
		ctx,
		pool,
		"main schema",
		func(m *migrate.Migrate) error { return m.Migrate(106) },
	); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	var beforeAccess string
	if err := pool.QueryRow(ctx, `SELECT pg_get_viewdef('attachment_access'::regclass)`).
		Scan(&beforeAccess); err != nil {
		t.Fatal(err)
	}
	var pageID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO wiki_pages(company_id,slug,title,created_by_user_id)
 VALUES($1,'existing-whiteboard-context','Existing knowledge',$2) RETURNING id`, f.CompanyA, f.UserA).Scan(&pageID); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		name    string
		version uint
	}{
		{"upgrade", 107},
		{"rollback", 106},
		{"reapply", 107},
	} {
		if err := runMigration(
			ctx,
			pool,
			step.name,
			func(m *migrate.Migrate) error { return m.Migrate(step.version) },
		); err != nil {
			t.Fatal(err)
		}
		var version uint
		var dirty bool
		if err := pool.QueryRow(ctx, `SELECT version,dirty FROM schema_migrations`).
			Scan(&version, &dirty); err != nil || version != step.version ||
			dirty {
			t.Fatalf(
				"%s schema = %d, dirty %v, error %v; want %d clean",
				step.name,
				version,
				dirty,
				err,
				step.version,
			)
		}
		var title string
		if err := pool.QueryRow(ctx, `SELECT title FROM wiki_pages WHERE id=$1 AND company_id=$2`, pageID, f.CompanyA).
			Scan(&title); err != nil ||
			title != "Existing knowledge" {
			t.Fatalf("%s existing Wiki title = %q, error %v", step.name, title, err)
		}
		if step.version == 106 {
			for _, table := range []string{"boards", "board_items", "board_files", "board_agent_access"} {
				assertTableDoesNotExist(t, ctx, pool, table)
			}
			var afterAccess string
			if err := pool.QueryRow(ctx, `SELECT pg_get_viewdef('attachment_access'::regclass)`).
				Scan(&afterAccess); err != nil ||
				afterAccess != beforeAccess {
				t.Fatalf(
					"rollback attachment access matches original = %v, error %v",
					afterAccess == beforeAccess,
					err,
				)
			}
			continue
		}
		// The complete feature must arrive in one migration, including
		// lifecycle fencing and file references.
		if _, err := pool.Exec(
			ctx,
			`SELECT lifecycle_revision,attachment_id,created_by_agent_id,created_by_run_id,updated_by_agent_id,updated_by_run_id FROM board_items LIMIT 0;
 SELECT attachment_id,preview_attachment_id,width,height FROM board_files LIMIT 0;
 SELECT state,plain_text,text_revision FROM board_notes LIMIT 0`,
		); err != nil {
			t.Fatalf("%s complete Whiteboard schema: %v", step.name, err)
		}
		var boardID uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO boards(company_id,title,created_by)
 VALUES($1,'Migration board',$2) RETURNING id`, f.CompanyA, f.UserA).Scan(&boardID); err != nil {
			t.Fatal(err)
		}
		var lifecycle int64
		if err := pool.QueryRow(ctx, `INSERT INTO board_items(company_id,board_id,type,x,y,width,height,created_by,updated_by)
 VALUES($1,$2,'sticky',0,0,200,100,$3,$3) RETURNING lifecycle_revision`, f.CompanyA, boardID, f.UserA).
			Scan(&lifecycle); err != nil ||
			lifecycle != 1 {
			t.Fatalf("%s item lifecycle = %d, error %v; want 1", step.name, lifecycle, err)
		}
		assertStatementRejected(
			t,
			ctx,
			pool,
			"23503",
			`INSERT INTO board_items(company_id,board_id,type,x,y,width,height,properties,created_by,updated_by)
 VALUES($1,$2,'image',0,0,100,100,jsonb_build_object('attachmentId',gen_random_uuid()),$3,$3)`,
			f.CompanyA,
			boardID,
			f.UserA,
		)
		if _, err := pool.Exec(
			ctx,
			`INSERT INTO board_agent_access(company_id,board_id,agent_id,access) VALUES($1,$2,$3,'edit')`,
			f.CompanyA,
			boardID,
			f.AgentA,
		); err != nil {
			t.Fatal(err)
		}
		key := uuid.New()
		if _, err := pool.Exec(
			ctx,
			`INSERT INTO board_operations(company_id,board_id,actor_user_id,actor_run_id,request_key,request_hash,seq) VALUES($1,$2,$3,'00000000-0000-0000-0000-000000000000',$4,$5,0)`,
			f.CompanyA,
			boardID,
			f.UserA,
			key,
			make([]byte, 32),
		); err != nil {
			t.Fatal(err)
		}
		assertStatementRejected(
			t,
			ctx,
			pool,
			"23505",
			`INSERT INTO board_operations(company_id,board_id,actor_user_id,actor_run_id,request_key,request_hash,seq) VALUES($1,$2,$3,'00000000-0000-0000-0000-000000000000',$4,$5,0)`,
			f.CompanyA,
			boardID,
			f.UserA,
			key,
			make([]byte, 32),
		)
		if _, err := pool.Exec(
			ctx,
			`INSERT INTO board_operations(company_id,board_id,actor_user_id,actor_run_id,request_key,request_hash,seq) VALUES($1,$2,$3,gen_random_uuid(),$4,$5,0)`,
			f.CompanyA,
			boardID,
			f.UserA,
			key,
			make([]byte, 32),
		); err != nil {
			t.Fatal(err)
		}

	}
}
