// Server integration harness: JSON on stdin contains Go-produced binary updates.
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { readFileSync } from "node:fs";
const require = createRequire(
  new URL("../../../../frontend/package.json", import.meta.url),
);
const Y = require("yjs");
const { yDocToProsemirrorJSON } = require("@tiptap/y-tiptap");
const input = JSON.parse(readFileSync(0, "utf8"));
const doc = new Y.Doc();
for (const update of input.updates)
  Y.applyUpdate(doc, Buffer.from(update, "base64"));
// Go omits empty mark attrs in JSON; the client adapter emits {} for them.
function withMarkAttributes(block) {
  return {
    ...block,
    ...(block.marks
      ? { marks: block.marks.map((mark) => ({ attrs: {}, ...mark })) }
      : {}),
    ...(block.content
      ? { content: block.content.map(withMarkAttributes) }
      : {}),
  };
}
// The actual client collaboration adapter projects the XML, not a test decoder.
assert.deepEqual(
  yDocToProsemirrorJSON(doc, "content"),
  withMarkAttributes(input.expected),
);
doc.destroy();
