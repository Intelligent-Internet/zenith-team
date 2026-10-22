import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
const require = createRequire(
  new URL("../../../../frontend/package.json", import.meta.url),
);
const { build } = require("esbuild");
const { Schema } = require("@tiptap/pm/model");
async function load(file) {
  const url = new URL(
    "../../../../frontend/src/features/project-canvas/documents/intelligent-ui/" +
      file,
    import.meta.url,
  );
  const result = await build({
    entryPoints: [fileURLToPath(url)],
    bundle: true,
    write: false,
    format: "cjs",
    platform: "node",
    target: "es2022",
    define: { "import.meta.url": JSON.stringify(url.href) },
    logLevel: "silent",
  });
  const module = { exports: {} };
  new Function("require", "module", "exports", result.outputFiles[0].text)(
    require,
    module,
    module.exports,
  );
  return module.exports;
}
const { supportsReactiveVersion, supportsCaptureVersions } =
  await load("versions.ts");
const capabilityURL = new URL(
  "../../../../frontend/src/features/project-canvas/documents/document-authoring-capability.ts",
  import.meta.url,
);
const capabilityBuild = await build({
  entryPoints: [fileURLToPath(capabilityURL)],
  bundle: true,
  write: false,
  format: "cjs",
  platform: "node",
  target: "es2022",
  logLevel: "silent",
});
const capabilityModule = { exports: {} };
new Function(
  "require",
  "module",
  "exports",
  capabilityBuild.outputFiles[0].text,
)(require, capabilityModule, capabilityModule.exports);
const { documentAuthoringCapability, canEditWithDocumentCapability } =
  capabilityModule.exports;
assert.deepEqual(documentAuthoringCapability(1, false), {
  intelligentUIAuthoringEnabled: false,
  reactiveReadOnly: false,
});
assert.deepEqual(documentAuthoringCapability(2, false), {
  intelligentUIAuthoringEnabled: false,
  reactiveReadOnly: true,
});
assert.deepEqual(documentAuthoringCapability(2, true), {
  intelligentUIAuthoringEnabled: true,
  reactiveReadOnly: false,
});
assert.equal(
  canEditWithDocumentCapability(true, true),
  false,
  "schema-2 document edits stay disabled during rollback",
);
assert.equal(
  canEditWithDocumentCapability(true, false),
  true,
  "ordinary editing resumes when authoring is enabled",
);
assert.equal(
  canEditWithDocumentCapability(false, false),
  false,
  "deployment capability does not override document permission",
);
for (const type of ["uiDocument"]) {
  assert.equal(supportsReactiveVersion(type, {}), true);
  assert.equal(supportsReactiveVersion(type, { catalogVersion: 1 }), true);
  for (const version of [null, 0, 2, -1, "1", true])
    assert.equal(
      supportsReactiveVersion(type, { catalogVersion: version }),
      false,
    );
}
for (const type of [
  "uiScope",
  "uiMetric",
  "uiChart",
  "uiRepeat",
  "uiValue",
  "uiFutureComponent",
])
  assert.equal(
    supportsReactiveVersion(type, {}),
    false,
    "retired native nodes are not offered as editable inline documents",
  );
assert.equal(supportsCaptureVersions(undefined), true);
assert.equal(supportsCaptureVersions({}), true);
const capture = { schemaVersion: 2, languageVersion: 1, catalogVersion: 1 };
assert.equal(supportsCaptureVersions({ page: capture }), true);
for (const [key, value] of [
  ["schemaVersion", 3],
  ["languageVersion", 2],
  ["catalogVersion", 2],
  ["schemaVersion", "2"],
]) {
  assert.equal(
    supportsCaptureVersions({ page: { ...capture, [key]: value } }),
    false,
  );
  assert.equal(
    supportsCaptureVersions({
      page: capture,
      included: { ...capture, [key]: value },
    }),
    false,
  );
}
for (const value of [[], "invalid", 1, { page: null }, { page: {} }])
  assert.equal(supportsCaptureVersions(value), false);
const { editorRuntime, disposeEditorRuntime } = await load("editor-runtime.ts");
const schema = new Schema({
  nodes: {
    doc: { content: "block*" },
    text: { group: "inline" },
    uiScope: {
      group: "block",
      content: "block*",
      attrs: {
        blockId: {},
        initial: { default: {} },
        computed: { default: {} },
        sources: { default: {} },
        catalogVersion: { default: 1 },
      },
    },
    uiGrid: {
      group: "block",
      content: "block*",
      attrs: { blockId: {}, catalogVersion: { default: 1 } },
    },
  },
});
const scope = (id) =>
  schema.node("uiScope", { blockId: id, initial: { count: 1 } });
const editor = {};
const registry = editorRuntime(editor);
registry.update(
  schema
    .node("doc", null, [
      schema.node("uiGrid", { blockId: "grid" }, [scope("inside")]),
      scope("outside"),
    ])
    .toJSON(),
);
assert.equal(
  registry.get("inside"),
  undefined,
  "retired native scopes cannot activate in the inline editor",
);
const outside = registry.get("outside");
registry.update(
  schema
    .node("doc", null, [
      schema.node("uiGrid", { blockId: "grid", catalogVersion: 2 }, [
        scope("inside"),
      ]),
      scope("outside"),
    ])
    .toJSON(),
);
assert.equal(
  registry.get("inside"),
  undefined,
  "unknown container cannot retain active descendant scope runtimes",
);
assert.equal(
  registry.get("outside"),
  outside,
  "retired sibling scopes also remain inactive",
);
registry.update(
  schema
    .node("doc", null, [
      schema.node("uiScope", { blockId: "new", catalogVersion: 2 }, [
        scope("nested"),
      ]),
    ])
    .toJSON(),
);
assert.equal(registry.get("new"), undefined);
assert.equal(registry.get("nested"), undefined);
assert.equal(registry.get("outside"), undefined);
disposeEditorRuntime(editor);
console.log(
  "Version guards retain supported history and prevent unknown captures or containers from using current runtime semantics.",
);
