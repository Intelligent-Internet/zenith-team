# Zenith Team branding

Zenith is the shared brand prefix. This product is **Zenith Team**; other
products can use names such as Zenith Agent and Zenith Studio. Use the full
name “Zenith Team” in navigation, page titles, onboarding, product copy,
desktop menus, notifications, and release names. Keep the existing symbol.
A build that serves a public website names its origin in
`VITE_ZENITH_TEAM_PUBLIC_SITE_ORIGIN`; canonical links, social metadata, the
sitemap, and links to the public legal pages use it. A build without it (such
as a self-hosted install) has none of them.

## Technical identities

Every technical identity uses the Zenith Team name. The product's earlier
name is a legacy name: new code, docs, and identifiers never use it, and
nothing reads it for compatibility.

| Kind | Identity |
|---|---|
| Go module | `github.com/Intelligent-Internet/zenith-team/server` |
| Command-line tool | `zen` (`zen auth login`, `zen daemon start`, `zen task get`) |
| Binaries | `zenith-team-server`, `zenith-team-mcp`, `zenith-team-readyz` |
| MCP server name | `zenith-team` (tools appear as `mcp__zenith-team__…`) |
| Environment variables | `ZENITH_TEAM_*` (for example `ZENITH_TEAM_HOME`, `ZENITH_TEAM_SERVER_URL`) |
| State directory | `~/.zenith-team` |
| HTTP headers | `X-Zenith-Team-Run-Token`, `X-Zenith-Team-Daemon-ID` |
| Cookies, metrics | `zenith_team_*` |
| Browser storage keys | `zenith-team.*`, `zenith-team:*` |
| Deep links, renderer origin | `zenith-team://`, `zenith-team-dev://`, `zenith-team-app://renderer` |
| Desktop bundle IDs | `com.ii.zenith-team`, `com.ii.zenith-team.dev`, `com.ii.zenith-team.local` |
| Desktop app and data folder | `Zenith Team`, `Zenith Team Dev`, `Zenith Team Local` |
| Windows Squirrel names | `zenithteam`, `zenithteamdev`, `zenithteamlocal` |

Use `ZENITH_TEAM_` / `zenith-team` / `zenith_team` rather than a bare
`ZENITH_` / `zenith` so Zenith Team never collides with another Zenith
product on the same machine. New installer downloads use the `Zenith-Team-`
filename prefix.

The rename was a clean break: installed desktop apps, signed-in sessions,
and registered daemons from before it do not carry over. Install the current
app and sign in again.

### Contracts that still use the legacy name

A few identities belong to systems outside this repository and change only
when those systems change first:

- the hosted service's domain names, repository and deployment names;
- the plugins repository's name and its per-plugin folder layout, read by
  `bundlectl` and pinned by commit in the lock files;
- the integration target the bundled Zenith Studio CLI exports skills for.

## Legal and external branding

Public legal copy uses the product name with unchanged substantive terms.

External branding, including OAuth consent screens, hosted email templates,
and deployment domains, is managed in the corresponding provider
configuration. Repository changes do not publish those external settings.
