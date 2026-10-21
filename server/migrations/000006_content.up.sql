-- Content: wiki, boards, canvases and documents.

CREATE TABLE wiki_collections (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    name text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT wiki_collections_pkey PRIMARY KEY (id),
    CONSTRAINT wiki_collections_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT wiki_collections_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX wiki_collections_company_name_key ON wiki_collections (company_id, lower(name));

CREATE TABLE canvas_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    canvas_id uuid NOT NULL,
    item_type text NOT NULL,
    artifact_id uuid,
    pinned_version_id uuid,
    version_policy text DEFAULT 'pinned' NOT NULL,
    parent_frame_id uuid,
    title text DEFAULT '' NOT NULL,
    x double precision DEFAULT 0 NOT NULL,
    y double precision DEFAULT 0 NOT NULL,
    width double precision DEFAULT 320 NOT NULL,
    height double precision DEFAULT 260 NOT NULL,
    stack_order bigint DEFAULT 0 NOT NULL,
    representation text DEFAULT 'preview' NOT NULL,
    view_config jsonb DEFAULT '{}'::jsonb NOT NULL,
    geometry_revision bigint DEFAULT 1 NOT NULL,
    content_revision bigint DEFAULT 1 NOT NULL,
    created_by_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz,
    CONSTRAINT canvas_items_pkey PRIMARY KEY (id),
    CONSTRAINT canvas_items_company_id_project_id_canvas_id_id_key UNIQUE (company_id, project_id, canvas_id, id),
    CONSTRAINT canvas_items_check CHECK (((item_type = 'frame') AND (artifact_id IS NULL) AND (pinned_version_id IS NULL)) OR ((item_type = 'artifact') AND (artifact_id IS NOT NULL))),
    CONSTRAINT canvas_items_check1 CHECK (parent_frame_id IS DISTINCT FROM id),
    CONSTRAINT canvas_items_item_type_check CHECK (item_type IN ('artifact', 'frame')),
    CONSTRAINT canvas_items_representation_check CHECK (representation IN ('compact', 'excerpt', 'preview')),
    CONSTRAINT canvas_items_version_policy_check CHECK (version_policy IN ('pinned', 'accepted', 'draft')),
    CONSTRAINT canvas_items_version_selection_check CHECK ((version_policy <> 'accepted') OR (pinned_version_id IS NULL)),
    CONSTRAINT canvas_items_view_config_check CHECK (jsonb_typeof(view_config) = 'object'),
    CONSTRAINT canvas_items_company_id_project_id_artifact_id_fkey FOREIGN KEY (company_id, project_id, artifact_id) REFERENCES project_artifacts(company_id, project_id, id) ON DELETE CASCADE,
    CONSTRAINT canvas_items_company_id_project_id_artifact_id_pinned_vers_fkey FOREIGN KEY (company_id, project_id, artifact_id, pinned_version_id) REFERENCES project_artifact_versions(company_id, project_id, artifact_id, id) ON DELETE SET NULL (pinned_version_id),
    CONSTRAINT canvas_items_company_id_project_id_canvas_id_fkey FOREIGN KEY (company_id, project_id, canvas_id) REFERENCES project_canvases(company_id, project_id, id) ON DELETE CASCADE,
    CONSTRAINT canvas_items_company_id_project_id_canvas_id_parent_frame__fkey FOREIGN KEY (company_id, project_id, canvas_id, parent_frame_id) REFERENCES canvas_items(company_id, project_id, canvas_id, id) ON DELETE SET NULL (parent_frame_id)
);
CREATE INDEX canvas_items_artifact ON canvas_items (artifact_id) WHERE (deleted_at IS NULL);
CREATE INDEX canvas_items_board ON canvas_items (canvas_id, id) WHERE (deleted_at IS NULL);

