BEGIN;

CREATE TABLE wiki_collections (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    name text NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT wiki_collections_pkey PRIMARY KEY (id),
    CONSTRAINT wiki_collections_company_id_id_key UNIQUE (company_id, id)
);
CREATE UNIQUE INDEX wiki_collections_company_name_key
    ON wiki_collections (company_id, lower(name));

CREATE TABLE wiki_pages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    collection_id uuid,
    slug text NOT NULL,
    title text NOT NULL,
    summary text NOT NULL DEFAULT '',
    current_revision_id uuid,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    version bigint NOT NULL DEFAULT 0,
    created_by_user_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    archived_at timestamptz,
    -- The current revision's title and body as 'simple' lexemes, rewritten
    -- with current_revision_id so search reads one indexed page row instead
    -- of tokenizing every current body.
    search_vector tsvector NOT NULL DEFAULT ''::tsvector,
    CONSTRAINT wiki_pages_pkey PRIMARY KEY (id),
    CONSTRAINT wiki_pages_company_id_id_key UNIQUE (company_id, id),
    CONSTRAINT wiki_pages_company_slug_key UNIQUE (company_id, slug),
    CONSTRAINT wiki_pages_collection_fkey FOREIGN KEY (company_id, collection_id)
        REFERENCES wiki_collections(company_id, id) ON DELETE SET NULL (collection_id),
    CONSTRAINT wiki_pages_status_archived_check CHECK
        ((status = 'active' AND archived_at IS NULL) OR
         (status = 'archived' AND archived_at IS NOT NULL))
);

CREATE TABLE wiki_revisions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    page_id uuid NOT NULL,
    number bigint NOT NULL,
    title text NOT NULL,
    body text NOT NULL,
    summary text NOT NULL DEFAULT '',
    collection_id uuid,
    author_user_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT wiki_revisions_pkey PRIMARY KEY (id),
    CONSTRAINT wiki_revisions_company_page_id_key UNIQUE (company_id, page_id, id),
    CONSTRAINT wiki_revisions_page_number_key UNIQUE (page_id, number),
    CONSTRAINT wiki_revisions_page_fkey FOREIGN KEY (company_id, page_id)
        REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE,
    CONSTRAINT wiki_revisions_collection_fkey FOREIGN KEY (company_id, collection_id)
        REFERENCES wiki_collections(company_id, id) ON DELETE SET NULL (collection_id)
);

ALTER TABLE wiki_pages ADD CONSTRAINT wiki_pages_current_revision_fkey
    FOREIGN KEY (company_id, id, current_revision_id)
    REFERENCES wiki_revisions(company_id, page_id, id)
    DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE wiki_links (
    company_id uuid NOT NULL,
    from_page_id uuid NOT NULL,
    to_page_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT wiki_links_pkey PRIMARY KEY (company_id, from_page_id, to_page_id),
    CONSTRAINT wiki_links_from_fkey FOREIGN KEY (company_id, from_page_id)
        REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE,
    CONSTRAINT wiki_links_to_fkey FOREIGN KEY (company_id, to_page_id)
        REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE,
    CONSTRAINT wiki_links_no_self_check CHECK (from_page_id <> to_page_id)
);
CREATE INDEX wiki_links_to_idx ON wiki_links (company_id, to_page_id, from_page_id);

CREATE TABLE wiki_favorites (
    company_id uuid NOT NULL,
    page_id uuid NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT wiki_favorites_pkey PRIMARY KEY (company_id, page_id, user_id),
    CONSTRAINT wiki_favorites_page_fkey FOREIGN KEY (company_id, page_id)
        REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE
);
CREATE INDEX wiki_favorites_user_idx ON wiki_favorites (company_id, user_id, page_id);

CREATE INDEX wiki_pages_company_status_updated_idx
    ON wiki_pages (company_id, status, updated_at DESC, id DESC);
CREATE INDEX wiki_pages_collection_idx
    ON wiki_pages (company_id, collection_id, updated_at DESC, id DESC);
CREATE INDEX wiki_pages_search_idx ON wiki_pages USING gin (search_vector);

COMMIT;
