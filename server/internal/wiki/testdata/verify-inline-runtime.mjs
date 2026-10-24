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
assert.throws(() => evaluate('Math.random()'), /Math\.random\(\) is not a supported UI function/);
assert.throws(() => evaluate('Date.now()'), /Date\.now\(\) is not a supported UI function/);
assert.throws(() => evaluate('new Date()'), /current time is unavailable/);
assert.throws(() => evaluate('new Set([1])'), /new Set\(\) is not supported/);
assert.throws(() => evaluate('"a".replace("a", (x) => x)'), /text search and replacement/);
assert.throws(() => evaluate('"x".repeat(100000)'), /Text exceeds/);
assert.throws(() => evaluate('Array.from({length:5000})'), /1000 items/);
assert.throws(() => evaluate('[1].concat(Array(1000))'), /1000 items/);
// Pure deterministic helpers authors commonly reach for.
for (const [expression, expected] of [
  ['Array.from({length:3},(_,i)=>i*2)', [0,2,4]],
  ['Array(3).fill(0)', [0,0,0]],
  ['[1,2,3].findIndex((x)=>x===2)', 1],
  ['[[1],[2]].flat()', [1,2]],
  ['[1].concat([2],3)', [1,2,3]],
  ['[1,2,3].at(-1)', 3],
  ['[1,2,3].reverse()', [3,2,1]],
  ['"7".padStart(3,"0")', '007'],
  ['"a-b-c".replaceAll("-","+")', 'a+b+c'],
  ['"ab".repeat(2)', 'abab'],
  ['Object.fromEntries([["a",1]]).a', 1],
  ['Object.assign({}, {a:1}, {b:2}).b', 2],
  ['Number.isFinite(3) && Number.isInteger(3)', true],
  ['parseInt("42",10) + parseFloat("0.5")', 42.5],
  ['Math.max(...[1,5,2])', 5],
  ['Math.sign(-3) + Math.trunc(2.7) + Math.hypot(3,4)', 6],
  ['Math.PI > 3', true],
  ['(255).toString(16)', 'ff'],
  ['String.fromCharCode(65)', 'A'],
  ['new Date(1700000000 * 1000).toISOString()', '2023-11-14T22:13:20.000Z'],
  ['new Date(Date.UTC(2026,0,2)).getUTCDate()', 2],
]) assert.deepEqual(JSON.parse(JSON.stringify(evaluate(expression))), expected, expression);
const frozen = parseInlineUI('{@body const [items]=DIL.useState([1,2]); items.fill(0);}');
assert.throws(() => new Interpreter({state:new Map(),refs:new Map(),memo:new Map(),effects:[],theme:'light',width:800,change(){},action(){}}).program(frozen.nodes[0].program, new Interpreter({state:new Map(),refs:new Map(),memo:new Map(),effects:[],theme:'light',width:800,change(){},action(){}}).environment()), /local mutable/);
assert.equal(parseInlineUI('{@module function day(ts){ return new Date(ts * 1000).getUTCDay(); }}').diagnostic, undefined);
assert.match(parseInlineUI('{@module function bad(){ return new Set(); }}').diagnostic, /pure/);
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
const showcase = readFileSync(new URL('./component-demo.zenith-ui', import.meta.url), 'utf8');
const branches = '<box>{#if false}<text>A</text>{:else if true}<text>B</text>{:else if false}<text>C</text>{:else}<text>D</text>{/if}</box>';
const orbit = readFileSync(new URL('./orbit-demo.zenith-ui', import.meta.url), 'utf8');
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
// Recovery leaves strict authoring checks intact and reports every safe repair.
const recovered = parseInlineUI('<box gap="1" invented="x"><button disabled={false || ""}>Go</button><text>{missing.value}</text><text>Kept</text></box>', {recover:true});
assert.equal(recovered.complete, true);
assert.ok(recovered.diagnostics.some(d => d.code === 'unknown_prop'));
assert.ok(recovered.diagnostics.some(d => d.code === 'invalid_literal'));
assert.equal(recovered.nodes[0].props.gap, undefined);
const partialBody = parseInlineUI('{@body const [n,setN]=DIL.useState(1); const next =', {recover:true});
assert.equal(partialBody.nodes[0].type, 'body');
assert.equal(partialBody.nodes[1].type, 'pending');
const nestedRepair = parseInlineUI('<box><text>Kept</box><text>Sibling</text>', {recover:true});
assert.equal(nestedRepair.nodes[1].children[0].value, 'Sibling');
assert.ok(nestedRepair.diagnostics.some(d => d.code === 'unclosed_block'));
for (let end=1; end<=source.length; end++) {
  const partial = parseInlineUI(source.slice(0,end), {recover:true});
  assert.equal(partial.diagnostic, undefined, `recovered stream ${end}`);
}
const stableHost = {state:new Map(),refs:new Map(),memo:new Map(),effects:[],theme:'light',width:800,change(key,value){this.state.set(key,value)},action(){}};
const mountStable = script => {
  const r = new Interpreter(stableHost), e = r.environment();
  r.hookScope = 'response:body';
  r.program(parseInlineUI('{@body '+script+'}').nodes[0].program,e);
  return [r,e];
};
let [stable, stableEnv] = mountStable('const [count,setCount]=DIL.useState(2);');
stable.event = true;
stable.call(stableEnv.setCount,[9]);
stable.event = false;
[stable, stableEnv] = mountStable('const heading="New text"; const [count,setCount]=DIL.useState(2);');
assert.equal(stableEnv.count,9,'named state survives source offsets changing');
const chess = readFileSync(new URL('./chess-retry.zenith-ui', import.meta.url),'utf8');
const chessProgram = parseInlineUI(chess).nodes[0].program;
const chessHost = {state:new Map(),refs:new Map(),memo:new Map(),effects:[],theme:'light',width:800,change(key,value){this.state.set(key,value)},action(){throw new Error('chess has no host actions')}};
let chessRuntime, chessEnv;
const mountChess = () => {
  chessHost.execution = {remaining:50000,depth:0,event:false,hostActions:false};
  chessRuntime = new Interpreter(chessHost);
  chessEnv = chessRuntime.environment();
  chessRuntime.program(chessProgram,chessEnv);
};
const choose = (r,c) => {
  chessRuntime.remaining = loaded.exports.interactionBudget;
  chessRuntime.event = true;
  try { chessRuntime.call(chessEnv.choose,[r,c]); } catch (e) { throw new Error(`chess square ${r},${c}: ${e.message}`, {cause:e}); }
  chessRuntime.event = false;
  mountChess();
};
mountChess();
choose(6,4);
assert.deepEqual(chessEnv.targets,[[5,4],[4,4]]);
choose(4,4);
assert.equal(chessEnv.board[4][4],'P');
assert.equal(chessEnv.turn,'b');
assert.deepEqual(chessEnv.history,['e4']);
choose(1,4); choose(3,4);
assert.deepEqual(chessEnv.history,['e4','e5']);
chessRuntime.event = true;
chessRuntime.call(chessEnv.reset,[]);
mountChess();
// Fool's mate traverses sliding pieces, check detection and terminal state.
choose(6,5);choose(5,5);choose(1,4);choose(3,4);choose(6,6);choose(4,6);choose(0,3);choose(4,7);
assert.equal(chessEnv.ended,true);
assert.equal(chessEnv.status,'Black wins by checkmate');
const bounded = new Interpreter({state:new Map(),refs:new Map(),memo:new Map(),effects:[],theme:'light',width:800,change(){},action(){}});
assert.throws(() => bounded.program(parseInlineUI('{@body while(true) {}}').nodes[0].program,bounded.environment()), /work budget/);
const immutable = new Interpreter({state:new Map(),refs:new Map(),memo:new Map(),effects:[],theme:'light',width:800,change(){},action(){}});
assert.throws(() => immutable.program(parseInlineUI('{@body const [items]=DIL.useState([1]); items.push(2);}').nodes[0].program,immutable.environment()), /local mutable/);
console.log('Repair diagnostics, streaming prefixes, stable state, chess moves/checkmate and execution limits passed.');
await import('./verify-inline-render.mjs');
