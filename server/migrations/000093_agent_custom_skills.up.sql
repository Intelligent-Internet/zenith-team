CREATE TABLE skill_imports (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL,
    created_by_user_id UUID NOT NULL,
    source_type TEXT NOT NULL CHECK (source_type IN ('github', 'zip', 'markdown')),
    source_url TEXT NOT NULL DEFAULT '',
    source_commit TEXT NOT NULL DEFAULT '',
    ready BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT now() + interval '24 hours',
    UNIQUE (company_id, id),
    FOREIGN KEY (company_id, agent_id) REFERENCES agents(company_id, id) ON DELETE CASCADE
);
CREATE INDEX skill_imports_agent ON skill_imports(company_id, agent_id, created_at);

CREATE TABLE skill_versions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    import_id UUID NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL,
    source_path TEXT NOT NULL,
    content_hash TEXT NOT NULL CHECK (content_hash ~ '^[a-f0-9]{64}$'),
    size_bytes BIGINT NOT NULL,
    file_count INTEGER NOT NULL,
    manifest JSONB NOT NULL CHECK (jsonb_typeof(manifest) = 'array'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (company_id, id),
    UNIQUE (import_id, name),
    FOREIGN KEY (company_id, import_id) REFERENCES skill_imports(company_id, id) ON DELETE CASCADE
);

CREATE TABLE agent_skills (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL,
    version_id UUID NOT NULL,
    name TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (company_id, agent_id, name),
    FOREIGN KEY (company_id, agent_id) REFERENCES agents(company_id, id) ON DELETE CASCADE,
    FOREIGN KEY (company_id, version_id) REFERENCES skill_versions(company_id, id)
);

CREATE INDEX agent_skills_version ON agent_skills(version_id);

CREATE TABLE run_skills (
    company_id UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    run_id UUID NOT NULL,
    version_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, version_id),
    FOREIGN KEY (company_id, run_id) REFERENCES runs(company_id, id) ON DELETE CASCADE,
    FOREIGN KEY (company_id, version_id) REFERENCES skill_versions(company_id, id)
);
CREATE INDEX run_skills_version ON run_skills(version_id);
