import type { Node } from "acorn";

// Generated expressions are interpreted as data. No browser/global objects or
// JavaScript Function/eval enter this environment.
type AST = Node & Record<string, any>;
export type Environment = Record<string, unknown>;
export type Callable = { invoke: (args: unknown[]) => unknown };
export type RuntimeExecution = {
  remaining: number;
  depth: number;
  event: boolean;
  hostActions: boolean;
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
class ReturnValue {
  constructor(readonly value: unknown) {}
}
export class Interpreter {
  hookScope = "";
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
    if (--this.remaining < 0)
      throw new Error("UI computation exceeded its work budget.");
  }
  call(value: unknown, args: unknown[]) {
    this.tick();
    if (!isCallable(value))
      throw new Error("Only supported UI functions can be called.");
    if (++this.depth > 32) {
      this.depth--;
      throw new Error("UI call depth exceeded.");
    }
    try {
      return value.invoke(args);
    } finally {
      this.depth--;
    }
  }
  lookup(env: Environment, name: string): unknown {
    safeKey(name);
    if (name in env) return env[name];
    throw new Error(`Unknown UI variable: ${name}.`);
  }
  environment(): Environment {
    const env: Environment = Object.create(null);
    const numeric = (fn: (...args: number[]) => number) =>
      callable((args) => fn(...args.map(Number)));
    env.Math = Object.assign(Object.create(null), {
      min: numeric(Math.min),
      max: numeric(Math.max),
      round: numeric(Math.round),
      floor: numeric(Math.floor),
      ceil: numeric(Math.ceil),
      abs: numeric(Math.abs),
      sqrt: numeric(Math.sqrt),
      pow: numeric(Math.pow),
    });
    env.String = callable((args) => String(args[0] ?? ""));
    env.Number = callable((args) => Number(args[0]));
    env.Boolean = callable((args) => Boolean(args[0]));
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
    };
    env.undefined = undefined;
    env.GenUI = Object.fromEntries(
      [
        "copy",
        "openUrl",
        "issueNewTurn",
        "openEntityDetail",
        "launchEcosystemWidget",
      ].map((name) => [
        name,
        callable((args) => {
          if (!this.hostActions)
            throw new Error("Host actions require a user interaction.");
          args.forEach(checkLocalValue);
          return this.host.action(name, args);
        }),
      ]),
    );
    return env;
  }
  property(object: unknown, rawKey: unknown): unknown {
    const key = safeKey(rawKey);
    if (object == null)
      throw new Error(`Cannot read ${key} from an empty value.`);
    if (Array.isArray(object)) {
      if (object.length > 1000)
        throw new Error("Collection exceeds 1000 items.");
      if (key === "length") return object.length;
      if (["map", "filter", "find", "some", "every"].includes(key))
        return callable(([fn]) =>
          (object as any)[key]((value: unknown, index: number) =>
            this.call(fn, [value, index]),
          ),
        );
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
      if (key === "sort")
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
    }
    if (
      typeof object === "number" &&
      ["toLocaleString", "toFixed"].includes(key)
    )
      return callable((args) =>
        key === "toFixed"
          ? object.toFixed(Math.max(0, Math.min(20, Number(args[0] ?? 0))))
          : object.toLocaleString(
              typeof args[0] === "string" ? args[0] : undefined,
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
    const key = `${this.hookScope}:${node.start}:${name}`;
    const args = node.arguments.map((arg: AST) => this.evaluate(arg, env));
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
          this.host.state.get(key),
          callable(([next]) => {
            if (!this.event)
              throw new Error("State changes require a callback.");
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
          node.arguments.map((arg: AST) => this.evaluate(arg, env)),
        );
      case "AssignmentExpression":
        if (
          node.left.type === "MemberExpression" &&
          node.left.property.name === "current" &&
          node.operator === "="
        ) {
          const ref = this.evaluate(node.left.object, env);
          if (![...this.host.refs.values()].includes(ref))
            throw new Error("Only local refs can be assigned.");
          ref.current = this.evaluate(node.right, env);
          return ref.current;
        }
        break;
      case "SequenceExpression": {
        let value;
        for (const expression of node.expressions)
          value = this.evaluate(expression, env);
        return value;
      }
    }
    throw new Error(`Unsupported UI expression: ${node.type}.`);
  }
  function(node: AST, env: Environment): Callable {
    return callable((args) => {
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
        for (const declaration of node.declarations)
          this.bind(
            declaration.id,
            declaration.init ? this.evaluate(declaration.init, env) : undefined,
            env,
          );
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
      case "EmptyStatement":
        return;
      default:
        throw new Error(`Unsupported UI statement: ${node.type}.`);
    }
  }
}

export function sameDependencies(a: unknown[], b: unknown[]) {
  return (
    a.length === b.length &&
    a.every((value, index) => Object.is(value, b[index]))
  );
}
