---
title: Connectors and plugins
summary: Company policy over provider-discovered tools and packages
---

Connectors and Plugins are owned by the Agent domain. PostgreSQL is the
authority for Company policy and the latest discovery projection; provider
credentials and provider-local installation state remain daemon-local.

The user plane lists Runtime-scoped seek-paged catalogs and coordinates setup
actions. Reading a catalog and asking for a refresh are any active member's,
for a Runtime they registered: the computer is theirs (ADR 0014), so neither
needs a Company permission. Plugin install and uninstall take
`plugins.manage`, which every role holds because they reach only the
caller's own computer; resolving and accepting a Connector
contract set Company policy and stay `connectors.manage` (owners and
admins). A Runtime publishes replacement discovery snapshots through the
daemon bearer plane:

```
PUT  /api/v1/daemon/companies/{companyId}/runtimes/{runtimeId}/connectors/discovery
POST /api/v1/daemon/companies/{companyId}/runtimes/{runtimeId}/connectors/discovery/reaffirm
PUT  /api/v1/daemon/companies/{companyId}/runtimes/{runtimeId}/plugins/discovery
```

Each snapshot carries a UUID generation, a canonical manifest digest, and an
observation time. Exact generation replays are harmless and renew nothing;
incompatible reuse is `connector_discovery_conflict` or
`plugin_discovery_conflict`. Like every runtime-scoped daemon route, all
three require `X-Zenith-Team-Daemon-ID` to match the runtime's `daemonId`; a
mismatch is `404 runtime_not_found` (see
[daemon.md](daemon.md#authentication-and-transport)).

A Run claim admits only Connector discovery observed in the last ten
minutes. A running daemon therefore re-observes its providers about every
three minutes. When the manifest is unchanged it sends
`{schemaVersion: 1, generation, manifestDigest, observedAt}` to `reaffirm`
instead of the whole snapshot: the server moves only that generation's
observation time forward and answers `{applied}` (`false` when the time is
not newer). A generation or digest that is not the Runtime's current one, or
a Runtime with none, is `409 connector_discovery_conflict`, and the daemon
then publishes the full snapshot under a new generation; it does the same on
a `404` from a server without the route. Plugin discovery has no reaffirm.

Codex plugin items may also carry presentation: `longDescription` (installed
or featured plugins only), up to three `defaultPrompts`, `brandColor`,
`developerName`, `websiteUrl`, `privacyPolicyUrl` and `termsOfServiceUrl`.
They follow `connectorIdentities` in that order and are part of the manifest
digest. The daemon sends them only after a heartbeat acknowledgement from the
server listed `plugin-presentation-v1` in `serverCapabilities`. Plugin
discovery decodes strictly, so an older server would refuse them. A daemon
that sends none keeps working. Claude items never carry them.

The daemon also completes Codex items that Codex left bare, with no wire
change. For a plugin with no display name or short description it fills
the empty `name`, `description`, `developerName` and `websiteUrl` from the
marketplace's own `.claude-plugin/marketplace.json`. A plugin still without
a name gets a readable one from its slug ("runway-api" is "Runway API").
`catalogRank` puts the plugins of OpenAI's own marketplaces (`openai-*`)
ahead of all others, in Codex's order within each group. The digest covers
the items as sent, so each Runtime republishes its Codex catalog once after
the daemon update.

A plugin discovery snapshot holds at most 10,000 items, with `catalogRank`
0–9,999. The limit used to be 5,000. Codex's catalog has passed 5,000, and
an older server refuses a larger snapshot with `422 validation_failed`.
A connector discovery snapshot holds at most 10,000 items in an 8 MiB body.
The limit used to be 5,000 items and 2 MiB, and an older server refuses a
larger snapshot. Codex's hosted app directory has held over 4,500 apps
(about 1.8 MB) and grows by hundreds a week.

A Connector is an executable provider integration with an exact source/native
identity and reviewed tool contract. A Plugin is an installable provider
package and may name Connector identities it contributes. Provider is always
`codex` or `claude`; no provider-native configuration or secret crosses the
server interface.

List query parameters use snake_case. In particular the Connector review
filter is `review_required=true`. It also lists Connectors that lost their
connection since their review was flagged; only a Connector still
`connected` with `canExecute` can have its changed contract accepted by a
reconnect, so the Settings pages offer **Reconnect** for those alone and
**Connect** for the rest.

## Select the exact computer

Both `GET /companies/{companyId}/connectors` and `GET
/companies/{companyId}/plugins` accept `runtime_id=<uuid>` alongside the optional
`provider`. Keep both filters unchanged when following `nextCursor`.

An explicit Runtime may be offline or have no assigned Agent. It must be one
the caller registered in this Company and not removed: a runtime is its
registrant's alone (ADR 0014), so another member's computer is never a
discovery context, even for the Company owner. A `runtime_id` that is not the
caller's — another member's, another company's, removed or unknown — is
`404 runtime_not_found`, the answer every runtime route gives. If `provider`
is supplied it must match: the caller's own Runtime of the other provider is
the provider-unavailable conflict (`409`). Neither falls back to a different
computer. Omission keeps the caller's default Runtime, the one carrying a
binding of an active Agent.

An offline response is last-reported discovery, not proof of current execution
availability. The Plugin list's `observedAt` is when the selected Runtime last
reported its catalog, or `null` before its first report; `stale` is true once
that is more than ten minutes ago or the Runtime has not been seen recently. Per-Agent switches are documented in [Agents](agents.md#configure-inherited-capabilities).
Plugin install/uninstall and Connector review/setup retain their existing routes;
a Plugin preference never writes a Connector policy.

## Plugins the list leaves out

`GET /companies/{companyId}/plugins` leaves out a Codex Plugin that copies a
curated one. The Plugin is left out when all of these hold:

- its marketplace, the part of its native id after the last `@`, does not
  start with `openai-`;
- it is not installed;
- the same Runtime observes an `…@openai-curated-remote` Plugin that is
  installed or can be installed, with the same name ignoring case, spaces
  and punctuation;
- the slugs, developers or website hosts also agree, or the curated id is
  an opaque `app-…` id. `github.com` and other code-hosting or package
  sites do not count as agreeing hosts.

The rule is decided when the snapshot is stored, so the rows, `totalCount`,
`categoryCounts`, `query` search and `nextCursor` paging all leave out the
same Plugins. `installedCount` is unaffected, since a left-out Plugin is
never installed. `getPlugin` still reads such a Plugin by id, and installing
it lists it again from the next discovery. Claude catalogs and OpenAI's own
marketplaces are never filtered. No field or parameter changed.

## Borrowed logos

A Claude Plugin reports no logo. Some Codex Plugins report none either, or
only one on `chatgpt.com`, which loads only in a signed-in ChatGPT session.
For such a row, the list and the read below set `iconUrl` to the first
loadable logo of:

1. a Codex Plugin with the same name ignoring case, spaces and punctuation,
   else a Codex app with the same name ignoring case;
2. the only Codex Plugin with a logo whose website has the same host, not
   counting `www.` (never a code-hosting or package site such as
   `github.com`);
3. the only Codex Plugin with a logo and the same developer, ignoring case.

Steps 2 and 3 lend only when the lender's name agrees with the borrower's,
for example "Sentry CLI" and "Sentry". Lenders are observed on one of the
caller's own Runtimes in the same Company (ADR 0014); another member's
computer never lends. A `chatgpt.com` logo is never lent. A Claude Plugin has no website
or developer, so it borrows by name only. With nothing to borrow a row keeps
its own `iconUrl`, if any, and the client draws a monogram when there is no
logo that loads. Workflow app readiness borrows by name only.

## Read one Plugin

```
GET /api/v1/companies/{companyId}/plugins/{pluginId}?runtime_id=<uuid>
```

`getPlugin` serves the Settings plugin page. Like the list, it is a member
read of one of the caller's own Runtimes (ADR 0014; an older server
required `plugins.manage`). `runtime_id` is optional and means what
it means on the list: one of the caller's Runtimes, or else the caller's
default Runtime, the one carrying a binding of an active Agent. It never
falls back to a different computer. The
response is `PluginDetailResponseV1`:

```json
{
  "schemaVersion": 1,
  "plugin": { "id": "0199…", "provider": "codex", "nativeId": "gmail@openai-curated-remote", "name": "Gmail", "status": "installed", "installed": true, "action": "uninstall" },
  "runtime": { "id": "0199…", "name": "MacBook Pro", "online": true },
  "stale": false,
  "longDescription": "Use Gmail to summarize inbox activity…",
  "defaultPrompts": ["Summarize the last 5 messages in [subject line]…"],
  "brandColor": "#EA4335",
  "developerName": "Google",
  "websiteUrl": "https://…",
  "apps": [
    { "source": "codex_app", "nativeId": "connector_…", "connectorId": "0199…", "name": "Gmail", "description": "Find and reference emails from your inbox.", "iconUrl": "https://…", "status": "connected", "action": "none" }
  ],
  "usedBy": [{ "agentId": "0199…", "name": "CEO Assistant" }]
}
```

- `plugin` is the same projection as a catalog row (abbreviated above),
  with the same borrowed `iconUrl`. A Plugin the list leaves out as a copy
  is still read here.
- `stale` is the catalog's own flag: the Runtime was not seen recently, has
  no plugin snapshot, or its plugin discovery is older than ten minutes.
  Plugin discovery has no keepalive, so it is usually `true` on a
  long-running daemon. The Settings plugin page does not
  read it; it shows `plugin.observedAt` as "Synced … ago" and says it shows
  the last report only when the Runtime is offline (`runtime.online`) or, on
  Desktop, the live read failed.
- The presentation fields are omitted when the Runtime did not report them:
  every Claude Plugin, a daemon without `plugin-presentation-v1`, and the
  long description of a Plugin that is neither installed nor featured.
  `defaultPrompts` is always present and may be empty.
- `apps` resolves each of the Plugin's `connectorIdentities` by exact source
  and native id against that Runtime's Connector observations, else the
  Company's Connector catalog, ordered by name. An app the Runtime has not
  observed as a Connector has no `connectorId`, `status` or `action`.
- `usedBy` lists the unarchived Agents the caller has bound to that
  Runtime, when the Runtime observes the Plugin installed and enabled and
  the Agent has not turned it off. Two members reading their own computers
  each see their own Agents.

Another Company's Plugin, a caller with no Runtime of the Plugin's
provider (or, with `runtime_id` omitted, none that carries a binding of an
unarchived Agent), a `runtime_id` that is not one of the caller's Runtimes
(another member's, another company's, removed or unknown), or a Plugin the
selected Runtime has not observed, is `404 plugin_not_found`. Unlike the
list, which answers such a `runtime_id` with `404 runtime_not_found`, the
page answers `plugin_not_found` for all of them. An unknown, repeated or
empty query parameter is `422 validation_failed`. The read writes nothing
and grants nothing.

On Zenith Team Desktop running on the Plugin's computer, the plugin page also
reads the Plugin live through that computer's daemon loopback server. That
read is not part of this API and is never stored.

## Refresh a computer's catalog

`POST /companies/{companyId}/plugins/refresh` and
`POST /companies/{companyId}/connectors/refresh` take `{"schemaVersion":1}`
and act on the caller's default Runtime. The server cannot reach the machine,
so the answer for a Runtime of the caller's is `503 plugin_action_unavailable`
or `503 connector_action_unavailable`; with no Runtime of the caller's it is
the provider-unavailable `409`. Zenith Team Desktop refreshes by asking its
own daemon, which re-reads the provider and publishes a new snapshot through
the daemon plane above. Only the daemon that registered a Runtime can publish
for it, so a member can refresh only their own computer, and only from it.
