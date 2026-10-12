# Local development quickstart

The shortest path to a running Zenith Team stack. Deeper workflows, verification
lanes, and gotchas are in
[`../AGENTS.md`](../AGENTS.md) and the operations runbook in
[`../deploy/README.md`](../deploy/README.md); the infra stack and
every environment override are in [`../docker/README.md`](../docker/README.md).

## Prerequisites

- **Go** 1.26
- **Node.js** 22 or newer
- **pnpm** 9 (`corepack enable` picks the pinned `9.14.2` version)
- **PostgreSQL** 16 or newer for native local development
- **Docker** (optional) for the bundled PostgreSQL, Redis, and Silo stack

## One-time setup

```bash
cd frontend && pnpm install   # the client workspace installs on its own
```

## Run the server from source

Create the local environment file once, then update its PostgreSQL and optional
email/Google credentials:

```bash
cp server/.env.example server/.env
```

Use a separate terminal per long-running process:

```bash
make infra-up         # 1. PostgreSQL + Redis + Silo
make server           # 2. Go API                                  → :8788
make frontend         # 3. Client app (Vite)                       → :5173
```

Then open **http://localhost:5173**, create an account, and onboard a company.

- `make server` exports the Docker development environment and applies database
  migrations at startup. Attachments always use S3-compatible object storage;
  the local stack supplies Silo.
- `make server-local` is for a custom `server/.env` whose PostgreSQL, Redis,
  and S3-compatible object-storage endpoints are already reachable. The example
  sets `STORAGE_DRIVER=local` to use the Silo service and bucket created by
  `make infra-up`, without configuring S3 credentials manually. Explicit
  `STORAGE_S3_*` values override the local defaults.
- `make frontend` points the client at `http://127.0.0.1:8788`.
- Provider runtimes (Codex, Claude Code) are detected by a machine-side daemon.
  Desktop starts its isolated daemon automatically; for a terminal-managed
  daemon, build its Task tools with `make boardly-mcp-dev`, then run
  `(cd server && BOARDLY_MCP_EXECUTABLE="$PWD/../.dev/bin/boardly-mcp" go run ./cmd/zen daemon start)`.
  The MCP binary provides Project Page editing and the built-in authoring guides;
  desktop configures it automatically.

Health checks once the server is up:

```bash
curl -s http://127.0.0.1:8788/healthz   # liveness
curl -s http://127.0.0.1:8788/readyz    # readiness (DB + storage + realtime)
```

### Optional Docker infrastructure

Use `make infra-up` followed by `make server` when you specifically need
the bundled PostgreSQL/Redis/Silo stack. Only that workflow needs cleanup:

```bash
make infra-down       # stop containers, keep your dev database + bucket
make infra-reset      # stop and wipe all volumes (fresh start)
```

## The client app in `frontend/`

`frontend/` is the **Zenith Team founder-cockpit** app and the tree client
work belongs in. It is a separate pnpm workspace (`ii-company`, pinned to pnpm
9.14.2) — **not** part of the root workspace — and it talks to the same Go
`server/` above: REST under `/api/v1` plus one receive-only native WebSocket at
`/api/v1/ws?company_id={companyId}`, with cookie-session auth. The desktop release ships
`frontend/`'s Electron app. Run it in isolation:

```bash
cd frontend
pnpm install          # once — corepack switches to the pinned pnpm 9.14.2
pnpm dev              # Vite dev server (or: make frontend from the repo root)
```

Point it at a backend with `VITE_API_URL`; without it, dev on `:5173` targets
the matching loopback host on the Go API port `8788` (e.g.
`http://localhost:8788/api/v1`), and deployed origins use same-origin
`/api/v1`. See [`../frontend/README.md`](../frontend/README.md).

## Verify your changes

```bash
make gate             # Go build/vet/test -race, lint, typecheck, test, build
```

`make gate` is the single release-quality command. It also validates the
OpenAPI document and checks the committed generated TypeScript contract.
Individual lanes (`make api-lint`, `make api-types-check`, `make go-gate`,
`make lint`, `make typecheck`, `make test`, `make build`) run pieces in
isolation. Run `make help` for the full target list.