CREATE TABLE canvas_edges (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    canvas_id uuid NOT NULL,
    source_item_id uuid NOT NULL,
    target_item_id uuid NOT NULL,
    label text DEFAULT '' NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    deleted_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT canvas_edges_pkey PRIMARY KEY (id),
    CONSTRAINT canvas_edges_check CHECK (source_item_id <> target_item_id),
    CONSTRAINT canvas_edges_company_id_project_id_canvas_id_fkey FOREIGN KEY (company_id, project_id, canvas_id) REFERENCES project_canvases(company_id, project_id, id) ON DELETE CASCADE,
    CONSTRAINT canvas_edges_company_id_project_id_canvas_id_source_item_i_fkey FOREIGN KEY (company_id, project_id, canvas_id, source_item_id) REFERENCES canvas_items(company_id, project_id, canvas_id, id) ON DELETE CASCADE,
    CONSTRAINT canvas_edges_company_id_project_id_canvas_id_target_item_i_fkey FOREIGN KEY (company_id, project_id, canvas_id, target_item_id) REFERENCES canvas_items(company_id, project_id, canvas_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX canvas_edges_pair ON canvas_edges (canvas_id, source_item_id, target_item_id) WHERE (deleted_at IS NULL);

CREATE TABLE canvas_templates (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    title text NOT NULL,
    content jsonb NOT NULL,
    created_by_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT canvas_templates_pkey PRIMARY KEY (id),
    CONSTRAINT canvas_templates_content_check CHECK (jsonb_typeof(content) = 'object'),
    CONSTRAINT canvas_templates_company_id_project_id_fkey FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id) ON DELETE CASCADE
);

CREATE TABLE canvas_personal_views (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    user_id uuid NOT NULL,
    title text NOT NULL,
    config jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT canvas_personal_views_pkey PRIMARY KEY (id),
    CONSTRAINT canvas_personal_views_config_check CHECK (jsonb_typeof(config) = 'object'),
    CONSTRAINT canvas_personal_views_company_id_project_id_fkey FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id) ON DELETE CASCADE,
    CONSTRAINT canvas_personal_views_company_id_user_id_fkey FOREIGN KEY (company_id, user_id) REFERENCES company_members(company_id, user_id) ON DELETE CASCADE
);

CREATE TABLE boards (
    id uuid NOT NULL,
    company_id uuid NOT NULL,
    head_seq bigint DEFAULT 0 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT boards_pkey PRIMARY KEY (id),
    CONSTRAINT boards_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT boards_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT boards_output_fkey FOREIGN KEY (company_id, id) REFERENCES outputs(company_id, id) ON DELETE CASCADE
);

CREATE TABLE board_files (
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    attachment_id uuid NOT NULL,
    preview_attachment_id uuid,
    width integer,
    height integer,
    created_by uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT board_files_pkey PRIMARY KEY (company_id, board_id, attachment_id),
    CONSTRAINT board_files_dimensions_check CHECK ((width IS NULL) = (height IS NULL)),
    CONSTRAINT board_files_height_check CHECK (height > 0),
    CONSTRAINT board_files_width_check CHECK (width > 0),
    CONSTRAINT board_files_company_id_attachment_id_fkey FOREIGN KEY (company_id, attachment_id) REFERENCES attachments(company_id, id) ON DELETE CASCADE,
    CONSTRAINT board_files_company_id_board_id_fkey FOREIGN KEY (company_id, board_id) REFERENCES boards(company_id, id) ON DELETE CASCADE,
    CONSTRAINT board_files_company_id_preview_attachment_id_fkey FOREIGN KEY (company_id, preview_attachment_id) REFERENCES attachments(company_id, id) ON DELETE SET NULL (preview_attachment_id)
);
CREATE INDEX board_files_attachment_idx ON board_files (company_id, attachment_id);
CREATE INDEX board_files_created_idx ON board_files (created_at);
CREATE INDEX board_files_preview_idx ON board_files (company_id, preview_attachment_id) WHERE (preview_attachment_id IS NOT NULL);

CREATE TABLE board_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    type text NOT NULL,
    x double precision NOT NULL,
    y double precision NOT NULL,
    width double precision NOT NULL,
    height double precision NOT NULL,
    rotation double precision DEFAULT 0 NOT NULL,
    stack_order bigint DEFAULT 0 NOT NULL,
    parent_frame_id uuid,
    reading_order bigint DEFAULT 0 NOT NULL,
    properties jsonb DEFAULT '{}'::jsonb NOT NULL,
    geometry_revision bigint DEFAULT 1 NOT NULL,
    content_revision bigint DEFAULT 1 NOT NULL,
    created_by uuid NOT NULL,
    updated_by uuid NOT NULL,
    created_by_agent_id uuid,
    updated_by_agent_id uuid,
    created_by_run_id uuid,
    updated_by_run_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz,
    lifecycle_revision bigint DEFAULT 1 NOT NULL,
    attachment_id uuid GENERATED ALWAYS AS (((properties ->> 'attachmentId'))::uuid) STORED,
    CONSTRAINT board_items_pkey PRIMARY KEY (id),
    CONSTRAINT board_items_company_id_board_id_id_key UNIQUE (company_id, board_id, id),
    CONSTRAINT board_items_creator_run_attribution CHECK ((created_by_agent_id IS NULL) = (created_by_run_id IS NULL)),
    CONSTRAINT board_items_file_check CHECK ((type IN ('image', 'file')) = (attachment_id IS NOT NULL)),
    CONSTRAINT board_items_properties_check CHECK (jsonb_typeof(properties) = 'object'),
    CONSTRAINT board_items_type_check CHECK (type IN ('sticky', 'text', 'shape', 'stroke', 'image', 'file', 'frame')),
    CONSTRAINT board_items_updater_run_attribution CHECK ((updated_by_agent_id IS NULL) = (updated_by_run_id IS NULL)),
    CONSTRAINT board_items_company_id_board_id_fkey FOREIGN KEY (company_id, board_id) REFERENCES boards(company_id, id) ON DELETE CASCADE,
    CONSTRAINT board_items_company_id_board_id_parent_frame_id_fkey FOREIGN KEY (company_id, board_id, parent_frame_id) REFERENCES board_items(company_id, board_id, id) ON DELETE SET NULL (parent_frame_id) DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT board_items_file_fkey FOREIGN KEY (company_id, board_id, attachment_id) REFERENCES board_files(company_id, board_id, attachment_id)
);
CREATE INDEX board_items_board_idx ON board_items (board_id, id);
CREATE INDEX board_items_file_idx ON board_items (board_id, attachment_id) WHERE (attachment_id IS NOT NULL);
CREATE INDEX board_items_parent_idx ON board_items (board_id, parent_frame_id) WHERE (parent_frame_id IS NOT NULL);
CREATE INDEX board_items_tombstone_idx ON board_items (deleted_at) WHERE (deleted_at IS NOT NULL);

