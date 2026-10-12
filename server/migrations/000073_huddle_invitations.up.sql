CREATE TABLE huddle_invitations (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    company_id uuid NOT NULL,
    huddle_id uuid NOT NULL,
    inviter_user_id uuid NOT NULL,
    invitee_user_id uuid NOT NULL,
    request_key uuid NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','accepted','declined','expired','revoked')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision BETWEEN 1 AND 9007199254740991),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id),
    UNIQUE (company_id,id),
    UNIQUE (company_id,huddle_id,invitee_user_id,request_key),
    FOREIGN KEY (company_id,huddle_id) REFERENCES huddles(company_id,id) ON DELETE CASCADE,
    FOREIGN KEY (company_id,inviter_user_id) REFERENCES company_members(company_id,user_id) ON DELETE RESTRICT,
    FOREIGN KEY (company_id,invitee_user_id) REFERENCES company_members(company_id,user_id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX huddle_invitations_pending_idx ON huddle_invitations(company_id,huddle_id,invitee_user_id) WHERE status='pending';
