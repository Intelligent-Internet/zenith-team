# ADR 0016: Platform content bundles are built in a separate plugins repository and pinned here

Status: Accepted

## Context

Zenith Team ships platform content that is not code: the `zenith-workflows`
catalog (per Workflow, a `workflow.yaml` form the page shows and a
`playbook.md` a Task Run loads) and release data for managed Tools such as
Zenith Studio (exact npm versions, integrities and expected skill files).
This content changes more often than the code and has its own authors and
reviewers. Hundreds of content files in the product repository crowd its
history and its reviews.

The content must still behave like part of a release. Every Company must see
the same catalog for a given release. No build, test or fresh clone may need
a credential. The `zen` CLI must not carry the Workflow bytes. A Windows
checkout must not change a single byte, because the skill loader matches
`SKILL.md` frontmatter lines byte for byte.

## Decision

1. **The content lives in a separate plugins repository.** It is written,
   reviewed and released there. Each plugin has one version and is released
   with a tag of the form `<plugin>--vX.Y.Z`.
2. **This repository vendors each release as a pinned, verified copy.**
   - Workflows: one canonical, uncompressed USTAR tar and one lock per plugin
     component in `server/internal/platformbundle`
     (`zenith-workflows.bundle.tar` and `zenith-workflows.lock.json`). The
     lock names the plugin, version, tag and source commit, the tar's size
     and sha256, a tree hash over every file, and the retired Workflow keys.
   - Managed Tools: one metadata-only lock in
     `server/internal/daemon/tool_releases`, with no archive.
   - The Workflow skill's `SKILL.md` stays here, in
     `server/internal/platformbundle/harness/`, beside the skill's name and
     description. A content release cannot break its frontmatter.
3. **`bundlectl` (`server/cmd/bundlectl`) is the only writer.** Never edit a
   tar or a lock by hand.
   - `make bundle-bump TAG=<plugin>--vX.Y.Z` runs `bundlectl import`. It
     reads the tag with the maintainer's own git credentials, checks the
     plugin with the server's own parser and loader, and applies the release
     rules: the version never goes down, and a removed Workflow key must be
     retired in a MAJOR release. Managed Tools add `COMPONENT=managedTools`.
   - `make bundle-verify` re-imports every pinned tag and requires identical
     bytes. `make bundle-diff` shows what a pin change adds, removes or
     changes, compared with `BASE` (default `origin/main`).
4. **The server checks every tar against its lock at load:** size and
   sha256, a strict tar layout, the tree hash and file count, text encoding
   and line endings, no retired key, and a canonical re-encode. On any
   mismatch the server refuses to start. `zenith-team-mcp` drops a bundle
   that fails and keeps serving the compiled platform skill.
5. **`make tools-prepare` (`bundlectl prepare`) resolves managed Tool
   skills** before the daemon and CLI are built. It downloads the exact
   public npm packages the lock names, verifies them, and writes the skills
   and license into the git-ignored `generated/` folder. A checksum-keyed
   cache lets later builds work offline. Make targets, CI and desktop builds
   run it first.
6. **The `zen` CLI never links the bundle bytes.** `make go-shape` fails if
   `cmd/zen` depends on `internal/platformbundle`. Shared checks live in the
   asset-free `internal/contentbundle` package.
7. **`.gitattributes` keeps pinned files byte-exact.** It marks the tar as
   binary and the locks and harness files as LF-only, so a Windows checkout
   builds the same bytes.

**Who can update a pin.** Only maintainers with access to the plugins
repository can bump or verify a pin. Contributors without that access build
and test against the committed pin and cannot change it. `make bundle-diff`
reads only this repository, so any reviewer can run it on a pin change.

## Consequences

- Every Company sees the same catalog for a given Zenith Team release, and
  the same commit always carries the same content.
- Content changes reach users only with a release that bumps the pin.
- No build or test needs a credential. The first build needs public npm
  access for managed Tools; later builds can use the cache.
- New vocabulary, such as a category, field type or icon, lands in this
  repository first. Content can use it only after that.
- A pin change shows a binary diff; reviewers read `bundle-diff` instead.
- Outside contributors cannot change Workflow content here. They propose
  Workflow content changes through an issue.

The compiled skill allowlist also includes `zenith-intelligent-ui`, whose
authoring guidance ships with the inline document grammar. Only explicitly
named skill directories are embedded.
