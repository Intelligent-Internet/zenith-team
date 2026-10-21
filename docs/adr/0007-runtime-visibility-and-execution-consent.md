# ADR 0007: Runtime registration, management and execution consent are separate

Status: Accepted. Date: 2026-09-09. Decisions 2–4 are superseded by
[ADR 0014](0014-private-runtimes-and-agent-bindings.md).

Later changes:

- [ADR 0011](0011-hosted-agents-and-invisible-private-runtimes.md) withdrew
  decision 2's administrative view of private registrations (a private
  runtime is visible to its registrant alone). It also added hosting consent
  to decision 4: the Agents a registrant binds to their own private runtime
  run for every active member.
- [ADR 0013](0013-members-create-agents.md) withdrew decision 5's owner-only
  Agent configuration. Any active member creates Agents on a runtime they may
  use, and the creator or a Company owner or admin configures them.
- [ADR 0014](0014-private-runtimes-and-agent-bindings.md) superseded
  decisions 2–4. A runtime is always private, with no sharing decision to
  take, and an Agent runs for a member only on a computer that member
  registered and bound it to. Decision 1 stands: registration is still the
  daemon's, and management is still separate from execution.

## Context

Before this decision, the daemon registered provider runtimes for every
Company its user belonged to. All active members could read that roster, and
the Company owner could bind Agents to any same-Company runtime. Registration
therefore exposed another member's connection and enabled execution without
an explicit sharing decision. Calling the row “Registered by Sam” explains attribution but does
not establish consent.

This approach was chosen after comparing a private default, member
filtering, administrative management and runtime-owner-only sharing. Company
remains Zenith Team's tenant boundary; Workspace remains a local execution
directory.

## Decision

1. Each provider Runtime in each Company defaults to **Private**. Its owner is
   the authenticated registrant, not the Company owner by implication.
   Registration/reconnection does not share it. Sharing one provider in one
   Company grants nothing to another provider or Company.
2. Ordinary members list their own runtimes and those explicitly shared with
   the Company. Company owners/admins may also list private registrations for
   management. Their private-registration projection excludes usage, work
   totals, local configuration and machine diagnostics.
3. Only the runtime owner may change **Private ↔ Public**.
   Company owners/admins cannot share another person's runtime, including via
   rename, replacement registration, bulk edits or ownership transfer.
4. A private runtime permits execution authorized by its runtime owner only.
   A shared runtime permits execution authorized by active Company members,
   subject to existing Agent, Task, Conversation, Schedule, Resource and
   Connector permissions. Company owner/admin roles do not override this gate.
   Unknown/inactive runtime ownership authorizes nobody.
5. Company Agent configuration remains owner-only. Passing runtime access
   does not grant Agent-edit rights. Company owners/admins and the runtime
   owner may rename/remove registrations subject to lifecycle constraints;
   that management right grants neither Agent migration rights nor remote
   process control. Removal requiring Agent changes still needs the Company
   owner's explicit compatible migration or prior rebind.
6. Changing sharing must be enforced on the server and at execution admission.
   A frozen Run preserves inputs and attribution, not irrevocable permission.
   Restoring sharing does not resurrect cancelled Runs or silently restart work.

## Alternatives and tradeoffs

- **Automatic Company-wide use after registration:** least setup friction,
  but membership becomes implicit permission to spend another person's
  provider credentials and machine capacity. Rejected.
- **Company owner/admin override of private access:** easier operational
  recovery, but makes the registrant's Private choice ineffective. Rejected.
- **Hide private registrations from all administrators:** stronger metadata
  privacy, but prevents Company registration management. The selected policy
  retains a deliberately limited management view; Private copy explains it.
- **Require manual enrollment in each Company:** avoids automatic roster
  entries, but is a different onboarding choice. Not part of this decision.
  Automatic registration remains; it produces private, unshared rows.

## Consequences

The first shared-Company setup needs a deliberate runtime-sharing review.
Solo founders may use their own private runtime; other members cannot trigger
new execution there until it is shared. Existing product work/read permissions
remain independent: making a runtime private does not erase already saved
Company Tasks, transcripts or results.

Existing rows cannot be silently grandfathered as shared. A private-default
cutover needs consent review, admission fences and truthful interruption
handling. Partial UI-only rollout is insufficient.
