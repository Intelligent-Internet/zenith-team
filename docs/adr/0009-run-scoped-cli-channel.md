# ADR 0009: The zen CLI reaches the product API from inside a Run through a daemon-brokered channel, never with a credential

Status: Accepted. Date: 2026-09-15.

## Context

Agents need a command-line way to read their Task and Conversation, ask the
founder a Question and report a Task failure, so that built-in Skills can
name concrete commands. The provider process
the daemon launches already has an isolated `ZENITH_TEAM_HOME` with no
credential, a copy of the CLI on `PATH` (for scoped commands), and the
run-scoped `brt_` token in its environment. The server accepts that token
only as *proof* beside an ordinary bearer session: it never authenticates a
request on its own, and there is exactly one principal type.

Two obvious designs were rejected. Handing the shell a bearer session (the
daemon owner's, or a new run-issued one) breaks the rule that the provider
process holds no credential: a provider that can read a human credential can
bypass every Run-scoped tool. Teaching the server to mint a principal from the run token
alone would add a second authentication model to every read endpoint and
would have to re-implement Run scope (own Task, own Conversation) in each
handler, or leak the origin user's whole Company to the agent.

## Decision

The daemon owns the channel. For every attended Run (Task or direct
Conversation; never a Schedule) it writes `run.json` into the Run's isolated
`ZENITH_TEAM_HOME`: the daemon's loopback endpoint, a 32-byte local capability
bound to that Run, the frozen Run identity, and the list of commands the
Run's policy admits. The CLI reads the file, enters **Agent mode**, and posts
every product call as a `RunChannelRequest` to `POST /v1/run/api` on the
daemon. The daemon checks the capability, matches the request against a
closed `(method, path)` policy derived from the same frozen execution profile
that decides the zenith-team-mcp tool allowlist, and forwards it to the server
with its own session plus `X-Zenith-Team-Run-Token` and the caller's
`Idempotency-Key` — the exact pair zenith-team-mcp sends. The server is
unchanged. The channel dies with the Run: revocation cancels in-flight
forwards and drops the capability.

Agent mode never falls back to Operator credentials: a Run environment
without `run.json` fails, flags cannot move the Company, and operator
commands (`auth`, `daemon start|stop`, `config set`, `company use`) refuse to
run inside a Run. The system prompt advertises exactly the admitted commands,
so the agent is never told about a command the daemon would refuse.

## Consequences

- One transport-agnostic command implementation serves both modes; the
  difference between a terminal and a Run is which requests the server, via
  the daemon's policy, admits.
- Artifact uploads are requested through the `present_output` MCP tool and
  completed in the daemon over the Run channel; the
  former MCP presentation tool is retired. MCP-local reads such as the Skill
  index remain MCP tools. Adding another route to the channel is a policy row
  in `runCLICommands`, not a new auth path.
- The channel costs one loopback hop per call and requires the daemon that
  launched the Run, which is always alive for the Run's duration.
