import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
const frontend = fileURLToPath(
  new URL("../../../../frontend/", import.meta.url),
);
const require = createRequire(frontend + "package.json");
const { build } = require("esbuild");
const Module = require("node:module");
// Isolate unrelated providers/media while retaining the real parser, renderer,
// and SVG primitives. This verifies structure, not browser layout or interaction.
const providers = {
  "next-themes": 'export const useTheme = () => ({resolvedTheme:"light"});',
  streamdown: "export const Streamdown = ({children}) => children;",
  "@/shared/ui/icon": "export const Icon = () => null;",
  "@/shared/ui/icons":
    "export const ChevronLeft = () => null; export const ChevronRight = () => null;",
  "./code-block": "export const InlineCodeBlock = () => null;",
};
const result = await build({
  stdin: {
    contents: 'export {InlineDocument} from "./inline-document";',
    resolveDir: frontend + "src/shared/inline-ui",
    loader: "tsx",
  },
  bundle: true,
  write: false,
  platform: "node",
  format: "cjs",
  external: ["react", "react/jsx-runtime", "recharts"],
  loader: { ".css": "empty" },
  tsconfigRaw: {
    compilerOptions: {
      jsx: "react-jsx",
      baseUrl: frontend,
      paths: { "@/*": ["src/*"] },
    },
  },
  plugins: [
    {
      name: "unrelated-providers",
      setup(build) {
        build.onResolve({ filter: /.*/ }, (args) =>
          Object.hasOwn(providers, args.path)
            ? { path: args.path, namespace: "provider" }
            : undefined,
        );
        build.onLoad({ filter: /.*/, namespace: "provider" }, (args) => ({
          contents: providers[args.path],
          loader: "js",
        }));
      },
    },
  ],
});
const loaded = new Module(frontend + ".inline-render-check.cjs");
loaded.filename = frontend + ".inline-render-check.cjs";
loaded.paths = Module._nodeModulePaths(frontend);
loaded._compile(result.outputFiles[0].text, loaded.filename);
const { createElement } = require("react");
const { renderToStaticMarkup } = require("react-dom/server");
const render = (source, options = {}) =>
  renderToStaticMarkup(
    createElement(loaded.exports.InlineDocument, { source, ...options }),
  );
assert.match(render("<button>Continue</button>"), /<button/);
assert.doesNotMatch(
  render("```zenith-ui\n<button>Continue</button>\n```"),
  /<button/,
);
for (const [source, maxSourceBytes] of [
  ["Ordinary saved answer. " + "x".repeat(262144), 262144],
  ["Unicode answer: " + "界".repeat(90000), 262144],
  ["A later segment still needs to be readable.", 0],
]) {
  const plain = render(source, {
    maxSourceBytes,
    renderMarkdown: (text) => createElement("p", null, text),
  });
  assert.ok(
    plain.includes(`<p>${source}</p>`),
    "Long Markdown remains readable.",
  );
  assert.ok(
    !plain.includes("interactive size limit"),
    "Plain text is not a UI error.",
  );
}
const oversizedComponent = render("<button>Continue</button>", {
  maxSourceBytes: 0,
});
assert.doesNotMatch(oversizedComponent, /<button/);
assert.match(oversizedComponent, /&lt;button&gt;Continue/);
console.log(
  "Raw components render; fenced code and over-budget source remain literal and readable.",
);

const source =
  '<svg viewBox="0 0 100 100">{#if false}<circle r={1}/>{:else if true}<circle r={2}/>{/if}{#each [10,20] as x}<rect x={x} width={5} height={5}/>{/each}<><path d="M0 0 L10 10"/></></svg>';
const html = renderToStaticMarkup(
  createElement(loaded.exports.InlineDocument, { source }),
);
assert.match(html, /<circle[^>]*r="2"/);
assert.equal((html.match(/<rect\b/g) ?? []).length, 2);
assert.match(html, /<path[^>]*d="M0 0 L10 10"/);
assert.doesNotMatch(html, /not connected|not supported|Couldn't display/);
console.log("Inline SVG branches, loops and fragments retain SVG context.");

const chartSource =
  '<Chart content={{chartType:"bar",title:"Quarterly amount",xKey:"quarter",series:[{dataKey:"amount"}],data:[{quarter:"Q1",amount:12},{quarter:"Q2",amount:19}]}} />';
const chart = renderToStaticMarkup(
  createElement(loaded.exports.InlineDocument, { source: chartSource }),
);
assert.match(chart, /<figure/);
assert.match(chart, /Quarterly amount/);
assert.match(chart, /View data/);
assert.match(chart, /<table/);
assert.match(chart, /Q1/);
assert.match(chart, /Q2/);
assert.match(chart, /19/);
assert.doesNotMatch(chart, /Couldn't display|not supported/);
console.log(
  "Inline chart source renders its labelled figure and accessible data table.",
);
