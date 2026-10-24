import {
  compileExpression,
  EvaluationError,
  type EvaluationEnvironment,
} from "./expression";

type CompiledExpression = (environment: EvaluationEnvironment) => unknown;
type Entry = { source: string; evaluate: CompiledExpression; bytes: number };

// Worker-local and reconstructible. Neither environments nor evaluated results
// enter this cache. A worker's retirement discards every retained definition.
export function createCompiledExpressionCache() {
  const entries = new Map<string, Entry>();
  let retainedBytes = 0;
  return {
    async compile(expression: unknown): Promise<CompiledExpression> {
      const source = JSON.stringify(expression, (_key, value: unknown) => {
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
      });
      if (typeof source !== "string")
        throw new EvaluationError("expression.object");
      const encoded = new TextEncoder().encode(source);
      if (encoded.length > 2097152)
        throw new EvaluationError("expression.limit");
      const digest = await crypto.subtle.digest("SHA-256", encoded);
      const hash = Array.from(new Uint8Array(digest), (byte) =>
        byte.toString(16).padStart(2, "0"),
      ).join("");
      // This interpreter implements only document schema 2, language 1 and
      // catalog 1. A new interpreter must use a distinct version namespace.
      const key = `schema2:language1:catalog1:${hash}`;
      const cached = entries.get(key);
      if (cached?.source === source) {
        entries.delete(key);
        entries.set(key, cached);
        return cached.evaluate;
      }
      // Compile the exact bytes hashed above, even if the caller changed its
      // original object while Web Crypto was working.
      const evaluate = compileExpression(JSON.parse(source));
      const bytes = encoded.length;
      // Account for the retained source and owned tree with a conservative
      // source-byte multiplier, and separately bound the number of entries.
      while (entries.size >= 128 || retainedBytes + bytes * 4 > 8388608) {
        const oldest = entries.keys().next().value;
        if (oldest === undefined) break;
        retainedBytes -= entries.get(oldest)!.bytes * 4;
        entries.delete(oldest);
      }
      const replaced = entries.get(key);
      if (replaced) retainedBytes -= replaced.bytes * 4;
      entries.set(key, { source, evaluate, bytes });
      retainedBytes += bytes * 4;
      return evaluate;
    },
  };
}
