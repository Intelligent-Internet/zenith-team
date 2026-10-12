CREATE TABLE company_invitations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
 email text NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','accepted','revoked','expired')),
 generation integer NOT NULL DEFAULT 1,
 invited_by_user_id uuid NOT NULL,
 accepted_user_id uuid,
 expires_at timestamptz NOT NULL,
 renewal_requested_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 version bigint NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX company_invitations_pending_email_idx ON company_invitations(company_id,lower(email)) WHERE status='pending';
CREATE INDEX company_invitations_company_idx ON company_invitations(company_id,created_at DESC,id DESC);
CREATE TABLE company_invitation_tokens (
 invitation_id uuid NOT NULL REFERENCES company_invitations(id) ON DELETE CASCADE,
 generation integer NOT NULL,
 token_hash bytea NOT NULL UNIQUE,
 encrypted_token bytea,
 recipient_email text NOT NULL,
 company_name text NOT NULL,
 inviter_name text NOT NULL,
 expires_at timestamptz NOT NULL,
 delivery_status text NOT NULL DEFAULT 'queued' CHECK (delivery_status IN ('queued','sending','sent','failed','cancelled')),
 attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease_until timestamptz,
 provider_id text,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (invitation_id,generation)
);
CREATE INDEX company_invitation_delivery_idx ON company_invitation_tokens(next_attempt_at) WHERE delivery_status IN ('queued','sending');
ALTER TABLE inbox_items DROP CONSTRAINT inbox_items_kind_check,
ADD CONSTRAINT inbox_items_kind_check CHECK (kind IN ('task_assigned','blocker','run_completed','run_failed','approval_request','question_request','runtime_offline','invitation_renewal'));
