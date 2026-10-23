import type { Node } from "acorn";

// Generated expressions are interpreted as data. No browser/global objects or
// JavaScript Function/eval enter this environment.
type AST = Node & Record<string, any>;
export type Environment = Record<string, unknown>;
export type Callable = { invoke: (args: unknown[]) => unknown; pure?: boolean };
export type RuntimeExecution = {
  remaining: number;
  depth: number;
  event: boolean;
  hostActions: boolean;
  pure?: boolean;
  deadline?: number;
};
export type RuntimeHost = {
  execution?: RuntimeExecution;
  state: Map<string, unknown>;
  refs: Map<string, { current: unknown }>;
  memo: Map<string, { value: unknown; dependencies: unknown[] }>;
  change: (key: string, value: unknown) => void;
  action: (name: string, args: unknown[]) => unknown;
  theme: string;
  width: number;
  diagnostic?: (error: unknown, start: number) => void;
  effects: Array<{
    key: string;
    callback: Callable;
    dependencies: unknown[] | undefined;
  }>;
};
const callable = (invoke: Callable["invoke"]): Callable => ({ invoke });
export const isCallable = (value: unknown): value is Callable =>
  !!value &&
  typeof value === "object" &&
  typeof (value as Callable).invoke === "function";
const safeKey = (value: unknown) => {
  const key = String(value);
  if (["__proto__", "constructor", "prototype", "invoke"].includes(key))
    throw new Error("This property is unavailable.");
  return key;
};
export function valueText(value: unknown): string {
  if (value == null) return "";
  if (value instanceof Date)
    return Number.isNaN(value.getTime()) ? "Invalid Date" : value.toISOString();
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}
export function checkLocalValue(value: unknown) {
  let count = 0;
  const visit = (value: unknown, depth: number) => {
    if (++count > 20000 || depth > 32)
      throw new Error("Value exceeds the local UI budget.");
    if (value == null || typeof value === "boolean") return;
    if (typeof value === "number" && Number.isFinite(value)) return;
    if (typeof value === "string" && value.length <= 65536) return;
    if (Array.isArray(value) && value.length <= 1000) {
      value.forEach((v) => visit(v, depth + 1));
      return;
    }
    if (typeof value === "object" && !isCallable(value)) {
      for (const [key, child] of Object.entries(value)) {
        safeKey(key);
        visit(child, depth + 1);
      }
      return;
    }
    throw new Error("UI state must contain bounded JSON values.");
  };
  visit(value, 0);
}
const text = (value: unknown) => {
  const result = String(value ?? "");
  if (result.length > 65536) throw new Error("Text exceeds the UI budget.");
  return result;
};
class LoopControl {
  constructor(readonly type: "break" | "continue") {}
}

function freezeLocal(value: unknown): unknown {
  if (value && typeof value === "object" && !Object.isFrozen(value)) {
    Object.values(value).forEach(freezeLocal);
    Object.freeze(value);
  }
  return value;
}

class ReturnValue {
  constructor(readonly value: unknown) {}
}
export const interactionBudget = 250000;

