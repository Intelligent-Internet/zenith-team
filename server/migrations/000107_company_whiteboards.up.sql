-- Company Whiteboards and human/Agent collaboration.
-- Size and geometry limits live at the service write boundary; the schema owns
-- closed vocabularies, structural JSON rules and Company-scoped relationships.

CREATE TABLE boards (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    title text NOT NULL,
    purpose text NOT NULL DEFAULT '',
    visibility text NOT NULL DEFAULT 'private' CHECK (visibility IN ('private','company','public')),
    company_access text NOT NULL DEFAULT 'read' CHECK (company_access IN ('read','edit')),
    public_token uuid UNIQUE,
    CONSTRAINT boards_visibility_token_check CHECK ((visibility='public') = (public_token IS NOT NULL)),
    head_seq bigint NOT NULL DEFAULT 0,
    revision bigint NOT NULL DEFAULT 1,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    archived_at timestamptz,
    UNIQUE (company_id, id)
);

CREATE INDEX boards_library_idx ON boards(company_id, updated_at DESC, id);

CREATE TABLE board_member_access (
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    user_id uuid NOT NULL,
    access text NOT NULL CHECK (access IN ('read','edit','manage')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (board_id,user_id),
    FOREIGN KEY (company_id,board_id) REFERENCES boards(company_id,id) ON DELETE CASCADE,
    FOREIGN KEY (company_id,user_id) REFERENCES company_members(company_id,user_id) ON DELETE CASCADE
);

-- Agent access intersects the live Run author's own Board access.
CREATE TABLE board_agent_access (
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    access text NOT NULL CHECK (access IN ('read','edit')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (board_id,agent_id),
    FOREIGN KEY (company_id,board_id) REFERENCES boards(company_id,id) ON DELETE CASCADE,
    FOREIGN KEY (company_id,agent_id) REFERENCES agents(company_id,id) ON DELETE CASCADE
);

-- One resource access projection serves HTTP, libraries, media and audiences.
-- Company permissions remain in Go; no role grants are inferred here.
CREATE VIEW board_access AS
SELECT b.company_id,b.id AS board_id,m.user_id,
 CASE WHEN b.created_by=m.user_id OR g.access='manage' THEN 'manage'
      WHEN g.access='edit' OR (b.visibility='company' AND b.company_access='edit') THEN 'edit'
      ELSE 'read' END::text AS access
FROM boards b
JOIN companies c ON c.id=b.company_id AND c.deleting_at IS NULL
JOIN company_members m ON m.company_id=b.company_id AND m.status='active'
JOIN users u ON u.id=m.user_id AND u.disabled_at IS NULL AND u.deleted_at IS NULL
LEFT JOIN board_member_access g ON g.company_id=b.company_id AND g.board_id=b.id AND g.user_id=m.user_id
WHERE b.created_by=m.user_id OR g.user_id IS NOT NULL OR b.visibility IN ('company','public');

CREATE TABLE board_favorites (
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (board_id, user_id),
    FOREIGN KEY (company_id, board_id) REFERENCES boards(company_id, id) ON DELETE CASCADE
);

CREATE TABLE board_create_receipts (
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    actor_user_id uuid NOT NULL,
    request_key uuid NOT NULL,
    board_id uuid NOT NULL,
    request_hash bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (company_id, actor_user_id, request_key),
    FOREIGN KEY (company_id, board_id) REFERENCES boards(company_id, id) ON DELETE CASCADE
);

CREATE INDEX board_create_receipts_created_idx ON board_create_receipts(created_at);

-- Files are ordinary Company attachments that a Board references. An image or
-- file item places one registered file; images also keep a server-generated
-- preview attachment and their pixel dimensions.
CREATE TABLE board_files (
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    attachment_id uuid NOT NULL,
    preview_attachment_id uuid,
    width integer CHECK (width > 0),
    height integer CHECK (height > 0),
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (company_id, board_id, attachment_id),
    CONSTRAINT board_files_dimensions_check CHECK ((width IS NULL) = (height IS NULL)),
    FOREIGN KEY (company_id, board_id) REFERENCES boards(company_id, id) ON DELETE CASCADE,
    FOREIGN KEY (company_id, attachment_id) REFERENCES attachments(company_id, id) ON DELETE CASCADE,
    FOREIGN KEY (company_id, preview_attachment_id) REFERENCES attachments(company_id, id)
        ON DELETE SET NULL (preview_attachment_id)
);

CREATE INDEX board_files_attachment_idx ON board_files(company_id, attachment_id);
-- Retention releases references no item places once they are old enough.
CREATE INDEX board_files_created_idx ON board_files(created_at);
CREATE INDEX board_files_preview_idx ON board_files(company_id, preview_attachment_id)
    WHERE preview_attachment_id IS NOT NULL;

-- Lifecycle revisions fence text sessions buffered before deletion/restoration.
-- File placements reference a file registered on the same Board.
CREATE TABLE board_items (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    type text NOT NULL CHECK (type IN ('sticky', 'text', 'shape', 'stroke', 'image', 'file', 'frame')),
    x double precision NOT NULL,
    y double precision NOT NULL,
    width double precision NOT NULL,
    height double precision NOT NULL,
    rotation double precision NOT NULL DEFAULT 0,
    stack_order bigint NOT NULL DEFAULT 0,
    parent_frame_id uuid,
    reading_order bigint NOT NULL DEFAULT 0,
    properties jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(properties)='object'),
    geometry_revision bigint NOT NULL DEFAULT 1,
    content_revision bigint NOT NULL DEFAULT 1,
    created_by uuid NOT NULL,
    updated_by uuid NOT NULL,
    created_by_agent_id uuid,
    updated_by_agent_id uuid,
    created_by_run_id uuid,
    updated_by_run_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    lifecycle_revision bigint NOT NULL DEFAULT 1,
    attachment_id uuid GENERATED ALWAYS AS ((properties->>'attachmentId')::uuid) STORED,
    UNIQUE (company_id, board_id, id),
    CONSTRAINT board_items_file_check CHECK ((type IN ('image', 'file')) = (attachment_id IS NOT NULL)),
    CONSTRAINT board_items_file_fkey FOREIGN KEY (company_id, board_id, attachment_id)
        REFERENCES board_files(company_id, board_id, attachment_id),
    CONSTRAINT board_items_creator_run_attribution CHECK ((created_by_agent_id IS NULL) = (created_by_run_id IS NULL)),
    CONSTRAINT board_items_updater_run_attribution CHECK ((updated_by_agent_id IS NULL) = (updated_by_run_id IS NULL)),
    FOREIGN KEY (company_id, board_id) REFERENCES boards(company_id, id) ON DELETE CASCADE,
    -- Purging a frame leaves its children on the Board, outside any frame.
    FOREIGN KEY (company_id, board_id, parent_frame_id) REFERENCES board_items(company_id, board_id, id)
        ON DELETE SET NULL (parent_frame_id) DEFERRABLE INITIALLY DEFERRED
);

CREATE INDEX board_items_board_idx ON board_items(board_id, id);
-- Deleting a frame detaches its children, and purging one clears their
-- parent, without scanning the whole Board.
CREATE INDEX board_items_parent_idx ON board_items(board_id, parent_frame_id) WHERE parent_frame_id IS NOT NULL;
CREATE INDEX board_items_tombstone_idx ON board_items(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX board_items_file_idx ON board_items(board_id, attachment_id) WHERE attachment_id IS NOT NULL;

CREATE TABLE board_notes (
    item_id uuid PRIMARY KEY,
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    state bytea NOT NULL,
    plain_text text NOT NULL,
    text_revision bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (company_id, board_id, item_id) REFERENCES board_items(company_id, board_id, id) ON DELETE CASCADE
);

CREATE INDEX board_notes_search_idx ON board_notes USING gin(to_tsvector('simple', plain_text));
CREATE INDEX board_notes_board_idx ON board_notes(board_id);

CREATE TABLE board_strokes (
    item_id uuid PRIMARY KEY,
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    points jsonb NOT NULL CHECK (jsonb_typeof(points)='array'),
    FOREIGN KEY (company_id, board_id, item_id) REFERENCES board_items(company_id, board_id, id) ON DELETE CASCADE
);

CREATE TABLE board_edges (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    source_id uuid NOT NULL,
    target_id uuid NOT NULL,
    label text NOT NULL DEFAULT '',
    relation text NOT NULL DEFAULT '',
    revision bigint NOT NULL DEFAULT 1,
    created_by uuid NOT NULL,
    updated_by uuid NOT NULL,
    created_by_agent_id uuid,
    updated_by_agent_id uuid,
    created_by_run_id uuid,
    updated_by_run_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    CONSTRAINT board_edges_creator_run_attribution CHECK ((created_by_agent_id IS NULL) = (created_by_run_id IS NULL)),
    CONSTRAINT board_edges_updater_run_attribution CHECK ((updated_by_agent_id IS NULL) = (updated_by_run_id IS NULL)),
    FOREIGN KEY (company_id, board_id) REFERENCES boards(company_id, id) ON DELETE CASCADE,
    FOREIGN KEY (company_id, board_id, source_id) REFERENCES board_items(company_id, board_id, id) ON DELETE CASCADE,
    FOREIGN KEY (company_id, board_id, target_id) REFERENCES board_items(company_id, board_id, id) ON DELETE CASCADE,
    CHECK (source_id<>target_id)
);

CREATE INDEX board_edges_board_idx ON board_edges(board_id, id);
-- Deleting an item removes its connectors, and purging it cascades to them,
-- by endpoint rather than by scanning the Board.
CREATE INDEX board_edges_source_idx ON board_edges(board_id, source_id);
CREATE INDEX board_edges_target_idx ON board_edges(board_id, target_id);
CREATE INDEX board_edges_tombstone_idx ON board_edges(deleted_at) WHERE deleted_at IS NOT NULL;

-- Retry receipts keep only the acknowledgement: the request hash, the
-- draft-to-ID mapping of created objects and the committed sequence. A retry
-- answers with the objects' current state. Member calls use the nil Run ID so
-- the key is a plain primary key. Retention drops receipts after 30 days.
CREATE TABLE board_operations (
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    actor_user_id uuid NOT NULL,
    actor_run_id uuid NOT NULL,
    request_key uuid NOT NULL,
    request_hash bytea NOT NULL,
    seq bigint NOT NULL,
    ids jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (board_id, actor_user_id, actor_run_id, request_key),
    FOREIGN KEY (company_id, board_id) REFERENCES boards(company_id, id) ON DELETE CASCADE
);

CREATE INDEX board_operations_created_idx ON board_operations(created_at);

CREATE TABLE board_changes (
    company_id uuid NOT NULL,
    board_id uuid NOT NULL,
    seq bigint NOT NULL,
    changes jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (board_id, seq),
    FOREIGN KEY (company_id, board_id) REFERENCES boards(company_id, id) ON DELETE CASCADE
);

CREATE OR REPLACE VIEW attachment_access AS
SELECT arm.company_id, arm.attachment_id, arm.user_id
FROM (
    SELECT file.company_id, file.attachment_id, access.user_id
    FROM board_files AS file
    JOIN board_access AS access ON access.company_id = file.company_id AND access.board_id = file.board_id
    UNION ALL
    SELECT file.company_id, file.preview_attachment_id, access.user_id
    FROM board_files AS file
    JOIN board_access AS access ON access.company_id = file.company_id AND access.board_id = file.board_id
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
