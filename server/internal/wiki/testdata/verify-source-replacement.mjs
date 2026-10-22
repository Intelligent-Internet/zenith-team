// Server-owned check of the production client's guarded subtree transaction.
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
const require = createRequire(new URL("../../../../frontend/package.json", import.meta.url));
const { build } = require("esbuild");
const { Schema } = require("@tiptap/pm/model");
const { EditorState } = require("@tiptap/pm/state");
const result = await build({
  entryPoints: [fileURLToPath(new URL("../../../../frontend/src/features/project-canvas/documents/source-replacement.ts", import.meta.url))],
  bundle: true, write: false, format: "esm", platform: "node", target: "es2022",
});
const { reconcileSourceReplacement } = await import(`data:text/javascript;base64,${Buffer.from(result.outputFiles[0].text).toString("base64")}`);
const schema = new Schema({ nodes: {
  doc: { content: "block+" },
  blockquote: { content: "block+", group: "block", attrs: { blockId: { default: null } } },
  paragraph: { content: "text*", group: "block", attrs: { blockId: { default: null } } },
  listItem: { content: "paragraph block*", group: "block", attrs: { blockId: { default: null } } },
  text: {},
} });
const paragraph = (id, text = id) => schema.node("paragraph", { blockId: id }, schema.text(text));
const quote = (id, children) => schema.node("blockquote", { blockId: id }, children);
const before = quote("root", [paragraph("a"), paragraph("b"), paragraph("c")]);
for (const [name, after, stable] of [
  ["insert", quote("root", [paragraph("new"), ...Array.from({ length: 3 }, (_, i) => before.child(i))]), ["a", "b", "c"]],
  ["remove", quote("root", [paragraph("b"), paragraph("c")]), ["b", "c"]],
  ["reorder", quote("root", [paragraph("c"), paragraph("b"), paragraph("a")]), []],
  ["replace all", quote("root", [paragraph("new")]), []],
  ["edit child", quote("root", [paragraph("a", "edited"), paragraph("b"), paragraph("c")]), ["b", "c"]],
  ["nest child", quote("root", [paragraph("a"), quote("nested", [paragraph("b")]), paragraph("c")]), ["a", "c"]],
]) {
  const doc = schema.node("doc", null, [paragraph("outside-before"), before, paragraph("outside-after")]);
  const state = EditorState.create({ schema, doc });
  const position = doc.child(0).nodeSize;
  const tr = state.tr;
  const anchors = new Map();
  doc.descendants((node, pos) => { if (node.attrs.blockId) anchors.set(node.attrs.blockId, pos + 1); });
  reconcileSourceReplacement(tr, before, after, position);
  const next = state.apply(tr).doc;
  next.check();
  assert.ok(next.child(1).eq(after), `${name}: replacement differs`);
  for (const id of ["outside-before", "outside-after", ...stable]) {
    const mapped = tr.mapping.mapResult(anchors.get(id));
    assert.equal(mapped.deleted, false, `${name}: anchor for ${id} was deleted`);
  }
}
assert.throws(() => {
  const tr = EditorState.create({ schema, doc: schema.node("doc", null, [before]) }).tr;
  reconcileSourceReplacement(tr, before, quote("foreign", [paragraph("new")]), 0);
}, /identity/);
const itemBefore = schema.node("listItem", { blockId: "item" }, [paragraph("old-heading"), quote("nested", [paragraph("kept")])]);
const itemAfter = schema.node("listItem", { blockId: "item" }, [paragraph("new-heading"), quote("nested", [paragraph("kept")])]);
const itemState = EditorState.create({ schema, doc: schema.node("doc", null, [itemBefore]) });
const itemTransaction = itemState.tr;
const nestedAnchor = 1 + itemBefore.child(0).nodeSize + 1;
reconcileSourceReplacement(itemTransaction, itemBefore, itemAfter, 0);
assert.equal(itemTransaction.mapping.mapResult(nestedAnchor).deleted, false, "replacing a required heading must retain the nested block anchor");
assert.ok(itemState.apply(itemTransaction).doc.child(0).eq(itemAfter), "required leading child must not leave an automatically fitted paragraph");
console.log("Source replacement preserves untouched anchors across insert/remove and validates structural results.");
