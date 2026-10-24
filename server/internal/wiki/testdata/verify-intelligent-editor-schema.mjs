import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";

const require = createRequire(
  new URL("../../../../frontend/package.json", import.meta.url),
);
const { build } = require("esbuild");
const Module = require("node:module");
const path = require("node:path");
const frontendRoot = fileURLToPath(
  new URL("../../../../frontend/", import.meta.url),
);
const sourceDir = fileURLToPath(
  new URL(
    "../../../../frontend/src/features/project-canvas/documents/intelligent-ui/",
    import.meta.url,
  ),
);
const entry = `
  import StarterKit from "@tiptap/starter-kit";
  import { getSchema } from "@tiptap/core";
  import { inlineDocumentBlockTypes, inlineDocumentExtensions } from "./inline-document-node.tsx";
  export const schema = getSchema([...inlineDocumentExtensions(), StarterKit]);
  export const registeredTypes = inlineDocumentBlockTypes;
`;
const schemaOnlyImports = {
  "@/features/common/components/inline-artifact-reference":
    "export const InlineArtifactReference = () => null;",
  "@/features/common/hooks/use-inline-entity-action":
    "export const useInlineEntityAction = () => undefined;",
  "./render-surface": "export const NodeViewWrapper = () => null;",
  "@/shared/inline-ui/inline-document":
    "export const InlineDocument = () => null;",
  "./document-context":
    "export const IntelligentDocumentContext = {}; export const IntelligentPreviewInteractionContext = {}; export const InlineStreamingContext = {};",
};
const bundled = await build({
  stdin: {
    contents: entry,
    resolveDir: sourceDir,
    sourcefile: "schema-check.ts",
  },
  plugins: [
    {
      name: "schema-context-stub",
      setup(build) {
        build.onResolve({ filter: /.*/ }, (args) =>
          Object.hasOwn(schemaOnlyImports, args.path)
            ? { path: args.path, namespace: "schema-only" }
            : undefined,
        );
        build.onLoad({ filter: /.*/, namespace: "schema-only" }, (args) => ({
          contents: schemaOnlyImports[args.path],
          loader: "js",
        }));
      },
    },
  ],
  bundle: true,
  metafile: true,
  write: false,
  format: "cjs",
  platform: "node",
  target: "es2022",
  define: {
    "import.meta.env": JSON.stringify({
      BASE_URL: "/",
      DEV: false,
      PROD: true,
      VITE_API_TIMEOUT_MS: "10000",
      VITE_API_URL: "http://localhost",
      VITE_ZENITH_TEAM_DESKTOP_ENV: "",
      VITE_LIVEKIT_URL: "",
    }),
  },
  logLevel: "silent",
});
// Authoring must not silently reconnect to the native capture interpreter.
for (const file of Object.keys(bundled.metafile.inputs)) {
  assert.doesNotMatch(
    file,
    /(?:scope-runtime|editor-runtime|evaluation-client|binding-remap|capture-inputs)\./,
  );
}
const schemaModule = new Module(
  path.join(frontendRoot, ".intelligent-ui-schema-check.cjs"),
);
schemaModule.filename = path.join(
  frontendRoot,
  ".intelligent-ui-schema-check.cjs",
);
schemaModule.paths = Module._nodeModulePaths(frontendRoot);
schemaModule._compile(bundled.outputFiles[0].text, schemaModule.filename);
const { schema, registeredTypes } = schemaModule.exports;
const catalog = JSON.parse(
  await (
    await import("node:fs/promises")
  ).readFile(new URL("../intelligent_catalog.json", import.meta.url), "utf8"),
);
const activeCatalog = catalog.filter(
  (entry) => entry.nodeType === "uiDocument",
);
const expectedBlockTypes = activeCatalog
  .filter((definition) => definition.placement !== "inline")
  .map((definition) => definition.nodeType)
  .sort();
assert.deepEqual([...registeredTypes].sort(), expectedBlockTypes);

for (const definition of activeCatalog) {
  const type = schema.nodes[definition.nodeType];
  assert.ok(type, `${definition.nodeType} must be registered in Tiptap`);
  assert.equal(
    type.isInline,
    definition.placement === "inline",
    `${definition.nodeType} must use its catalog placement`,
  );

  const attrs = Object.fromEntries(
    Object.entries(definition.editorDefaults).map(([key, value]) => [
      key,
      value,
    ]),
  );
  const content = definition.nodeType === "uiDocument"
    ? [schema.text("<text>Hello</text>")]
    : definition.children
    ? [schema.nodes.paragraph.create()]
    : undefined;
  const node = type.createAndFill(attrs, content);
  assert.ok(node, `${definition.nodeType} must form a valid editor node`);
  assert.ok(
    node.eq(schema.nodeFromJSON(node.toJSON())),
    `${definition.nodeType} must survive editor-schema JSON round-trip`,
  );
}
console.log(
  "reactive catalog nodes register, retain placement, and round-trip through the production Tiptap schema",
);
