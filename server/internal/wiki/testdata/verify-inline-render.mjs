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
    "export const ChevronLeft = () => null; export const ChevronRight = () => null; export const ChevronDown = () => null; export const Check = () => null;",
  "./code-block": "export const InlineCodeBlock = () => null;",
};
const result = await build({
  stdin: {
    contents: 'export {InlineDocument} from "./inline-document"; export {appDocument} from "./app-block";',
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

const orbitSource = `{@body const [orbit, setOrbit] = DIL.useState(0);}
<svg width="100%" height="220" viewBox="0 0 600 220" aria-label="Orbit preview">
<defs><radialGradient id="starGlow"><stop offset="0%" stop-color="#ffd166"/><stop offset="100%" stop-opacity="0"/></radialGradient></defs>
<ellipse cx="300" cy="110" rx="118" ry="43" fill="none" stroke="#a5a0e8" stroke-opacity="0.35" stroke-dasharray="3 7"/>
<circle cx="300" cy="110" r="48" fill="url(#starGlow)"/>
<circle cx={300 + 118 * Math.cos(orbit)} cy={110 + 43 * Math.sin(orbit)} r="7"/>
</svg><button onClick={() => setOrbit(orbit + 0.9)}>Advance orbit</button>`;
const orbit = render(orbitSource);
assert.doesNotMatch(orbit, /Couldn't display|Only supported UI functions/);
assert.match(orbit, /cx="418" cy="110"/);
assert.match(orbit, /aria-label="Orbit preview"/);
assert.match(orbit, /stop-color="#ffd166"/);
assert.match(orbit, /stop-opacity="0"/);
assert.match(orbit, /stroke-opacity="0.35"/);
assert.match(orbit, /stroke-dasharray="3 7"/);
assert.match(render('<row justify="space-between"><text>Left</text><text>Right</text></row>'), /justify-content:space-between/);
const gradientId = orbit.match(/<radialGradient id="([^"]+)"/)[1];
assert.ok(orbit.includes(`fill="url(#${gradientId})"`));
const repeated = render(orbitSource + orbitSource);
const ids = [...repeated.matchAll(/<radialGradient id="([^"]+)"/g)].map(match => match[1]);
assert.equal(ids.length, 2);
assert.notEqual(ids[0], ids[1], 'SVG definitions are isolated per illustration');
const unsafeSvg = render('<svg><circle fill="url(https://example.com/image.svg#star)" stroke="url(data:image/svg+xml,bad)"/><script>bad</script></svg>');
assert.doesNotMatch(unsafeSvg, /fill="url|stroke="url|<script/);
console.log('Orbit math, local SVG gradients, attribute aliases and reference isolation passed.');

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

const appSource = '<AppBlock title="Orbit" app_block_id="orbit"><div>Star</div><script>requestAnimationFrame(() => {});</script></AppBlock>';
assert.match(render(appSource), /sandbox="allow-scripts"/);
assert.match(render(appSource, {readOnly:true}), /sandbox=""/);
assert.doesNotMatch(render(appSource), /allow-same-origin/);
const chessHTML = '<!doctype html><html><head><style>.chess{display:grid}</style></head><body><div class="chess">Pocket Chess</div><script>globalThis.__appBlockHTMLExecuted = true;</script></body></html>';
const chessSource = '<box><AppBlock app_block_id="pocket-chess" html={`' + chessHTML + '`} /></box>';
for (const readOnly of [false, true]) {
  const chess = render(chessSource, {readOnly});
  assert.match(chess, /&lt;!doctype html&gt;/);
  assert.match(chess, /Pocket Chess/);
  assert.match(chess, /globalThis.__appBlockHTMLExecuted = true;/);
  assert.match(chess, readOnly ? /sandbox=""/ : /sandbox="allow-scripts"/);
  assert.doesNotMatch(chess, /Couldn't display|allow-same-origin/);
}
assert.equal(globalThis.__appBlockHTMLExecuted, undefined, 'App HTML must not execute in the host renderer');
assert.match(render('<AppBlock html={42} />'), /AppBlock.html requires string/);
assert.match(render('{@body const html = 42;}<AppBlock html={html} />'), /AppBlock.html requires string/);
assert.match(render('<AppBlock html="&lt;div&gt;String HTML&lt;/div&gt;" />'), /String HTML/);
console.log('AppBlock HTML string input renders through the sandbox and rejects non-string values.');
const appHTML = loaded.exports.appDocument('<div>Example</div>', 'channel-123', true);
assert.match(appHTML, /connect-src 'none'/);
assert.match(appHTML, /form-action 'none'/);
assert.match(appHTML, /prefers-reduced-motion/);
const {runInNewContext} = await import('node:vm');
const events = new Map();
const messages = [];
const parent = {postMessage(message) { messages.push(message); }};
const context = {parent, addEventListener(name, callback) { events.set(name, callback); }};
runInNewContext(appHTML.match(/<script>([\s\S]*?)<\/script>/)[1], context);
let observed;
context.__effect(() => { observed = context.__bindings.speed; });
events.get('message')({source:parent,data:{channel:'wrong',type:'bindings',values:{speed:9},setters:[]}});
assert.equal(observed, undefined);
events.get('message')({source:parent,data:{channel:'channel-123',type:'bindings',values:{speed:2},setters:['setSpeed']}});
assert.equal(observed, 2);
assert.throws(() => context.__bindings.setSpeed(3), /direct interaction/);
const interaction = {isTrusted:true, type:'click', eventPhase:2};
events.get('click')(interaction);
context.__bindings.setSpeed(3);
assert.equal(messages.at(-1).type, 'set');
assert.equal(messages.at(-1).value, 3);
assert.throws(() => context.__effect(() => context.__bindings.setSpeed(5)), /direct interaction/);
interaction.eventPhase = 0;
assert.throws(() => context.__bindings.setSpeed(4), /direct interaction/);
events.get('click')({isTrusted:false, type:'click', eventPhase:2});
assert.throws(() => context.__bindings.setSpeed(4), /direct interaction/);
console.log('AppBlock isolation markup, binding delivery and direct-interaction guard passed.');

// Authoring contracts: use real parser/rendered semantics, not source matching.
const checkbox = render('<checkbox checked={false} value="choice" id="choice">Choice</checkbox>');
assert.doesNotMatch(checkbox, /checked=""/);
assert.match(checkbox, /id="choice"/);
assert.match(checkbox, /value="choice"/);
assert.match(render('<Link url="https://example.org" title="Open example"/>'), />Open example<\/a>/);
const carousel = render('<carousel>\n<carousel-item>A</carousel-item>\n<carousel-item>B</carousel-item>\n</carousel>');
assert.equal((carousel.match(/flex-basis:/g) ?? []).length, 2);
const carouselLoop = render('<carousel>{#each [1,2] as n}<carousel-item>{n}</carousel-item>{/each}</carousel>');
assert.equal((carouselLoop.match(/flex-basis:/g) ?? []).length, 2);
assert.match(render('<table emptyLabel="Nothing here">{#each [] as x}<table-row><table-cell>{x}</table-cell></table-row>{/each}</table>'), /Nothing here/);
const headers = render('<table stickyHeader columnSizing={{0:120,1:"50%"}} dividers="all"><table-row header><table-cell>Name</table-cell><table-cell>Value</table-cell></table-row><table-row><table-cell>A</table-cell><table-cell>1</table-cell></table-row></table>');
assert.equal((headers.match(/<th /g) ?? []).length, 2);
assert.match(headers, /scope="col"/);
assert.match(headers, /sticky top-0/);
assert.match(headers, /<col style="width:120px"/);
assert.match(headers, /border-r/);
assert.match(render('<radio-group name="delivery" required><radio value="mail">Mail</radio></radio-group>'), /name="delivery"/);
assert.match(render('<radio-group name="delivery" required><radio value="mail">Mail</radio></radio-group>'), /required=""/);
assert.match(render('<label htmlFor="choose">Choice</label><select id="choose" required placeholder="Choose" value="" options={[{label:"A",value:"a"}]}/>'), /id="choose"/);
const svgProps = render('<svg preserveAspectRatio="none" color="red" size={100} viewBox="0 0 10 10"><circle r="2" fill="currentColor"/></svg>');
assert.match(svgProps, /preserveAspectRatio="none"/);
assert.match(svgProps, /color="red"/);
assert.match(svgProps, /width="100"/);
assert.match(render('<box padding={1} gap={2}><text>Spacing</text></box>'), /padding:4pt;gap:8pt/);
assert.match(render('<box theme="dark"><text>Dark</text></box>'), /color-scheme:dark/);
assert.match(render('<carousel><carousel-media-item media={{src:"https://example.org/image.png",alt:"Example"}}/></carousel>'), /alt="Example"/);
assert.match(render('<WritingBlock recipient="team@example.org" subject="Draft">Hello</WritingBlock>'), /team@example.org/);
assert.match(render('<WritingBlock recipient="team@example.org" subject="Draft">Hello</WritingBlock>'), /Copy draft/);
assert.match(render('<box>{@body const x=1}</box>'), /document top level/);
console.log('Native child collections, controlled values, labels, table options, SVG props, spacing and draft metadata passed.');

// Gold-standard authoring syntax must render real controls, not source fallback.
const goldControls = render('<input type="number" value={2}/><Link href="https://example.com">Example</Link><radio-group name="priority" value="normal" options={[{label:"Low",value:"low"},{label:"Normal",value:"normal"},{label:"High",value:"high",disabled:true}]}/>');
assert.match(goldControls, /type="number"/);
assert.match(goldControls, /href="https:\/\/example.com"/);
assert.equal((goldControls.match(/type="radio"/g) ?? []).length, 3);
assert.match(goldControls, /checked="" value="normal"/);
assert.match(goldControls, /disabled=""[^>]*value="high"|value="high"[^>]*disabled=""/);
assert.doesNotMatch(goldControls, /Couldn't display/);
const readOnlyOptions = render('<radio-group options={[{label:"Yes",value:true}]}/>', {readOnly:true});
assert.match(readOnlyOptions, /disabled=""/);
const {readFileSync} = await import('node:fs');
const playground = readFileSync(new URL('../../mcp/tools/builtin/zenith-intelligent-ui/playground.zenith-ui', import.meta.url), 'utf8');
const playgroundHTML = render(playground);
assert.doesNotMatch(playgroundHTML, /Couldn't display|Show source/);
assert.match(playgroundHTML, /50<\/span> credits/);
assert.match(playgroundHTML, /type="number"/);
assert.match(playgroundHTML, /checked="" value="Normal"/);
console.log('Gold-standard playground and control aliases render with correct defaults and read-only controls.');

const mixedMarkdown = render('<text>**Strong <italic>and nested</italic> text**, ~~old~~ and `literal {value}`.</text>');
assert.match(mixedMarkdown, /<strong>Strong <em/);
assert.match(mixedMarkdown, /<s>old<\/s>/);
assert.match(mixedMarkdown, /literal \{value\}/);
const prose = render('<box>\n| Name | Value |\n| --- | --- |\n| A | 2 |\n\n- [x] Complete\n- [ ] Pending\n\n\\(x^{2}\\)\n</box>');
assert.match(prose, /<table/);
assert.match(prose, /type="checkbox"/);
assert.match(prose, /katex/);
assert.doesNotMatch(prose, /Couldn't display/);
assert.match(render('<box padding=0 gap="0"><text>Typed props</text><button disabled=false>Enabled</button></box>'), /padding:0pt;gap:0/);
const band = render('<Chart content={{chartType:"line",xKey:"x",series:[{dataKey:"y",lowerKey:"lo",upperKey:"hi"}],data:[{x:"A",y:2,lo:1,hi:3}]}}/>');
assert.match(band, /1.*–.*3/);
for (const variant of ['standard','email','chat_message','social_post','document']) {
 const draft = render('<WritingBlock id="12345" variant="'+variant+'" options={[{label:"Default",content:"**First**"},{label:"Alternate",content:"Second"}]}>**First**</WritingBlock>');
 assert.doesNotMatch(draft, /Couldn't display/);
 assert.match(draft, /<strong>First<\/strong>/);
 assert.match(draft, /Alternate/);
}
console.log('Final guide Markdown, typed attributes, chart bands and WritingBlock variants render.');

for (const name of ['example-1','example-2','example-3','example-5','example-6','example-7','example-8','example-9','example-10','example-11','example-12','example-13']) {
  const fixture = new URL('../../mcp/tools/builtin/zenith-intelligent-ui/references/'+name+'.zenith-ui', import.meta.url);
  assert.ok((await import('node:fs')).existsSync(fixture), name);
  const output = render(readFileSync(fixture,'utf8'));
  assert.doesNotMatch(output, /Couldn't display|Show source/, name);
}
assert.match(render('**Outer <italic>mixed</italic> Markdown**'), /<strong>Outer <em/);
assert.match(render('{@body const value="**not markup**";}<text>{value}</text>'), /\*\*not markup\*\*/);
console.log('Final guide native examples and outer Markdown composition passed.');

assert.match(render('<box>{#if true}**Conditional prose**{/if}</box>'), /<strong>Conditional prose<\/strong>/);