CREATE TABLE board_notes (
    item_id uuid NOT NULL,
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    state bytea NOT NULL,
    plain_text text NOT NULL,
    text_revision bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT board_notes_pkey PRIMARY KEY (item_id),
    CONSTRAINT board_notes_company_id_board_id_item_id_fkey FOREIGN KEY (company_id, board_id, item_id) REFERENCES board_items(company_id, board_id, id) ON DELETE CASCADE
);
CREATE INDEX board_notes_board_idx ON board_notes (board_id);

CREATE TABLE board_strokes (
    item_id uuid NOT NULL,
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    points jsonb NOT NULL,
    CONSTRAINT board_strokes_pkey PRIMARY KEY (item_id),
    CONSTRAINT board_strokes_points_check CHECK (jsonb_typeof(points) = 'array'),
    CONSTRAINT board_strokes_company_id_board_id_item_id_fkey FOREIGN KEY (company_id, board_id, item_id) REFERENCES board_items(company_id, board_id, id) ON DELETE CASCADE
);

CREATE TABLE board_edges (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    source_id uuid NOT NULL,
    target_id uuid NOT NULL,
    label text DEFAULT '' NOT NULL,
    relation text DEFAULT '' NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    created_by uuid NOT NULL,
    updated_by uuid NOT NULL,
    created_by_agent_id uuid,
    updated_by_agent_id uuid,
    created_by_run_id uuid,
    updated_by_run_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz,
    line text DEFAULT 'solid' NOT NULL,
    start_marker text DEFAULT 'none' NOT NULL,
    end_marker text DEFAULT 'arrow' NOT NULL,
    route text DEFAULT 'flow' NOT NULL,
    anchor_y double precision,
    source_label text DEFAULT '' NOT NULL,
    target_label text DEFAULT '' NOT NULL,
    CONSTRAINT board_edges_pkey PRIMARY KEY (id),
    CONSTRAINT board_edges_check CHECK ((source_id <> target_id) OR (route = 'message')),
    CONSTRAINT board_edges_creator_run_attribution CHECK ((created_by_agent_id IS NULL) = (created_by_run_id IS NULL)),
    CONSTRAINT board_edges_end_marker_check CHECK (end_marker IN ('none', 'arrow', 'open', 'triangle', 'diamond', 'diamond_filled', 'one', 'zero_one', 'many', 'zero_many')),
    CONSTRAINT board_edges_line_check CHECK (line IN ('solid', 'dashed')),
    CONSTRAINT board_edges_route_check CHECK (route IN ('flow', 'straight', 'elbow', 'message')),
    CONSTRAINT board_edges_start_marker_check CHECK (start_marker IN ('none', 'arrow', 'open', 'triangle', 'diamond', 'diamond_filled', 'one', 'zero_one', 'many', 'zero_many')),
    CONSTRAINT board_edges_updater_run_attribution CHECK ((updated_by_agent_id IS NULL) = (updated_by_run_id IS NULL)),
    CONSTRAINT board_edges_company_id_board_id_fkey FOREIGN KEY (company_id, board_id) REFERENCES boards(company_id, id) ON DELETE CASCADE,
    CONSTRAINT board_edges_company_id_board_id_source_id_fkey FOREIGN KEY (company_id, board_id, source_id) REFERENCES board_items(company_id, board_id, id) ON DELETE CASCADE,
    CONSTRAINT board_edges_company_id_board_id_target_id_fkey FOREIGN KEY (company_id, board_id, target_id) REFERENCES board_items(company_id, board_id, id) ON DELETE CASCADE
);
CREATE INDEX board_edges_board_idx ON board_edges (board_id, id);
CREATE INDEX board_edges_source_idx ON board_edges (board_id, source_id);
CREATE INDEX board_edges_target_idx ON board_edges (board_id, target_id);
CREATE INDEX board_edges_tombstone_idx ON board_edges (deleted_at) WHERE (deleted_at IS NOT NULL);