export class Interpreter {
  hookScope = "";
  hookBinding = "";
  module = false;
  readonly execution: RuntimeExecution;
  constructor(readonly host: RuntimeHost) {
    this.execution = host.execution ??= {
      remaining: 50000,
      depth: 0,
      event: false,
      hostActions: false,
    };
  }
  get remaining() {
    return this.execution.remaining;
  }
  set remaining(value: number) {
    this.execution.remaining = value;
  }
  get depth() {
    return this.execution.depth;
  }
  set depth(value: number) {
    this.execution.depth = value;
  }
  get event() {
    return this.execution.event;
  }
  set event(value: boolean) {
    this.execution.event = value;
  }
  get hostActions() {
    return this.execution.hostActions;
  }
  set hostActions(value: boolean) {
    this.execution.hostActions = value;
  }
  tick() {
    if (
      --this.remaining < 0 ||
      (this.remaining % 256 === 0 &&
        this.execution.deadline !== undefined &&
        performance.now() > this.execution.deadline)
    )
      throw new Error("UI computation exceeded its work budget.");
  }
  call(value: unknown, args: unknown[], name?: string) {
    this.tick();
    if (!isCallable(value))
      throw new Error(
        name
          ? `${name}() is not a supported UI function.`
          : "Only supported UI functions can be called.",
      );
    if (++this.depth > 32) {
      this.depth--;
      throw new Error("UI call depth exceeded.");
    }
    const previous = this.execution.pure;
    this.execution.pure = previous || value.pure || this.module;
    try {
      return value.invoke(args);
    } finally {
      this.execution.pure = previous;
      this.depth--;
    }
  }
  lookup(env: Environment, name: string): unknown {
    safeKey(name);
    if (name in env) return env[name];
    // Model-data refs (turn0product0, turn1search2, ...) are a deferred
    // integration: say so instead of reporting an authoring typo.
    if (/^turn\d+[a-z]+\d+$/.test(name))
      throw new Error(
        `Model-data binding ${name} is not connected in Zenith yet.`,
      );
    throw new Error(`Unknown UI variable: ${name}.`);
  }
  environment(): Environment {
    const env: Environment = Object.create(null);
    const numeric = (fn: (...args: number[]) => number) =>
      callable((args) => fn(...args.map(Number)));
    env.Math = Object.assign(
      Object.create(null),
      Object.fromEntries(
        (
          [
            "min",
            "max",
            "round",
            "floor",
            "ceil",
            "trunc",
            "sign",
            "abs",
            "sqrt",
            "cbrt",
            "pow",
            "hypot",
            "exp",
            "log",
            "log2",
            "log10",
            "sin",
            "cos",
            "tan",
            "asin",
            "acos",
            "atan",
            "atan2",
          ] as const
        ).map((name) => [
          name,
          numeric(Math[name] as (...args: number[]) => number),
        ]),
      ),
      { PI: Math.PI, E: Math.E },
    );
    // Pure deterministic helpers only. Current time and randomness stay
    // unavailable so renders, captures and streaming previews are reproducible.
    const parseInteger = callable((args) =>
      Number.parseInt(
        String(args[0]),
        args[1] === undefined ? undefined : Number(args[1]),
      ),
    );
    const parseDecimal = callable((args) => Number.parseFloat(String(args[0])));
    env.String = Object.assign(
      callable((args) => text(args[0])),
      {
        fromCharCode: callable((args) => {
          if (args.length > 1000)
            throw new Error("Collection exceeds 1000 items.");
          return String.fromCharCode(...args.map(Number));
        }),
      },
    );
    env.Number = Object.assign(
      callable((args) => Number(args[0])),
      {
        isFinite: callable((args) => Number.isFinite(args[0])),
        isInteger: callable((args) => Number.isInteger(args[0])),
        isNaN: callable((args) => Number.isNaN(args[0])),
        parseInt: parseInteger,
        parseFloat: parseDecimal,
        MAX_SAFE_INTEGER: Number.MAX_SAFE_INTEGER,
        MIN_SAFE_INTEGER: Number.MIN_SAFE_INTEGER,
        EPSILON: Number.EPSILON,
      },
    );
    env.parseInt = parseInteger;
    env.parseFloat = parseDecimal;
    env.isNaN = callable((args) => Number.isNaN(Number(args[0])));
    env.isFinite = callable((args) => Number.isFinite(Number(args[0])));
    env.Boolean = callable((args) => Boolean(args[0]));
    const bounded = (length: unknown) => {
      const size = Number(length);
      if (!Number.isInteger(size) || size < 0 || size > 1000)
        throw new Error("Collection exceeds 1000 items.");
      return size;
    };
    env.Array = Object.assign(
      callable((args) =>
        args.length === 1 && typeof args[0] === "number"
          ? Array.from({ length: bounded(args[0]) }, () => undefined)
          : [...args],
      ),
      {
        isArray: callable((args) => Array.isArray(args[0])),
        of: callable((args) => [...args]),
        from: callable(([source, map]) => {
          const values =
            typeof source === "string" || Array.isArray(source)
              ? [...source]
              : source && typeof source === "object" && "length" in source
                ? Array.from(
                    { length: bounded((source as { length: unknown }).length) },
                    () => undefined,
                  )
                : [];
          bounded(values.length);
          return map === undefined
            ? values
            : values.map((value, index) => this.call(map, [value, index]));
        }),
      },
    );
    env.Date = Object.assign(Object.create(null), {
      UTC: numeric(Date.UTC),
      parse: callable((args) => Date.parse(String(args[0]))),
    });
    env.JSON = {
      stringify: callable((args) => {
        checkLocalValue(args[0]);
        return JSON.stringify(args[0]);
      }),
      parse: callable((args) => {
        const value = JSON.parse(String(args[0]));
        checkLocalValue(value);
        return value;
      }),
    };
    env.Object = {
      keys: callable((args) => Object.keys(args[0] as object)),
      values: callable((args) => Object.values(args[0] as object)),
      entries: callable((args) => Object.entries(args[0] as object)),
      fromEntries: callable(([entries]) => {
        if (!Array.isArray(entries))
          throw new Error("Object.fromEntries requires an array of pairs.");
        bounded(entries.length);
        const value = Object.create(null);
        for (const entry of entries) {
          if (!Array.isArray(entry))
            throw new Error("Object.fromEntries requires [key, value] pairs.");
          value[safeKey(entry[0])] = entry[1];
        }
        return value;
      }),
      // Copies into a new object; authored code cannot mutate state through it.
      assign: callable((args) => {
        const value = Object.create(null);
        for (const source of args) {
          if (source == null) continue;
          checkLocalValue(source);
          for (const [key, item] of Object.entries(source as object))
            value[safeKey(key)] = item;
        }
        return value;
      }),
    };
    env.undefined = undefined;
    env.GenUI = Object.fromEntries(
      ["copy", "openUrl", "issueNewTurn", "openEntityDetail"].map((name) => [
        name,
        callable((args) => {
          if (!this.hostActions || this.execution.pure)
            throw new Error("Host actions require a user interaction.");
          args.forEach(checkLocalValue);
          return this.host.action(name, args);
        }),
      ]),
    );
    Object.values(env).forEach(freezeLocal);
    return Object.create(Object.freeze(env));
  }
  property(object: unknown, rawKey: unknown): unknown {
    const key = safeKey(rawKey);
    if (object == null)
      throw new Error(`Cannot read ${key} from an empty value.`);
    if (Array.isArray(object)) {
      if (object.length > 1000)
        throw new Error("Collection exceeds 1000 items.");
      if (key === "length") return object.length;
      if (["push", "pop", "shift", "unshift"].includes(key))
        return callable((args) => {
          if (this.execution.pure || this.module || Object.isFrozen(object))
            throw new Error("Only local mutable arrays can be changed.");
          if (object.length + args.length > 1000)
            throw new Error("Collection exceeds 1000 items.");
          args.forEach(checkLocalValue);
          return (object as any)[key](...args);
        });
      if (
        [
          "map",
          "filter",
          "find",
          "findIndex",
          "findLast",
          "findLastIndex",
          "some",
          "every",
          "forEach",
          "flatMap",
        ].includes(key)
      )
        return callable(([fn]) => {
          const result = (object as any)[key]((value: unknown, index: number) =>
            this.call(fn, [value, index]),
          );
          if (Array.isArray(result) && result.length > 1000)
            throw new Error("Collection exceeds 1000 items.");
          return result;
        });
      if (key === "concat")
        return callable((args) => {
          const size = args.reduce<number>(
            (total, item) => total + (Array.isArray(item) ? item.length : 1),
            object.length,
          );
          if (size > 1000) throw new Error("Collection exceeds 1000 items.");
          return object.concat(...args);
        });
      if (key === "flat")
        return callable((args) => {
          const result = object.flat(
            Math.max(0, Math.min(32, Number(args[0] ?? 1))),
          );
          if (result.length > 1000)
            throw new Error("Collection exceeds 1000 items.");
          return result;
        });
      if (key === "at") return callable((args) => object.at(Number(args[0])));
      if (key === "lastIndexOf")
        return callable((args) => object.lastIndexOf(args[0]));
      // Reordering returns a copy, matching sort; state arrays stay immutable.
      if (key === "reverse" || key === "toReversed")
        return callable(() => [...object].reverse());
      if (key === "fill")
        return callable((args) => {
          if (this.execution.pure || this.module || Object.isFrozen(object))
            throw new Error("Only local mutable arrays can be changed.");
          checkLocalValue(args[0]);
          return object.fill(
            args[0],
            args[1] === undefined ? undefined : Number(args[1]),
            args[2] === undefined ? undefined : Number(args[2]),
          );
        });
      if (key === "reduce")
        return callable(([fn, initial]) =>
          object.reduce(
            (result, value, index) => this.call(fn, [result, value, index]),
            initial,
          ),
        );
      if (key === "slice")
        return callable((args) =>
          object.slice(
            Number(args[0]),
            args[1] === undefined ? undefined : Number(args[1]),
          ),
        );
      if (key === "join")
        return callable((args) => {
          const parts = object.map(valueText),
            separator = String(args[0] ?? ",");
          if (
            parts.reduce(
              (size, part) => size + part.length + separator.length,
              0,
            ) > 65536
          )
            throw new Error("Text exceeds the UI budget.");
          return parts.join(separator);
        });
      if (key === "includes")
        return callable((args) => object.includes(args[0]));
      if (key === "indexOf") return callable((args) => object.indexOf(args[0]));
      if (key === "sort" || key === "toSorted")
        return callable(([fn]) =>
          [...object].sort(
            fn ? (a, b) => Number(this.call(fn, [a, b])) : undefined,
          ),
        );
    }
    if (typeof object === "string") {
      if (key === "length") return object.length;
      if (["toUpperCase", "toLowerCase", "trim"].includes(key))
        return callable(() => (object as any)[key]());
      if (["includes", "startsWith", "endsWith"].includes(key))
        return callable((args) => (object as any)[key](String(args[0])));
      if (key === "slice")
        return callable((args) =>
          object.slice(
            Number(args[0]),
            args[1] === undefined ? undefined : Number(args[1]),
          ),
        );
      if (key === "split")
        return callable((args) => object.split(String(args[0]), 1000));
      if (["trimStart", "trimEnd", "toString"].includes(key))
        return callable(() => (object as any)[key]());
      if (["indexOf", "lastIndexOf"].includes(key))
        return callable((args) =>
          (object as any)[key](
            String(args[0]),
            args[1] === undefined ? undefined : Number(args[1]),
          ),
        );
      if (["charAt", "charCodeAt", "codePointAt", "at"].includes(key))
        return callable((args) => (object as any)[key](Number(args[0] ?? 0)));
      if (key === "substring")
        return callable((args) =>
          object.substring(
            Number(args[0]),
            args[1] === undefined ? undefined : Number(args[1]),
          ),
        );
      if (key === "padStart" || key === "padEnd")
        return callable((args) => {
          const length = Number(args[0]);
          if (!(length <= 65536))
            throw new Error("Text exceeds the UI budget.");
          return object[key](
            length,
            args[1] === undefined ? undefined : String(args[1]),
          );
        });
      if (key === "repeat")
        return callable((args) => {
          const count = Number(args[0]);
          if (!(count >= 0) || object.length * count > 65536)
            throw new Error("Text exceeds the UI budget.");
          return object.repeat(count);
        });
      // String patterns only: regular expressions and replacer callbacks are
      // unavailable, so replacement cost stays linear and predictable.
      if (key === "replace" || key === "replaceAll")
        return callable((args) => {
          if (typeof args[0] !== "string" || isCallable(args[1]))
            throw new Error(
              `${key} requires text search and replacement values.`,
            );
          const result = object[key](args[0], () => String(args[1] ?? ""));
          if (result.length > 65536)
            throw new Error("Text exceeds the UI budget.");
          return result;
        });
      if (key === "concat")
        return callable((args) =>
          text([object, ...args.map(valueText)].join("")),
        );
      if (key === "localeCompare")
        return callable((args) => object.localeCompare(String(args[0])));
    }
    if (object instanceof Date) {
      if (
        [
          "getTime",
          "valueOf",
          "getFullYear",
          "getMonth",
          "getDate",
          "getDay",
          "getHours",
          "getMinutes",
          "getSeconds",
          "getUTCFullYear",
          "getUTCMonth",
          "getUTCDate",
          "getUTCDay",
          "getUTCHours",
          "getUTCMinutes",
          "getUTCSeconds",
          "getTimezoneOffset",
          "toISOString",
          "toJSON",
        ].includes(key)
      )
        return callable(() => (object as any)[key]());
      if (
        ["toLocaleDateString", "toLocaleTimeString", "toLocaleString"].includes(
          key,
        )
      )
        return callable((args) =>
          (object as any)[key](
            typeof args[0] === "string" ? args[0] : undefined,
            args[1] && typeof args[1] === "object"
              ? (args[1] as Intl.DateTimeFormatOptions)
              : undefined,
          ),
        );
      return undefined;
    }
    if (typeof object === "number" && ["toString", "toPrecision"].includes(key))
      return callable((args) =>
        key === "toString"
          ? object.toString(
              args[0] === undefined
                ? undefined
                : Math.max(2, Math.min(36, Number(args[0]))),
            )
          : object.toPrecision(
              args[0] === undefined
                ? undefined
                : Math.max(1, Math.min(100, Number(args[0]))),
            ),
      );
    if (
      typeof object === "number" &&
      ["toLocaleString", "toFixed"].includes(key)
    )
      return callable((args) =>
        key === "toFixed"
          ? object.toFixed(Math.max(0, Math.min(20, Number(args[0] ?? 0))))
          : object.toLocaleString(
              typeof args[0] === "string" ? args[0] : undefined,
              args[1] && typeof args[1] === "object"
                ? (args[1] as Intl.NumberFormatOptions)
                : undefined,
            ),
      );
    if (
      typeof object === "object" &&
      Object.prototype.hasOwnProperty.call(object, key)
    )
      return (object as Environment)[key];
    if (
      (typeof object === "string" || Array.isArray(object)) &&
      Number.isInteger(Number(key))
    )
      return (object as any)[Number(key)];
    return undefined;
  }
  hook(name: string, node: AST, env: Environment): unknown {
    const args = node.arguments.map((arg: AST) => this.evaluate(arg, env));
    // Hook state lives in keyed slots so it survives recompiles while the
    // response streams: an explicit { key } wins, then the declared binding
    // name, then the source position.
    const options =
      name === "useState" || name === "useRef" ? args[1] : undefined;
    const explicit =
      options && typeof options === "object" && !Array.isArray(options)
        ? (options as { key?: unknown }).key
        : undefined;
    if (
      explicit !== undefined &&
      (typeof explicit !== "string" || !/^[\w.-]{1,64}$/.test(explicit))
    )
      throw new Error("Hook keys must be short identifiers.");
    const key = `${this.hookScope}:${explicit ? `key=${explicit}` : this.hookBinding || node.start}:${name}`;
    switch (name) {
      case "useState": {
        if (!this.host.state.has(key)) {
          const initial = isCallable(args[0])
            ? this.call(args[0], [])
            : args[0];
          checkLocalValue(initial);
          this.host.state.set(key, initial);
        }
        return [
          freezeLocal(this.host.state.get(key)),
          callable(([next]) => {
            if (!this.event || this.execution.pure)
              throw new Error(
                "State changes require an impure callback outside module helpers.",
              );
            const value = isCallable(next)
              ? this.call(next, [this.host.state.get(key)])
              : next;
            checkLocalValue(value);
            this.host.change(key, value);
          }),
        ];
      }
      case "useRef":
        if (!this.host.refs.has(key))
          this.host.refs.set(key, { current: args[0] });
        return this.host.refs.get(key);
      case "useId":
        return `inline-${key.replaceAll(":", "-")}`;
      case "useMemo":
      case "useCallback": {
        const dependencies = args[1];
        if (dependencies !== undefined && !Array.isArray(dependencies))
          throw new Error("Hook dependencies must be an array.");
        const cached = this.host.memo.get(key);
        if (
          Array.isArray(dependencies) &&
          cached &&
          sameDependencies(cached.dependencies, dependencies)
        )
          return cached.value;
        const value = name === "useMemo" ? this.call(args[0], []) : args[0];
        if (Array.isArray(dependencies))
          this.host.memo.set(key, { value, dependencies });
        return value;
      }
      case "useTheme":
        return this.host.theme;
      case "useBreakpoint":
        return this.host.width < 640
          ? "sm"
          : this.host.width < 1024
            ? "md"
            : "lg";
      case "useEffect":
        if (!isCallable(args[0]))
          throw new Error("Effect requires a function.");
        if (args[1] !== undefined && !Array.isArray(args[1]))
          throw new Error("Effect dependencies must be an array.");
        this.host.effects.push({
          key,
          callback: args[0],
          dependencies: args[1] as unknown[] | undefined,
        });
        return undefined;
      default:
        throw new Error(`Unsupported DIL hook: ${name}.`);
    }
  }
  bind(pattern: AST, value: unknown, env: Environment): void {
    if (pattern.type === "Identifier") {
      env[safeKey(pattern.name)] = value;
      return;
    }
    if (pattern.type === "ArrayPattern" && Array.isArray(value)) {
      pattern.elements.forEach((part: AST | null, index: number) => {
        if (part) this.bind(part, value[index], env);
      });
      return;
    }
    if (
      pattern.type === "ObjectPattern" &&
      value &&
      typeof value === "object"
    ) {
      for (const prop of pattern.properties)
        this.bind(
          prop.value,
          this.property(value, prop.key.name ?? prop.key.value),
          env,
        );
      return;
    }
    if (pattern.type === "AssignmentPattern") {
      this.bind(
        pattern.left,
        value === undefined ? this.evaluate(pattern.right, env) : value,
        env,
      );
      return;
    }
    throw new Error("Unsupported declaration binding.");
  }
  evaluate(raw: Node, env: Environment): any {
    this.tick();
    const node = raw as AST;
    switch (node.type) {
      case "Literal":
        if (node.regex || node.bigint)
          throw new Error("Regular expressions and BigInt are unsupported.");
        if (typeof node.value === "string" && node.value.length > 65536)
          throw new Error("Text exceeds the UI budget.");
        return node.value;
      case "Identifier":
        return this.lookup(env, node.name);
      case "ArrayExpression": {
        const values: unknown[] = [];
        for (const item of node.elements) {
          if (item?.type === "SpreadElement") {
            const value = this.evaluate(item.argument, env);
            if (!Array.isArray(value))
              throw new Error("Spread requires an array.");
            values.push(...value);
          } else values.push(item ? this.evaluate(item, env) : null);
          if (values.length > 1000)
            throw new Error("Collection exceeds 1000 items.");
        }
        return values;
      }
      case "ObjectExpression": {
        const value = Object.create(null);
        for (const prop of node.properties) {
          if (prop.type === "SpreadElement") {
            const spread = this.evaluate(prop.argument, env);
            checkLocalValue(spread);
            Object.assign(value, spread);
          } else {
            if (prop.kind !== "init")
              throw new Error("Getters and setters are unsupported.");
            value[
              safeKey(
                prop.computed
                  ? this.evaluate(prop.key, env)
                  : (prop.key.name ?? prop.key.value),
              )
            ] = this.evaluate(prop.value, env);
          }
        }
        return value;
      }
      case "MemberExpression": {
        const value = this.evaluate(node.object, env);
        if (node.optional && value == null) return undefined;
        return this.property(
          value,
          node.computed
            ? this.evaluate(node.property, env)
            : node.property.name,
        );
      }
      case "ChainExpression":
        return this.evaluate(node.expression, env);
      case "ConditionalExpression":
        return this.evaluate(
          this.evaluate(node.test, env) ? node.consequent : node.alternate,
          env,
        );
      case "LogicalExpression": {
        const left = this.evaluate(node.left, env);
        if (node.operator === "&&")
          return left && this.evaluate(node.right, env);
        if (node.operator === "||")
          return left || this.evaluate(node.right, env);
        if (node.operator === "??")
          return left ?? this.evaluate(node.right, env);
        break;
      }
      case "UnaryExpression": {
        const value = this.evaluate(node.argument, env);
        if (node.operator === "!") return !value;
        if (node.operator === "-") return -Number(value);
        if (node.operator === "+") return Number(value);
        if (node.operator === "typeof") return typeof value;
        break;
      }
      case "BinaryExpression": {
        const a = this.evaluate(node.left, env),
          b = this.evaluate(node.right, env);
        switch (node.operator) {
          case "+":
            if (
              (typeof a === "string" || typeof b === "string") &&
              String(a).length + String(b).length > 65536
            )
              throw new Error("Text exceeds the UI budget.");
            return a + b;
          case "-":
            return a - b;
          case "*":
            return a * b;
          case "/":
            return a / b;
          case "%":
            return a % b;
          case "**":
            return a ** b;
          case "===":
          case "==":
            return a === b;
          case "!==":
          case "!=":
            return a !== b;
          case "<":
            return a < b;
          case "<=":
            return a <= b;
          case ">":
            return a > b;
          case ">=":
            return a >= b;
        }
        break;
      }
      case "TemplateLiteral":
      case "TaggedTemplateExpression": {
        const raw = node.type === "TaggedTemplateExpression";
        if (
          raw &&
          (node.tag.type !== "MemberExpression" ||
            node.tag.object.name !== "String" ||
            node.tag.property.name !== "raw")
        )
          break;
        const template = raw ? node.quasi : node;
        let result = "";
        for (let i = 0; i < template.quasis.length; i++) {
          result += raw
            ? template.quasis[i].value.raw
            : (template.quasis[i].value.cooked ?? template.quasis[i].value.raw);
          if (template.expressions[i])
            result += valueText(this.evaluate(template.expressions[i], env));
          if (result.length > 65536)
            throw new Error("Text exceeds the UI budget.");
        }
        return result;
      }
      case "ArrowFunctionExpression":
      case "FunctionExpression":
        return this.function(node, env);
      case "CallExpression":
        if (
          node.callee.type === "MemberExpression" &&
          node.callee.object.name === "DIL"
        ) {
          if (this.event)
            throw new Error("Hooks must be declared at render time.");
          return this.hook(node.callee.property.name, node, env);
        }
        return this.call(
          this.evaluate(node.callee, env),
          this.arguments(node.arguments, env),
          calleeName(node.callee),
        );
      case "NewExpression": {
        const name = calleeName(node.callee);
        const args = this.arguments(node.arguments, env);
        // Only deterministic constructors: a Date needs an explicit instant.
        if (name === "Date") {
          if (!args.length)
            throw new Error(
              "new Date() needs an explicit value; the current time is unavailable.",
            );
          if (
            args.length > 7 ||
            !args.every(
              (arg) =>
                ["number", "string"].includes(typeof arg) ||
                arg instanceof Date,
            )
          )
            throw new Error(
              "new Date() requires numbers, an ISO string or a Date.",
            );
          return new Date(
            ...(args.map((arg) =>
              arg instanceof Date ? arg.getTime() : arg,
            ) as [number]),
          );
        }
        if (name === "Array")
          return this.call(this.lookup(env, "Array"), args, "Array");
        throw new Error(
          `new ${name ?? "expression"}() is not supported; use arrays, objects and the documented helpers.`,
        );
      }
      case "AssignmentExpression": {
        const right = this.evaluate(node.right, env);
        const previous =
          node.operator === "=" ? undefined : this.evaluate(node.left, env);
        const value =
          node.operator === "="
            ? right
            : node.operator === "+="
              ? previous + right
              : node.operator === "-="
                ? previous - right
                : node.operator === "*="
                  ? previous * right
                  : node.operator === "/="
                    ? previous / right
                    : undefined;
        if (!["=", "+=", "-=", "*=", "/="].includes(node.operator))
          throw new Error("Unsupported assignment operator.");
        this.assign(node.left, value, env);
        return value;
      }
      case "UpdateExpression": {
        const previous = Number(this.evaluate(node.argument, env));
        const value = previous + (node.operator === "++" ? 1 : -1);
        this.assign(node.argument, value, env);
        return node.prefix ? value : previous;
      }
      case "SequenceExpression": {
        let value;
        for (const expression of node.expressions)
          value = this.evaluate(expression, env);
        return value;
      }
    }
    throw new Error(`Unsupported UI expression: ${node.type}.`);
  }
  arguments(nodes: AST[], env: Environment): unknown[] {
    const values: unknown[] = [];
    for (const node of nodes) {
      if (node.type === "SpreadElement") {
        const value = this.evaluate(node.argument, env);
        if (!Array.isArray(value) && typeof value !== "string")
          throw new Error("Spread requires an array.");
        values.push(...value);
      } else values.push(this.evaluate(node, env));
      if (values.length > 1000)
        throw new Error("Collection exceeds 1000 items.");
    }
    return values;
  }
  assign(target: AST, value: unknown, env: Environment) {
    if (this.execution.pure || this.module)
      throw new Error("Module helpers cannot mutate local values.");
    checkLocalValue(value);
    if (target.type === "Identifier") {
      const key = safeKey(target.name);
      let owner = env;
      while (owner && !Object.hasOwn(owner, key))
        owner = Object.getPrototypeOf(owner);
      if (!owner || Object.isFrozen(owner))
        throw new Error("Only declared local variables can be assigned.");
      owner[key] = value;
      return;
    }
    if (target.type === "MemberExpression") {
      const object = this.evaluate(target.object, env);
      const key = safeKey(
        target.computed
          ? this.evaluate(target.property, env)
          : target.property.name,
      );
      if (
        !object ||
        typeof object !== "object" ||
        isCallable(object) ||
        Object.isFrozen(object)
      )
        throw new Error("Only local mutable values can be assigned.");
      if (Array.isArray(object) && (!/^\d+$/.test(key) || Number(key) >= 1000))
        throw new Error("Array assignment requires a bounded index.");
      const had = Object.hasOwn(object, key),
        previous = object[key];
      object[key] = value;
      try {
        checkLocalValue(object);
      } catch (error) {
        if (had) object[key] = previous;
        else delete object[key];
        throw error;
      }
      return;
    }
    throw new Error("Unsupported assignment target.");
  }
  // Only top-level initializers recover independently. Callback errors propagate
  // to the interaction boundary so a partially executed action is never hidden.
  declarations(raw: Node, env: Environment) {
    const program = raw as AST;
    for (const statement of program.body) {
      const parts =
        statement.type === "VariableDeclaration"
          ? statement.declarations.map((declaration: AST) => ({
              ...statement,
              declarations: [declaration],
            }))
          : [statement];
      for (const part of parts) {
        try {
          this.program(part, env);
        } catch (error) {
          if (!this.host.diagnostic || this.remaining < 0) throw error;
          this.host.diagnostic(error, part.start);
        }
      }
    }
  }
  function(node: AST, env: Environment): Callable {
    const fn = callable((args) => {
      const local = Object.create(env);
      node.params.forEach((param: AST, index: number) =>
        this.bind(param, args[index], local),
      );
      if (node.body.type !== "BlockStatement")
        return this.evaluate(node.body, local);
      try {
        this.program(node.body, local);
      } catch (error) {
        if (error instanceof ReturnValue) return error.value;
        throw error;
      }
      return undefined;
    });
    fn.pure = this.module || this.execution.pure;
    return fn;
  }
  program(raw: Node, env: Environment): void {
    const node = raw as AST;
    this.tick();
    switch (node.type) {
      case "Program":
      case "BlockStatement":
        for (const statement of node.body) this.program(statement, env);
        return;
      case "VariableDeclaration":
        for (const declaration of node.declarations) {
          const previous = this.hookBinding;
          this.hookBinding =
            declaration.id.type === "Identifier"
              ? declaration.id.name
              : (declaration.id.elements?.[0]?.name ?? "");
          try {
            this.bind(
              declaration.id,
              declaration.init
                ? this.evaluate(declaration.init, env)
                : undefined,
              env,
            );
          } finally {
            this.hookBinding = previous;
          }
        }
        return;
      case "FunctionDeclaration":
        env[safeKey(node.id.name)] = this.function(node, env);
        return;
      case "ExpressionStatement":
        this.evaluate(node.expression, env);
        return;
      case "ReturnStatement":
        throw new ReturnValue(
          node.argument ? this.evaluate(node.argument, env) : undefined,
        );
      case "IfStatement":
        if (this.evaluate(node.test, env)) this.program(node.consequent, env);
        else if (node.alternate) this.program(node.alternate, env);
        return;
      case "ForStatement": {
        const local = Object.create(env);
        if (node.init)
          node.init.type === "VariableDeclaration"
            ? this.program(node.init, local)
            : this.evaluate(node.init, local);
        while (!node.test || this.evaluate(node.test, local)) {
          this.tick();
          try {
            this.program(node.body, local);
          } catch (error) {
            if (!(error instanceof LoopControl)) throw error;
            if (error.type === "break") break;
          }
          if (node.update) this.evaluate(node.update, local);
        }
        return;
      }
      case "ForOfStatement": {
        const values = this.evaluate(node.right, env);
        if (!Array.isArray(values) && typeof values !== "string")
          throw new Error("For-of requires an array or string.");
        if (values.length > 1000)
          throw new Error("Collection exceeds 1000 items.");
        for (const value of values) {
          this.tick();
          const local = Object.create(env);
          if (node.left.type === "VariableDeclaration")
            this.bind(node.left.declarations[0].id, value, local);
          else this.assign(node.left, value, local);
          try {
            this.program(node.body, local);
          } catch (error) {
            if (!(error instanceof LoopControl)) throw error;
            if (error.type === "break") break;
          }
        }
        return;
      }
      case "WhileStatement":
      case "DoWhileStatement": {
        let first = node.type === "DoWhileStatement";
        while (first || this.evaluate(node.test, env)) {
          first = false;
          this.tick();
          try {
            this.program(node.body, env);
          } catch (error) {
            if (!(error instanceof LoopControl)) throw error;
            if (error.type === "break") break;
          }
        }
        return;
      }
      case "BreakStatement":
        throw new LoopControl("break");
      case "ContinueStatement":
        throw new LoopControl("continue");
      case "EmptyStatement":
        return;
      default:
        throw new Error(`Unsupported UI statement: ${node.type}.`);
    }
  }
}

// Readable callee for diagnostics, e.g. "items.findIndex" or "Math.random".
function calleeName(node: AST): string | undefined {
  if (node.type === "Identifier") return node.name;
  if (node.type === "MemberExpression" && !node.computed) {
    const object = calleeName(node.object);
    return object
      ? `${object}.${node.property.name}`
      : `.${node.property.name}`;
  }
  if (node.type === "MemberExpression" && node.object)
    return calleeName(node.object);
}

export function sameDependencies(a: unknown[], b: unknown[]) {
  return (
    a.length === b.length &&
    a.every((value, index) => Object.is(value, b[index]))
  );
}
