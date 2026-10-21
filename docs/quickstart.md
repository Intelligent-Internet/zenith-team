# Local development quickstart

The shortest path to a Zenith Team stack running from source, for working on
the code. To run Zenith Team for real use, follow
[SELF_HOSTING.md](../SELF_HOSTING.md) instead.

[CONTRIBUTING.md](../CONTRIBUTING.md) has the full development guide: the env
files, the daily workflow, every `make` target and the checks a pull request
needs. The infrastructure stack and its port overrides are in
[docker/README.md](../docker/README.md).

## Prerequisites

- **Go** 1.26 (`server/go.mod`)
- **Node.js** 24 (`.nvmrc`)
- **pnpm** 9.14.2: run `corepack enable`, which picks the version pinned in
  `frontend/package.json`
- **Docker** with Compose v2, for PostgreSQL 17, Redis and Silo storage
- **`codex` or `claude`** on your `PATH`, if you want to run agents
- **uv**, only for the voice worker in `voice-agent/`

`make doctor` checks these for you.

## One-time setup

```bash
make doctor
cd frontend && pnpm install && cd ..   # frontend/ is its own pnpm workspace
```

## Run the stack

```bash
make dev
```

`make dev` starts PostgreSQL, Redis and Silo in Docker, then runs the Go API
on `:8788` and the web app on `:5173` in the same terminal. `Ctrl-C` stops the
API and the web app; the containers keep running until `make infra-down`.

Prefer one terminal per process? Run these instead:

```bash
make infra-up    # PostgreSQL, Redis and Silo in Docker
make server      # Go API on :8788, with docker/dev.env
make frontend    # web app on :5173
```

Open <http://localhost:5173>, create an account, and set up a company.

- `make server` loads `docker/dev.env`, then your own overrides from
  `docker/dev.local.env` if that file exists. It applies database migrations at
  startup.
- Already run PostgreSQL, Redis and S3-compatible storage yourself? Copy
  `server/.env.example` to `server/.env`, point it at your services, and run
  `make server-local` instead of `make server`.
- The web app talks to the API at `http://127.0.0.1:8788`. Set `VITE_API_URL`
  in `frontend/.env` to use another address.

Check the server once it is up:

```bash
curl -s http://127.0.0.1:8788/healthz   # the process is up
curl -s http://127.0.0.1:8788/readyz    # PostgreSQL is reachable and its schema matches
```

## Run agents

Agents run through a daemon on your machine. The desktop app starts its own
(`cd frontend && pnpm electron:dev`). From a terminal:

```bash
make zenith-team-mcp-dev   # build the agent tool server into .dev/bin
cd server
go run ./cmd/zen auth login --server http://127.0.0.1:8788
ZENITH_TEAM_MCP_EXECUTABLE="$PWD/../.dev/bin/zenith-team-mcp" go run ./cmd/zen daemon start
```

The daemon needs `codex` or `claude` on your `PATH`, signed in with the
vendor's own login.

## Calls and voice agents

Huddles (calls and screen sharing) and voice agents need a local LiveKit and
the voice worker. See [huddles-local.md](huddles-local.md).

## Stop

```bash
make infra-down    # stop the containers, keep your data
make infra-reset   # stop them and delete all data
```

Stop `make dev`, `make server` and `make frontend` with `Ctrl-C`.

## Check your change

```bash
make go-unit                    # Go build, vet and unit tests (no Docker)
make lint typecheck             # frontend checks
make api-lint api-types-check   # OpenAPI contract and generated types
make gate                       # everything, including the integration suites
```

`make gate` needs Docker and `uv`. Run `make help` for every target.
