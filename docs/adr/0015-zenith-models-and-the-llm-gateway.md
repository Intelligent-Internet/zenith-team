# ADR 0015: Zenith models: a built-in `zenith` provider whose model calls go through a Zenith LLM gateway

Status: Proposed. Date: 2026-09-24.

This is not built yet. It would lift two current product limits: Zenith Team
ships exactly two providers and proxies no provider API. Builds on
[ADR 0009](0009-run-scoped-cli-channel.md).

## Context

Every Agent Run needs a vendor CLI (Codex or Claude Code) and that vendor's
login on the member's computer. A person who has never heard of Codex cannot
get an Agent to work. The goal is that a non-technical person installs only
the desktop app, signs in with Zenith, and has working Agents. So Zenith must
supply both the agent engine and the model, and pay for the model.

The product rules forbid that today. Zenith Team holds no provider
credentials, proxies no provider API, and ships exactly two providers.
Zenith's own credentials are also a poor thing to hand to an agent process:
- The access token grants the member's whole authority.
- The refresh token is single-use, and a replay revokes the session.
- ADR 0009 already rejected giving the agent process any bearer credential.

## Decision

1. **A third provider, `zenith`, runs Codex.** It is the existing Codex
   adapter in a Zenith mode, driving the official, unmodified Codex binary
   that the desktop app ships.
   - Codex is Apache-2.0 and already integrated.
   - No other agent engine is in scope, and Codex is not forked.
2. **Zenith pays; users choose nothing.**
   - The model vendor's key lives only on the Zenith server.
   - One configured model serves every Zenith Run in a deployment. It is set
     in the server's `.env` (`LLM_GATEWAY_PROVIDER`, `LLM_GATEWAY_MODEL`,
     `LLM_CONFIGS`, `LLM_PRICING`, `OPENAI_API_KEY`, `DEEPSEEK_API_KEY`).
   - Users see the model and cannot change it.
3. **The Zenith session is the authority, and it never leaves the daemon.**
   - Codex gets a per-Run **LLM capability**, a local random secret, as its
     "API key".
   - The daemon's LLM channel swaps that capability for the daemon's Zenith
     session plus the run proof. That is the same pair ADR 0009's Run channel
     sends.
   - The gateway checks all of these on every call:
     - the session;
     - a live Run for the same Company;
     - that the Run's runtime is `zenith`;
     - the Company switch;
     - the model.
   - There is no new token type and no OAuth server.
4. **The gateway forwards; it doesn't translate.** Codex speaks only the
   OpenAI Responses API. A model vendor qualifies only if it has a native
   Responses endpoint; OpenAI and DeepSeek are first. Each vendor differs
   only by a list of named fixes to requests and streamed events.
5. **The server's count is the bill.**
   - Each LLM call is priced from `LLM_PRICING` and recorded immutably.
   - Each call reserves its estimate against a monthly **LLM allowance**
     before it is forwarded. Zenith sets that allowance per Company; the
     Company cannot.
   - Owners and admins can only turn Zenith models off.
6. **The gateway route speaks the vendor's protocol.** Its bodies are
   OpenAI's snake_case JSON and its errors are OpenAI's error objects, so
   Codex parses them. This is an explicit exception to the AGENTS.md rules on
   camelCase JSON and the `{detail, code, requestId}` error envelope. It is
   the same kind of exception `pkg/agent` already has for vendor formats.
7. **DeepSeek runs on DeepSeek's own API, behind a guard.** Its data is
   processed in China and may be used for training by default. So production
   refuses it until `LLM_GATEWAY_DEEPSEEK_APPROVED=true`, which is set after
   a legal sign-off, a training opt-out and a quality gate.
8. **Own-login providers are untouched.** A user's own vendor credentials
   still never reach Zenith. The Zenith Codex home reads nothing from
   `~/.codex`, so a user's own Codex app keeps working beside it.

## Alternatives and tradeoffs

- **Give Codex the Zenith token through its `auth.command` helper.**
  Rejected. The agent's shell could print a key to the member's whole
  account, a second refresher would risk revoking the session, and it breaks
  ADR 0009.
- **Mint a short-lived, model-only token per Run and let Codex call the
  gateway directly.** Deferred. It needs a new token type. It becomes worth it
  if Agents ever run in Zenith's cloud, where there is no daemon.
- **Another open-source agent engine, or several.** Out of scope: Codex
  only.
- **An off-the-shelf gateway (LiteLLM, Bifrost, an aggregator).** Rejected.
  LiteLLM has 2026 supply-chain and critical advisories; Bifrost needs a Go
  version the server doesn't use and drops MCP tools in its translation; an
  aggregator lets a third party see the data and charges a fee.
- **Translating Responses to Chat Completions for Chat-only vendors.** Not
  needed. The chosen vendors speak Responses natively, and a translator is
  a large piece of code to keep in step with every Codex release.
- **A model picker or per-Company model catalogs.** Rejected: one model at
  a time.
- **Company-owner spending caps.** Rejected for now: Zenith sets the
  allowance.

## Consequences

- **The provider set is a wide change.** It touches Go code, several SQL
  CHECK constraints, OpenAPI enums, the web app and Electron. The server must
  deploy before the app, because registration refuses a batch that names an
  unknown provider.
- **Prompts pass through Zenith.** They contain customer code but are never
  stored or logged. The privacy policy's subprocessor list must name each
  model vendor before it serves customers.
- **The installer grows** by about 95–110 MB per platform, and the bundled
  Codex updates only with app releases.
- **One model means one failure domain.** A vendor outage or a bad model
  change affects every Zenith Run at once. Staging goes first, and a `.env`
  rollback applies from the next claim.
- **Some docs change with the code, not now.** `AGENTS.md`'s provider
  vocabulary, `ARCHITECTURE.md` and `providers.mdx` describe shipped behavior,
  so they change with the implementation.
