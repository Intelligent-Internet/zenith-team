# Zenith Studio release metadata

`zenith-team-plugins/plugins/zenith-studio/boardly/managed-tools/release.json`
is the source of the reviewed npm versions, SHA512 integrities, host contract,
package-relative skill source directories and expected Skill hash/count/size.
The plugin and this committed pin contain metadata only. A single
`zenith-studio.lock.json` carries the descriptor and plugin release provenance;
there is no committed Studio tar, zip or skill snapshot. The lock is written
only by `bundlectl import`; never edit it by hand.

## Build

`make tools-prepare` downloads the pinned public npm tarball, verifies SHA512,
checks its package identity, composes the declared skill source directories,
and verifies every byte against the reviewed Skill reference. It carries the
upstream MIT license into the generated output. No npm installation, lifecycle
scripts, CLI execution, login or private GitHub access is needed at build time.

The npm cache lives under the OS user cache at `zenith-team/npm/`, keyed by
SHA512. Every cache hit is verified, so subsequent builds work offline.
Missing downloads or invalid content fail preparation. The generated
`generated/zenith-studio.json` is git-ignored and embedded in the binary.
A clean checkout needs preparation before direct `go build` / `go test` of the
daemon or CLI. Desktop builds and the Go Make/CI lanes do this automatically.
For cross compilation, run preparation on the host before setting GOOS/GOARCH.

The daemon verifies the generated assets against the descriptor and the
complete reviewed Skill before startup. Studio CLI `0.1.15` supplies 422 upstream
files (1,585,332 bytes) with hash
`e5a142a096eedf26a67c614fd8d6b4022d48f0c4cbc59d2ddb8f66430b9766d7`.
Team applies `studio_skill.md` and `studio_runtime.md` only after verification.
The runtime overlay includes audio delivery as media and safe source-canvas
reuse on continuations. A regression test pins the final Run snapshot hash.

## Update

After plugin CI creates the release tag on main:

```sh
make bundle-bump TAG=zenith-studio--v1.2.1 COMPONENT=managedTools
make bundle-verify
make tools-prepare
make bundle-diff COMPONENT=managedTools
```

Before that tag exists, use `make bundle-local PLUGIN=zenith-studio
COMPONENT=managedTools`, then `make tools-prepare`. The generated development
pin is never committed: `TestPinnedToolBundlesAreReleases` blocks the merge
gate until an actual released plugin tag replaces it.

If a development pin was already committed on a feature branch, `bundle-bump`
recovers the preceding release from first-parent history before applying the
release rules. With complete history and no preceding release, it imports the
first release. Corrupt historical pins and shallow history without a baseline
are refused; fetch full history before retrying. The incoming tag must still
be published on main, and the development pin still cannot pass the merge gate.

Authors update metadata in the plugin using `scripts/pin-release.py` with a
reviewed CLI export and npm integrity. That script stores hashes, not files.
Team owns authentication, Agent account isolation, Run launchers, project
bindings, the editor and Output delivery. Executable installation remains a
daemon responsibility. Schedules do not receive Studio; FFmpeg and ffprobe
remain runtime prerequisites.

## Verification

Run `make go-shape`, `make go-sqlc-check`, the affected package tests, and
`make gate`. `bundlectl` tests cover offline cache reuse, corrupted cache and
downloads, invalid package identity, traversal, colliding sources and changed
skill files. The opt-in `ZENITH_TEAM_TEST_MANAGED_NPM=1` test
`TestPublishedStudioNPMInstallation` installs the real CLI, verifies its export,
renders a two-minute MP4 with audio and checks repeat preparation and repair.
It uses no person's credentials or paid media generation.
