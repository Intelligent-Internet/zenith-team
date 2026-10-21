# Zenith Team docs

[`ARCHITECTURE.md`](../ARCHITECTURE.md) is the one-page system overview and
[`AGENTS.md`](../AGENTS.md) holds the engineering rules. **The code is
authoritative for behavior.** These documents record intent and convention;
when one drifts from the code, the code is right and the doc gets fixed.

**New here?**

- To run Zenith Team for real use: [`SELF_HOSTING.md`](../SELF_HOSTING.md).
- To work on the code: [`quickstart.md`](quickstart.md) gets a local stack
  running, and [`CONTRIBUTING.md`](../CONTRIBUTING.md) is the full guide.
- To add calls and voice agents locally: [`huddles-local.md`](huddles-local.md).

## content/docs/: the product documentation

Product pages in `.mdx`: concepts, how Zenith Team works, runs, workspaces,
agents, conversations, providers, schedules and the human-in-the-loop
features. Start at [content/docs/index.mdx](content/docs/index.mdx). The
glossary is [content/docs/concepts.mdx](content/docs/concepts.mdx), and each
folder's `meta.json` sets the page order.

- [content/docs/self-hosting/](content/docs/self-hosting/): running your own
  server, every environment variable, the security model, and
  [privacy and network](content/docs/self-hosting/privacy-and-network.mdx).
- [content/docs/desktop-app.mdx](content/docs/desktop-app.mdx): building and
  running the desktop app.
- [content/docs/troubleshooting.mdx](content/docs/troubleshooting.mdx): fixes
  for common problems, and where the logs live.
- [content/docs/developers/](content/docs/developers/): architecture, design
  decisions, [conventions](content/docs/developers/conventions.mdx), testing,
  releasing and maintaining. These pages orient and link; `AGENTS.md` stays
  the rulebook.
- [content/docs/community.mdx](content/docs/community.mdx): who maintains the
  project and how decisions are made.

## adr/: architecture decision records

[adr/](adr/) records why the system is shaped the way it is. Code comments
cite these as `ADR 00NN`.

## api/: the HTTP and realtime API

The server's route table and the OpenAPI contract are authoritative; these
pages explain how clients use them.

| Path | Purpose |
| --- | --- |
| [api/overview.md](api/overview.md) | API map, scopes, and how the contract is laid out and checked |
| [api/conventions.md](api/conventions.md) | The rules every endpoint follows, including how the client integrates |
| [api/errors.md](api/errors.md) | Every error code and what a client should do about it |
| [api/realtime.md](api/realtime.md) | The receive-only client WebSocket, audiences, and REST recovery |
| [api/approvals.md](api/approvals.md) | Approvals, Questions, expiry, and continuation Runs |
| [api/spec/openapi.yaml](api/spec/openapi.yaml) | The machine-readable contract index used to generate client types |

## branding.md

[branding.md](branding.md) lists the product name and every technical
identity (`ZENITH_TEAM_`, `zenith-team`, `zenith_team`).

---

Deployment lives next to its manifests: [`deploy/README.md`](../deploy/README.md)
is the production runbook and environment reference, and
[`docker/README.md`](../docker/README.md) documents the development
infrastructure stack.
