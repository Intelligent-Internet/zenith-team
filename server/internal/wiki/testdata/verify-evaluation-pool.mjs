// Exercise the production scheduler with real Node worker threads. The adapter
// provides browser Worker events; deliberate hangs happen only in this harness.
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { Worker as Thread } from "node:worker_threads";
import { setTimeout as delay } from "node:timers/promises";
const require = createRequire(new URL("../../../../frontend/package.json", import.meta.url));
const { build } = require("esbuild");
const base = "../../../../frontend/src/features/project-canvas/documents/intelligent-ui/";
async function bundle(file, format = "cjs") {
  return (await build({ entryPoints: [fileURLToPath(new URL(base + file, import.meta.url))], bundle: true, write: false, format, platform: "node", target: "es2022", define: { "import.meta.url": JSON.stringify(new URL(base + file, import.meta.url).href) }, logLevel: "silent" })).outputFiles[0].text;
}
const workerCode = await bundle("evaluation.worker.ts");
const poolCode = await bundle("evaluation-pool.ts");
const live = new Set();
let peak = 0, terminated = 0, stale = false, malformed = false;
class WorkerAdapter {
  onmessage; onerror; onmessageerror;
  constructor() {
    this.thread = new Thread(`
      const { parentPort } = require("node:worker_threads");
      global.self = { postMessage: (data) => parentPort.postMessage(data) };
      ${workerCode}
      parentPort.on("message", (data) => {
        if (data.expression?.op === "literal" && data.expression.value === "__test_hang__") { while (true) {} }
        if (data.expression?.op === "literal" && data.expression.value === "__test_crash__") throw new Error("deliberate test failure");
        self.onmessage({ data });
      });
    `, { eval: true });
    live.add(this);
    peak = Math.max(peak, live.size);
    this.thread.on("message", (data) => {
      if (stale) {
        this.onmessage?.({ data: { ...data, sessionId: "retired-session" } });
        this.onmessage?.({ data: { ...data, generation: data.generation - 1 } });
      }
      if (malformed) this.onmessage?.({ data: { ...data, protocolVersion: 99 } });
      else this.onmessage?.({ data });
    });
    this.thread.on("error", () => this.onerror?.({ preventDefault() {} }));
  }
  postMessage(data) { this.thread.postMessage(data); }
  terminate() { live.delete(this); terminated++; void this.thread.terminate(); }
}
global.Worker = WorkerAdapter;
const poolModule = { exports: {} };
const timers = new Map();
const trackedTimeout = (callback, milliseconds) => {
  const handle = setTimeout(() => { timers.delete(handle); callback(); }, milliseconds);
  timers.set(handle, { callback, milliseconds });
  return handle;
};
const trackedClear = (handle) => { timers.delete(handle); clearTimeout(handle); };
const expireLongTimers = () => {
  for (const [handle, timer] of [...timers]) {
    if (timer.milliseconds !== 30000) continue;
    trackedClear(handle);
    timer.callback();
  }
};
new Function("require", "module", "exports", "setTimeout", "clearTimeout", poolCode)(require, poolModule, poolModule.exports, trackedTimeout, trackedClear);
const { scheduleEvaluation } = poolModule.exports;
let generation = 0;
const request = (value) => ({ protocolVersion: 1, sessionId: "viewer", generation: ++generation, expression: { op: "literal", value }, environment: { state: {}, data: {} } });
const evaluate = (value, signal = new AbortController().signal) => scheduleEvaluation(request(value), signal);
try {
  assert.equal(await evaluate(7), 7);
  stale = true;
  assert.equal(await evaluate(8), 8, "superseded replies must not fail the active evaluation");
  stale = false;
  const running = Array.from({ length: 4 }, () => new AbortController());
  const pending = running.map((controller) => evaluate("__test_hang__", controller.signal).catch((error) => error.code));
  await delay(100);
  assert.equal(live.size, 4);
  const queued = new AbortController();
  const queuedResult = evaluate("must not run", queued.signal).catch((error) => error.code);
  queued.abort();
  assert.equal(await queuedResult, "evaluation.cancelled");
  running.forEach((controller) => controller.abort());
  assert.deepEqual(await Promise.all(pending), Array(4).fill("evaluation.cancelled"));
  assert.equal(live.size, 0, "cancelled running workers must be terminated");
  assert.deepEqual(await Promise.all(Array.from({ length: 16 }, (_, i) => evaluate(i))), Array.from({ length: 16 }, (_, i) => i));
  assert.equal(peak, 4, "pool must never exceed four workers");
  for (const size of ["count", "bytes"]) {
    const blockers = Array.from({ length: 4 }, () => new AbortController());
    const blocked = blockers.map((controller) => evaluate("__test_hang__", controller.signal).catch((error) => error.code));
    await delay(20);
    const controllers = Array.from({ length: size === "count" ? 4097 : 18 }, () => new AbortController());
    const payload = size === "count" ? 1 : "x".repeat(2000000);
    const results = controllers.map((controller) => evaluate(payload, controller.signal).catch((error) => error.code));
    controllers.forEach((controller) => controller.abort());
    const codes = await Promise.all(results);
    assert.ok(codes.includes("evaluation.queue"), `${size} queue bound must reject excess work`);
    assert.ok(codes.every((code) => code === "evaluation.queue" || code === "evaluation.cancelled"));
    blockers.forEach((controller) => controller.abort());
    await Promise.all(blocked);
    assert.equal(await evaluate("capacity restored"), "capacity restored", "cancel must release queue memory and slots");
  }
  await assert.rejects(evaluate("__test_crash__"), (error) => error.code === "evaluation.worker");
  assert.equal(await evaluate("after crash"), "after crash");
  const blockers = Array.from({ length: 4 }, () => new AbortController());
  const blocked = blockers.map((controller) => evaluate("__test_hang__", controller.signal).catch((error) => error.code));
  await delay(20);
  const deadline = evaluate("queued deadline").catch((error) => error.code);
  expireLongTimers();
  assert.equal(await deadline, "evaluation.queueTimeout", "queue deadline must settle waiting work");
  blockers.forEach((controller) => controller.abort());
  await Promise.all(blocked);
  const beforeTimeout = terminated;
  await assert.rejects(evaluate("__test_hang__"), (error) => error.code === "evaluation.timeout");
  assert.equal(terminated, beforeTimeout + 1);
  assert.equal(await evaluate("after timeout"), "after timeout");
  malformed = true;
  await assert.rejects(evaluate("bad protocol"), (error) => error.code === "evaluation.message");
  malformed = false;
  assert.equal(await evaluate("recovered"), "recovered");
  await assert.rejects(evaluate("x".repeat(2097152)), (error) => error.code === "value.size");
  expireLongTimers();
  assert.equal(live.size, 0, "idle deadline must release worker slots");
  assert.equal(timers.size, 0, "settled and retired work must not retain timers");
  assert.equal(await evaluate("after idle retirement"), "after idle retirement");
  console.log("Worker pool checks: stale replies, real-thread cancellation/timeout/replacement, malformed messages, request/queue size caps, queue cleanup and four-worker concurrency passed.");
} finally {
  for (const worker of live) worker.terminate();
  for (const handle of timers.keys()) trackedClear(handle);
}
// Exit naturally after worker termination; leaked timers/threads fail the Go deadline.
