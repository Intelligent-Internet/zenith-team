import { measureUI, uiNow } from "./performance";
import { createScopeDefinitionCache, isScopeScalar } from "./scope-definition";
import type { components } from "@/shared/api/generated/api.generated";
import { createEvaluationClient } from "./evaluation-client";
import {
  checkValue,
  EvaluationError,
  type EvaluationEnvironment,
} from "./expression";

type Block = components["schemas"]["CanvasBlock"];
type Coverage = components["schemas"]["IntelligentSourceCoverage"];
type Scalar = string | number | boolean | null;
export type SourceSnapshot =
  | {
      status: "ready";
      value: unknown;
      version: string;
      observedAt: string;
      coverage?: Coverage;
    }
  | {
      status: "empty";
      version: string;
      observedAt: string;
      coverage?: Coverage;
    }
  | { status: "loading" | "denied" | "deleted" | "stale" | "failed" };
export interface ScopeSnapshot {
  generation: number;
  inputStartedAt?: number;
  retainValues?: boolean;
  status: "loading" | "ready" | "failed";
  code?: string;
  inputChanges?: { reset: string[]; removed: string[] };
  environment: EvaluationEnvironment;
}
export type SourceProvenance = {
  status: SourceSnapshot["status"];
  version?: string;
  observedAt?: string;
  coverage?: Coverage;
};
export interface ScopeRuntime {
  getInputs(): Record<string, unknown>;
  getProvenance(): Record<string, SourceProvenance>;
  getSnapshot(): ScopeSnapshot;
  getSource(name: string): SourceSnapshot | undefined;
  subscribe(listener: () => void): () => void;
  setState(name: string, value: Scalar): void;
  setSource(name: string, source: SourceSnapshot): void;
  clearSource(name: string): void;
  updateDefinition(block: Block): void;
  dispose(): void;
}
const owns = (object: object, name: string) =>
  Object.prototype.hasOwnProperty.call(object, name);
