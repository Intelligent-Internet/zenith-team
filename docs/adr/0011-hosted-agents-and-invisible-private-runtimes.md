# ADR 0011: A private runtime is invisible to everyone else, and the Agents its registrant hosts are the Company's to use

Status: Superseded by [ADR 0014](0014-private-runtimes-and-agent-bindings.md).
Date: 2026-09-18.

ADR 0014 keeps a private runtime invisible and drops hosting entirely: a
runtime is always private, and an Agent runs for a member only on a computer
that member registered. This record amended
[ADR 0007](0007-runtime-visibility-and-execution-consent.md) decisions 2 and
4. It is kept for the history of how hosting was reasoned about.

## Context

ADR 0007 made a provider Runtime private to the member who registered it
and let a Company owner or admin see another member's private registration
in a management projection. Testing the delivered behavior in a two-member
Company showed two problems.

First, the management projection is still a disclosure: the Company owner
learned that a member runs Codex on a laptop called "Bobs-Laptop" without
that member having shared anything. A private connection is nobody else's
business until its registrant makes it Public.

Second, the delivered rule judged every Run by the member who asked for it.
A Task assigned to Lyra, whose runtime is the Company owner's own private
machine, could therefore be started by the owner alone; a colleague opening
a chat with Lyra was refused with `403 runtime_access_denied`. That defeats
the point of Company Agents: the owner hosts Lyra on their laptop precisely
so the team can work with her. If Agents belonged to a person, the claim
could admit a private runtime whenever the Agent's owner is the runtime's
owner. Zenith Team's Agents are Company-owned and have no such owner, so
that consent has to be recorded another way.

## Decision

1. **Private means invisible.** A private Runtime is listed and readable by
   its registrant alone. No Company role — owner or admin — sees it, renames
   it, removes it or probes it. Reads by id answer `404 runtime_not_found`,
   exactly like an unknown id. The registration projection of ADR 0007 is
   removed. Stale private registrations of members who left are cleaned up
   by the existing offline sweep, not by an administrator: the sweep marks
   them offline and prunes a row once no Agent is bound to it and no Run
   refers to it. An Agent such a row still hosts is recovered by rebinding
   it (an Agent update never reads the current binding), never by removing
   the machine on the departed member's behalf.
2. **Hosting is consent.** The member who binds an Agent to a Runtime (any active member since ADR 0013)
   is recorded as the Agent's binder (`agents.runtime_bound_by_user_id`).
   When the binder is the Runtime's registrant, the registrant *hosts* the
   Agent: they put a Company Agent on their own machine, and that is their
   standing consent for every active member to work with it there — chat,
   Task assignment, Schedules, continuations and retries — while the Runtime
   stays private. This is an owner-of-agent rule, attached to the binding
   because the Agent itself has no owner.
3. **Public keeps its meaning.** A Public Runtime is visible to every active
   member; members may bind Agents to it and any member's request runs
   there. The share admits exactly the work that hosting does not: Runs on
   Agents that someone other than the registrant bound. Stopping sharing
   cancels those and only those; the registrant's own Runs and the Runs of
   the Agents they host continue.
4. **Binding moves consent, editing does not.** The binder is rewritten only
   when the Agent's Runtime changes — on create, rebind, duplicate and the
   Agent migration of a Runtime removal. Saving instructions, model or
   thinking level never touches it, so an owner editing a colleague's hosted
   Agent cannot silently revoke the Company's access to it.
5. **The verdict travels with the Agent.** Every Agent reference the client
   renders carries `access.execute`, the server's answer for that viewer,
   computed from the same predicate the enqueue and the claim apply. Pickers
   lock and composers explain from that field, never from a role or a
   visibility guess.

Cutover: a Runtime that is already private stays private and now
disappears from other members' rosters. Existing bindings are credited to
the registrant when the registrant is a Company owner — the only role that
binds — and left unattributed otherwise; an unattributed binding on a
private Runtime admits nobody but the registrant until an owner rebinds it.

## Alternatives and tradeoffs

- **Keep the management projection:** lets an owner remove a departed
  member's stale registration by hand, at the price of disclosing every
  member's machines and providers. Rejected; the sweep already removes
  stale rows.
- **Give Company Agents an owner:** the most direct fix, but it reintroduces per-person Agents into an
  Agents-are-Company-owned model (ADR 0010) and would make "who owns Lyra"
  a question with no product answer. The binder records the one fact the
  rule needs without a new ownership concept.
- **Judge Runs by the binder alone and drop the authorizer:** simpler, but
  the authorizer is what lets a share be revoked precisely, keeps a revoked
  member's queued messages from becoming Runs, and is the audit trail of
  who asked. Both facts stay.
- **Add a per-Agent invocation policy (private / allow-list):** a second consent layer on *who may trigger an Agent*. Not
  adopted; Company Agents are the Company's to work with. Listed as a
  possible future decision, not part of this one.

## Consequences

A solo founder's setup needs no sharing at all: their Agents run on their
private machine and every member they invite can work with those Agents
immediately. A member who wants to lend their own machine still has to make
it Public, and only then can the Company owner place Agents on it. The
owner cannot see or manage a machine nobody shared, and the client no
longer has a "registration only" state to render.

Two edges follow from "visibility is per Runtime row, a name is per
machine". A rename with `applyToMachine` reaches every row the same daemon
registered, a private sibling included, because the machine is visible to
the caller through the row they named and one laptop should not carry two
names. And a departed member's private row that still hosts Agents is not
removable by anyone (an amendment of 2026-09-20 added the rename and removal
fences): the Company owner rebinds those Agents one by one; the row stays offline
and invisible and is kept while Run history refers to it.
