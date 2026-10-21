# `server/cmd`

One directory per executable, named for the binary it produces — with one
pinned exception: `cmd/mcp` builds the `zenith-team-mcp` binary, because desktop
packaging and agent discovery key on that name (the `-o` flag in every build
script sets it).

| Binary | Ships as |
| --- | --- |
| `server` | the API container (the only binary `server/Dockerfile` builds) |
| `zen` | the local CLI installed on a user's machine |
| `zenith-team-mcp` (from `cmd/mcp`) | inside the desktop bundle |
| `migrate` | operator tool, not deployed |
| `bundlectl` | maintainer and CI tool, never shipped |

## The binaries

- **`cmd/server`** — the Zenith Team cloud API process. `main.go` installs signal
  handling and translates the result of `run` into an exit code; the
  composition root itself is `run()` in `app.go`, split across sibling wiring
  files in the same package (`listeners.go` — the bus → realtime forwarder,
  `workers.go`, `runtime_sweeper.go`, `redis.go`, `auth.go`, `lifecycle.go`).
  It applies
  migrations at startup before serving — that is the rollout mechanism, not a
  convenience.
- **`cmd/zen`** — the Zenith Team CLI (cobra). In Operator mode
  `zen auth login|logout|status|whoami` owns the session,
  `zen daemon start|stop|status|restart|logs` (plus `probe-runtimes`)
  manages the local agent daemon (`internal/daemon`), `zen config` and
  `zen company use` manage saved settings, `zen doctor` diagnoses readiness,
  and the `task`, `run`, `conversation`, `question`, `approval`, `inbox`,
  `schedule`, `agent`, `runtime` families work with the server
  API. Inside a Run the same binary runs in Agent mode over the daemon's Run
  channel (ADR 0009). Credentials live in the shared `internal/authfile`
  store; the Run channel holds none.
- **`cmd/mcp`** — a thin shell over `internal/mcp.Run`; serves the Zenith Team
  MCP tools over stdio using the same auth file.
- **`cmd/migrate`** — applies (`-direction=up`, the default) or rolls back
  (`-direction=down`, which additionally requires `-confirm-down` because it
  drops business data) the embedded migration ladder without booting the API,
  reading `APP_DATABASE_URL`. It does **not** replace the server's startup
  migration; it exists for preparing local or CI databases and manual
  recovery. Usage needs no post-migration backfill; reports read immutable
  `run_usage` facts directly.
- **`cmd/bundlectl`** — maintains the platform content pinned in
  `internal/platformbundle` from Intelligent-Internet/zenith-team-plugins
  (ADR 0016). `import --tag <plugin>--vX.Y.Z` reads a release tag on
  `main` with the maintainer's own git credentials (a local checkout with
  an origin must hold the tag origin publishes, on origin's `main`),
  validates it with the server's own rules, holds it to the release rules
  against the pin it replaces and rewrites the tar and the lock together;
  `import --verify` re-imports every locked tag and fails unless the result
  is byte-identical; `import --dev` pins a local checkout for development,
  which is never committed. `check` validates plugin folders, such as
  `plugins/zenith-workflows` in a checkout, with the server's own parser
  and loader and reports every problem (as text or GitHub annotations);
  inside a git work tree it reads only what a commit would carry. `diff`
  compares the pin at a git revision with the working tree and fails on a
  change that breaks the release rules. It never reads or stores a token.
  The root Makefile's `bundle-*` targets wrap it; `bundle-bump`,
  `bundle-diff` and `bundle-verify` are for maintainers. It is never
  shipped: no image, desktop bundle or release builds it.

## Keep `main` small

golang-standards/project-layout puts it plainly in its [`cmd/README.md`]:
*"Don't put a lot of code in the application directory … It's common to have a
small `main` function that imports and invokes the code from the `/internal`
and `/pkg` directories and nothing else."*

Every binary here is that shape: `main` installs signal handling, calls a
`run` function (or `mcp.Run` / cobra `Execute`), and turns the returned error
into an exit code. `cmd/server` and `cmd/mcp` route through a `mainExitCode`
helper so `os.Exit` cannot skip deferred teardown.

`os.Exit` and `log.Fatal` belong in `main()` and nowhere else. Both skip
deferred functions, so calling either from a helper silently drops the
shutdown this server depends on. Return an error instead and let `main`
decide.

Layout, data access, and the lint/shape gates live in
[`CODEMAP.md`](../CODEMAP.md).

[`cmd/README.md`]: https://github.com/golang-standards/project-layout/blob/master/cmd/README.md
