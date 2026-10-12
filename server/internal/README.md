# `server/internal`

Private application code. The Go compiler enforces that nothing outside
`boardly.dev/boardly/server` can import any of it, which is why the default
answer to "where does this go?" is here — see
[`internal/README.md`] in golang-standards/project-layout. The shared
contract layer the daemon-side binaries also need lives in
[`../pkg`](../pkg/README.md).

## Flat, feature-named packages

Packages are named for the feature they own — `auth`, `company`, `task`,
`conversation`, `inbox`, `usage` — not for a layer. There is no `services/`,
no `models/`, no `repositories/`. Beyond the product domains sit:

- `execution/` — the run lifecycle kernel (enqueue/trigger, claim, transitions)
- `events/` — the in-process Bus; `events.Publish` fires post-commit
- `activity/` — bus listeners that record the activity feed; handlers never
  write activity inline
- `realtime/` and `daemonws/` — the client and daemon WebSocket planes
- `handler/` and `middleware/` — the only packages that import Gin
- `db/` — pgxpool, migration runner, readiness, `Store`/`WithinTx`
- `daemon/` (+ `execenv/`, `workspace/`, `repocache/`), `authfile/`, `mcp/` —
  the machine-side runtime, the shared auth.json store, and the boardly-mcp
  server; `mcp/` imports only `authfile` and its own subtree
- `infra/` (email, storage, validate), `config/`, `util/`,
  `testutil/dbtest/` — adapters, configuration, boundary helpers, test
  scaffolding

Per-package one-liners are in [`CODEMAP.md`](../CODEMAP.md); the per-domain
charters are in the repository root's [`AGENTS.md`](../../AGENTS.md) under
"Server domain map".

## The data path

```
handler → service → sqlc queries
```

Generated queries live in `pkg/db/generated` (package `db`, pgtype
end-to-end); the `.sql` sources live in `pkg/db/queries`. Product-domain
services hold `*db.Store` (this directory's `db` package): reads through
`store.Queries()`, writes through `store.WithinTx`. The daemon plane instead
takes `execution.TxStarter` plus `Queries.WithTx` directly because it does not
own a `Store`. Row-to-domain mapping lives in plain unexported functions;
there is no DAO type and no repository interface.

Cross-domain writes are exported functions taking the **caller's**
`*generated.Queries` handle — `inbox.CreateInTx(ctx, tx, pending, input)` is
the model: a transaction participant that also stages events on `pending` for
the caller to `events.Publish` after commit. This is what lets one commit
span several domains without any of them owning the transaction.

Interfaces are for collaborators the service should not construct — a
password manager, an OAuth provider, an object store, email senders — not for
the database.

## The composition root

There is no `internal/app`. The composition root is `cmd/server` itself:
`run()` in `cmd/server/app.go` builds every service, hub, relay and worker,
with the wiring split across sibling files in that package. By convention
only `cmd/server` constructs `handler.New`; domain packages never reach
transport.

## Adding code to a domain package

The full rules live in [`AGENTS.md`](../../AGENTS.md); the load-bearing ones:

1. Find the domain that owns the noun and add a **file**, not a package; a
   newcomer must be able to guess the location from the concept.
2. A new package needs a forcing function (an import cycle, or one package
   otherwise hosting two domains). Record the reason in the package comment.
3. A domain's second service is a named type `XService` with `NewXService`;
   bare `New()` stays reserved for the package's primary type.
4. Prefix by subject, never by package (`company.SearchPage`, not
   `company.CompanySearchPage`); closed enums use `Type`, not `Kind`.
5. Error sentinels are per-surface; collapse two only when `handler` maps
   both to the identical response —
   `internal/handler/error_mapping_test.go` adjudicates.
6. Never create `doc.go` (`make go-shape` fails the build); the package
   comment sits atop the package's primary file.

[`internal/README.md`]: https://github.com/golang-standards/project-layout/blob/master/internal/README.md
