import assert from "node:assert/strict";
import {createRequire} from "node:module";
import {fileURLToPath} from "node:url";
import {readFileSync} from "node:fs";
const frontend = fileURLToPath(new URL("../../../../frontend/", import.meta.url));
const require = createRequire(frontend + "package.json");
const {build} = require("esbuild");
const Module = require("node:module");
const result = await build({stdin:{contents:'export * from "./parser"; export * from "./runtime";',resolveDir:frontend+'src/shared/inline-ui',loader:'ts'},bundle:true,write:false,platform:'node',format:'cjs',tsconfigRaw:{}});
const loaded = new Module(frontend + '.inline-runtime-check.cjs');
loaded.filename = frontend + ".inline-runtime-check.cjs";
loaded.paths = Module._nodeModulePaths(frontend);
loaded._compile(result.outputFiles[0].text, loaded.filename);
const {parseInlineUI, Interpreter} = loaded.exports;
const source = '{@body const [quantity, setQuantity] = DIL.useState(2); const increase = DIL.useCallback(() => setQuantity(v => v + 1), []);}<box><text>{quantity * 25}</text><button onClick={increase}>Add</button></box>';
const parsed = parseInlineUI(source);
assert.equal(parsed.complete, true);
assert.equal(parsed.diagnostic, undefined);
for (let end=1;end<=source.length;end++) {
  const prefix = parseInlineUI(source.slice(0,end));
  assert.equal(prefix.diagnostic, undefined, `valid streaming prefix ${end}`);
}
const host = {state:new Map(),refs:new Map(),memo:new Map(),effects:[],theme:'light',width:800,change(key,value){this.state.set(key,value)},action(){throw new Error('host action reached')}};
let runtime = new Interpreter(host);
let env = runtime.environment();
runtime.program(parsed.nodes[0].program, env);
assert.equal(env.quantity,2);
assert.throws(()=>runtime.call(env.increase,[]), /callback/);
runtime.event=true;
runtime.call(env.increase,[]);
runtime.event=false;
runtime = new Interpreter(host);
env = runtime.environment();
runtime.program(parsed.nodes[0].program,env);
assert.equal(env.quantity,3, 'state persists across renders');
runtime.event=true;
runtime.call(env.increase,[]);
runtime.event=false;
const afterMemo = runtime.environment();
runtime.program(parsed.nodes[0].program, afterMemo);
assert.equal(afterMemo.quantity,4,'memoized callback uses the current event budget and permissions');

const evaluate = (expression) => {
  const node = parseInlineUI(`<text>{${expression}}</text>`).nodes[0].children[0];
  return runtime.evaluate(node.expression,env);
};
assert.equal(evaluate('quantity * 25'),75);
assert.equal(evaluate('(1234.56).toLocaleString("en-US", {maximumFractionDigits:0})'), '1,235');
assert.equal(evaluate('Math.cos(0)'), 1);
assert.equal(evaluate('Math.sin(0)'), 0);
assert.ok(Math.abs(evaluate('Math.sin(0.9)') - Math.sin(0.9)) < 1e-12);
assert.throws(() => evaluate('Math.random()'), /Only supported UI functions/);
assert.throws(()=>evaluate('window.location'),/Unknown UI variable/);
assert.throws(()=>evaluate('({}).constructor'),/unavailable/);
assert.throws(()=>evaluate('GenUI.openUrl("https://example.com")'),/user interaction/);
assert.throws(()=>evaluate('((x) => x(x))((x) => x(x))'),/depth/);
env.longText = 'a'.repeat(40000);
assert.throws(()=>evaluate('String.raw`${longText}${longText}`'),/Text exceeds/);
assert.equal(evaluate('String.raw`line\\n${quantity}`'), 'line\\n3');
assert.equal(parseInlineUI('```jsx\n<box>literal</box>\n```').nodes[0].type,'text');
assert.equal(parseInlineUI('<box><text>Partial').complete,false);
assert.ok(parseInlineUI('<box></row>').diagnostic);
assert.match(parseInlineUI('<box style={{padding:4}}>Bad</box>').diagnostic, /property.*style/);
assert.match(parseInlineUI('<grid><card>Bad</card></grid>').diagnostic, /grid-item/);
assert.match(parseInlineUI('{@module const x = DIL.useState(0)}<text>x</text>').diagnostic, /hook/i);
assert.match(parseInlineUI('{@body const f = () => DIL.useState(0)}<text>x</text>').diagnostic, /hook/i);
for (const source of [
  '<grid><svg><circle r="2"/></svg></grid>',
  '<grid>{"unstructured"}</grid>',
  '<text><card>Block</card></text>',
  '<input>Child</input>',
  '<box radius="invented"/>',
  '<checkbox checked="false"/>',
  '{@module const x = GenUI.copy("no")}<text>x</text>',
  '{@module const mutate = ref => {ref.current = 1}}<text>x</text>',
]) assert.ok(parseInlineUI(source).diagnostic, source);
const pureSource = parseInlineUI('{@module const apply = (fn) => fn(4); const clamp=(n,lo,hi)=>Math.max(lo,Math.min(hi,n));}');
assert.equal(pureSource.diagnostic, undefined);
const pureRuntime = new Interpreter(host);
const pureEnv = pureRuntime.environment();
pureRuntime.module = true;
pureRuntime.program(pureSource.nodes[0].program,pureEnv);
pureRuntime.module = false;
assert.equal(pureRuntime.call(pureEnv.clamp,[12,0,10]), 10);
pureRuntime.event = true;
assert.throws(() => pureRuntime.call(pureEnv.apply,[env.setQuantity]), /module helpers/);
pureRuntime.event = false;
const showcase = readFileSync(new URL('../../mcp/tools/builtin/zenith-intelligent-ui/component-demo.zenith-ui', import.meta.url), 'utf8');
const branches = '<box>{#if false}<text>A</text>{:else if true}<text>B</text>{:else if false}<text>C</text>{:else}<text>D</text>{/if}</box>';
const orbit = readFileSync(new URL('../../mcp/tools/builtin/zenith-intelligent-ui/orbit-demo.zenith-ui', import.meta.url), 'utf8');
for (const candidate of [showcase, branches, orbit]) {
  const document = parseInlineUI(candidate);
  assert.equal(document.diagnostic, undefined);
  assert.equal(document.complete, true);
  for (let end = 1; end <= candidate.length; end++) {
    assert.equal(parseInlineUI(candidate.slice(0, end)).diagnostic, undefined, `branch streaming prefix ${end}`);
  }
}
const branch = parseInlineUI(branches).nodes[0].children[0];
assert.equal(runtime.evaluate(branch.expression, env), false);
assert.equal(runtime.evaluate(branch.otherwise[0].expression, env), true);
assert.equal(branch.otherwise[0].children[0].children[0].value, 'B');
assert.equal(branch.otherwise[0].otherwise[0].otherwise[0].children[0].value, 'D');
console.log('Inline grammar, streaming, local state and interpreter boundaries passed.');
await import('./verify-inline-render.mjs');