CREATE TABLE board_operations (
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    actor_user_id uuid NOT NULL,
    actor_run_id uuid NOT NULL,
    request_key uuid NOT NULL,
    request_hash bytea NOT NULL,
    seq bigint NOT NULL,
    ids jsonb,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT board_operations_pkey PRIMARY KEY (board_id, actor_user_id, actor_run_id, request_key),
    CONSTRAINT board_operations_company_id_board_id_fkey FOREIGN KEY (company_id, board_id) REFERENCES boards(company_id, id) ON DELETE CASCADE
);
CREATE INDEX board_operations_created_idx ON board_operations (created_at);

CREATE TABLE board_changes (
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    seq bigint NOT NULL,
    changes jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT board_changes_pkey PRIMARY KEY (board_id, seq),
    CONSTRAINT board_changes_company_id_board_id_fkey FOREIGN KEY (company_id, board_id) REFERENCES boards(company_id, id) ON DELETE CASCADE
);

CREATE TABLE wiki_pages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    collection_id uuid,
    slug text NOT NULL,
    title text NOT NULL,
    summary text DEFAULT '' NOT NULL,
    current_revision_id uuid,
    status text DEFAULT 'active' NOT NULL,
    version bigint DEFAULT 0 NOT NULL,
    created_by_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    search_vector tsvector DEFAULT ''::tsvector NOT NULL,
    next_revision_number bigint DEFAULT 1 NOT NULL,
    source_output_id uuid,
    project_id uuid,
    theme text DEFAULT 'default' NOT NULL,
    CONSTRAINT wiki_pages_pkey PRIMARY KEY (id),
    CONSTRAINT wiki_pages_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT wiki_pages_company_slug_key UNIQUE (company_id, slug),
    CONSTRAINT wiki_pages_output_document_key UNIQUE (company_id, source_output_id, id),
    CONSTRAINT wiki_project_page_key UNIQUE (company_id, project_id, id),
    CONSTRAINT wiki_pages_document_owner_check CHECK ((source_output_id IS NULL) OR (project_id IS NULL)),
    CONSTRAINT wiki_pages_status_archived_check CHECK (((status = 'active') AND (archived_at IS NULL)) OR ((status = 'archived') AND (archived_at IS NOT NULL))),
    CONSTRAINT wiki_pages_status_check CHECK (status IN ('active', 'archived')),
    CONSTRAINT wiki_pages_theme_check CHECK (theme IN ('default', 'editorial', 'compact')),
    CONSTRAINT wiki_pages_collection_fkey FOREIGN KEY (company_id, collection_id) REFERENCES wiki_collections(company_id, id) ON DELETE SET NULL (collection_id),
    CONSTRAINT wiki_pages_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT wiki_pages_source_output_fkey FOREIGN KEY (company_id, source_output_id) REFERENCES outputs(company_id, id) ON DELETE CASCADE,
    CONSTRAINT wiki_project_fkey FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id) ON DELETE CASCADE
);
CREATE INDEX wiki_pages_collection_idx ON wiki_pages (company_id, collection_id, updated_at DESC, id DESC);
CREATE INDEX wiki_pages_company_status_updated_idx ON wiki_pages (company_id, status, updated_at DESC, id DESC);
CREATE INDEX wiki_pages_search_idx ON wiki_pages USING gin (search_vector);
CREATE INDEX wiki_pages_source_output_idx ON wiki_pages (source_output_id) WHERE (source_output_id IS NOT NULL);

CREATE TABLE wiki_revisions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    page_id uuid NOT NULL,
    number bigint NOT NULL,
    title text NOT NULL,
    body text NOT NULL,
    summary text DEFAULT '' NOT NULL,
    collection_id uuid,
    author_user_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    block_content jsonb DEFAULT '{}'::jsonb NOT NULL,
    published_at timestamptz,
    author_agent_id uuid,
    CONSTRAINT wiki_revisions_pkey PRIMARY KEY (id),
    CONSTRAINT wiki_revisions_company_key UNIQUE (company_id, id),
    CONSTRAINT wiki_revisions_company_page_id_key UNIQUE (company_id, page_id, id),
    CONSTRAINT wiki_revisions_page_number_key UNIQUE (page_id, number),
    CONSTRAINT wiki_revisions_block_content_check CHECK (jsonb_typeof(block_content) = 'object'),
    CONSTRAINT wiki_revisions_collection_fkey FOREIGN KEY (company_id, collection_id) REFERENCES wiki_collections(company_id, id) ON DELETE SET NULL (collection_id),
    CONSTRAINT wiki_revisions_page_fkey FOREIGN KEY (company_id, page_id) REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE
);
CREATE INDEX wiki_revisions_collection_idx ON wiki_revisions (company_id, collection_id) WHERE (collection_id IS NOT NULL);

