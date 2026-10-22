// Local interpreter calibration, not a browser or rendering benchmark.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
const require = createRequire(new URL("../../../../frontend/package.json", import.meta.url));
const { build } = require("esbuild");
const source = new URL("../../../../frontend/src/features/project-canvas/documents/intelligent-ui/expression.ts", import.meta.url);
const result = await build({ entryPoints: [fileURLToPath(source)], bundle: true, write: false, format: "esm", platform: "node", target: "es2022", logLevel: "silent" });
const { compileExpression, canonicalJSON } = await import(`data:text/javascript;base64,${Buffer.from(result.outputFiles[0].text).toString("base64")}`);
const fixtures = JSON.parse(await readFile(new URL("intelligent-expressions.json", import.meta.url), "utf8"));
console.log(JSON.stringify({ runtime: process.version, platform: process.platform, architecture: process.arch }));
const boundaries = JSON.parse(await readFile(new URL("intelligent-value-boundaries.json", import.meta.url), "utf8"));
const workloads = fixtures.filter((item) => item.name.startsWith("workload:"));
for (const recipe of boundaries) {
  const rows = Array(recipe.rows).fill(recipe.unit.repeat(recipe.repeat));
  if (recipe.lastRepeat !== undefined) rows[rows.length - 1] = recipe.unit.repeat(recipe.lastRepeat);
  workloads.push({ name: `bytes:${recipe.name}`, expression: { op: "count", input: { op: "data", name: "rows" } }, environment: { state: {}, data: { rows } }, value: recipe.value, code: recipe.code });
}
for (const fixture of workloads) {
  const compiled = compileExpression(fixture.expression);
  const run = () => {
    try {
      const value = compiled(fixture.environment);
      assert.equal(fixture.code, undefined);
      return value;
    } catch (error) {
      assert.equal(error.code, fixture.code);
      if (!fixture.code) throw error;
    }
  };
  const value = run();
  if (!fixture.code) assert.deepEqual(value, fixture.value);
  for (let i = 0; i < 10; i++) run();
  const samples = [];
  for (let i = 0; i < 100; i++) {
    const start = performance.now(); run(); samples.push(performance.now() - start);
  }
  samples.sort((a,b) => a-b);
  console.log(JSON.stringify({ workload: fixture.name, samples: samples.length, medianMs: samples[49], p95Ms: samples[94], maxMs: samples[99], inputBytes: Buffer.byteLength(JSON.stringify(fixture.environment)), canonicalBytes: Buffer.byteLength(canonicalJSON(fixture.environment)), outcome: fixture.code ?? "ready" }));
}
