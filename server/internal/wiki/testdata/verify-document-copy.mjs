// Server-owned check of production clipboard/duplication identity rewriting.
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
const require = createRequire(
  new URL("../../../../frontend/package.json", import.meta.url),
);
const { build } = require("esbuild");
const { Schema, Slice, Fragment } = require("@tiptap/pm/model");
const compiled = await build({
  entryPoints: [
    fileURLToPath(
      new URL(
        "../../../../frontend/src/features/project-canvas/documents/document-copy.ts",
        import.meta.url,
      ),
    ),
  ],
  bundle: true,
  write: false,
  format: "cjs",
  external: ["@tiptap/pm/model", "@tiptap/pm/state", "@tiptap/pm/transform"],
  platform: "node",
  target: "es2022",
});
const compiledModule = { exports: {} };
new Function("require", "module", "exports", compiled.outputFiles[0].text)(
  require,
  compiledModule,
  compiledModule.exports,
);
const { copyDocumentSlice, copyDocumentDrop } = compiledModule.exports;
const { EditorState, NodeSelection } = require("@tiptap/pm/state");
const schema = new Schema({
  nodes: {
    doc: { content: "block+" },
    callout: {
      content: "block+",
      group: "block",
      attrs: { blockId: { default: null } },
    },
    uiDocument: {
      group: "block",
      atom: true,
      attrs: { blockId: { default: null }, source: {} },
    },
    paragraph: {
      content: "text*",
      group: "block",
      attrs: { blockId: { default: null } },
    },
    artifactReference: {
      group: "block",
      attrs: { blockId: { default: null }, versionId: {}, blockTargetId: {} },
    },
    text: {},
  },
  marks: { link: { attrs: { href: {} } } },
});
const link = (href) => schema.text("link", [schema.mark("link", { href })]);
const paragraph = (id, children) =>
  schema.node("paragraph", { blockId: id }, children);
const original = schema.node("callout", { blockId: "scope" }, [
  paragraph("first", [
    link("#block-second"),
    link("#block-outside"),
    link("/pages/other#block-second"),
  ]),
  paragraph("second", [link("#block-first")]),
  schema.node("artifactReference", {
    blockId: "ref",
    versionId: "other-version",
    blockTargetId: "second",
  }),
]);
const before = original.toJSON();
const slice = new Slice(Fragment.from(original), 0, 0);
const shortPool = ["one"];
assert.equal(copyDocumentSlice(slice, shortPool).slice, null);
assert.deepEqual(shortPool, ["one"], "insufficient pool must not consume IDs");
const pool = ["new-scope", "new-first", "new-second", "new-ref", "unused"];
const copied = copyDocumentSlice(slice, pool).slice.content.firstChild;
copied.check();
assert.deepEqual(pool, ["unused"]);
assert.equal(copied.attrs.blockId, "new-scope");
assert.equal(copied.child(0).child(0).marks[0].attrs.href, "#block-new-second");
assert.equal(copied.child(0).child(1).marks[0].attrs.href, "#block-outside");
assert.equal(
  copied.child(0).child(2).marks[0].attrs.href,
  "/pages/other#block-second",
);
assert.equal(copied.child(1).child(0).marks[0].attrs.href, "#block-new-first");
assert.equal(
  copied.child(2).attrs.blockTargetId,
  "second",
  "version-qualified references stay external",
);
assert.deepEqual(
  original.toJSON(),
  before,
  "copy must not mutate authored source",
);
const partial = new Slice(
  Fragment.from(paragraph("partial", [link("#block-partial")])),
  1,
  1,
);
const copiedPartial = copyDocumentSlice(partial, ["fresh"]).slice;
assert.equal(copiedPartial.openStart, 1);
assert.equal(copiedPartial.openEnd, 1);
assert.equal(
  copiedPartial.content.firstChild.firstChild.marks[0].attrs.href,
  "#block-partial",
);
const shared = paragraph("shared", [schema.text("text")]);
const repeated = copyDocumentSlice(
  new Slice(Fragment.fromArray([shared, shared]), 0, 0),
  ["a", "b"],
).slice;
assert.equal(repeated.content.child(0).attrs.blockId, "a");
assert.equal(
  repeated.content.child(1).attrs.blockId,
  "b",
  "reused immutable node instances still need distinct identities",
);
console.log(
  "Copy allocates fresh IDs, rewrites internal links, preserves external references and handles partial slices.",
);

const destination = schema.node("doc", null, [
  original,
  paragraph("after", [schema.text("Keep this")]),
]);
const state = EditorState.create({
  schema,
  doc: destination,
  selection: NodeSelection.create(destination, 0),
});
const dropIds = ["drop-scope", "drop-first", "drop-second", "drop-ref"];
const drop = copyDocumentDrop(state, slice, original.nodeSize, dropIds);
assert.ok(drop.transaction);
const dropped = state.apply(drop.transaction);
dropped.doc.check();
assert.ok(
  dropped.doc.child(0).eq(original),
  "copy drop must not delete or rewrite the drag source",
);
assert.equal(dropped.doc.child(1).attrs.blockId, "drop-scope");
assert.equal(
  dropped.doc.child(1).child(0).firstChild.marks[0].attrs.href,
  "#block-drop-second",
);
assert.equal(dropped.doc.child(2).attrs.blockId, "after");
assert.equal(drop.transaction.getMeta("uiEvent"), "drop");
assert.equal(dropIds.length, 0);
assert.ok(dropped.selection instanceof NodeSelection);
const insufficientDropIds = ["reserved"];
const refused = copyDocumentDrop(
  state,
  slice,
  original.nodeSize,
  insufficientDropIds,
);
assert.equal(refused.transaction, null);
assert.deepEqual(insufficientDropIds, ["reserved"]);
assert.ok(state.doc.eq(destination));
console.log(
  "Copy drops retain the original subtree and selection targets, and refuse insufficient reservations atomically.",
);

const inlineSource =
  "{@body const [count, setCount] = DIL.useState(2);}<box><text>{count}</text><button onClick={() => setCount(count + 1)}>Add</button></box>";
const inline = schema.node("uiDocument", {
  blockId: "inline",
  source: inlineSource,
});
const inlineCopy = copyDocumentSlice(new Slice(Fragment.from(inline), 0, 0), [
  "inline-copy",
]).slice.content.firstChild;
assert.equal(inlineCopy.attrs.blockId, "inline-copy");
assert.equal(
  inlineCopy.attrs.source,
  inlineSource,
  "inline declarations are not remapped as Page bindings",
);
assert.equal(inline.attrs.blockId, "inline");
assert.equal(inline.attrs.source, inlineSource);
console.log(
  "Inline copies allocate one Page block identity and preserve the complete local-state definition.",
);
