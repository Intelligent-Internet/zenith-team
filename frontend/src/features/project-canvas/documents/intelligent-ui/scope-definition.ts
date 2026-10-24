import type { components } from "@/shared/api/generated/api.generated";
import {
  checkValue,
  EvaluationError,
  expressionDependencies,
  expressionUsesItem,
} from "./expression";

type Block = components["schemas"]["CanvasBlock"];
const owns = (object: object, name: string) =>
  Object.prototype.hasOwnProperty.call(object, name);
const object = (value: unknown): Record<string, unknown> => {
  if (!value || typeof value !== "object" || Array.isArray(value))
    throw new EvaluationError("scope.definition");
  return value as Record<string, unknown>;
};
export const isScopeScalar = (
  value: unknown,
): value is string | number | boolean | null =>
  value === null ||
  typeof value === "boolean" ||
  (typeof value === "number" && Number.isFinite(value)) ||
  (typeof value === "string" && new TextEncoder().encode(value).length <= 4096);

function compileScopeDefinition(next: Block) {
  if (next.type !== "uiScope") throw new EvaluationError("scope.definition");
  const attrs = next.attrs ?? {};
  const nextDefaults = object(attrs.initial);
  const nextComputed =
    attrs.computed === undefined ? {} : object(attrs.computed);
  const nextDescriptors =
    attrs.sources === undefined ? {} : object(attrs.sources);
  if (attrs.catalogVersion !== undefined && attrs.catalogVersion !== 1)
    throw new EvaluationError("catalog.version");
  if (
    Object.keys(nextDefaults).length > 64 ||
    Object.keys(nextComputed).length > 64 ||
    Object.keys(nextDescriptors).length > 16
  )
    throw new EvaluationError("scope.limit");
  for (const name of [
    ...Object.keys(nextDefaults),
    ...Object.keys(nextComputed),
    ...Object.keys(nextDescriptors),
  ]) {
    if (
      !/^[A-Za-z_][A-Za-z_0-9]{0,63}$/.test(name) ||
      ["__proto__", "prototype", "constructor"].includes(name)
    )
      throw new EvaluationError("state.definition");
  }
  for (const name of Object.keys(nextComputed))
    if (owns(nextDefaults, name)) throw new EvaluationError("state.duplicate");
  checkValue(nextDefaults);
  for (const value of Object.values(nextDefaults))
    if (!isScopeScalar(value)) throw new EvaluationError("state.definition");
  const plans = Object.fromEntries(
    Object.entries(nextComputed).map(([name, expression]) => [
      name,
      {
        dependencies: expressionDependencies(expression),
        usesItem: expressionUsesItem(expression),
        key: JSON.stringify(expression),
      },
    ]),
  );
  const definition = structuredClone({
    defaults: nextDefaults,
    computed: nextComputed,
    descriptors: nextDescriptors,
    plans,
  });
  const freeze = (value: unknown): void => {
    if (!value || typeof value !== "object" || Object.isFrozen(value)) return;
    for (const child of Object.values(value)) freeze(child);
    Object.freeze(value);
  };
  freeze(definition);
  return definition;
}

// Owned by one editor document. Only authored definitions are retained here;
// viewer inputs, authorized source responses and evaluation results stay outside.
export function createScopeDefinitionCache() {
  const entries = new Map<
    string,
    {
      source: string;
      plan: ReturnType<typeof compileScopeDefinition>;
      bytes: number;
    }
  >();
  let retainedBytes = 0;
  return {
    compile(block: Block) {
      const attrs = Object.fromEntries(
        Object.entries(block.attrs ?? {}).filter(
          ([, value]) => value !== undefined,
        ),
      );
      const source = JSON.stringify(
        { type: block.type, attrs },
        (_key, value: unknown) => {
          if (
            value === undefined ||
            typeof value === "function" ||
            typeof value === "symbol" ||
            typeof value === "bigint"
          )
            throw new EvaluationError("value.json");
          if (typeof value === "number" && !Number.isFinite(value))
            throw new EvaluationError("number.finite");
          return value;
        },
      );
      // A synchronous content hash keeps editor updates synchronous. Exact source
      // comparison below makes collisions cache misses, never stale plan reuse.
      let hash = 2166136261;
      for (let index = 0; index < source.length; index++) {
        hash = Math.imul(hash ^ source.charCodeAt(index), 16777619);
      }
      const key = `schema2:language1:catalog1:${hash >>> 0}`;
      const cached = entries.get(key);
      if (cached?.source === source) {
        entries.delete(key);
        entries.set(key, cached);
        return cached.plan;
      }
      const plan = compileScopeDefinition(block);
      const bytes = new TextEncoder().encode(source).length * 4;
      if (bytes > 8388608) return plan;
      if (cached) {
        entries.delete(key);
        retainedBytes -= cached.bytes;
      }
      while (entries.size >= 128 || retainedBytes + bytes > 8388608) {
        const oldest = entries.keys().next().value;
        if (oldest === undefined) break;
        retainedBytes -= entries.get(oldest)!.bytes;
        entries.delete(oldest);
      }
      entries.set(key, { source, plan, bytes });
      retainedBytes += bytes;
      return plan;
    },
    clear() {
      entries.clear();
      retainedBytes = 0;
    },
  };
}
