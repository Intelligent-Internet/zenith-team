# `server/tests`

Tests are **co-located with their package**: unit tests and
`*_integration_test.go` infrastructure suites sit flat beside the code they
exercise (today 85 integration files across 16 packages — `internal/db`,
`company`, `execution`, `task`, `agent`, `conversation`, `auth`, `attachment`,
`approval`, `activity`, `usage`, `schedule`, `mailbox`, `inbox`, `account`, and
`infra/storage`). There is no separate integration tree.

Every one of those files begins with:

```go
//go:build integration
```

followed by a blank line. That tag is the lane selector. A plain `go test ./...`
does not compile them, so it needs no PostgreSQL, Silo, or Redis; passing
`-tags=integration` adds them on top of the unit tests rather than replacing
them, so the tagged run is a superset.

Two consequences worth remembering:

- **A helper shared by both lanes must live in an untagged `*_test.go` file.**
  Put a fixture or fake that a unit test also uses inside a tagged file and the
  package's unit build fails with `undefined:`.
- **A build tag hides a file from `go build`, `go vet`, and the linter, not just
  from `go test`.** `make go-unit` runs `go vet -tags=integration ./...` for
  exactly this reason: without it, an integration suite could stop compiling and
  no blocking lane would notice.

This directory holds only the cross-cutting test infrastructure that cannot live
inside a tested package:

- **`testpostgres/`** — a parent command that owns one PostgreSQL
  (testcontainers) container while a child test command runs. The separate
  process exists because Go executes each tested package in its own process;
  no package-local singleton can span the full test command. It exports the
  server URL to children via `BOARDLY_TEST_DATABASE_URL` and removes its
  container on normal and handled-signal paths. `make go-shape` pins
  PostgreSQL testcontainers imports to this package alone.
- **`wsobserve/`** — the e2e smoke observer: connects two clients to the
  Boardly client WebSocket and prints one line per received frame, proving
  multi-socket fanout ordering.
- `smoke/` — currently an empty, untracked scratch directory; it contains no
  Go package.

Per-test database scaffolding lives with the other internal packages, in
[`internal/testutil/dbtest`](../internal/testutil/dbtest): each allocation
gets its own database on the shared server, so fixtures stay isolated without
container churn. Only tests and the test runner import it.

## Running them

```bash
make go-unit                                        # unit lane: no tag, no Docker
make go-integration                                 # tagged suites on the shared container
make go-test GO_TEST_ARGS='./internal/task -run TestName'
```

Infrastructure-backed suites run through the parent fixture so package test
processes cannot each launch a container. `make go-gate` uses the same fixture
for the complete tagged suite with `-race` (plus build, vet, and the shape
checks). The root Makefile exports `GOFLAGS=-p=2` to bound concurrent migration
and connection load on the one shared server.

**CI gates on `make go-unit` alone.** The `server-integration` job in
`.github/workflows/ci.yml` is dispatch-only: it does not run on pull requests or
on pushes to `main`. Nothing on the merge path exercises PostgreSQL, Silo, or
Redis, so run `make go-integration` yourself — or dispatch the CI workflow by
hand — before merging changes to migrations, sqlc queries, or fixtures.

Beyond the tag, a raw `go test` still never starts PostgreSQL implicitly: a
tagged test that does run without `BOARDLY_TEST_DATABASE_URL` skips, and CI (or
`TEST_REQUIRE_DOCKER=1`) makes that absence fatal instead. This prevents an
editor or an agent from unexpectedly launching dozens of containers.
