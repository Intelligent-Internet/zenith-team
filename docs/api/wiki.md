# Company Wiki API

The [OpenAPI contract](spec/wiki.yaml) defines the human Company Wiki routes and the Agent routes: reading for every Run, and one write for the founder's company-research Run.
Active Company members may list and read pages, collections, graph links and
revision history. Only owners and admins receive `wiki.publish`, which allows
creating collections and pages, renaming and deleting collections, editing,
archiving, restoring and restoring a historical revision. Every route under `/wiki/` requires a
human actor. Agents read: they search and read active pages through the
Run-proven routes in [Agent access](#agent-access). The one Agent write is the
founder's company-research Run saving its pages ([Agent writes](#agent-writes));
there is no Agent proposal or grant route and no Agent edit of a page a person
or another Run wrote.

Pages have a stable UUID and slug. Wiki and Project Pages use the same block
schema, CRDT draft storage and editor. Markdown is an import/export projection;
it is not a second editable source of truth. Creating a Wiki page creates an
unpublished draft, visible only to publishers. Ordinary members and Agents read
published revisions only.

- `GET /wiki/pages/{pageId}/document` reads the publisher's shared working draft.
- `POST /wiki/pages/{pageId}/document` merges block updates into that draft.
- `POST /wiki/pages/{pageId}/document/ids` reserves new block identities.
- `PATCH /wiki/pages/{pageId}/document/metadata` saves title, summary and collection
  with `expectedSequence`.
- `POST /wiki/pages/{pageId}/publish` requires both `expectedRevision` and
  `expectedSequence`; a page that was never published is at revision `0`. It
  freezes the exact saved draft, advances the published revision, and updates
  the public-to-Company search, link and citation indexes.
  Stale publication returns `409 wiki_page_conflict` without changing content.
- `POST /wiki/pages/{pageId}/restore-revision` restores history into the draft,
  guarded by both revisions. Publishing remains a separate action.

The Wiki view and Canvas's Wiki editor use these same routes and draft. Canvas
references show published Wiki content; the Canvas `publish_document` command
and whole-Markdown page PATCH no longer exist. Project Page saves remain live
working content, and submitting a Page as a Task result remains a separate
immutable snapshot flow. Neither Canvas acceptance nor Task result approval
publishes Wiki content. Archive changes status without deleting content or
history. Favorites belong to the calling member.

Deleting a collection (`DELETE /companies/{companyId}/wiki/collections/{collectionId}`)
requires the collection's `expectedRevision`; a stale revision returns
`409 wiki_collection_conflict`. Its active and archived pages move to Unfiled
and each page's `revision` advances, so a draft opened before the delete is
rejected as stale. Page content, revision history, favorites and links are
kept. The collection itself cannot be restored. Success is `204 No Content`.

The body field exports the block snapshot as Markdown. Before publication, credential formats (PEM
blocks, cloud and API keys, JWTs, bearer values, URL userinfo) in the title,
summary, body and collection names are replaced with `[redacted:<rule>]`;
email addresses and ordinary prose are kept verbatim.
Length limits count Unicode characters, as the schema's `maxLength` does: a
title holds 240, a summary 2048 and a body 262144 (a longer body is
`413 request_too_large`). A Wiki link is `[[page-uuid|visible label]]`; the label
is optional. The server extracts links only to existing pages in the same
Company. The graph shows active endpoints, at most 500 pages, and flags truncation.
Page details also expose incoming and outgoing links, each direction capped at
200 with its own truncation flag.

An Artifact citation is `[[artifact:<artifact-uuid>]]` for a whole Artifact or
`[[artifact:<artifact-uuid>#<ordinal>]]` for one of its items (0-based). It
carries no label, so a private Artifact's title never enters a body every
member and Agent can read. Citing grants no access: readers resolve each
citation for themselves through
[`GET /companies/{companyId}/artifacts/resolve`](spec/runs.yaml), and one that
is missing, deleted or not visible renders as unavailable, with no way to tell
which. When a draft is published, a citation that is not
already in the current body — or, for a restore, in the restored revision —
must name an Artifact item the author can open, otherwise the write is
`422 wiki_artifact_ref_unavailable`; existing citations are kept even if the
author cannot open them. A page cites at most 50 distinct Artifacts or items
(`422 wiki_artifact_ref_limit`). The server keeps a current index of each
page's citations for later backlinks.
Page search matches prefixes of the current title and body words as the
PostgreSQL text parser splits them — an email or host is one word, and a
partly typed one already matches; every word must match; text without letters
or digits is no filter — filters on collection/status or
the caller's favorites, and returns a paginated list. The API checks current
membership for every read and locks the Company and acting membership before
every publication transaction.

The API does not publish pages on the public internet. “Publish” only
updates the internal Company Wiki. Saving the working draft leaves that published content unchanged. Company Memory, Assets and Wiki pages remain
separate stores; no background sync is implied.

## Agent access

An Agent's Run may read the Company Wiki; only the company-research Run may write
it ([Agent writes](#agent-writes)). Every Run except a
Schedule Run (Task, direct chat, root Agent) can search and read the **active**
pages of its own Company through two routes that carry the Run's proof instead
of requiring a human actor:

- `GET /companies/{companyId}/runs/{runId}/agent-wiki/search?q=&limit=` —
  `q` is required, trimmed and at most 200 characters; `limit` is 1–10
  (default 5). Pages are matched with the same full-text index as page search,
  ranked by relevance then recency; the words of `q` are OR-ed as word prefixes,
  so a question in plain language still finds pages. Each item has the title,
  summary, a short snippet, the collection name, the page's content `revision`
  and `updatedAt`; `total` counts every match.
- `GET /companies/{companyId}/runs/{runId}/agent-wiki/pages/{pageId}?offset=` —
  returns at most 24000 characters of the current revision from `offset`, with
  `returnedChars`, `totalChars`, `truncated` and `nextOffset` for paging. An
  archived or missing page is `404 wiki_page_not_found`. Every read carries a
  fixed `notice`: page text is untrusted reference data, not instructions.

Both require `X-Boardly-Run-Token` for a live Run of this Company whose
authorizer is the caller (the proof's bearer, ADR 0017); anything else — no
token, another Run, another Company, a Schedule Run, another member, a Run with
no authorizer — is `401 run_token_invalid`. The Wiki has no Project, so no Run
scope narrows what a Run reads here. They live outside `/wiki/`, so the human-actor
fence above does not apply to them.

Reading is a per-Agent switch: the built-in capability group `wiki` in an
Agent's `capabilities.disabledBuiltIns` removes the tools from its Runs, and
the server independently refuses the routes with `403 wiki_agent_access_disabled`,
because the daemon's tool list alone is not authority.

### Agent writes

`POST /companies/{companyId}/runs/{runId}/agent-wiki/pages` saves pages for the
founder's onboarding research. It carries the same Run proof as the read routes
plus a required `Idempotency-Key` UUID. The server does not trust the daemon's
tool list: inside the write transaction it takes the Company and membership
locks, then proves the Run is `running`, its frozen execution profile source
(`runs.context.executionProfile.source`) is `company_research`, the proof's
bearer is the Run's authorizer, that member is active with `wiki.publish`, and
the Agent's `wiki` switch is on. Anything else is `403 wiki_agent_write_forbidden`
(`wiki_agent_access_disabled` for the switch).

The body is `{pages: [{key, title, summary?, markdown}]}`, at
most 12 pages a call, 40 for the whole company research, 1 MiB a request, with the ordinary page limits
(title 240, summary 2048, body 262144 characters; credentials redacted).
`key` is `[a-z0-9-]` up to 64 characters and is the Run's only handle on a page:

- A new key creates a page and publishes it at once, authored by the
  authorizing member with the Run's Agent recorded as the writer
  (`wiki_revisions.author_agent_id`; pages expose `lastAuthorAgentId` and
  `lastAuthorAgentName`, revisions `authorAgentId`), so search and the Wiki UI
  see it immediately. A known key publishes a new revision of the research's
  page with that key. An identical replay reports `unchanged` and writes
  nothing. A Run never reaches a page the research did not create, and a page
  a person archived is left alone with a warning. The research records the
  published revision it last wrote (`last_revision_id`) and the draft sequence
  that publish left (`last_draft_seq`): if the page's current revision differs,
  or its draft moved on without being published (the editor saves drafts
  continuously), a person edited it, and the page is skipped with a warning
  instead of replaced, including when a new link would have been added to it.
- A body that opens with a level-one heading repeating the title loses that
  heading: the Wiki shows the title above the body.
- Ownership and the 40-page cap cover the Company's whole research: every
  research Run, whether an automatic retry child or the founder's manual retry,
  owns the pages earlier attempts wrote, so it updates them instead of
  duplicating, and the 40 pages are counted over all attempts.
- `[[key]]` and `[[key|label]]` in `markdown` become links to the Run's page
  with that key, whether it is in the same call or an earlier one. A key with
  no page becomes plain text and a warning; when a later call creates that
  key, pages that named it are republished with the link. Link edges between
  pages of one call are recorded after all of them are published.
- The research does not choose a collection: a new page is filed in the
  Company's `Company` collection, found case-insensitively or created, and a
  page written before keeps whatever collection it is in now.
- The whole call is one transaction. The ordinary `wiki.page_created`,
  `wiki.page_updated` and `wiki.collection_created` events follow the commit,
  attributed to the Run's Agent on behalf of the member (D31).

The response lists `{key, pageId, title, revision, status}` per page (`status`
is `created`, `updated` or `unchanged`), `warnings`, and `totalPages` the
research owns. `run_wiki_writes(run_id, key, page_id)` records ownership, so
the pages the research wrote are counted and found by key (for example the
`company-overview` hub).

### Evidence trail

Each search and read is recorded in the same transaction as its answer, so
nothing the Agent saw is missing from the trail. A read records the page, the
exact revision whose text was returned, the page title at that time and the
character range; a search records its query (credential formats redacted) and
result count.

- `GET /companies/{companyId}/runs/{runId}/wiki-reads` (human) lists a Run's
  trail oldest first, at most 200 rows. Each read row carries the page's live
  state (`active` or `archived`, and its current revision), so a client can show
  that a source changed or was archived since. Visibility follows Run
  history (ADR 0017): a Task Run reads for the readers of its Task chat, that
  is of its Project; only the member who opened a direct or Schedule
  Conversation sees its Runs. A Run that does not
  exist or is not visible is `404 run_not_found`.
- `GET /companies/{companyId}/wiki/pages/{pageId}/agent-reads?limit=` (human,
  1–50, default 10) lists the Runs that read a page, newest first, with the
  revision of each Run's latest read, restricted to Runs the caller may see by the same rule. `taskId` names the
  Run's Task only when the caller reads it, else it is `null`.

Deleting a page or a Run deletes its trail.
