# Self-hosting Zenith Team

This is the short path to your own Zenith Team server, on one machine, with
Docker Compose. The full guide, with every option, is the
[self-hosting quickstart](docs/content/docs/self-hosting/quickstart.mdx).

The stack has five parts: the web app, the API server, PostgreSQL, Redis and
S3-compatible file storage. Agents do not run on the server. They run on the
computers your members connect, through each computer's own `codex` or
`claude` CLI.

## What you need

On the server:

- Docker with Compose v2 (the `docker compose` command)
- `make`, `git` and `openssl`

On each computer that runs agents:

- the Codex CLI (`codex`) or Claude Code (`claude`), installed and signed in
- the `zen` CLI and `zenith-team-mcp` (step 4)

## 1. Get the code and create the settings file

```bash
git clone https://github.com/Intelligent-Internet/zenith-team.git
cd zenith-team
git checkout v1.2.3     # optional: a release tag from the releases page
make selfhost-env
```

`make selfhost-env` copies `deploy/production.env.example` to `deploy/.env`,
fills the three required secrets with random values, and makes the file
readable only by you. It never overwrites an existing `deploy/.env`. Never
commit that file.

## 2. Start the stack

```bash
make selfhost-up
```

This builds the images from your checkout, starts everything, and waits until
the server is healthy. The server sets up the database on its first start.

```bash
make selfhost-status                      # the server should be "healthy"
curl -fsS http://localhost:8788/readyz    # {"status":"ready"}
```

To use the published images instead of building, set `APP_SERVER_IMAGE` and
`APP_WEB_IMAGE` in `deploy/.env` to a release such as
`ghcr.io/intelligent-internet/zenith-team-server:1.2.3`, then run
`docker compose -f deploy/docker-compose.production.yml pull server web` and
`docker compose -f deploy/docker-compose.production.yml up -d --wait`.

## 3. Create your account

Open <http://localhost:8080> and sign up with your email and a password. Use
exactly that address: `http://127.0.0.1:8080` counts as a different address,
and the server refuses it with `origin is not allowed`.

Onboarding creates your company. Its last screen sets up your CEO Agent, which
needs a connected computer.

## 4. Connect a computer that runs agents

1. Sign in to the Codex CLI or Claude Code with the vendor's own login.
   Zenith Team never holds these credentials.
2. Get `zen` and `zenith-team-mcp`: download the archive for your system from
   the [releases page](https://github.com/Intelligent-Internet/zenith-team/releases),
   or build them with Go 1.26:

   ```bash
   make tools-prepare
   cd server
   go build -o ../.dev/bin/zen ./cmd/zen
   go build -o ../.dev/bin/zenith-team-mcp ./cmd/mcp
   ```

   Put both on your `PATH`. `zen` does not build for Windows yet.
3. Sign in and start the daemon:

   ```bash
   zen auth login --server http://localhost:8080
   export ZENITH_TEAM_MCP_EXECUTABLE="$(command -v zenith-team-mcp)"
   zen daemon start
   ```

Back in the browser, choose **Refresh** on the CEO Agent setup screen. Your
computer appears under **Registered runtimes**.

The desktop app starts its own daemon instead. For now you build it from
source with your server's address built in: see
[Desktop app](docs/content/docs/desktop-app.mdx).

## 5. Open it to other people

Out of the box, both ports listen on `127.0.0.1` only. To let other people and
other computers in, put the server behind HTTPS with a reverse proxy, then set
your address in `deploy/.env`:

```dotenv
AUTH_WEB_ORIGINS=https://zenith.example.com,zenith-team-app://renderer
AUTH_WEB_APP_ORIGIN=https://zenith.example.com
```

Keep `zenith-team-app://renderer` for the desktop app. Caddy and nginx
examples are in [Reverse proxy and TLS](docs/content/docs/self-hosting/reverse-proxy-and-tls.mdx).
For a server other people use, also turn on email codes and password reset:
[Sign-in setup](docs/content/docs/self-hosting/auth-setup.mdx).

## Day to day

```bash
make selfhost-logs   # follow the logs
make selfhost-down   # stop the stack; the data volumes stay
make selfhost-up     # start it again
```

Back up PostgreSQL and file storage on a schedule:
[File storage](docs/content/docs/self-hosting/storage.mdx). To move to a new
release, follow [Upgrading](docs/content/docs/self-hosting/upgrading.mdx).

## Learn more

| Topic | Page |
| --- | --- |
| Every setting in `deploy/.env` | [Environment variables](docs/content/docs/self-hosting/environment-variables.mdx) |
| What the server protects, and the hardening checklist | [Security model](docs/content/docs/self-hosting/security-model.mdx) |
| What the server and the apps send over the network | [Privacy and network](docs/content/docs/self-hosting/privacy-and-network.mdx) |
| Fixes for common problems, and where the logs live | [Troubleshooting](docs/content/docs/troubleshooting.mdx) |
| The full operations runbook | [deploy/README.md](deploy/README.md) |

Zenith Team is pre-1.0. Read the release notes before you upgrade: a release
can include breaking changes.
