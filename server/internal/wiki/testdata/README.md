# Intelligent UI codec fixtures

`intelligent-yjs.json` is generated with the frontend's pinned Yjs dependency.
It contains all 15 reactive primitives, nested native blocks, Unicode, rich text
around an inline value, and independent text and metric-label edits from two
clients. Its entity IDs and Yjs client IDs are fixed for reproducibility.

From the repository root, after installing frontend dependencies:

```sh
node server/internal/wiki/testdata/generate-intelligent-yjs.mjs
```

The ordinary wiki unit suite decodes this fixture, merges both update orders and
replays an update. The server integration lane additionally invokes
`verify-intelligent-yjs.mjs` to project Go-encoded state, Go-merged state and a
Go-generated delta through the actual `@tiptap/y-tiptap` JSON adapter. This check
requires Node and the installed frontend dependencies; it needs no PostgreSQL.
From `server/`:

```sh
go test -tags=integration ./internal/wiki -run 'TestReactiveBlocks(ReadByYjs|MergeYjsUpdates)' -count=1
```

These checks establish binary codec and client projection compatibility. They do
not establish browser rendering, collaborative cursors, local control
independence, or permission/reconnect behavior. The integration lane also builds
the production reactive Tiptap extensions with rendering-only imports stubbed,
then verifies all catalog node registrations, placement classes, valid editor
node construction, and ProseMirror JSON round trips. That establishes editor
schema compatibility, not browser rendering or node-view behavior.

The inline-runtime integration check server-renders the shared inline parser and
components. It verifies SVG nesting through conditionals, loops and fragments,
and an inline chart's labelled figure and expandable data table. The retired
native ChartView/DataTableView implementations and their checks are deleted.
Server rendering does not verify responsive layout or browser interactions.

```sh
go test -tags=integration ./internal/wiki -run TestInlineRuntimeContract -count=1
```

The editor-schema check also inspects bundler inputs to prevent the inline Page
node from importing the native scope/capture interpreter. Remaining native
expression and capture fixtures below protect document evidence internals; they
do not describe the component authoring language. See `docs/design/inline-components.md`.

## Shared expression contract

`intelligent-expressions.json` contains 64 named cases with independently stated
expected values or error codes. The Go unit lane evaluates each case and checks
that inputs were not mutated. The integration lane transpiles the actual client
`expression.ts` with the installed esbuild dependency, then runs the same cases
under Node. This covers every language-1 operation, strict types, Unicode
comparison, stable sorting, numeric formatting, nanosecond date differences,
short-circuiting, unsafe fields, and collection/depth/step limits.

```sh
go test -tags=integration ./internal/wiki -run TestUISharedExpressionFixtures -count=1
```

This is an interpreter contract check, not browser testing. Worker scheduling,
React rendering and independent multi-client controls require separate evidence.

## Scope definition migration

`verify-intelligent-scope.mjs` bundles the production client scope runtime and
checks compatible input retention, implicit/explicit catalog version equivalence,
changed-type resets, removed declarations, invalid-definition atomicity and
independent viewer/copy instances. It also verifies that denied and stale source
statuses immediately remove previously authorized rows from the expression
environment while retaining sanitized provenance. These cases use no computed
expressions, so no Worker substitute or browser is involved. Worker lifecycle,
reconnect and rendered status messages remain separate verification concerns.

```sh
go test -tags=integration ./internal/wiki -run TestUIScopeDefinitionMigrationInClient -count=1
```

## Member source replacement

`verify-source-replacement.mjs` bundles the production client replacement helper
and runs it against the installed ProseMirror model/state packages under Node.
It checks insertion, removal, reordering, nesting, complete descendant replacement,
identity refusal and preservation of untouched sibling/outside anchors.
`TestSourceReplacementClientTransaction` runs it in the server integration lane.
This checks transaction semantics, not browser rendering, collaboration transport,
or dialog interactions.
