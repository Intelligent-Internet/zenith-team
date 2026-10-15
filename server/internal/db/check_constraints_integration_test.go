//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

// wantCheckConstraints is every CHECK constraint the schema is allowed to have.
//
// Most are one column with a closed set of string literals: the column's type
// in all but name. The small structural subset makes normalized projections
// total (for example, a Task has exactly one assignee and at least one creator).
//
// Everything else moved out. Length and non-blank rules duplicated the request
// binder; timestamp ordering restated what the writer controls; a version
// ceiling of 2^53 forced every UPDATE to carry a guard against a state no row
// can reach; and the multi-branch lifecycle checks encoded business rules the
// services already enforce and could say something useful about. A rule in a
// CHECK surfaces as a constraint name, which is not an error message anyone can
// act on — the service layer answers 422 or 409 with a sentence instead.
//
// A small subset carries structural integrity that cannot be delegated to an
// individual request binder: dependency and conversation shape, plus the
// creator and single assignee rules. Those constraints make projection mappers
// total without re-validating rows after every read.
//
// C1 additionally pins bounded discussion wire integers, canonical pair and
// grant shapes, immutable receipt digests and bounded metadata. These are the
// persisted discussion contract from C0, validated before writes by the service.
//
// D26 pins the Plugin presentation's prompt array shape (at most three) and
// brand color format, which clients render without re-validating.
//
// The list is pinned rather than counted so both directions fail loudly:
// reintroducing a policy constraint, or quietly dropping a vocabulary or
// structural projection invariant.
var wantCheckConstraints = []string{
	// Message resource receipts retain ordered, non-empty UTF-16 ranges.
	"message_skills_check",
	"message_skills_position_check",
	"message_skills_start_offset_check",
	"message_connectors_check",
	"message_connectors_position_check",
	"message_connectors_start_offset_check",

	"conversations_document_thread_check",
	"document_changes_storage_check",
	"document_changes_sequence_check",
	"document_writing_requests_sequence_check",
	"document_changes_actor_type_check",
	"document_changes_change_type_check",
	"document_changes_label_check",
	"document_changes_check",
	"document_changes_check1",

	"page_suggestion_reviews_action_check",
	"page_suggestion_reviews_before_sequence_check",
	"page_suggestion_reviews_after_sequence_check",
	"page_suggestion_reviews_before_theme_check",
	"page_suggestion_reviews_result_theme_check",
	"output_documents_submitted_sequence_check",
	"board_items_creator_run_attribution",
	"board_items_updater_run_attribution",
	"board_edges_creator_run_attribution",
	"board_edges_updater_run_attribution",
	// Whiteboard wire vocabularies, JSON shapes, file placements and explicit edges.
	"board_items_type_check",
	"board_items_properties_check",
	"board_items_file_check",
	"board_files_width_check",
	"board_files_height_check",
	"board_files_dimensions_check",
	"board_strokes_points_check",
	"board_edges_check",
	"board_edges_end_marker_check",
	"board_edges_line_check",
	"board_edges_route_check",
	"board_edges_start_marker_check",
	// Custom skill source vocabulary, immutable digest and projection shape.
	"agent_skills_revision_check",
	"skill_imports_source_type_check",
	"skill_versions_content_hash_check",
	"skill_versions_manifest_check",
	"conversation_studio_projects_binding_revision_check",
	"conversation_studio_projects_creation_protocol_check",
	"studio_receipt_identity",
	"analytics_budget_alerts_threshold_percent_check",
	"analytics_budgets_agent_shape_check",
	"analytics_budgets_monthly_limit_usd_ticks_check",
	"analytics_budgets_owner_kind_check",
	"model_prices_model_check",
	"model_prices_nonnegative_check",
	"model_prices_provider_check",
	"run_usage_billing_mode_check",

	// Canvas vocabularies, source/placement shape, JSON object shape and receipt digest.
	// Size limits and numeric movement bounds are enforced by the service.
	"asset_versions_source_type_check",
	"asset_versions_origin_check",
	"project_artifact_versions_source_type_check",
	"project_artifact_versions_check",
	"project_page_changes_status_check",
	"project_page_changes_before_theme_check",
	"project_page_changes_after_theme_check",
	"project_presentations_theme_check",
	"canvas_items_item_type_check",
	"canvas_items_version_policy_check",
	"canvas_items_representation_check",
	"canvas_items_view_config_check",
	"canvas_items_check",
	"canvas_items_check1",
	"canvas_items_version_selection_check",
	"wiki_canvas_drafts_schema_version_check",
	"wiki_canvas_drafts_structure_seq_check",
	"wiki_canvas_drafts_content_check",
	"wiki_revisions_block_content_check",
	"canvas_templates_content_check",
	"canvas_personal_views_config_check",
	"canvas_edges_check",
	"review_threads_anchor_check",
	"review_threads_status_check",
	"review_threads_resolution_check",
	"review_threads_subject_check",
	"review_threads_document_subject_check",
	"wiki_pages_document_owner_check",
	"review_thread_events_action_check",
	"artifact_review_decisions_action_check",
	"project_canvas_operations_request_hash_check",
	"project_files_file_type_check",
	// 000086: the Project permission catalog, Project visibility, and the
	// bound on Task chats without a Project (ADR 0017).
	"project_grants_permission_check",
	"projects_visibility_check",
	// 000101: the Group grant catalog (the same as a person's), and a Group's
	// name, description and revision, which clients render and send back.
	"project_group_grants_permission_check",
	"company_groups_name_check",
	"company_groups_description_check",
	"company_groups_version_check",
	"conversations_task_project_check",
	"wiki_pages_status_check",
	"wiki_pages_theme_check",
	"wiki_pages_status_archived_check",
	"wiki_links_no_self_check",
	"run_wiki_reads_kind_check",
	"run_wiki_reads_shape_check",
	// Huddle lifecycle/wire shape and nonnegative financial balances.
	"huddle_ai_budget_reservations_reserved_usd_ticks_check",
	"huddle_ai_budget_reservations_spent_usd_ticks_check",
	"huddle_ai_budgets_monthly_limit_usd_ticks_check",
	"huddle_ai_states_state_check",
	"huddle_work_proposals_state_check",
	"huddle_ai_sessions_state_check",
	"huddle_ai_sessions_model_seconds_check",
	"huddle_ai_sessions_spent_usd_ticks_check",
	"huddle_ai_usage_receipts_model_seconds_check",
	"huddle_ai_usage_receipts_input_tokens_check",
	"huddle_ai_usage_receipts_check",
	"huddle_ai_usage_receipts_output_tokens_check",

	"huddle_connections_check",
	"huddle_connections_check1",
	"huddle_connections_reservation_expiry_check",
	"huddle_connections_revision_check",
	"huddle_connections_status_check",
	"huddle_invitations_revision_check",
	"huddle_invitations_status_check",
	"huddles_check",
	"huddles_revision_check",
	"huddles_room_name_check",
	"huddles_state_check",
	"calendar_events_recurrence_interval_check",
	"calendar_events_recurrence_weekdays_check",
	"calendar_events_recurrence_monthly_mode_check",
	"calendar_events_recurrence_count_check",
	"calendar_events_recurrence_end_check",
	"calendar_events_recurrence_options_check",
	"conversation_message_reactions_emoji_check",
	"conversation_thread_states_grant_generation_check",
	"conversation_thread_states_follow_check",
	"conversation_thread_states_read_seq_check",
	"conversation_thread_states_manual_unread_seq_check",
	"conversation_thread_states_unread_count_check",
	"conversation_thread_states_version_check",
	"conversation_agents_check",
	"conversation_agents_generation_check",
	"conversation_agents_status_check",
	"conversation_agents_version_check",
	"conversation_channels_configuration_revision_check",
	"conversation_channels_create_fingerprint_check",
	"conversation_channels_topic_check",
	"conversation_human_directs_check",
	"conversation_items_revision_check",
	"conversation_members_check",
	"conversation_members_generation_check",
	"conversation_members_joined_seq_check",
	"conversation_members_source_type_check",
	"conversation_members_status_check",
	"conversation_members_version_check",
	"conversation_user_states_discussion_revision_check",
	"conversation_user_states_grant_generation_check",
	"conversation_user_states_hidden_through_seq_check",
	"conversation_user_states_notification_level_check",
	"conversation_user_states_main_read_seq_check",
	"conversation_user_states_manual_unread_seq_check",
	"conversation_user_states_unread_count_check",
	"conversations_discussion_revision_check",
	"conversations_head_seq_check",

	"company_invitation_tokens_delivery_status_check",
	"company_invitations_role_check",
	"company_invitations_status_check",
	"company_share_links_role_check",
	"activity_events_revision_check",
	"activity_events_schema_version_check",
	"activity_events_visibility_check",
	// D31: only an Agent acts for a person, and a Run is named only with
	// the person it works for.
	"activity_events_on_behalf_of_check",
	"activity_events_run_check",
	"runs_connector_selection_mode_check",
	"runs_revision_check",
	"runs_run_type_check",
	"activity_events_actor_type_check",
	"company_milestone_attestations_catalog_version_check",
	"company_milestone_attestations_milestone_type_check",
	"agents_provider_check",
	"approvals_continuation_presence_check",
	"approvals_continuation_status_check",
	"approvals_commerce_manifest_shape_check",
	"approvals_execution_status_check",
	"approvals_origin_scope_check",
	"approvals_presentation_shape_check",
	"approvals_status_check",
	"approvals_type_check",
	"asset_versions_integrity_status_check",
	"assets_status_check",
	"attachments_status_check",
	"calendars_type_check",
	"calendars_check",
	"calendar_attendees_response_status_check",
	"calendar_events_type_check",
	"calendar_events_color_check",
	"calendar_events_recurrence_check",
	"commerce_accounts_card_payments_status_check",
	"commerce_accounts_controller_dashboard_check",
	"commerce_accounts_controller_fees_check",
	"commerce_accounts_controller_losses_check",
	"commerce_accounts_controller_requirements_check",
	"commerce_accounts_environment_check",
	"commerce_accounts_status_check",
	"commerce_accounts_transfers_status_check",
	"commerce_customers_environment_check",
	"commerce_customers_subject_hmac_check",
	"commerce_entitlements_revoked_reason_check",
	"commerce_entitlements_source_identity_check",
	"commerce_entitlements_source_type_check",
	"commerce_entitlements_status_check",
	"commerce_gateway_operations_environment_check",
	"commerce_gateway_operations_request_sha_check",
	"commerce_gateway_operations_state_check",
	"commerce_gateway_operations_type_check",
	"commerce_manifests_digest_check",
	"commerce_manifests_document_check",
	"commerce_manifests_schema_version_check",
	"commerce_manifests_state_check",
	"commerce_offers_billing_shape_check",
	"commerce_offers_environment_check",
	"commerce_offers_status_check",
	"commerce_offers_type_check",
	"commerce_operations_environment_check",
	"commerce_operations_metadata_schema_check",
	"commerce_operations_request_sha256_check",
	"commerce_operations_state_check",
	"commerce_operations_type_check",
	"commerce_payments_environment_check",
	"commerce_payments_provider_shape_check",
	"commerce_payments_status_check",
	"commerce_rate_limits_action_check",
	"commerce_rate_limits_subject_hmac_check",
	"commerce_site_challenges_action_check",
	"commerce_site_challenges_environment_check",
	"commerce_site_challenges_nonce_check",
	"commerce_site_signing_keys_environment_check",
	"commerce_site_signing_keys_identity_check",
	"commerce_site_signing_keys_jwk_check",
	"commerce_site_signing_keys_state_check",
	"commerce_sites_environment_check",
	"commerce_sites_status_check",
	"commerce_sites_verification_hash_check",
	"commerce_subscriptions_environment_check",
	"commerce_subscriptions_status_check",
	"commerce_terms_acceptances_sha256_check",
	"commerce_terms_catalog_sha256_check",
	"commerce_webhook_account_leases_environment_check",
	"commerce_webhook_receipts_environment_check",
	"commerce_webhook_receipts_livemode_check",
	"commerce_webhook_receipts_payload_hash_check",
	"commerce_webhook_receipts_state_check",
	"company_members_role_check",
	"company_members_status_check",
	"company_resources_resource_type_check",
	"connector_catalog_items_action_check",
	"connector_catalog_items_category_check",
	"connector_catalog_items_contract_check",
	"connector_catalog_items_provider_check",
	"connector_catalog_items_source_check",
	"connector_catalog_items_status_check",
	"connector_discovery_generations_provider_check",
	"connector_runtime_observations_action_check",
	"connector_runtime_observations_category_check",
	"connector_runtime_observations_contract_check",
	"connector_runtime_observations_provider_check",
	"connector_runtime_observations_status_check",
	"conversation_items_author_kind_check",
	"conversation_items_type_check",
	"conversation_studio_projects_title_check",
	"conversation_studio_projects_description_check",
	"conversation_studio_projects_binding_revision_check",
	"conversation_studio_projects_creation_protocol_check",
	"studio_receipt_identity",
	"conversation_run_queue_items_state_check",
	"conversations_kind_check",
	"conversations_request_key_kind_check",
	"conversations_shape_check",
	"conversations_channel_execution_shape",
	"channel_invocations_status_check",
	"channel_invocations_publication_status_check",
	"conversations_general_shape_check",
	// D31: a session is a person's or a daemon's.
	"auth_sessions_client_check",
	"crash_reports_kind_check",
	"deliverable_review_actions_status_check",
	"deliverables_status_check",
	"email_verification_flows_client_check",
	"inbox_items_entity_type_check",
	"inbox_items_kind_check",
	"inbox_items_scope_check",
	"mailbox_drafts_status_check",
	"mailbox_message_attachments_disposition_check",
	"mailbox_received_receipts_outcome_check",
	"oauth_accounts_provider_check",
	"oauth_login_flows_client_check",
	"oauth_login_flows_provider_check",
	"output_items_role_check",
	"output_grants_permission_check",
	"outputs_payload_object_check",
	"outputs_presentation_number_check",
	"outputs_preview_url_type_check",
	"outputs_type_check",
	"outputs_actor_type_check",
	"outputs_actor_check",
	"outputs_visibility_check",
	"output_studio_canvases_canvas_check",
	"output_publications_failure_code_check",
	"output_publications_route_check",
	"output_publications_route_pending_check",
	"output_publications_status_check",
	"plugin_catalog_items_provider_check",
	"plugin_discovery_generations_provider_check",
	"plugin_runtime_observations_action_check",
	"plugin_runtime_observations_brand_color_check",
	"plugin_runtime_observations_category_check",
	"plugin_runtime_observations_connector_identities_check",
	"plugin_runtime_observations_default_prompts_check",
	"plugin_runtime_observations_provider_check",
	"plugin_runtime_observations_status_check",
	"questions_continuation_presence_check",
	"questions_continuation_status_check",
	"questions_origin_scope_check",
	"questions_status_check",
	"run_usage_provider_check",
	"run_usage_usage_basis_check",
	"runs_schedule_connector_selection_check",
	"runs_status_check",
	"runtimes_provider_check",
	"runtimes_provider_sign_in_check",
	"runtimes_status_check",
	"runtimes_unusable_reason_check",
	"schedule_firings_cancelled_run_check",
	"schedule_firings_missed_count_check",
	"schedule_firings_disposition_check",
	"schedule_firings_materialization_check",
	"schedule_firings_reason_check",
	"schedule_firings_request_key_check",
	"schedule_firings_trigger_check",
	"schedule_sources_provider_check",
	"schedule_sources_provider_source_check",
	"schedule_sources_source_check",
	"schedules_occurrence_state_check",
	"schedules_pause_reason_check",
	"schedules_provider_check",
	"schedules_status_check",
	"schedules_trigger_shape_check",
	"schedules_trigger_type_check",
	"social_connections_platform_check",
	"social_oauth_flows_platform_check",
	"social_posts_platform_check",
	"social_posts_status_check",
	"task_dependencies_distinct_tasks_check",
	"task_merge_jobs_request_type_check",
	"task_merge_jobs_status_check",
	"task_report_batches_delivery_authority_check",
	"task_report_batches_delivery_type_check",
	"task_report_events_completion_source_check",
	"task_report_events_failure_origin_check",
	"task_schedules_generation_check",
	// D31: a Task a Run creates names its person and the creating Agent.
	"tasks_creator_present_check",
	"tasks_milestone_type_check",
	"tasks_priority_check",
	"tasks_status_check",
	"tasks_assignment_status_check",
	"tasks_schedule_disposition_check",
	"tasks_task_schedule_generation_check",
}