// The owner creates this once per document/viewer/scope identity and disposes it
// on removal. There is intentionally no storage, CRDT writer, or product API here.
export function createScopeRuntime(
  block: Block,
  parent?: ScopeRuntime,
  definitions = createScopeDefinitionCache(),
): ScopeRuntime {
  const client = createEvaluationClient();
  const listeners = new Set<() => void>();
  let disposed = false;
  let generation = 0;
  let inputStarted: number | undefined;
  let definition = block;
  let defaults: Record<string, unknown> = {};
  let inputs: Record<string, unknown> = {};
  let computed: Record<string, unknown> = {};
  let plans: ReturnType<typeof definitions.compile>["plans"] = {};
  let descriptors: Record<string, unknown> = {};
  let inputChanges: ScopeSnapshot["inputChanges"];
  const sources = new Map<string, SourceSnapshot>();
  const results = new Map<
    string,
    { expression: string; dependencies: string; value: unknown }
  >();
  let snapshot: ScopeSnapshot = {
    generation,
    status: "loading",
    environment: { state: {}, data: {} },
  };
  const freeze = (value: unknown): void => {
    if (!value || typeof value !== "object" || Object.isFrozen(value)) return;
    for (const child of Object.values(value)) freeze(child);
    Object.freeze(value);
  };
  const publish = (next: ScopeSnapshot) => {
    if (inputChanges) next = { ...next, inputChanges };
    const inheritedStart = parent?.getSnapshot().inputStartedAt;
    const started =
      inputStarted === undefined
        ? inheritedStart
        : inheritedStart === undefined
          ? inputStarted
          : Math.max(inputStarted, inheritedStart);
    if (!disposed && started !== undefined)
      next = { ...next, inputStartedAt: started };
    freeze(next);
    snapshot = next;
    listeners.forEach((listener) => listener());
  };
  const resolve = async (current: number) => {
    const started = uiNow();
    const inherited = parent?.getSnapshot();
    if (inherited && inherited.status !== "ready") {
      results.clear();
      publish({
        generation: current,
        status: inherited.status,
        retainValues: inherited.retainValues,
        code: inherited.code,
        environment: {
          state: { ...inherited.environment.state, ...inputs },
          data: {},
        },
      });
      return;
    }
    const itemBinding = inherited?.environment.hasItem
      ? { item: inherited.environment.item, hasItem: true }
      : {};
    const state = { ...inherited?.environment.state, ...inputs };
    const data = { ...inherited?.environment.data };
    // Shadow ancestor definitions even while a local definition is unavailable.
    for (const name of Object.keys(computed)) delete state[name];
    for (const name of Object.keys(descriptors)) {
      delete data[name];
      const source = sources.get(name);
      if (source?.status === "ready") data[name] = source.value;
      if (source?.status === "empty") data[name] = [];
    }
    publish({
      generation: current,
      status: "loading",
      retainValues: snapshot.retainValues,
      environment: { state: { ...state }, data, ...itemBinding },
    });
    const active = new Set<string>(),
      done = new Set<string>();
    const compute = async (name: string): Promise<void> => {
      if (done.has(name)) return;
      if (active.has(name)) throw new EvaluationError("state.cycle");
      active.add(name);
      const expression = computed[name];
      const dependencies = plans[name].dependencies;
      for (const dependency of dependencies) {
        if (dependency.startsWith("state.")) {
          const target = dependency.slice(6);
          if (owns(computed, target)) await compute(target);
        }
      }
      if (disposed || current !== generation)
        throw new EvaluationError("evaluation.cancelled");
      const values = dependencies.map((dependency) => {
        const [family, name] = dependency.split(".");
        const environment = family === "state" ? state : data;
        if (!owns(environment, name)) {
          const source = sources.get(name);
          throw new EvaluationError(
            family === "data"
              ? `source.${source?.status ?? "loading"}`
              : "state.undeclared",
          );
        }
        return environment[name];
      });
      const expressionKey = plans[name].key,
        dependencyKey = JSON.stringify([
          values,
          plans[name].usesItem ? itemBinding : null,
        ]);
      const cached = results.get(name);
      const value =
        cached?.expression === expressionKey &&
        cached.dependencies === dependencyKey
          ? cached.value
          : await client.evaluate(expression, { state, data, ...itemBinding });
      if (disposed || current !== generation)
        throw new EvaluationError("evaluation.cancelled");
      state[name] = value;
      results.set(name, {
        expression: expressionKey,
        dependencies: dependencyKey,
        value,
      });
      active.delete(name);
      done.add(name);
    };
    try {
      for (const name of Object.keys(computed).sort()) await compute(name);
      checkValue({ state, data, ...itemBinding });
      if (!disposed && current === generation) {
        measureUI("evaluation", started, "ready");
        publish({
          generation: current,
          status: "ready",
          environment: { state, data, ...itemBinding },
        });
      }
    } catch (error) {
      if (!disposed && current === generation) {
        measureUI(
          "evaluation",
          started,
          error instanceof EvaluationError && error.code.startsWith("source.")
            ? "unavailable"
            : "failed",
        );
        publish({
          generation: current,
          status: "failed",
          code:
            error instanceof EvaluationError ? error.code : "evaluation.failed",
          environment: { state: { ...inputs }, data: {} },
        });
      }
    }
  };
  const refresh = (retainValues = false) => {
    if (disposed) return;
    const current = ++generation;
    client.cancel();
    publish({
      generation: current,
      status: "loading",
      retainValues,
      environment: {
        state: { ...parent?.getSnapshot().environment.state, ...inputs },
        data: {},
      },
    });
    // Coalesce synchronous definition/source changes without publishing old work.
    queueMicrotask(() => {
      if (!disposed && current === generation) void resolve(current);
    });
  };
  const updateDefinition = (next: Block) => {
    if (disposed) return;
    const attrs = next.attrs ?? {};
    const compiled = definitions.compile(next);
    const {
      defaults: nextDefaults,
      computed: nextComputed,
      descriptors: nextDescriptors,
    } = compiled;
    const sameScope =
      definition.attrs?.blockId === attrs.blockId &&
      (definition.attrs?.catalogVersion ?? 1) === (attrs.catalogVersion ?? 1);
    const nextInputs: Record<string, unknown> = {};
    const reset: string[] = [];
    const removed = Object.keys(defaults).filter(
      (name) => !owns(nextDefaults, name),
    );
    for (const [name, value] of Object.entries(nextDefaults)) {
      const compatible =
        sameScope &&
        owns(defaults, name) &&
        typeof defaults[name] === typeof value &&
        (defaults[name] === null) === (value === null);
      nextInputs[name] = compatible ? inputs[name] : value;
      if (!compatible && owns(defaults, name)) reset.push(name);
    }
    for (const name of sources.keys()) {
      if (
        !sameScope ||
        JSON.stringify(descriptors[name]) !==
          JSON.stringify(nextDescriptors[name])
      )
        sources.delete(name);
    }
    if (
      !sameScope ||
      JSON.stringify(descriptors) !== JSON.stringify(nextDescriptors)
    )
      results.clear();
    for (const name of results.keys())
      if (!owns(nextComputed, name)) results.delete(name);
    inputChanges =
      reset.length || removed.length ? { reset, removed } : undefined;
    definition = structuredClone(next);
    defaults = structuredClone(nextDefaults);
    inputs = nextInputs;
    computed = nextComputed;
    plans = compiled.plans;
    descriptors = structuredClone(nextDescriptors);
    refresh();
  };
  updateDefinition(block);
  const unsubscribe = parent?.subscribe(() =>
    refresh(
      parent.getSnapshot().status === "ready" ||
        parent.getSnapshot().retainValues === true,
    ),
  );
  return {
    getInputs() {
      if (disposed) return {};
      const selected = { ...parent?.getInputs(), ...inputs };
      for (const name of Object.keys(computed)) delete selected[name];
      return selected;
    },
    getProvenance() {
      if (disposed) return {};
      const result = { ...parent?.getProvenance() };
      for (const name of Object.keys(descriptors)) {
        const source = sources.get(name);
        result[name] =
          source?.status === "ready" || source?.status === "empty"
            ? {
                status: source.status,
                version: source.version,
                observedAt: source.observedAt,
                coverage: source.coverage ? { ...source.coverage } : undefined,
              }
            : { status: source?.status ?? "loading" };
      }
      return result;
    },
    getSnapshot: () => snapshot,
    getSource(name) {
      if (disposed) return undefined;
      if (!owns(descriptors, name)) return parent?.getSource(name);
      const source = sources.get(name);
      return source ? structuredClone(source) : { status: "loading" };
    },
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    updateDefinition,
    setState(name, value) {
      if (disposed) return;
      if (owns(computed, name)) throw new EvaluationError("binding.readonly");
      if (!owns(defaults, name)) {
        if (parent) {
          parent.setState(name, value);
          return;
        }
        throw new EvaluationError("binding.undeclared");
      }
      if (
        !isScopeScalar(value) ||
        typeof value !== typeof defaults[name] ||
        (value === null) !== (defaults[name] === null)
      )
        throw new EvaluationError("binding.type");
      if (Object.is(inputs[name], value)) return;
      inputStarted = uiNow();
      inputs = { ...inputs, [name]: value };
      refresh(true);
    },
    clearSource(name) {
      if (disposed || !owns(descriptors, name)) return;
      sources.delete(name);
      results.clear();
      refresh();
    },
    setSource(name, source) {
      if (disposed) return;
      if (!owns(descriptors, name))
        throw new EvaluationError("source.undeclared");
      if (source.status === "ready") checkValue(source.value);
      sources.set(name, structuredClone(source));
      // Revocation removes both direct snapshots and all derived cached values.
      if (source.status !== "ready" && source.status !== "empty")
        results.clear();
      refresh(source.status === "ready" || source.status === "empty");
    },
    dispose() {
      disposed = true;
      inputStarted = undefined;
      generation++;
      unsubscribe?.();
      client.dispose();
      sources.clear();
      results.clear();
      inputs = {};
      publish({
        generation,
        status: "failed",
        code: "evaluation.disposed",
        environment: { state: {}, data: {} },
      });
      listeners.clear();
    },
  };
}
