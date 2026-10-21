# Contributing to Zenith Team

Thanks for helping. This page explains how to set up the code, how to propose
a change, and what a pull request needs before it can merge.

Everyone taking part follows our [Code of Conduct](CODE_OF_CONDUCT.md).

## Before you start

| You want to… | Do this |
| --- | --- |
| Ask a question | Post in [GitHub Discussions](https://github.com/Intelligent-Internet/zenith-team/discussions). |
| Report a bug | Open a [bug report](https://github.com/Intelligent-Internet/zenith-team/issues/new/choose). |
| Fix a bug or a doc | Open a pull request. An issue first is welcome but not required. |
| Add a feature or change behavior | Open a [feature request](https://github.com/Intelligent-Internet/zenith-team/issues/new/choose) first. Wait until a maintainer agrees on the direction, then open a pull request. |
| Report a security problem | Do **not** open an issue. Follow [SECURITY.md](SECURITY.md). |

Opening an issue first for features saves you from building something we
cannot merge.

## How the repository fits together

| Part | What it is | Tooling |
| --- | --- | --- |
| `server/` | The Go module: API server, `zen` CLI and daemon, `zenith-team-mcp`, migrations | Go 1.26 (`server/go.mod`) |
| `frontend/` | The web app and the Electron desktop app. Its own pnpm workspace; the repository root is not a JavaScript workspace. | Node.js 24 (`.nvmrc`), pnpm 9.14.2 |
| `voice-agent/` | The optional voice worker for calls | A uv project, Python 3.13 |
| `docker/compose.yml` | Development infrastructure: PostgreSQL 17, Redis and Silo (S3-compatible storage), plus LiveKit when Huddles are on | Docker Compose v2 |
| `deploy/` | The self-hosting stack and the production runbook | Docker Compose v2 |

In development the API server and the web app run from source, not in
containers. The API listens on port `8788` and the web app on port `5173`.
The daemon runs from source too, through `go run ./cmd/zen`.

## Set up your environment

You need:

- Go 1.26
- Node.js 24 (Electron needs 22.12 or newer)
- pnpm 9.14.2: run `corepack enable`, which picks the version pinned in
  `frontend/package.json`
- Docker with Compose v2, for the local stack and the integration tests
- `codex` or `claude` on your `PATH`, if you want to run agents
- [uv](https://docs.astral.sh/uv/), only for `voice-agent/` and `make gate`

### First-time setup

1. Fork the repository and clone your fork.
2. Check your tools:

   ```bash
   make doctor
   ```

3. Install the frontend workspace:

   ```bash
   cd frontend && pnpm install && cd ..
   ```

4. Start everything:

   ```bash
   make dev
   ```

   This starts the Docker infrastructure, then runs the API and the web app in
   the same terminal. Open <http://localhost:5173> and create an account.

5. To run agents, start a daemon in another terminal:

   ```bash
   make zenith-team-mcp-dev   # build the agent tool server into .dev/bin
   cd server
   go run ./cmd/zen auth login --server http://127.0.0.1:8788
   ZENITH_TEAM_MCP_EXECUTABLE="$PWD/../.dev/bin/zenith-team-mcp" go run ./cmd/zen daemon start
   ```

   The desktop app starts its own daemon instead: `cd frontend && pnpm electron:dev`.

The longer walkthrough is [docs/quickstart.md](docs/quickstart.md). Calls and
voice agents are in [docs/huddles-local.md](docs/huddles-local.md).

### Env files

Configuration is environment variables only. These files hold them:

| File | In git | Read by | What it is for |
| --- | --- | --- | --- |
| `docker/dev.env` | Yes | `make server`, `make dev`, `make infra-up` | Development-only defaults that match `docker/compose.yml` |
| `docker/dev.local.env` | No | The same, after `docker/dev.env` | Your own overrides, for example `HUDDLES_ENABLED='true'` |
| `server/.env` | No | `make server-local` | Your own PostgreSQL, Redis and storage. Copy it from `server/.env.example`. |
| `frontend/.env` | No | The Vite dev server and `pnpm electron:dev` | Client settings such as `VITE_API_URL`. Copy it from `frontend/.env.example`. |
| `voice-agent/.env` | No | `make voice-start`, `make voice-doctor` | The local voice worker. `make voice-setup` creates it from `.env.example`. |
| `deploy/.env` | No | The self-hosting stack (`make selfhost-*`) | `make selfhost-env` creates it with generated secrets |

**Never commit a `.env` file.** Git ignores `.env` and `.env.*` everywhere,
except the `.env.example` templates, and `docker/dev.local.env`.

### Daily workflow

1. Update your branch: `git pull --rebase` (or rebase on the upstream `main`).
2. If `frontend/pnpm-lock.yaml` changed, run `cd frontend && pnpm install`.
3. Start the stack with `make dev`, or with `make infra-up`, `make server` and
   `make frontend` in three terminals. The server applies new migrations when
   it starts.
4. Make your change, and run the checks for what you touched (see
   [Run the checks](#run-the-checks)).
5. When you are done, stop the API and the web app with `Ctrl-C`, and the
   containers with `make infra-down`. `make infra-reset` also deletes the
   development data.

### Two checkouts at once

Running two checkouts side by side is not supported out of the box:

- the web dev server always uses port `5173`,
- `docker/compose.yml` names one Compose project, `zenith-team`, so both
  checkouts share the same containers and data, and
- LiveKit and the voice worker use fixed ports.

Run one checkout's `make dev` at a time. The two checkouts then share one
development database, so a migration from one branch is already applied when
you switch to the other. Use `make infra-reset` to start clean.

## Know the rules

[AGENTS.md](AGENTS.md) is the engineering rulebook: the repository map, which
package owns what, naming, and the rules that must not break. Read it before
you change server code. [server/CODEMAP.md](server/CODEMAP.md) shows where
things live in the server. [Conventions](docs/content/docs/developers/conventions.mdx)
is a short digest of both. If a doc and the code disagree, the code wins. Fix
the doc in the same pull request.

Use the product name **Zenith Team** (or **Zenith** for short). Technical
names use `zen`, `ZENITH_TEAM_`, `zenith-team` and `zenith_team`. See
[docs/branding.md](docs/branding.md).

## Make your change

### Server (`server/`)

- Write the test first when you change behavior.
- Put tests beside the code they test. Tests that need PostgreSQL, Redis or
  object storage go in `*_integration_test.go` files with a
  `//go:build integration` tag.
- Format Go code with `make go-fmt`.

### Database

- Migrations live in `server/migrations` as `NNNNNN_name.up.sql` and
  `NNNNNN_name.down.sql`.
- Migrations are append-only. Never edit a migration that is already on
  `main`. Add a new one after the latest.
- After you change a migration or a query in `server/pkg/db/queries`, run
  `make go-sqlc` and commit the generated code. It uses the pinned sqlc.
- Run `make go-integration` (needs Docker). CI runs these suites every night,
  not on pull requests.

### API

- The HTTP contract is in `docs/api/spec/`. `openapi.yaml` is an index; edit
  the domain file it points to.
- After an API change, run `make api-generate` and commit the regenerated
  TypeScript types.
- A new error code must also be listed in `docs/api/errors.md`.

### Frontend (`frontend/`)

- `frontend/` is its own pnpm workspace. Run `pnpm` commands inside it.
- Do not add frontend tests. Check your change with lint, typecheck, build and
  a look in the running app.
- UI icons come from Iconsax through the shared icon components. See
  [frontend/AGENTS.md](frontend/AGENTS.md).
- Add screenshots of every changed screen to your pull request, at a wide and
  a narrow window size.

### Docs

Product docs are `.mdx` pages in `docs/content/docs`. The page order is in
`meta.json` in the same folder.

## Typical flows

### Add an HTTP endpoint

1. Describe it in the domain file under `docs/api/spec/`, and add its path to
   the `openapi.yaml` index. Run `make api-lint`.
2. Run `make api-generate` and commit the regenerated TypeScript types.
3. Write the test, then the handler in `server/internal/handler/<resource>.go`.
   Register the route in `Handler.registerRoutes`, add it to `wantRouteTable`
   in `route_table_test.go`, and say who may call it in `route_actor.go`.
4. Map domain errors through an `errorMapping` table. List any new error code
   in `docs/api/errors.md`.
5. Run `make go-unit`. Its tests fail when the routes, the spec and the error
   codes disagree.

The full recipe is
[Adding an HTTP endpoint](server/CODEMAP.md#adding-an-http-endpoint).

### Add a migration and a query

1. Add `NNNNNN_name.up.sql` and `NNNNNN_name.down.sql` in `server/migrations`,
   numbered after the latest file there.
2. Write the query in the domain's file in `server/pkg/db/queries`.
3. Run `make go-sqlc` and commit the generated code.
4. Use the new query from the domain service, inside `db.Store.WithinTx` when
   it writes.
5. Run `make go-unit`, then `make go-integration`.

### Change the UI

1. Make the change in `frontend/`, following
   [frontend/AGENTS.md](frontend/AGENTS.md): Tailwind for styling, Iconsax for
   icons.
2. Run `make lint typecheck build`.
3. Check the screen in the running app, in light and dark themes.
4. Take screenshots at a wide and a narrow window size, and add them to the
   pull request.

### Add a server setting

1. Read it in `server/internal/config`, where every setting is checked at
   start.
2. Add it to `deploy/production.env.example`. A test in `make go-unit` fails
   when the server reads a setting the template does not list.
3. Document it on the
   [Environment variables](docs/content/docs/self-hosting/environment-variables.mdx)
   page in the same pull request.

## Run the checks

CI picks its lanes from the files you change. Each lane also runs when a
shared file it reads changes, such as `docs/api/**` or
`deploy/production.env.example`. A change to the CI workflow runs every lane,
and a change to the `Makefile` runs all but the macOS sandbox tests.

| You changed | CI runs | Run it yourself with |
| --- | --- | --- |
| `server/**`, `docs/api/**`, `deploy/production.env.example`, `deploy/docker-compose.production.yml` | Format check, lint (only findings new in your branch fail), build, vet, unit tests with `-race`, the pinned sqlc drift check, and a Windows smoke test | `make go-fmt-check go-lint go-unit` |
| `server/internal/daemon/**` | Also the daemon's sandbox tests on macOS | Runs on macOS only |
| `frontend/**`, `docs/api/spec/**` | Lint, typecheck, API contract checks, a production build and the Electron security scan | `make lint typecheck api-lint api-types-check build` |
| `voice-agent/**` | `ruff` lint and format check, the Python tests and a Docker build | `make voice-gate` (needs `uv`) |
| `deploy/**`, a Dockerfile | Builds both images and boots the self-hosting stack | `make selfhost-env selfhost-up` on a throwaway checkout |

`ci-gate` is the one required check. It fails when a lane that should have run
did not succeed.

Every night CI runs every lane plus the PostgreSQL integration suites. Run
`make go-integration` yourself when you change migrations, queries or test
fixtures.

`make gate` runs everything, including the integration suites. It needs Docker
and `uv`.

The Security workflow also runs on every pull request: `gitleaks` for secrets,
and `actionlint` and `zizmor` for workflows. Its dependency audits only
report. More detail is in
[Testing](docs/content/docs/developers/testing.mdx).

## Command reference

Run these from the repository root. `make help` prints the same list.

### Local development

| Command | What it does |
| --- | --- |
| `make doctor` | Checks the tools you need |
| `make dev` | Starts the infrastructure, then the API and the web app in one terminal |
| `make infra-up` | Starts PostgreSQL, Redis and Silo (and LiveKit when `HUDDLES_ENABLED='true'`) |
| `make server` | Runs the API on `:8788` with `docker/dev.env` and `docker/dev.local.env` |
| `make server-local` | Runs the API with your own services from `server/.env` |
| `make frontend` | Runs the web app on `:5173` |
| `make zenith-team-mcp-dev` | Builds `zenith-team-mcp` into `.dev/bin` for a daemon you start yourself |
| `make debug-app` | Starts the desktop app in debug mode |
| `make infra-down` | Stops the infrastructure and keeps its data |
| `make infra-reset` | Stops the infrastructure and deletes its data |
| `make livekit-up`, `livekit-down`, `livekit-logs` | Manages only LiveKit, for Huddles |
| `make voice-setup` | Creates `voice-agent/.env` and installs the worker's dependencies |
| `make voice-doctor` | Checks the voice setup before a call |
| `make voice-start`, `voice-status`, `voice-stop` | Runs, inspects or stops the local voice worker |

### Go server

| Command | What it does |
| --- | --- |
| `make go-unit` | Shape checks, sqlc drift check, build, vet and unit tests with `-race`. No Docker. |
| `make go-integration` | The integration suites (and the unit tests) with `-race`, on a shared PostgreSQL container |
| `make go-test GO_TEST_ARGS='./internal/task -run TestName'` | Chosen tests, with the integration tag, on the shared container |
| `make go-gate` | Build, vet and the complete suite with `-race`, plus the shape and sqlc checks |
| `make go-fmt`, `make go-fmt-check` | Formats Go code, or only reports what needs formatting |
| `make go-lint`, `make go-lint-fix` | Lints Go code with the pinned golangci-lint, or applies fixes |
| `make go-sqlc` | Regenerates `server/pkg/db/generated` with the pinned sqlc |
| `make go-sqlc-check` | Fails when the generated code is stale |
| `make go-vuln` | Scans the Go dependencies for known vulnerabilities |
| `make go-shape` | Checks repository rules a linter cannot express |
| `make tools-prepare` | Downloads the pinned npm packages the daemon and CLI embed (needs the network once) |

### Frontend and API contract

| Command | What it does |
| --- | --- |
| `make lint` | Import-cycle and layer check, then the format check |
| `make typecheck` | TypeScript type check |
| `make build` | Type check and production build |
| `make api-lint` | Validates the OpenAPI contract |
| `make api-generate` | Regenerates the TypeScript API types |
| `make api-types-check` | Fails when the generated types are stale |
| `make api-docs` | Browses the contract locally |
| `make api-html`, `make api-bundle` | Renders the contract to one HTML file, or flattens it to one YAML file (both git-ignored) |

### Everything, and self-hosting

| Command | What it does |
| --- | --- |
| `make gate` | Every check, including the integration suites and `voice-gate` |
| `make voice-gate` | `ruff` lint and format check, and the voice worker's tests |
| `make selfhost-env` | Creates `deploy/.env` with generated secrets |
| `make selfhost-up`, `selfhost-down` | Builds and starts, or stops, the self-hosting stack |
| `make selfhost-status`, `selfhost-logs` | Shows the stack's containers, or follows their logs |

### Maintainer-only targets

Workflow and skill content is built in a separate plugins repository and
pinned here ([ADR 0016](docs/adr/0016-platform-content-bundles.md)).
These targets read that repository, so they need access to it. Only
maintainers can run them:

| Command | What it does |
| --- | --- |
| `make bundle-bump TAG=<plugin>--vX.Y.Z` | Pins a released plugin version |
| `make bundle-verify` | Re-imports every pinned release and requires identical bytes |
| `make bundle-local`, `make bundle-check` | Pin or check a local checkout of the plugins repository |

`make bundle-diff` only compares the committed pin with a git revision of this
repository, so anyone can run it to review a pin change.

## When something goes wrong

[Troubleshooting](docs/content/docs/troubleshooting.mdx) covers the common
problems: a daemon that will not connect, `origin is not allowed`, a port
already in use, and where every log lives.

## Commit your work

### Conventional Commits

Write commit messages as [Conventional Commits](https://www.conventionalcommits.org/),
with a scope:

```text
feat(tasks): let members reorder task dependencies
fix(daemon): retry the claim after a dropped connection
docs(cli): explain the --server flag
```

Common types are `feat`, `fix`, `docs`, `refactor`, `test`, `chore` and `ci`.
The scope is the area you changed. Pull request titles follow the same form,
because they become the squash-merge commit message, and a check enforces it.

### Sign off every commit (DCO)

We use the [Developer Certificate of Origin](https://developercertificate.org/)
(DCO). Signing off says you wrote the change, or have the right to submit it
under the project's license. Add the sign-off with `-s`:

```bash
git commit -s -m "fix(daemon): retry the claim after a dropped connection"
```

This adds a `Signed-off-by: Your Name <you@example.com>` line. The name and
email must match your Git author. The [DCO GitHub App](https://github.com/apps/dco)
checks every commit in a pull request.

Forgot to sign off?

```bash
git commit --amend -s --no-edit          # the last commit
git rebase --signoff main                # every commit on your branch
git push --force-with-lease
```

There is no CLA.

## Open a pull request

- Keep it to one topic. Small pull requests get reviewed faster.
- Fill in the pull request template. Link the issue with `Closes #123`.
- Show how you tested it: the commands you ran and their output.
- Add screenshots for any UI change.
- Keep **Allow edits by maintainers** turned on, so we can push small fixes.
- Make sure CI is green.

### Code owner review

A pull request needs a review from the code owners of the paths it changes.
`@Intelligent-Internet/zenith-team-maintainers` owns every path except these
security-sensitive ones, which `@Intelligent-Internet/zenith-team-security`
owns:

- `server/internal/auth/`
- `server/internal/config/`
- `server/internal/middleware/`
- `server/internal/daemon/execenv/`
- `server/pkg/agent/`
- `server/migrations/`
- `frontend/electron/`
- `.github/`
- `.gitleaks.toml`
- `SECURITY.md`

[.github/CODEOWNERS](.github/CODEOWNERS) is the source of truth.

### Secrets and test data

- Never commit credentials, tokens, private keys or internal URLs.
- Test fixtures use clearly fake values, such as `example-not-a-real-key`.
- Never use strings shaped like real keys, such as `sk-…`, `ghp_…`, `re_…`
  or `rk_live…`. Secret scanners flag them.

## AI-assisted contributions

Pull requests written with AI tools are welcome. They meet the same bar as any
other change:

- You understand every line you submit and can explain it in review.
- You show evidence that it works: test output, screenshots, or both.
- You say in the pull request which tool you used.

AI coding agents should read [AGENTS.md](AGENTS.md). It is the single guide
for agents; [CLAUDE.md](CLAUDE.md) points to it.

## License

Zenith Team is under the [MIT License](LICENSE). By contributing, you agree
that your contribution is licensed under the same MIT License.
