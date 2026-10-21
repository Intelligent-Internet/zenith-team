# ADR 0013: Any member creates Agents; the creator and the Company's owners configure them

Status: Accepted. Date: 2026-09-20.

Amends [ADR 0007](0007-runtime-visibility-and-execution-consent.md) decision
5 and refines [ADR 0011](0011-hosted-agents-and-invisible-private-runtimes.md).
Amended by [ADR 0014](0014-private-runtimes-and-agent-bindings.md): creating
an Agent no longer places it on a machine for the whole Company. Any member
still creates Agents and the creator still configures them; the create may
set the new Agent up on one of the creator's own computers, which is their
binding and nobody else's.

## Context

Before this decision, every Agent operation was the Company owner's: members could work
with Agents but not create, read the roster of, or change them. Combined with
ADR 0011 that left a gap: in a team workspace, any member should be able to
create their own agents on a runtime they may use, and an
agent on a member's private machine is that member's to place and the
workspace's to use. In Zenith Team a member's private machine could host a
Company Agent only if an owner placed one there while the machine was public,
which is the one arrangement hosting consent does not cover. Users with any
role should be able to create Agents.

## Decision

1. **Any active member creates Agents**, on a runtime they may use — their
   own, or one shared with the Company. Creation sits outside the permission
   catalog, like runtime consent (ADR 0012 decision 4): it is a per-object
   rule, not a role's grant. The root Agent is the exception both ways:
   creating it is the onboarding step that stamps the Company's agent-setup
   completion, and configuring it stays with `agents.manage` whoever created
   it, so an onboarding owner who is later demoted does not keep the CEO
   through the creator record. A bind onto a runtime the caller cannot see —
   another member's private connection — answers `404 runtime_not_found`
   like an unknown id, never a 403 that would confirm the registration.
2. **The creator is recorded** (`agents.created_by_user_id`, attribution, no
   FK). Agents that predate the record have none.
3. **Configuration is the creator's and the `agents.manage` holders'** — the Company owners and admins of the permission catalog ([ADR 0012](0012-roles-assign-permissions-enforce.md)).
   Update, rebind, archive and restore are refused for anyone else with
   `403 agent_creator_required`. The rule is "only the agent's owner, or an
   admin, can manage this agent", with the creator standing in for the
   owner a Company-owned Agent does not have.
4. **Every active member reads every Agent.** Agent configuration is not
   secret, and members need the roster to give work, to pick a parent for an
   Agent they create, and to see whose machine an Agent lives on.
5. **Machine operations follow runtime access, not role.** Model discovery
   and setup refresh are open to any member who may use the runtime — its
   registrant, or anyone while it is public. Runtime rename and removal stay
   with `runtimes.manage` (owners and admins), the registrant renaming their
   own machine aside, and only for connections the caller can see; the
   Company's Connector and Plugin policy stays with `connectors.manage` and
   `plugins.manage`.
6. **Hosting consent now reaches every member.** A member who creates an
   Agent on their own private machine is its binder and its registrant at
   once, so the whole Company may work with it there (ADR 0011). The client
   renders configuration controls from the server's per-viewer
   `access.configure` on the Agent, never from a role. Because that field
   and `runtime.access.execute` are the viewer's, a lifecycle event carries
   the acting member's verdicts; a client applying `agent.created` or
   `agent.updated` keeps its own and refetches when the binding or the
   creator changed.

## Alternatives and tradeoffs

- **Keep creation owner-only and rely on sharing:** a member's machine would
  host Company Agents only while public, and an owner's rebinding could not
  give the member's binding the hosting standing. Rejected: it leaves the
  gap this decision closes.
- **Let every member configure every Agent:** simplest, but it lets any
  member rebind another member's Agent onto a machine and revoke that
  member's hosting by accident. The creator-or-owner rule keeps the person
  who placed the Agent in charge of it.
- **Per-Agent invocation policy:**
  not adopted; Company Agents remain the Company's to work with.

## Consequences

Members see the Agents page, can add Agents and see whose machine each runs
on. An Agent's profile is read-only for everyone but its creator and the
Company's owners and admins, with a note naming who can change it. Onboarding
is unchanged. The `zen agent create` command works for members through the
same route. Existing Agents, created before the record, stay configurable by
owners and admins alone. Nothing stamps a creator after the insert — a
rebind rewrites the binder, never the creator — so only recreating such an
Agent gives it one.
