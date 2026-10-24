import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
const require = createRequire(
  new URL("../../../../frontend/package.json", import.meta.url),
);
const { build } = require("esbuild");
const source = new URL(
  "../../../../frontend/src/features/project-canvas/documents/intelligent-ui/interaction-policy.ts",
  import.meta.url,
);
const result = await build({
  entryPoints: [fileURLToPath(source)],
  bundle: true,
  write: false,
  format: "esm",
  platform: "node",
  target: "es2022",
  logLevel: "silent",
});
const { allowsLocalInteraction } = await import(
  `data:text/javascript;base64,${Buffer.from(result.outputFiles[0].text).toString("base64")}`
);
for (const preview of [false, true]) {
  assert.equal(allowsLocalInteraction("live", preview), true);
  assert.equal(allowsLocalInteraction("snapshot", preview), false);
  assert.equal(allowsLocalInteraction("comparison", preview), preview);
}
console.log(
  "Preview local interaction requires opt-in; snapshots remain read-only.",
);

const captured = await build({
  entryPoints: [
    fileURLToPath(
      new URL(
        "../../../../frontend/src/features/project-canvas/documents/intelligent-ui/capture-inputs.ts",
        import.meta.url,
      ),
    ),
  ],
  bundle: true,
  write: false,
  format: "esm",
  platform: "node",
  target: "es2022",
  logLevel: "silent",
});
const { collectCaptureInputs } = await import(
  `data:text/javascript;base64,${Buffer.from(captured.outputFiles[0].text).toString("base64")}`
);
const { Schema } = require("@tiptap/pm/model");
const schema = new Schema({
  nodes: {
    doc: { content: "block+" },
    paragraph: { group: "block", content: "text*" },
    text: { group: "inline" },
    uiDocument: { group: "block", atom: true, attrs: { source: {} } },
  },
});
const editor = (content) => ({
  state: { doc: schema.nodeFromJSON({ type: "doc", content }) },
  isDestroyed: false,
});
const plain = await collectCaptureInputs(editor([{ type: "paragraph" }]));
assert.deepEqual(plain.selected, { page: {} });
plain.validate();
await assert.rejects(
  collectCaptureInputs(
    editor([
      { type: "uiDocument", attrs: { source: "<button>Count</button>" } },
    ]),
  ),
  /Wait for this component to render/,
);
console.log(
  "Capture waits for rendered inline state and accepts ordinary Pages.",
);
