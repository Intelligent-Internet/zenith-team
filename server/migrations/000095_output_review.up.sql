-- Immutable Outputs can be reviewed without a Project. Access remains output_access.
CREATE TABLE output_review_threads (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 company_id uuid NOT NULL, output_id uuid NOT NULL,
 anchor jsonb NOT NULL CHECK (jsonb_typeof(anchor) = 'object'),
 status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
 revision bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (company_id, output_id, id),
 FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE CASCADE
);
CREATE INDEX output_review_threads_output ON output_review_threads(output_id, created_at, id);
CREATE TABLE output_review_comments (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 company_id uuid NOT NULL, output_id uuid NOT NULL, thread_id uuid NOT NULL,
 author_user_id uuid NOT NULL, body text NOT NULL,
 revision bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), edited_at timestamptz, deleted_at timestamptz,
 FOREIGN KEY (company_id, output_id, thread_id) REFERENCES output_review_threads(company_id, output_id, id) ON DELETE CASCADE
);
CREATE INDEX output_review_comments_thread ON output_review_comments(thread_id, created_at, id);
CREATE TABLE output_review_operations (
 company_id uuid NOT NULL, output_id uuid NOT NULL, actor_user_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 request_key uuid NOT NULL, fingerprint bytea NOT NULL, result_id uuid NOT NULL,
 PRIMARY KEY (output_id, actor_user_id, request_key),
 FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE CASCADE
);
