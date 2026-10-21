<div align="center">

# Zenith Team

**Run a company of AI agents that plan the work, do it on your own computer, and report back.**

[![CI](https://github.com/Intelligent-Internet/zenith-team/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/Intelligent-Internet/zenith-team/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Latest release](https://img.shields.io/github/v/release/Intelligent-Internet/zenith-team)](https://github.com/Intelligent-Internet/zenith-team/releases/latest)

[Docs](docs/README.md) · [Self-hosting](SELF_HOSTING.md) · [Roadmap](ROADMAP.md) · [Contributing](CONTRIBUTING.md) · [Security](SECURITY.md)

</div>

## What is Zenith Team

Zenith Team is a workspace where you run a company of persistent AI agents.
You set the goals. A CEO Agent and its team propose a plan, you review it, and
the work becomes tasks that go to the right agent.

It has two parts:

- **The server** is written in Go. It owns the product state: companies,
  agents, tasks, conversations and run history.
- **The daemon** runs on your own machine. It picks up work and runs it
  through the Codex or Claude Code CLI you already use, then streams the
  results back. Your provider sign-in never leaves your machine.

You use it from the web app or the desktop app.

**Privacy.** The server sends no telemetry. Everything it sends, and to whom,
is listed in [Privacy and network](docs/content/docs/self-hosting/privacy-and-network.mdx).

## Status

Zenith Team is **pre-1.0**. A release can include breaking changes, so read
the release notes before you upgrade.

The product's earlier name was retired in a clean break. Desktop apps,
sign-ins and daemons from before the rename do not carry over: install the
current release and sign in again.

## Features

- **A team of persistent agents.** Each agent has its own role and
  instructions, and reports to a CEO Agent. → [Agents](docs/content/docs/agents.mdx)
- **Plans you can review.** Agents propose task changes as a Company Plan.
  → [Company Plan](docs/content/docs/company-plan.mdx)
- **Clear ownership.** Every task names the person who owns it and the agent
  that does the work. → [Tasks](docs/content/docs/tasks.mdx)
- **Work runs on your computer.** Each run uses your own agent CLI, inside a
  managed workspace. → [Daemon and runtimes](docs/content/docs/daemon-and-runtimes.mdx) ·
  [Workspaces](docs/content/docs/workspaces.mdx)
- **You stay in the loop.** Agents ask you for approvals and answers instead
  of guessing. → [Approvals and questions](docs/content/docs/approvals-and-questions.mdx)
- **Schedules.** Set up one-off or recurring agent work.
  → [Schedules](docs/content/docs/schedules.mdx)
- **Ready-made workflows.** Fill in a short form, pick an agent, and it runs
  as a normal task. → [Workflows](docs/content/docs/workflows.mdx)

## Get started

### Self-host with Docker

You need Docker with Docker Compose, `make`, `git` and `openssl`.

```bash
git clone https://github.com/Intelligent-Internet/zenith-team.git
cd zenith-team
make selfhost-env   # create deploy/.env with generated secrets
make selfhost-up    # start PostgreSQL, Redis, Silo storage, the API server and the web app
```

Open <http://localhost:8080> and create your account. The web app also passes
API requests through to the server. To stop everything, run
`make selfhost-down`.

Next steps:

- [SELF_HOSTING.md](SELF_HOSTING.md): the short guide, including how to
  connect a computer that runs agents and how to open the server to others.
- [Environment variables](docs/content/docs/self-hosting/environment-variables.mdx):
  every setting in `deploy/.env`.
- [Security model](docs/content/docs/self-hosting/security-model.mdx): what
  the server protects, and what runs on your machines.

### Desktop app: build from source for now

The desktop app starts its own daemon, so it connects your computer with
nothing else to run. There is no ready-made download yet: each build has its
server's address built in, so you build it for your own server.

```bash
cd frontend
pnpm install
ZENITH_TEAM_DESKTOP_PROD_ORIGIN=https://zenith.example.com pnpm electron:make
```

The unsigned installers land in `frontend/out/make`. See
[Desktop app](docs/content/docs/desktop-app.mdx) for unsigned-build warnings,
updates and uninstalling.

### From source (dev loop)

Use this to work on the code. Install the tools listed under
[Development](#development) first; `make doctor` checks them.

```bash
cd frontend && pnpm install && cd ..   # once: frontend/ is its own pnpm workspace

make dev         # PostgreSQL, Redis and Silo in Docker, then the API (:8788) and the desktop app
```

The desktop app opens once the API is ready; create an account there. For the
web app instead, run `make dev DEV_DESKTOP=false` and open
<http://localhost:5173>. Prefer one terminal per process? Run `make infra-up`,
`make server` and `make desktop` (or `make frontend`) separately.

Already run PostgreSQL, Redis and S3-compatible storage yourself? Run
`cp server/.env.example server/.env`, point it at your services, and use
`make server-local` instead of `make server`.

To run agents, start a daemon. It needs `codex` or `claude` on your `PATH`:

```bash
make zenith-team-mcp-dev   # build the agent tool server into .dev/bin
cd server
go run ./cmd/zen auth login --server http://127.0.0.1:8788
ZENITH_TEAM_MCP_EXECUTABLE="$PWD/../.dev/bin/zenith-team-mcp" go run ./cmd/zen daemon start
```

The full walkthrough is in [docs/quickstart.md](docs/quickstart.md).

## Supported agent CLIs

Zenith Team runs agents through these two CLIs. Install and sign in to them
yourself; Zenith Team never holds your provider credentials.

| Agent CLI | Command | How Zenith Team runs it |
| --- | --- | --- |
| Claude Code | `claude` | One process per turn, in stream-JSON mode |
| Codex | `codex` | A `codex app-server` process, over JSON-RPC |

Sessions, skills, models and usage for each are in
[Providers](docs/content/docs/providers.mdx).

## Documentation

| I want to… | Start here |
| --- | --- |
| Learn the main ideas | [Core concepts](docs/content/docs/concepts.mdx) |
| Get to my first finished task | [Quickstart](docs/content/docs/quickstart.mdx) |
| See what happens when an agent runs | [How Zenith Team works](docs/content/docs/how-zenith-team-works.mdx) |
| Host Zenith Team myself | [SELF_HOSTING.md](SELF_HOSTING.md) |
| Connect my computer | [Daemon and runtimes](docs/content/docs/daemon-and-runtimes.mdx) |
| Use the `zen` CLI | [CLI reference](docs/content/docs/cli.mdx) |
| Understand the code | [Architecture](docs/content/docs/developers/architecture.mdx) |
| Run the stack locally | [Local development quickstart](docs/quickstart.md) |
| Fix a problem | [Troubleshooting](docs/content/docs/troubleshooting.mdx) |
| Contribute a change | [CONTRIBUTING.md](CONTRIBUTING.md) |

All product docs live in [docs/content/docs](docs/content/docs/); the
[docs index](docs/README.md) explains the layout.

## Architecture

```text
 ┌────────────┐  ┌──────────────┐
 │  Web app   │  │ Desktop app  │
 └─────┬──────┘  └──────┬───────┘
       └───────┬────────┘
               │ HTTP + WebSocket
    ┌──────────▼─────────┐      ┌───────────────────────┐
    │   Go API server    │─────▶│ PostgreSQL  (state)   │
    │  companies, tasks, │─────▶│ Redis       (realtime)│
    │  runs, permissions │─────▶│ S3 storage  (files)   │
    └──────────▲─────────┘      └───────────────────────┘
               │ claims runs, streams results back
┌──────────────┴────────────────────────────┐
│ Your machine                              │
│   zen daemon ──▶ codex / claude CLI       │
│   provider sign-in stays on this machine  │
└───────────────────────────────────────────┘
```

The server is the only writer of product state. The daemon claims queued runs,
prepares a workspace, starts the agent CLI and sends back each finished step.
Read [ARCHITECTURE.md](ARCHITECTURE.md) for the full picture.

## Development

| Tool | Version |
| --- | --- |
| Go | 1.26 (`server/go.mod`) |
| Node.js | 24 (`.nvmrc`; Electron needs 22.12 or newer) |
| pnpm | 9.14.2, pinned in `frontend/package.json` (run `corepack enable`) |
| Docker | For the local PostgreSQL, Redis and Silo stack, and the integration tests |

| Path | What it holds |
| --- | --- |
| `server/` | Go module: API server, `zen` CLI and daemon, `zenith-team-mcp`, migrations |
| `frontend/` | Web app and Electron desktop app (its own pnpm workspace) |
| `docker/` | Local development stack |
| `deploy/` | Self-hosting and production files |
| `docs/` | Product docs, API contract, decision records |

The main checks:

```bash
make go-unit                            # Go build, vet and unit tests (no Docker)
make lint typecheck                     # frontend checks
make api-lint api-types-check           # OpenAPI contract and generated types
```

Run `make help` to see every target. [AGENTS.md](AGENTS.md) holds the
engineering rules.

## Contributing

Contributions are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) before you
open a pull request. The [roadmap](ROADMAP.md) shows what comes next and where
help is wanted. Everyone taking part follows our
[Code of Conduct](CODE_OF_CONDUCT.md). Questions go to
[GitHub Discussions](https://github.com/Intelligent-Internet/zenith-team/discussions);
see [SUPPORT.md](SUPPORT.md).

## Security

Please do not report security problems in public issues. Follow
[SECURITY.md](SECURITY.md) to report them privately.

## License

Zenith Team is released under the [MIT License](LICENSE). Third-party
components keep their own licenses, including the free
[Iconsax](https://iconsax.io) icons in the app; see
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