// TestSchemaKeepsOnlyEnumAndStructuralCheckConstraints pins the constraint
// surface itself.
//
// The structural constraints — primary keys, foreign keys with their ON DELETE,
// and the partial unique indexes carrying real invariants — are asserted by the
// suites beside this one, because a query plan and a race depend on them. This
// test prevents that category from growing through accidental duplication.
func TestSchemaKeepsOnlyEnumAndStructuralCheckConstraints(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT conname
		FROM pg_constraint
		JOIN pg_namespace ON pg_namespace.oid = pg_constraint.connamespace
		WHERE contype = 'c' AND nspname = 'public'
		ORDER BY conname`)
	if err != nil {
		t.Fatalf("read check constraints: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan constraint name: %v", err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate check constraints: %v", err)
	}

	want := make(map[string]bool, len(wantCheckConstraints))
	for _, name := range wantCheckConstraints {
		want[name] = true
	}
	seen := make(map[string]bool, len(got))
	for _, name := range got {
		seen[name] = true
		if !want[name] {
			t.Errorf("unexpected CHECK constraint %q — a rule belongs in the "+
				"service layer unless it is a closed vocabulary or structural invariant", name)
		}
	}
	for _, name := range wantCheckConstraints {
		if !seen[name] {
			t.Errorf("missing CHECK constraint %q", name)
		}
	}
}
