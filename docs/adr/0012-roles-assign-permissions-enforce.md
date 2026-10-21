# ADR 0012: Roles assign, permissions enforce: the Company permission catalog

Status: Accepted. Date: 2026-09-19.

Amended by [ADR 0014](0014-private-runtimes-and-agent-bindings.md):
`runtimes.manage` leaves the catalog — a computer is its registrant's and
nobody else can see it — and `tasks.assign` joins it, held by owners and
admins, for handing a Task to another member.

## Context

A Company membership has carried a role — `owner`, `admin`, `member` — since
the tenancy schema, but only `owner` meant anything. `admin` was inert:
nothing could assign it, every invitation and share link granted `member`,
and enforcement was binary. Two middleware constructors, about thirty-five
hand-written comparisons against `owner` in eight domain packages, and
forty-six `role = 'owner'` predicates inside sqlc queries each encoded the
same product decision, and the client repeated it at some eighty sites and
rendered an `admin` as "Member".

Enterprise use needs a real permission model: a person holds a role, the
role decides what they may do, the server enforces it, the client hides what
is not allowed, and a change of role reaches every open session live. Two
common shapes fall short. Flat roles matched by exact string at the router,
with fine resource rules inside handlers, leave each check site encoding
policy. An integer ladder declared per handler and copied into a client
permission store drifts between client and server when the server publishes
no resolved permissions, and it does not carry a role change to open
sessions.

## Decision

1. **Roles are assigned; permissions are checked.** A closed catalog of
   named permissions (`agents.manage`, `members.invite`,
   `mailbox.manage`, …) lives in one file, `internal/auth/permission.go`,
   with one static table mapping each role to its set. A handler, service
   or query asks `Can(permission)`; nothing outside that file compares a
   role to `owner`. A route declares its permission through
   `middleware.RequireCompanyPermission`; a service rechecks it beside the
   lock it takes; a query that must stay honest under concurrent revocation
   takes the roles holding the permission as a `text[]` parameter.
2. **The server publishes the resolved set.** Every `Company` read carries
   the calling member's `permissions`, and `auth.company_access_changed`
   carries the new `role` and `permissions` when it changes. The client
   gates every affordance on that set and never re-derives policy from the
   role name. `company.updated`, which reaches every member, omits both.
3. **Three roles ship.** An owner holds every permission. An admin holds
   every permission except `company.delete`, `members.owners.manage`,
   `mailbox.manage` and `results.publish`. A member holds none of the
   catalog and works through the member-gated routes. A company keeps at
   least one active owner; ownership moves only through the role-change
   and transfer-ownership routes, and nobody is invited as owner.
4. **Two rules stay outside the catalog.** Runtime consent — a
   registrant's Private/Public decision (ADR 0007, ADR 0011) — is not a
   permission and no role overrides it: an admin may remove or rename a
   runtime they can see but never use a private one. Actor type is a
   property of the request: a Run acting through its proof holds its
   authorizer's role but may not invite, remove, promote, transfer or
   delete (`middleware.RequireHumanActor`, `403 human_actor_required`).
5. **Facts about the invitation surface reach only its audience.**
   `invitation.changed` and `invitation.share_link_changed` name their
   recipients — the active members holding `members.invite` at commit —
   inside the mutation transaction, and the router fans out to those
   sockets alone. The hub caches no role.

## Alternatives and tradeoffs

- **A role-hierarchy helper only (`role.AtLeast(admin)`):** cheapest, but
  every check site still encodes a product decision, adding a fourth role
  is another sweep, and the client can receive no resolved answer.
- **An integer ladder with project-level roles:** Zenith Projects are
  ownership containers, not membership scopes, so there is nothing to cap
  a project role against, and integers buy ordering comparisons that named
  permissions make unnecessary.
- **A policy engine (ABAC / ReBAC):** the per-object rules the product has
  — registrant consent, creator-private conversations and schedules,
  origin-scoped approvals — are already targeted predicates that ignore the
  company role on purpose. An engine adds operating cost and nothing the
  product needs.
- **Custom roles now:** the catalog makes them a later table-in-database
  change rather than a rewrite; not shipped until a customer needs
  per-company role editing.

## Consequences

`admin` is a real tier: an admin runs the company day to day and cannot
end it or its money. A read-only Viewer is one catalog row and one CHECK
change away, plus daemon-plane gating that this decision does not do. The
one owner-only rule that is not the owner's alone — publishing results to
public URLs — stays owner-only until publication is decided separately, and
is named `results.publish` so that decision changes a row, not a sweep. The old
`company_owner_required` code is gone; clients branch on
`company_permission_required` and, for machine actors,
`human_actor_required`.

## Amendment: artifact and channel privacy (2026-09-22)

The catalog now grants `conversations.discuss` to Owner/Admin/Member and
`channels.manage` to Owner/Admin. No Company role grants channel content access
without current membership. Artifact grants remain separate from Company roles.
As amended on 2026-10-05, Artifact owners/managers may create public links for
any Artifact without approval or a Company policy. The former
`company.sharing.manage` permission and its policy surface are removed. The older
`results.publish` publication surface remains disabled pending an authorized
application gateway.

## Amendment: Project access scopes (2026-09-28)

[ADR 0017](0017-project-access-scopes.md) makes Projects access scopes and
retires this record's line that Projects are "ownership containers, not
membership scopes". Roles still assign and permissions still enforce: the
Company catalog is unchanged, and a second closed catalog of Project
permissions is granted per person on a Project, never through a new role.
`projects.manage` keeps creating Projects and gains settings, grants and
archive on every Project, but no content. The rejection of a policy engine
above stands.
