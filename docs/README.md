# Zenith Team Docs

[`ARCHITECTURE.md`](../ARCHITECTURE.md) is the one-page system overview and
[`AGENTS.md`](../AGENTS.md) carries the engineering rules. **The code is
authoritative for behavior** — these documents record intent and convention,
and when one drifts from the code, the code is right and the doc gets fixed.

**New here?** [`quickstart.md`](quickstart.md) is the shortest path to a
running local stack; [`huddles-local.md`](huddles-local.md) adds calls and
voice Agents.

## content/docs/ — the product documentation set

Product-facing `.mdx` pages: terminology and concepts, how Zenith Team works, the
run lifecycle, the per-company workspace structure, agents and departments,
conversations, providers, schedules, and the human-in-the-loop surfaces.
Start at [content/docs/index.mdx](content/docs/index.mdx); the glossary is
[content/docs/concepts.mdx](content/docs/concepts.mdx), and
[content/docs/meta.json](content/docs/meta.json) carries the page order.
[content/docs/developers/](content/docs/developers/) is the short developer
section — code layering, the load-bearing invariants behind the ADRs, and
local setup — which orients and links; `AGENTS.md` stays the rulebook.

## design/ — Product and output design notes

| Path | Purpose |
| --- | --- |
| [design/chat-visual-refinement.md](design/chat-visual-refinement.md) | Accepted chat appearance, theme/gradient rules, geometry, and validation history |
| [design/05-run-deliverables.md](design/05-run-deliverables.md) | Frozen Run results and founder review |
| [design/06-output-publication.md](design/06-output-publication.md) | Automatic app Artifact publication and explicit file publication |

## api/

The server route table and generated OpenAPI contract are authoritative; these
pages explain how clients use them.

| Path | Purpose |
| --- | --- |
| [api/overview.md](api/overview.md) | API map, scopes, projections, and contract layout |
| [api/client-migration.md](api/client-migration.md) | Current frontend integration and convergence status |
| [api/realtime.md](api/realtime.md) | Receive-only client WebSocket, typed audiences, and REST recovery |
| [api/approvals.md](api/approvals.md) | Durable Approvals, Questions, expiry, and continuation Runs |
| [api/spec/openapi.yaml](api/spec/openapi.yaml) | Machine-readable contract index used to generate client types |

## Feature implementation handoffs

See the [feature spec index](specs/README.md) for owning PRD/TECH pairs and
delivery status. Studio mode follows the same structure: [product requirements](specs/studio-mode/PRD.md)
and [technical design](specs/studio-mode/TECH.md) cover the native canvas, binding,
authentication and collaboration boundary.

---

Deployment lives next to its manifests: [`deploy/README.md`](../deploy/README.md)
is the production runbook and environment reference, and
[`docker/README.md`](../docker/README.md) documents the development
infrastructure stack. Pre-rewrite documentation was removed with the rewrite;
use git history for archaeology.
