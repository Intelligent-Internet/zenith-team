# Pinned platform content

This directory pins the platform content zenith-team takes from
[Intelligent-Internet/boardly-plugins](https://github.com/Intelligent-Internet/boardly-plugins)
([ADR 0016](../../../docs/adr/0016-platform-content-bundles.md)).
Each pinned component is two files, a canonical uncompressed USTAR tar and a
lock, and both are committed, so no build, test or fresh clone needs a
credential or the network.

Today one component is pinned: plugin `zenith-workflows`, component
`workflows`, version `1.0.0`, tag `zenith-workflows--v1.0.0`. The artifact
`zenith-workflows.bundle.tar` holds the Workflow catalog, 145 Workflows as
290 files (`<key>/workflow.yaml` and `<key>/playbook.md`), in 1,527,808
bytes with SHA256
`fdc5b261668cc4b4218e6c1fe4cfbaf50f0f7ee0114218af8b9354f1ea092282`.
`zenith-workflows.lock.json` is the source of truth: it also names the
boardly-plugins commit, the manifest's SHA256, the content folder's git tree
id, the tree hash over all 290 files and the retired keys. Where this file
and the lock disagree, the lock wins.

Version 1.0.0 carries the catalog zenith-team held in
`internal/mcp/tools/builtin/zenith-workflows/` before the move. The tar's
files plus the harness below are byte-identical to that directory, and the
skill's `ContentHash` is unchanged.

The content lives in boardly-plugins, under
`plugins/zenith-workflows/boardly/workflows/`, and is released there with a
`zenith-workflows--vX.Y.Z` tag. Nothing reaches a Run until this repository
bumps the pin.

## The harness stays here

`harness/zenith-workflows/SKILL.md` is laid over the pinned files at `.`,
so the page and a Task Run read the same skill root as before the move. It
stays in zenith-team, beside the skill's name and description constants in
`platformbundle.go`, because zenith-team-mcp matches its `name:` and
`description:` frontmatter lines byte for byte, and its "Connected apps"
and "Attached files" sections mirror the daemon's Run policy and the Task
footer. A content author can therefore never break every Task Run by
editing a frontmatter line.

## Verification at load

`Load` reads every `<plugin>.lock.json` once per process. The lock must
decode strictly and equal the bytes `MarshalLock` writes, every tar must
have a lock, and then `Decode` checks the tar in this order:

1. size and SHA256 against the lock;
2. a strict USTAR allowlist in bytewise path order: regular files only,
   mode 0644, owner 0:0, mtime 0, and only `<key>/workflow.yaml` and
   `<key>/playbook.md`;
3. the tree hash and file count;
4. UTF-8 with no byte order mark, LF only, and the size caps;
5. no retired key present;
6. a canonical re-encode that must equal the artifact.

`WorkflowCatalog` then lays the harness over every verified workflows
component and refuses a Workflow key pinned by two plugins.
`internal/workflow` parses that catalog for the page and refuses it whole on
any error. `tools.PlatformSkills` and `tools.OpenTaskSkillService` load it
as a platform skill: a reserved key, the frontmatter lines byte for byte and
the skill caps.

## When verification fails

- **The server** does not start. `cmd/server` loads the Workflow catalog and
  every pin before it opens the database, and logs each pin's plugin,
  component, version, SHA256 and commit.
- **zenith-team-mcp** drops the failing bundle and says so on stderr. It keeps
  serving `zenith-platform` and the Commerce procedure, and the dropped key
  stays reserved, so no custom Skill snapshot can take it.

## Bumping to a new release (maintainers only)

`bundle-bump`, `bundle-diff` and `bundle-verify` are maintainer tools.
Contributors do not need them: the tar and the lock are committed, so every
build and test works offline.

The import reads boardly-plugins with git, using your own GitHub credentials:

```sh
gh auth login && gh auth setup-git
```

Then, from the repository root:

```sh
make bundle-bump TAG=zenith-workflows--vX.Y.Z
make bundle-diff
make gate
```

`bundle-bump` runs `bundlectl import`, which reads the tag (it must be on
boardly-plugins' `main`), validates the plugin with the server's own rules
and rewrites the tar and the lock together. With `PLUGINS_REPO` set to a
local checkout that has an `origin`, the tag must also be the one origin
publishes, on origin's `main` as last fetched. `bundle-diff` shows which
Workflows and files the bump adds, removes or changes. Open the pull
request with the importer's summary in `.dev/bundle-bump.md` as its
description, since the tar's own diff is binary.

Both hold the bump to the release rules against the release it replaces,
and refuse it otherwise:

- the version never goes lower;
- a pinned version is re-imported only with byte-identical content, since a
  release tag never moves;
- a removed Workflow is listed in the plugin's
  `.boardly-plugin/retired-keys.json`, in a MAJOR release;
- a retired key stays retired, and no plugin ships or retires a key another
  plugin owns.

Re-importing a pinned version from another commit with byte-identical
content changes only the lock's commit, and the import says so. That is how
a provisional commit gives way to the tagged one:
`make bundle-bump TAG=zenith-workflows--v1.0.0` once the tag exists. When
the pin directory holds a development pin, or a pin that does not verify,
the import is judged against the release committed at `HEAD` instead, so a
bump also repairs a broken pin. If `HEAD` itself holds a development pin,
the importer searches first-parent history for the preceding release and
applies the same release rules. Complete history with no preceding release
allows a first release import; a corrupt historical pin or shallow history
without a release baseline is refused. Development pins still fail the gate.

To check that the committed pin is exactly the locked tag, re-import it and
expect no change:

```sh
make bundle-verify
```

## Local development

To try content from a plugins checkout before it is tagged:

```sh
make bundle-local PLUGINS=../boardly-plugins
```

That writes a development pin (version `0.0.0-dev+<12-hex commit>`, with
`.dirty` unless the plugin's files are exactly those of the checkout's
`HEAD`, and no tag), which the server and zenith-team-mcp serve like a release.
Files git ignores, such as a Finder `.DS_Store`, are never read. Undo it with
`git restore server/internal/platformbundle`. Never commit a development
pin: `TestPinnedBundlesAreReleases` fails the gate until a bump replaces
it.

To see every problem zenith-team would find in a plugins checkout, with the same
parser and loader the server uses:

```sh
make bundle-check PLUGINS=../boardly-plugins
```

It reads what a commit would carry, so files git ignores are left out, and
it holds a plugin this build already pins to the key rules above.

## Rules

- Never edit the tar or the lock by hand. `bundlectl import` is their only
  writer: `ParseLock` refuses a lock that is not in canonical form, and
  `Decode` refuses a tar that does not match its lock.
- The root `.gitattributes` marks the tar binary and the lock and the
  harness LF-only, so a Windows checkout builds the same bytes. The
  `server-windows` CI job proves it.
- `cmd/zen` must never link this package; that is why it is a package of its
  own. It imports `internal/mcp/tools` and the asset-free `internal/contentbundle`
  verifier, so zenith-team-mcp stays sealed.
- `testdata/format1.tar` pins the bytes `Encode` writes for format 1, so a Go
  release that changed `archive/tar`'s output cannot silently rewrite every
  committed artifact on the next bump. It changes only with the format.

## Managed Tool components

Studio shares release provenance checks through `internal/contentbundle`,
which owns the codec without embedding assets. Its single JSON lock lives under
`internal/daemon/tool_releases` and contains inline metadata and provenance,
without an archive. `bundlectl prepare`
downloads checksum-pinned npm skills and license into an ignored `generated/`
folder; the daemon and `zen` embed those assets with the metadata.
Use `COMPONENT=managedTools` with `bundle-bump`, `bundle-local` and
`bundle-diff`; `bundle-verify` checks both pin directories. A plugin declares
exactly one component; only Workflow pins need a tar beside their lock.
