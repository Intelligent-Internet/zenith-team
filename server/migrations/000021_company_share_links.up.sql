CREATE TABLE company_share_links (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 company_id uuid NOT NULL UNIQUE REFERENCES companies(id) ON DELETE CASCADE,
 token_hash bytea NOT NULL UNIQUE,
 encrypted_token bytea,
 created_by_user_id uuid NOT NULL,
 expires_at timestamptz NOT NULL,
 revoked_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 version bigint NOT NULL DEFAULT 1
);