CREATE TABLE wiki_links (
    company_id uuid NOT NULL,
    from_page_id uuid NOT NULL,
    to_page_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT wiki_links_pkey PRIMARY KEY (company_id, from_page_id, to_page_id),
    CONSTRAINT wiki_links_no_self_check CHECK (from_page_id <> to_page_id),
    CONSTRAINT wiki_links_from_fkey FOREIGN KEY (company_id, from_page_id) REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE,
    CONSTRAINT wiki_links_to_fkey FOREIGN KEY (company_id, to_page_id) REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE
);
CREATE INDEX wiki_links_to_idx ON wiki_links (company_id, to_page_id, from_page_id);

CREATE TABLE wiki_favorites (
    company_id uuid NOT NULL,
    page_id uuid NOT NULL,
    user_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT wiki_favorites_pkey PRIMARY KEY (company_id, page_id, user_id),
    CONSTRAINT wiki_favorites_page_fkey FOREIGN KEY (company_id, page_id) REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE,
    CONSTRAINT wiki_favorites_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX wiki_favorites_user_idx ON wiki_favorites (company_id, user_id, page_id);

CREATE TABLE wiki_artifact_refs (
    company_id uuid NOT NULL,
    page_id uuid NOT NULL,
    output_id uuid NOT NULL,
    ordinal integer,
    created_at timestamptz DEFAULT statement_timestamp() NOT NULL,
    CONSTRAINT wiki_artifact_refs_key UNIQUE NULLS NOT DISTINCT (company_id, page_id, output_id, ordinal),
    CONSTRAINT wiki_artifact_refs_item_fkey FOREIGN KEY (output_id, ordinal) REFERENCES output_items(output_id, ordinal) ON DELETE CASCADE,
    CONSTRAINT wiki_artifact_refs_output_fkey FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE CASCADE,
    CONSTRAINT wiki_artifact_refs_page_fkey FOREIGN KEY (company_id, page_id) REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE
);
CREATE INDEX wiki_artifact_refs_output_idx ON wiki_artifact_refs (output_id, ordinal, page_id);

CREATE TABLE wiki_canvas_drafts (
    company_id uuid NOT NULL,
    page_id uuid NOT NULL,
    state bytea NOT NULL,
    markdown text NOT NULL,
    head_seq bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    schema_version integer DEFAULT 0 NOT NULL,
    content jsonb DEFAULT '{}'::jsonb NOT NULL,
    legacy_state bytea,
    legacy_markdown text,
    structure_seq bigint DEFAULT 0 NOT NULL,
    title text DEFAULT '' NOT NULL,
    summary text DEFAULT '' NOT NULL,
    collection_id uuid,
    restored_revision_id uuid,
    CONSTRAINT wiki_canvas_drafts_pkey PRIMARY KEY (page_id),
    CONSTRAINT wiki_canvas_drafts_content_check CHECK (jsonb_typeof(content) = 'object'),
    CONSTRAINT wiki_canvas_drafts_schema_version_check CHECK (schema_version = ANY (ARRAY[0, 1])),
    CONSTRAINT wiki_canvas_drafts_structure_seq_check CHECK ((structure_seq >= 0) AND (structure_seq <= head_seq)),
    CONSTRAINT wiki_canvas_drafts_collection_fkey FOREIGN KEY (company_id, collection_id) REFERENCES wiki_collections(company_id, id) ON DELETE SET NULL (collection_id),
    CONSTRAINT wiki_canvas_drafts_company_id_page_id_fkey FOREIGN KEY (company_id, page_id) REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE,
    CONSTRAINT wiki_canvas_drafts_restored_revision_fkey FOREIGN KEY (company_id, page_id, restored_revision_id) REFERENCES wiki_revisions(company_id, page_id, id)
);
CREATE INDEX wiki_canvas_drafts_collection_idx ON wiki_canvas_drafts (company_id, collection_id) WHERE (collection_id IS NOT NULL);

CREATE TABLE wiki_canvas_checkpoints (
    company_id uuid NOT NULL,
    page_id uuid NOT NULL,
    revision_id uuid NOT NULL,
    through_seq bigint NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT wiki_canvas_checkpoints_pkey PRIMARY KEY (revision_id),
    CONSTRAINT wiki_canvas_checkpoints_company_id_page_id_revision_id_fkey FOREIGN KEY (company_id, page_id, revision_id) REFERENCES wiki_revisions(company_id, page_id, id) ON DELETE CASCADE
);
CREATE INDEX wiki_canvas_checkpoints_page_seq_idx ON wiki_canvas_checkpoints (company_id, page_id, through_seq);

CREATE TABLE wiki_block_ids (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    page_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT wiki_block_ids_pkey PRIMARY KEY (id),
    CONSTRAINT wiki_block_ids_scope_key UNIQUE (company_id, page_id, id),
    CONSTRAINT wiki_block_ids_company_id_page_id_fkey FOREIGN KEY (company_id, page_id) REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE
);
CREATE INDEX wiki_block_ids_page ON wiki_block_ids (page_id);

CREATE TABLE page_suggestion_reviews (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    change_id uuid NOT NULL,
    actor_user_id uuid NOT NULL,
    request_key uuid NOT NULL,
    fingerprint text NOT NULL,
    action text NOT NULL,
    selection jsonb NOT NULL,
    feedback text DEFAULT '' NOT NULL,
    before_sequence bigint NOT NULL,
    after_sequence bigint NOT NULL,
    result jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    before_revision_id uuid,
    result_revision_id uuid,
    before_theme text,
    result_theme text,
    CONSTRAINT page_suggestion_reviews_pkey PRIMARY KEY (id),
    CONSTRAINT page_suggestion_reviews_company_id_actor_user_id_request_ke_key UNIQUE (company_id, actor_user_id, request_key),
    CONSTRAINT page_suggestion_reviews_action_check CHECK (action IN ('apply', 'reject', 'undo', 'request_changes', 'resolve')),
    CONSTRAINT page_suggestion_reviews_after_sequence_check CHECK (after_sequence >= 0),
    CONSTRAINT page_suggestion_reviews_before_sequence_check CHECK (before_sequence >= 0),
    CONSTRAINT page_suggestion_reviews_before_theme_check CHECK (before_theme IN ('default', 'editorial', 'compact')),
    CONSTRAINT page_suggestion_reviews_result_theme_check CHECK (result_theme IN ('default', 'editorial', 'compact')),
    CONSTRAINT page_suggestion_reviews_before_revision_fkey FOREIGN KEY (company_id, before_revision_id) REFERENCES wiki_revisions(company_id, id),
    CONSTRAINT page_suggestion_reviews_company_id_change_id_fkey FOREIGN KEY (company_id, change_id) REFERENCES project_page_changes(company_id, id) ON DELETE CASCADE,
    CONSTRAINT page_suggestion_reviews_result_revision_fkey FOREIGN KEY (company_id, result_revision_id) REFERENCES wiki_revisions(company_id, id)
);
CREATE INDEX page_suggestion_review_history ON page_suggestion_reviews (company_id, change_id, created_at, id);

CREATE TABLE document_changes (
    company_id uuid NOT NULL,
    page_id uuid NOT NULL,
    sequence bigint NOT NULL,
    actor_type text NOT NULL,
    actor_id uuid,
    change_type text NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    delta bytea NOT NULL,
    checkpoint bytea,
    label text DEFAULT '' NOT NULL,
    CONSTRAINT document_changes_pkey PRIMARY KEY (company_id, page_id, sequence),
    CONSTRAINT document_changes_actor_type_check CHECK (actor_type IN ('user', 'agent', 'system')),
    CONSTRAINT document_changes_change_type_check CHECK (change_type IN ('initial', 'edit', 'restore')),
    CONSTRAINT document_changes_check CHECK (((actor_type = 'system') AND (actor_id IS NULL)) OR ((actor_type <> 'system') AND (actor_id IS NOT NULL))),
    CONSTRAINT document_changes_check1 CHECK ((change_type <> 'initial') OR (checkpoint IS NOT NULL)),
    CONSTRAINT document_changes_label_check CHECK (length(label) <= 200),
    CONSTRAINT document_changes_sequence_check CHECK (sequence > 0),
    CONSTRAINT document_changes_storage_check CHECK (((checkpoint IS NOT NULL) AND (octet_length(delta) = 0)) OR ((checkpoint IS NULL) AND (octet_length(delta) > 0))),
    CONSTRAINT document_changes_company_id_page_id_fkey FOREIGN KEY (company_id, page_id) REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE
);
CREATE INDEX document_changes_actor ON document_changes (company_id, page_id, actor_id, sequence DESC);
CREATE INDEX document_changes_checkpoint ON document_changes (company_id, page_id, sequence DESC) WHERE (checkpoint IS NOT NULL);
CREATE INDEX document_changes_time ON document_changes (company_id, page_id, created_at DESC, sequence DESC);

CREATE TABLE document_writing_requests (
    company_id uuid NOT NULL,
    run_id uuid NOT NULL,
    page_id uuid NOT NULL,
    request_key uuid NOT NULL,
    target jsonb NOT NULL,
    before_content jsonb NOT NULL,
    sequence bigint NOT NULL,
    created_at timestamptz DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT document_writing_requests_pkey PRIMARY KEY (run_id),
    CONSTRAINT document_writing_requests_sequence_check CHECK (sequence >= 0),
    CONSTRAINT document_writing_requests_company_id_page_id_fkey FOREIGN KEY (company_id, page_id) REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE,
    CONSTRAINT document_writing_requests_company_id_run_id_fkey FOREIGN KEY (company_id, run_id) REFERENCES runs(company_id, id) ON DELETE CASCADE
);

-- Links to these tables from tables created earlier.
ALTER TABLE project_canvases ADD CONSTRAINT project_overview_page_fkey FOREIGN KEY (company_id, project_id, overview_page_id) REFERENCES wiki_pages(company_id, project_id, id) ON DELETE SET NULL (overview_page_id);
ALTER TABLE project_page_changes ADD CONSTRAINT project_page_changes_company_id_project_id_page_id_fkey FOREIGN KEY (company_id, project_id, page_id) REFERENCES wiki_pages(company_id, project_id, id);
ALTER TABLE project_page_managed_blocks ADD CONSTRAINT project_page_managed_blocks_company_id_page_id_block_id_fkey FOREIGN KEY (company_id, page_id, block_id) REFERENCES wiki_block_ids(company_id, page_id, id) ON DELETE CASCADE;
ALTER TABLE project_artifact_versions ADD CONSTRAINT project_artifact_versions_company_id_wiki_page_id_wiki_rev_fkey FOREIGN KEY (company_id, wiki_page_id, wiki_revision_id) REFERENCES wiki_revisions(company_id, page_id, id) ON DELETE SET NULL (wiki_page_id, wiki_revision_id);
ALTER TABLE project_presentations ADD CONSTRAINT project_presentations_company_id_project_id_page_id_fkey FOREIGN KEY (company_id, project_id, page_id) REFERENCES wiki_pages(company_id, project_id, id);
ALTER TABLE project_presentation_sources ADD CONSTRAINT project_presentation_sources_company_id_wiki_page_id_wiki__fkey FOREIGN KEY (company_id, wiki_page_id, wiki_revision_id) REFERENCES wiki_revisions(company_id, page_id, id);
ALTER TABLE run_wiki_reads ADD CONSTRAINT run_wiki_reads_page_fkey FOREIGN KEY (company_id, page_id) REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE;
ALTER TABLE run_wiki_reads ADD CONSTRAINT run_wiki_reads_revision_id_fkey FOREIGN KEY (revision_id) REFERENCES wiki_revisions(id) ON DELETE CASCADE;
ALTER TABLE review_threads ADD CONSTRAINT review_threads_document_fkey FOREIGN KEY (company_id, page_id) REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE;
ALTER TABLE review_threads ADD CONSTRAINT review_threads_document_revision_fkey FOREIGN KEY (company_id, document_revision_id) REFERENCES wiki_revisions(company_id, id) ON DELETE CASCADE;
ALTER TABLE review_threads ADD CONSTRAINT review_threads_page_fkey FOREIGN KEY (company_id, project_id, page_id) REFERENCES wiki_pages(company_id, project_id, id) ON DELETE CASCADE;
ALTER TABLE output_documents ADD CONSTRAINT output_documents_company_id_page_id_fkey FOREIGN KEY (company_id, page_id) REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE;
ALTER TABLE run_wiki_writes ADD CONSTRAINT run_wiki_writes_page_fkey FOREIGN KEY (company_id, page_id) REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE;
ALTER TABLE wiki_pages ADD CONSTRAINT wiki_pages_current_revision_fkey FOREIGN KEY (company_id, id, current_revision_id) REFERENCES wiki_revisions(company_id, page_id, id) DEFERRABLE INITIALLY DEFERRED;

-- attachment_access: who may read a file. A file with an active project_files
-- row follows the readers of those Projects and nobody else, wherever it was
-- shared. A Board's files follow its Whiteboard Artifact. Otherwise a file
-- attached to a message follows the chat's readers, and a file in neither
-- follows its uploader. An Artifact file the person cannot open through
-- output_access is excluded. The view says who, not whether the bytes are
-- ready: callers keep their own attachments.status check. List queries reach
-- it through a correlated lookup (see the note above conversation_readers).
--
-- The chat arm follows the same rule inside the view: each member of the
-- file's Company is a candidate, checked by a LATERAL ... LIMIT 1 lookup that
-- names both the chat and the person, so each conversation_readers arm answers
-- from its own indexes. Joined on the chat instead, Postgres would not push
-- the chat id into the UNION ALL arms, and every file lookup would build the
-- person's whole readable set of chats before matching the one chat. One row
-- per file and reader.
CREATE VIEW attachment_access AS
SELECT arm.company_id, arm.attachment_id, arm.user_id
FROM (
    -- A Board's file and its preview: each person is probed against the
    -- Whiteboard Artifact, as a message file is against its chat.
    SELECT file.company_id, file.attachment_id, candidate.user_id
    FROM board_files AS file
    JOIN company_members AS candidate
      ON candidate.company_id = file.company_id
    CROSS JOIN LATERAL (
        SELECT 1
        FROM output_access AS access
        WHERE access.company_id = file.company_id
          AND access.output_id = file.board_id
          AND access.user_id = candidate.user_id
        LIMIT 1
    ) AS reads_board
    UNION ALL
    SELECT file.company_id, file.preview_attachment_id, candidate.user_id
    FROM board_files AS file
    JOIN company_members AS candidate
      ON candidate.company_id = file.company_id
    CROSS JOIN LATERAL (
        SELECT 1
        FROM output_access AS access
        WHERE access.company_id = file.company_id
          AND access.output_id = file.board_id
          AND access.user_id = candidate.user_id
        LIMIT 1
    ) AS reads_board
    WHERE file.preview_attachment_id IS NOT NULL
    UNION ALL
    SELECT DISTINCT file.company_id, file.attachment_id, access.user_id
    FROM project_attachment_scopes AS file
    JOIN project_access AS access
      ON access.company_id = file.company_id AND access.project_id = file.project_id
    WHERE file.removed_at IS NULL
    UNION ALL
    SELECT link.company_id, link.attachment_id, candidate.user_id
    FROM message_attachments AS link
    JOIN conversation_items AS item
      ON item.company_id = link.company_id
     AND item.id = link.message_id
     AND item.deleted_at IS NULL
    JOIN company_members AS candidate
      ON candidate.company_id = link.company_id
    CROSS JOIN LATERAL (
        SELECT 1
        FROM conversation_readers AS reader
        WHERE reader.company_id = item.company_id
          AND reader.conversation_id = item.conversation_id
          AND reader.user_id = candidate.user_id
        LIMIT 1
    ) AS reads_chat
    WHERE NOT EXISTS (
        SELECT 1 FROM project_attachment_scopes AS file
        WHERE file.company_id = link.company_id
          AND file.attachment_id = link.attachment_id
          AND file.removed_at IS NULL
    )
    UNION ALL
    SELECT attachment.company_id, attachment.id, member.user_id
    FROM attachments AS attachment
    JOIN companies AS company
      ON company.id = attachment.company_id AND company.deleting_at IS NULL
    JOIN company_members AS member
      ON member.company_id = attachment.company_id
     AND member.user_id = attachment.uploaded_by_user_id
     AND member.status = 'active'
    JOIN users AS account
      ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
    WHERE NOT EXISTS (
        SELECT 1 FROM message_attachments AS link
        WHERE link.company_id = attachment.company_id AND link.attachment_id = attachment.id
    )
      AND NOT EXISTS (
        SELECT 1 FROM project_attachment_scopes AS file
        WHERE file.company_id = attachment.company_id
          AND file.attachment_id = attachment.id
          AND file.removed_at IS NULL
    )
) AS arm
WHERE NOT EXISTS (
    SELECT 1
    FROM asset_versions AS artifact_version
    JOIN output_items AS artifact_item
      ON artifact_item.asset_version_id = artifact_version.id
     AND artifact_item.company_id = artifact_version.company_id
    WHERE artifact_version.attachment_id = arm.attachment_id
      AND artifact_version.company_id = arm.company_id
      AND NOT EXISTS (
          SELECT 1 FROM output_access AS access
          WHERE access.company_id = artifact_item.company_id
            AND access.output_id = artifact_item.output_id
            AND access.user_id = arm.user_id
      )
);

CREATE VIEW canvas_version_sources AS
 SELECT v.*, COALESCE(v.output_id,p.source_output_id) AS access_output_id
 FROM project_artifact_versions v LEFT JOIN wiki_pages p ON p.company_id=v.company_id AND p.id=v.wiki_page_id;

CREATE VIEW company_wiki_pages AS SELECT * FROM wiki_pages WHERE source_output_id IS NULL AND project_id IS NULL;
