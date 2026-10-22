// Server-owned integration harness for the production client scope runtime.
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
const require = createRequire(
  new URL("../../../../frontend/package.json", import.meta.url),
);
const { build } = require("esbuild");
const result = await build({
  entryPoints: [
    fileURLToPath(
      new URL(
        "../../../../frontend/src/features/project-canvas/documents/intelligent-ui/scope-runtime.ts",
        import.meta.url,
      ),
    ),
  ],
  bundle: true,
  write: false,
  format: "esm",
  platform: "node",
  target: "es2022",
});
const { createScopeRuntime } = await import(
  `data:text/javascript;base64,${Buffer.from(result.outputFiles[0].text).toString("base64")}`
);
const definition = (initial, extra = {}) => ({
  type: "uiScope",
  attrs: { blockId: "scope-a", name: "scenario", initial, ...extra },
});
const runtime = createScopeRuntime(
  definition({ amount: 1, enabled: true, optional: null }),
);
try {
  assert.equal(runtime.getSnapshot().inputStartedAt, undefined);
  runtime.setState("amount", 9);
  const inputStarted = runtime.getSnapshot().inputStartedAt;
  assert.equal(typeof inputStarted, "number");
  runtime.setState("amount", 9);
  assert.equal(
    runtime.getSnapshot().inputStartedAt,
    inputStarted,
    "unchanged input does not restart timing",
  );
  const child = createScopeRuntime(
    definition({ child: 1 }, { blockId: "child" }),
    runtime,
  );
  await Promise.resolve();
  assert.equal(
    child.getSnapshot().inputStartedAt,
    inputStarted,
    "nested output inherits input timing",
  );
  assert.equal(
    Object.hasOwn(runtime.getInputs(), "inputStartedAt"),
    false,
    "diagnostic timing is not scenario state",
  );
  child.dispose();
  assert.equal(
    child.getSnapshot().inputStartedAt,
    undefined,
    "disposed scope drops timing",
  );
  runtime.updateDefinition(
    definition(
      { amount: 2, enabled: true, optional: null },
      { catalogVersion: 1 },
    ),
  );
  await Promise.resolve();
  assert.equal(
    runtime.getInputs().amount,
    9,
    "explicit default version must preserve viewer input",
  );
  assert.equal(runtime.getSnapshot().inputChanges, undefined);
  assert.equal(runtime.getSnapshot().status, "ready");
  runtime.updateDefinition(
    definition({ amount: "new", optional: "filled", added: false }),
  );
  await Promise.resolve();
  assert.deepEqual(runtime.getInputs(), {
    amount: "new",
    optional: "filled",
    added: false,
  });
  assert.deepEqual(runtime.getSnapshot().inputChanges, {
    reset: ["amount", "optional"],
    removed: ["enabled"],
  });
  assert.throws(() => runtime.setState("amount", 2), /binding.type/);
  assert.throws(() => runtime.setState("enabled", false), /binding.undeclared/);
  runtime.setState("amount", "viewer");
  runtime.updateDefinition(
    definition({ amount: "updated default", optional: "filled", added: false }),
  );
  await Promise.resolve();
  assert.equal(runtime.getInputs().amount, "viewer");
  assert.equal(runtime.getSnapshot().inputChanges, undefined);
  assert.throws(
    () => runtime.updateDefinition(definition({ amount: [] })),
    /state.definition/,
  );
  assert.equal(
    runtime.getInputs().amount,
    "viewer",
    "invalid replacement must leave inputs intact",
  );
  const secondViewer = createScopeRuntime(definition({ amount: "new" }));
  const copy = createScopeRuntime(
    definition({ amount: "new" }, { blockId: "scope-copy" }),
  );
  try {
    assert.equal(
      secondViewer.getInputs().amount,
      "new",
      "viewer instances must be isolated",
    );
    assert.equal(
      copy.getInputs().amount,
      "new",
      "copied scopes must start from defaults",
    );
  } finally {
    secondViewer.dispose();
    copy.dispose();
  }
} finally {
  runtime.dispose();
}

const sourceRuntime = createScopeRuntime({
  type: "uiScope",
  attrs: {
    blockId: "scope-source",
    initial: {},
    sources: {
      tasks: {
        type: "tasks",
        projectId: "11111111-1111-4111-8111-111111111111",
      },
    },
  },
});
try {
  const rows = [{ id: "task-a", title: "Restricted task" }];
  sourceRuntime.setSource("tasks", {
    status: "ready",
    value: rows,
    version: "revision-a",
    observedAt: "2026-10-19T00:00:00Z",
    coverage: { rowCount: 1, limit: 10, complete: true },
  });
  await Promise.resolve();
  assert.deepEqual(sourceRuntime.getSnapshot().environment.data.tasks, rows);
  sourceRuntime.setSource("tasks", { status: "denied" });
  assert.deepEqual(
    sourceRuntime.getSnapshot().environment.data,
    {},
    "permission loss clears previously authorized source rows immediately",
  );
  assert.equal(sourceRuntime.getProvenance().tasks.status, "denied");
  sourceRuntime.setSource("tasks", { status: "stale" });
  assert.deepEqual(
    sourceRuntime.getSnapshot().environment.data,
    {},
    "stale sources cannot continue feeding expressions",
  );
  assert.equal(sourceRuntime.getProvenance().tasks.status, "stale");
} finally {
  sourceRuntime.dispose();
}
assert.deepEqual(runtime.getInputs(), {});
assert.equal(runtime.getSnapshot().status, "failed");
assert.equal(runtime.getSnapshot().inputStartedAt, undefined);

// A collaborative options edit must not make the native select imply that a
// preserved local value changed. Check the actual selected-option markup.
