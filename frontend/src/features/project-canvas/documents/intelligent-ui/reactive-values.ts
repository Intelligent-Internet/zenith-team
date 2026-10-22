import { measureUI, measureUIFrame, uiNow } from "./performance";
import {
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import type { NodeViewProps } from "@tiptap/react";
import { IntelligentInstanceContext } from "./document-context";
import { editorRuntime } from "./editor-runtime";
import { evaluateValue } from "./value-evaluation";
import {
  expressionDependencies,
  expressionUsesItem,
  validateExpression,
  type EvaluationEnvironment,
} from "./expression";
import type { ScopeSnapshot } from "./scope-runtime";

const empty: ScopeSnapshot = Object.freeze({
  generation: 0,
  status: "ready",
  environment: { state: {}, data: {} },
});
const noSubscribe = () => () => {};
const emptySnapshot = () => empty;

export function useScope({ editor, node, getPos }: NodeViewProps) {
  const instance = useContext(IntelligentInstanceContext);
  const registry = editorRuntime(editor);
  useSyncExternalStore(
    registry.subscribe,
    registry.getSnapshot,
    registry.getSnapshot,
  );
  const position = getPos();
  let boundary = node.type.name === "uiScope";
  let template = false;
  if (position !== undefined) {
    const at = editor.state.doc.resolve(
      Math.min(position, editor.state.doc.content.size),
    );
    for (let depth = at.depth; depth > 0; depth--)
      if (at.node(depth).type.name === "uiRepeat") template = true;
  }
  let scope =
    node.type.name === "uiScope" ? registry.get(node.attrs.blockId) : undefined;
  if (!scope && position !== undefined) {
    const resolved = editor.state.doc.resolve(
      Math.min(position, editor.state.doc.content.size),
    );
    for (let depth = resolved.depth; depth > 0; depth--) {
      const ancestor = resolved.node(depth);
      if (ancestor.type.name === "uiScope") {
        boundary = true;
        scope = registry.get(ancestor.attrs.blockId);
        break;
      }
    }
  }
  if (!boundary) scope ??= instance?.runtime;
  const snapshot = useSyncExternalStore(
    scope?.subscribe ?? noSubscribe,
    scope?.getSnapshot ?? emptySnapshot,
    emptySnapshot,
  );
  return { scope, snapshot, template };
}
export function useValue(
  expression: unknown,
  snapshot: ScopeSnapshot,
  mode: "live" | "comparison" | "snapshot",
) {
  const expressionKey = JSON.stringify(expression);
  const plan = useMemo(() => {
    if (!expression) return undefined;
    try {
      validateExpression(expression);
      const node = expression as { op: string; value: unknown };
      if (node.op === "literal")
        return { literal: true as const, value: node.value, dependencies: [] };
      if (mode === "snapshot")
        return { failed: true as const, dependencies: [] };
      return {
        dependencies: expressionDependencies(expression),
        usesItem: expressionUsesItem(expression),
      };
    } catch {
      return { failed: true as const, dependencies: [] };
    }
  }, [expressionKey, mode]);
  const environment: EvaluationEnvironment = { state: {}, data: {} };
  let available = snapshot.status === "ready";
  for (const dependency of plan?.dependencies ?? []) {
    const [family, name] = dependency.split(".") as ["state" | "data", string];
    if (
      !Object.prototype.hasOwnProperty.call(snapshot.environment[family], name)
    )
      available = false;
    else environment[family][name] = snapshot.environment[family][name];
  }
  if (plan?.usesItem && snapshot.environment.hasItem) {
    environment.hasItem = true;
    environment.item = snapshot.environment.item;
  }
  const key =
    available && plan && !plan.literal && !plan.failed
      ? JSON.stringify([expressionKey, environment])
      : "";
  const [result, setResult] = useState<{
    key: string;
    expressionKey?: string;
    status: "ready" | "failed";
    value?: unknown;
  }>({ key: "", status: "failed" });
  useEffect(() => {
    if (!key || result.key === key) return;
    const started = uiNow();
    const job = evaluateValue(key, expression, environment);
    let active = true;
    void job.promise.then(
      (value) => {
        if (active) {
          measureUI("evaluation", started, "ready");
          setResult({ key, expressionKey, status: "ready", value });
        }
      },
      () => {
        if (active) {
          measureUI("evaluation", started, "failed");
          setResult({ key, expressionKey, status: "failed" });
        }
      },
    );
    return () => {
      active = false;
      job.release();
    };
  }, [key]); // The key includes only the expression's observed dependencies.
  useEffect(() => {
    // Do not keep private results after a source/definition becomes unavailable.
    if (!available && !snapshot.retainValues)
      setResult({ key: "", status: "failed" });
  }, [available, snapshot.retainValues]);
  const measuredInput = useRef<number | undefined>(undefined);
  useEffect(() => {
    const started = snapshot.inputStartedAt;
    if (
      mode === "snapshot" ||
      started === undefined ||
      measuredInput.current === started ||
      result.status !== "ready" ||
      result.key !== key ||
      !key
    )
      return;
    let current = true;
    const cancel = measureUIFrame(
      "input-to-frame",
      started,
      () => current,
      () => {
        measuredInput.current = started;
      },
    );
    return () => {
      current = false;
      cancel();
    };
  }, [key, result, snapshot.inputStartedAt, mode]);
  if (plan?.literal) return { status: "ready" as const, value: plan.value };
  if (plan?.failed) return { status: "failed" as const };
  if (!available) {
    if (
      snapshot.retainValues &&
      result.expressionKey === expressionKey &&
      result.status === "ready"
    )
      return { ...result, updating: true };
    return {
      status: snapshot.status === "ready" ? "failed" : snapshot.status,
      value: undefined,
    };
  }
  return result.key === key
    ? result
    : result.expressionKey === expressionKey && result.status === "ready"
      ? { ...result, updating: true }
      : { status: "loading" as const, value: undefined };
}
