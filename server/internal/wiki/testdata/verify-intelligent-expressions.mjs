// Server-owned cross-runtime check, executed by the Go integration lane.
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { readFileSync } from "node:fs";
const require = createRequire(
  new URL("../../../../frontend/package.json", import.meta.url),
);
const { transformSync } = require("esbuild");
const source = readFileSync(
  new URL(
    "../../../../frontend/src/features/project-canvas/documents/intelligent-ui/expression.ts",
    import.meta.url,
  ),
  "utf8",
);
const { code } = transformSync(source, {
  loader: "ts",
  format: "esm",
  target: "es2022",
});
const { evaluateExpression, EvaluationError } = await import(
  `data:text/javascript;base64,${Buffer.from(code).toString("base64")}`
);
const cases = JSON.parse(
  readFileSync(
    new URL("intelligent-expressions.json", import.meta.url),
    "utf8",
  ),
);
for (const fixture of cases) {
  const before = JSON.stringify(fixture);
  if (fixture.code)
    assert.throws(
      () => evaluateExpression(fixture.expression, fixture.environment),
      (error) =>
        error instanceof EvaluationError && error.code === fixture.code,
      fixture.name,
    );
  else
    assert.deepEqual(
      evaluateExpression(fixture.expression, fixture.environment),
      fixture.value,
      fixture.name,
    );
  assert.equal(
    JSON.stringify(fixture),
    before,
    `${fixture.name}: input mutated`,
  );
}
console.log(
  `Verified ${cases.length} expression fixtures against the client interpreter.`,
);

const boundaries = JSON.parse(readFileSync(new URL("intelligent-value-boundaries.json", import.meta.url), "utf8"));
for (const fixture of boundaries) {
  const rows = Array(fixture.rows).fill(fixture.unit.repeat(fixture.repeat));
  if (fixture.lastRepeat !== undefined) rows[rows.length - 1] = fixture.unit.repeat(fixture.lastRepeat);
  const expression = { op: "count", input: { op: "data", name: "rows" } };
  for (const binding of [{}, { hasItem: false }, { item: null, hasItem: false }]) {
    const environment = { state: {}, data: { rows }, ...binding };
    if (fixture.code) assert.throws(() => evaluateExpression(expression, environment), (error) => error instanceof EvaluationError && error.code === fixture.code, fixture.name);
    else assert.equal(evaluateExpression(expression, environment), fixture.value, fixture.name);
    assert.equal(Object.hasOwn(environment, "hasItem"), Object.hasOwn(binding, "hasItem"), "normalization does not mutate caller input");
  }
}
console.log(`Verified ${boundaries.length} shared byte boundaries across omitted/explicit/null item envelopes.`);
