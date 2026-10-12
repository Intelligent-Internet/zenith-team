ALTER TABLE companies
    ADD COLUMN avatar_attachment_id uuid,
    ADD CONSTRAINT companies_avatar_attachment_fkey
        FOREIGN KEY (id, avatar_attachment_id)
        REFERENCES attachments(company_id, id)
        ON DELETE SET NULL (avatar_attachment_id);

CREATE INDEX companies_avatar_attachment_idx ON companies(avatar_attachment_id)
    WHERE avatar_attachment_id IS NOT NULL;
