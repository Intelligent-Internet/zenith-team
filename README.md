# Zenith Team

Zenith Team is a cloud-first web and desktop workspace where a founder operates
persistent AI Agents organized into Departments, backed by local Codex and
Claude Code runtimes. The Go server owns product state; a machine-side daemon
runs the agents.

Zenith is the shared product-family prefix; this product is Zenith Team.
See [branding](./docs/branding.md) for display names and stable technical IDs.

[`ARCHITECTURE.md`](./ARCHITECTURE.md) is the system overview,
[`AGENTS.md`](./AGENTS.md) carries the engineering rules, and
[`docs/`](./docs/README.md) holds product and engineering guidance.

## Requirements

- Go 1.26
- Node.js 22 or newer
- pnpm 9 (the client pins `pnpm@9.14.2`)
- Docker for PostgreSQL, Redis, and object-storage integration tests

## Quickstart

Each long-running process gets its own terminal:

```bash
cp server/.env.example server/.env
```

```bash
make server-local
```

```bash
make frontend
```

`make server-local` exports the git-ignored `server/.env`, runs against a
native PostgreSQL, and applies migrations at startup → `:8788`. The example's
`STORAGE_DRIVER=local` uses Silo at `http://localhost:9000`; run `make infra-up`
to start Silo and create the attachment bucket. To use the
full Docker PostgreSQL/Redis/Silo stack instead, run `make infra-up` followed
by `make server`. If both email secrets are left empty, the server keeps the
immediate-signup development flow.

`make frontend` runs the client in [`frontend/`](./frontend/README.md), which
is its own pnpm workspace — install inside that directory, not at the root.

To actually run agents you also need a daemon and at least one provider CLI
(`claude` or `codex`) on your PATH:

```bash
cd server && go run ./cmd/zen auth login
```

```bash
make boardly-mcp-dev
(cd server && BOARDLY_MCP_EXECUTABLE="$PWD/../.dev/bin/boardly-mcp" go run ./cmd/zen daemon start)
```

The desktop app supplies this MCP binary automatically. Terminal-managed daemons
need the explicit path to provide Task tools such as Project Page editing and
the built-in authoring guides.

The full walkthrough is in [docs/quickstart.md](./docs/quickstart.md);
`make help` lists every target.

## Verification

```bash
make gate
```

`make gate` is the single release-quality command. Day-to-day workflows,
verification lanes, and gotchas are in
[AGENTS.md](./AGENTS.md) (operations runbook: [deploy/README.md](./deploy/README.md)); the infra
stack is documented in [docker/](./docker/README.md).

## Deployment

Production runs as one environment-configured server container plus
PostgreSQL and Redis. The runbook, manifests, environment reference, and
Grafana dashboard live in [deploy/](./deploy/README.md).
