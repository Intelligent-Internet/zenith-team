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
// Real Iconsax names, so icon validation is exercised without bundling SVGs.
const iconsSource = (await import("node:fs")).readFileSync(frontend + "src/shared/ui/icons.tsx", "utf8");
const iconsaxNames = [...iconsSource.slice(iconsSource.indexOf("export const iconsaxIcons = {")).split(/\n}/)[0].matchAll(/^\s+"?([\w-]+)"?:/gm)].map((match) => match[1]);
const providers = {
  "next-themes": 'export const useTheme = () => ({resolvedTheme:"light"});',
  streamdown: "export const Streamdown = ({children}) => children;",
  "@/shared/ui/icon": "export const Icon = () => null;",
  "@/shared/ui/icons":
    "export const ChevronLeft = () => null; export const ChevronRight = () => null; export const ChevronDown = () => null; export const Check = () => null; export const iconsaxIcons = Object.fromEntries(" + JSON.stringify(iconsaxNames) + ".map((name) => [name, () => null]));",
  "./code-block": "export const InlineCodeBlock = () => null;",
};
const result = await build({
  stdin: {
    contents: 'export {InlineDocument} from "./inline-document"; export {appDocument} from "./app-block"; export {PreviewContinuationContext} from "@/shared/chat/preview-continuation-context";',
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
// Pretty-printed inline children must not become indented Markdown code blocks.
assert.match(
  render('<box><row><button disabled={false}>\n            {"♔"}\n          </button></row></box>'),
  /<span>♔<\/span>/,
);
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
assert.match(render('<row justify="between"><text>Left</text><text>Right</text></row>'), /justify-content:space-between/);
assert.match(render('<row justify="space-between"><text>Left</text></row>'), /invalid_literal/);
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
  '<Chart content={{chartType:"bar",xKey:"quarter",series:[{dataKey:"amount",label:"Quarterly amount"}],data:[{quarter:"Q1",amount:12},{quarter:"Q2",amount:19}]}} />';
const chart = renderToStaticMarkup(
  createElement(loaded.exports.InlineDocument, { source: chartSource }),
);
assert.match(chart, /<figure/);
assert.match(chart, /aria-label="Quarterly amount"/);
// The guide's chart contract: renderer-chosen colors, no area or title.
for (const extra of ['chartType:"area"', 'chartType:"bar",title:"T"', 'chartType:"bar",series:[{dataKey:"y",color:"red"}]']) {
  const html = render(`<Chart content={{${extra.includes('series') ? extra : extra + ',series:[{dataKey:"y"}]'},xKey:"x",data:[{x:"a",y:1}]}}/>`);
  assert.doesNotMatch(html, /<figure/, extra);
}
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
const chessSource = '<box><AppBlock app_block_id="pocket-chess">' + chessHTML + '</AppBlock></box>';
for (const readOnly of [false, true]) {
  const chess = render(chessSource, {readOnly});
  assert.match(chess, /&lt;!doctype html&gt;/);
  assert.match(chess, /Pocket Chess/);
  assert.match(chess, /globalThis.__appBlockHTMLExecuted = true;/);
  assert.match(chess, readOnly ? /sandbox=""/ : /sandbox="allow-scripts"/);
  assert.doesNotMatch(chess, /Couldn't display|allow-same-origin/);
}
assert.equal(globalThis.__appBlockHTMLExecuted, undefined, 'App HTML must not execute in the host renderer');
// The guide's AppBlock takes its raw HTML/JS body only.
assert.match(render('<AppBlock html="<div>x</div>"></AppBlock>'), /unknown_prop/);
assert.match(render('<AppBlock variant="inline" title="Inline">Body</AppBlock>'), /sandbox="allow-scripts"/);
console.log('AppBlock raw bodies render through the sandbox; no html property.');
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

// Final-guide authoring syntax must render real controls, not source fallback.
const goldControls = render('<input inputType="number" value={2}/><Link url="https://example.com">Example</Link><radio-group name="priority" value="normal"><radio value="low">Low</radio><radio value="normal">Normal</radio><radio value="high" disabled>High</radio></radio-group>');
assert.match(goldControls, /type="number"/);
assert.match(goldControls, /href="https:\/\/example.com"/);
assert.equal((goldControls.match(/type="radio"/g) ?? []).length, 3);
assert.match(goldControls, /checked="" value="normal"/);
assert.match(goldControls, /disabled=""[^>]*value="high"|value="high"[^>]*disabled=""/);
assert.doesNotMatch(goldControls, /Couldn't display|Display details/);
assert.match(render('<radio-group><radio value="yes">Yes</radio></radio-group>', {readOnly:true}), /disabled=""/);
// One spelling per concept: earlier aliases are unknown properties now.
for (const alias of ['<input type="number"/>', '<Link href="https://example.com">x</Link>', '<radio-group options={[{label:"A",value:"a"}]}/>', '<card border radius="xl">x</card>', '<box aria-label="x">x</box>', '<FollowUp text="x"/>']) {
  assert.match(render(alias), /unknown_prop/, alias);
}
// Labels written before a control name it without ids.
const labelled = render('<label>Quantity</label><input inputType="number" value={2}/>');
assert.match(labelled, /<label[^>]*for="([^"]+)-control"[^>]*id="\1-label"|<label[^>]*id="([^"]+)-label"[^>]*for="\2-control"/);
assert.match(labelled, /aria-labelledby="[^"]+-label"/);
assert.doesNotMatch(labelled, /aria-label="input"/);
// Buttons and badges use semantic color x variant; primary is valid.
assert.doesNotMatch(render('<button color="primary">Go</button><button color="danger" variant="outline">Delete</button><badge color="success">Live</badge>'), /Display details/);
assert.match(render('<button loading>Saving</button>'), /aria-busy="true"[\s\S]*Saving/);
// Explicit keyed state slots, as in the reference runtime.
assert.match(render('{@body const [seats, setSeats] = DIL.useState(8, {key:"seats"});}<text>{seats}</text>'), /<span>8<\/span>/);
// AsyncImage aspect ratios may use 5:4.
assert.match(render('<AsyncImage src="https://example.org/a.png" aspectRatio="5:4" alt="A"/>'), /aspect-ratio:5\/4/);
const {readFileSync} = await import('node:fs');
const playground = readFileSync(new URL('./playground.zenith-ui', import.meta.url), 'utf8');
const playgroundHTML = render(playground);
assert.doesNotMatch(playgroundHTML, /Couldn't display|Show source/);
assert.match(playgroundHTML, /50<\/span> credits/);
assert.match(playgroundHTML, /type="number"/);
assert.match(playgroundHTML, /checked="" value="Normal"/);
console.log('Final-guide controls, label association, tones, keyed state and alias rejection passed.');

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

const chessRetry = (await import("node:fs")).readFileSync(new URL("./chess-retry.zenith-ui", import.meta.url), "utf8");
const chessRetryHTML = render(chessRetry);
assert.equal((chessRetryHTML.match(/<button /g) ?? []).length, 65);
assert.doesNotMatch(chessRetryHTML, /Display details|couldn.t be displayed/);
const repairedHTML = render('<box><text>Before</text><button disabled={false || ""}>Go</button><text>{missing.value}</text><text>After</text></box>');
assert.match(repairedHTML, /Before/);
assert.match(repairedHTML, /After/);
assert.match(repairedHTML, /normalized_boolean/);
assert.match(repairedHTML, /evaluation_error/);
assert.doesNotMatch(repairedHTML, /Couldn.t display this response/);
assert.match(render('<text>Arrived</text><slider min={', {streaming:true}), /Preparing slider/);
assert.match(render('<AppBlock><script>window.bad=true', {streaming:true}), /Preparing application/);
assert.doesNotMatch(render('<AppBlock><script>window.bad=true', {streaming:true}), /<iframe/);
assert.doesNotMatch(render('<button>Ready</button><text>Still', {streaming:true}), /disabled=""/);
assert.match(render('<button>Ready</button>', {streaming:true,readOnly:true}), /disabled=""/);
assert.match(render('{@body const broken=missing.x; const fine=42;}<text>{fine}</text>'), /42/);
console.log('Chess markup, local recovery, staged apps, streaming controls and readable siblings passed.');

let repairCalls = 0;
const repairHost = {conversationId:'preview',prepareDraft(){},submit:async()=>{repairCalls++;}};
const renderWithRepair = options => renderToStaticMarkup(createElement(loaded.exports.PreviewContinuationContext.Provider,{value:repairHost},createElement(loaded.exports.InlineDocument,{source:'<text>{unknown}</text>',...options})));
assert.match(renderWithRepair({}), /Ask Agent to repair/);
assert.doesNotMatch(renderWithRepair({readOnly:true}), /Ask Agent to repair/);
assert.doesNotMatch(renderWithRepair({streaming:true}), /Ask Agent to repair/);
assert.equal(repairCalls,0,'display recovery never sends a model request automatically');
assert.match(render('{@body const fine=42; const bad=;}<text>{fine}</text>'), /42/);
const recoveredTable = render('<table><table-row><card>Bad</card><table-cell>Kept</table-cell></table-row></table>');
assert.match(recoveredTable, /Kept/);
assert.doesNotMatch(recoveredTable, /<tr[^>]*><span/);
const svgText = render('<svg viewBox="0 0 10 10"><text>{42}</text></svg>');
assert.match(svgText, /<text[^>]*>42<\/text>/);
assert.doesNotMatch(svgText, /<span/);
console.log('Explicit repair authority, completed declaration recovery and structural fallbacks passed.');

// Icons: Iconsax names only. Other icon sets (Lucide) and the product's own
// aliases are unknown names, reported and omitted, never redrawn as a glyph.
assert.doesNotMatch(render('<icon name="tick-circle"/><icon name="arrow-right-02"/>'), /unknown_icon/);
for (const name of ['check', 'chevron-right', 'x', 'chart'])
  assert.match(render(`<icon name="${name}"/>`), /unknown_icon/, name);
const unknownIcon = render('<row><icon name="not-a-real-icon"/><text>Kept</text></row>');
assert.match(unknownIcon, /unknown_icon/);
assert.match(unknownIcon, /Kept/);
console.log('Iconsax icon names render; other icon sets are diagnosed and omitted.');

// Streaming: every prefix of every authoring example renders without raw
// markup leaking, error states, or per-iteration placeholder floods.
const visibleText = (html) =>
  html
    .replace(/<details[\s\S]*?<\/details>/g, "")
    .replace(/<(pre|code)[\s\S]*?<\/\1>/g, "")
    .replace(/<[^>]*data-inline-stage[^>]*>[^<]*<\/[a-z]+>/g, "")
    .replace(/<[^>]+>/g, " ")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">");
const sweep = (label, source, step = 1) => {
  const full = "Here is the result:\n\n" + source + "\nDone.";
  for (let end = 1; end < full.length; end += step) {
    const html = render(full.slice(0, end), { streaming: true });
    assert.doesNotMatch(html, /data-inline-stage="unavailable"|couldn.t be displayed|role="alert"/, `${label} @${end}`);
    const leak = visibleText(html).match(/<\/?[a-zA-Z][\w-]*[\s/>]|\{[@#:/][a-z]|=\{|\/>/);
    assert.equal(leak, null, `${label} leaked ${leak?.[0]} @${end}`);
    assert.ok((html.match(/data-inline-stage="loading"/g) ?? []).length <= 4, `${label} placeholder flood @${end}`);
  }
};
const skill = readFileSync(new URL('../../mcp/tools/builtin/zenith-intelligent-ui/SKILL.md', import.meta.url), 'utf8');
const skillExamples = [...skill.matchAll(/```jsx\n([\s\S]*?)```/g)].map((match) => match[1]);
assert.ok(skillExamples.length >= 10);
skillExamples.forEach((source, index) => {
  const output = render(source);
  // Provider-bound syntax examples may report unavailable integrations only.
  if (!/turn0product0/.test(source))
    assert.doesNotMatch(output, /Display details|couldn.t be displayed/, `SKILL.md example ${index}`);
  sweep(`SKILL.md example ${index}`, source);
});
sweep('chess', chessRetry, 7);
const draftStream = render('<WritingBlock id="12345">Hello there</Writi', { streaming: true });
assert.match(draftStream, /Hello there/);
assert.doesNotMatch(draftStream, /&lt;\/Writi/);
console.log('Streaming prefixes of every skill example and the chess fixture stay clean.');

// Budget: a full legal-move search at render (an agent-authored chess board)
// costs 60k–210k steps; it must render in the opening and an open position.
const budgetChess = readFileSync(new URL('./chess-budget.zenith-ui', import.meta.url), 'utf8');
for (const position of [null, '....k...........................................................R...K..R']) {
  const source = position ? budgetChess.replace('["rnbqkbnr","pppppppp","........","........","........","........","PPPPPPPP","RNBQKBNR"].join("")', JSON.stringify('....k......................Q..................B................R...K..R'.padEnd(64, '.').slice(0, 64))) : budgetChess;
  const html = render(source);
  assert.doesNotMatch(html, /work budget|time limit|Display details/, position ?? 'opening');
  assert.equal((html.match(/<button[^>]*title="Square/g) ?? []).length, 64, position ?? 'opening');
}
assert.match(render('{@body let n=0; while(true){n++;}}<text>{n}</text>'), /work budget|time limit/);
console.log('Render budget fits a full chess legal-move search and still stops runaway loops.');

// HTML in prose: a bare component-named tag mid-sentence with no closing tag
// is prose about HTML and stays literal; genuine UI with those names renders.
const proseText = (html) =>
  html.replace(/<details[\s\S]*?<\/details>/g, "").replace(/<[^>]+>/g, "")
    .replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&").trim();
for (const sentence of [
  "Use a <button> element for actions.",
  "Every <input> needs a label.",
  "Put rows in a <table> element.",
  "Wrap code in <code> tags.",
  "Use the <title> tag in HTML head.",
  "Close it with </div> and </button>.",
  "Use <text> in SVG for captions.",
]) {
  const html = render(sentence);
  assert.equal(proseText(html), sentence, sentence);
  assert.doesNotMatch(html, /couldn.t be displayed|Display details/, sentence);
  for (let end = 1; end < sentence.length; end++)
    assert.doesNotMatch(render(sentence.slice(0, end), { streaming: true }), /data-inline-stage/, `${sentence} @${end}`);
}
assert.match(render('<label>Name</label>\n<input value="a"/>'), /<label[^>]*>Name<\/label>/);
// A prose mention stays literal even when the same answer has real controls
// with that name further down (found in the browser pass).
const mixed = render('Use a <button> element for actions.\n\n{@body const [n, setN] = DIL.useState(1);}\n<box>\n<button onClick={() => setN(n + 1)}>Add</button>\n<text>{n}</text>\n</box>');
assert.match(proseText(mixed), /^Use a <button> element for actions\./);
assert.match(mixed, /<button[^>]*>Add<\/button>/);
assert.doesNotMatch(mixed, /couldn.t be displayed|Display details/);
assert.match(render('Click <button>Go</button> to continue.'), /<button[^>]*>[\s\S]*Go[\s\S]*<\/button>/);
assert.match(render('Totals:\n<table>\n<table-row><table-cell>A</table-cell></table-row>\n</table>'), /<table/);
assert.match(render('Here is the form <form onSubmit={() => {}}><button submit>Send</button></form>'), /<form/);
console.log('HTML tag names in prose stay literal; genuine UI with the same names renders.');

// Browser-found layout regressions: a block pressable fills its container
// (buttons otherwise shrink to their content and a grid square collapses to
// 0x0), and table bodies carry no whitespace text nodes.
assert.match(render('<grid columns={2}><grid-item><pressable aspectRatio={1}><text> </text></pressable></grid-item></grid>'), /<button[^>]*class="[^"]*\bw-full\b/);
assert.doesNotMatch(render('<pressable inline><text>x</text></pressable>'), /<button[^>]*class="[^"]*\bw-full\b/);
assert.doesNotMatch(render('<table>\n  <table-row>\n    <table-cell>A</table-cell>\n  </table-row>\n</table>'), /<tbody>\s+<|<tr>\s+<td|<\/td>\s+<\/tr>/);
console.log('Pressables fill their grid cells and table bodies hold no whitespace nodes.');
