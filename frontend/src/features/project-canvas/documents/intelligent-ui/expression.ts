// Language 1 interpreter. Only JSON values cross this boundary; it has no host API.
export class EvaluationError extends Error {
  constructor(readonly code: string) {
    super(`intelligent ui: ${code}`);
  }
}
export interface EvaluationEnvironment {
  state: Record<string, unknown>;
  data: Record<string, unknown>;
  item?: unknown;
  hasItem?: boolean;
}
const fail = (code: string): never => {
  throw new EvaluationError(code);
};
const own = (value: object, key: string) =>
  Object.prototype.hasOwnProperty.call(value, key);
const safe = (key: string) =>
  !["__proto__", "prototype", "constructor"].includes(key);
const bytes = (text: string) => new TextEncoder().encode(text).length;
const record = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === "object" && !Array.isArray(value);
const number = (value: unknown): number =>
  typeof value === "number" ? value : fail("number.type");
const boolean = (value: unknown): boolean =>
  typeof value === "boolean" ? value : fail("boolean.type");
const finite = (value: number): number =>
  Number.isFinite(value) ? (value === 0 ? 0 : value) : fail("number.finite");

export function checkValue(value: unknown): void {
  let count = 0,
    size = 0;
  const visit = (value: unknown, depth: number): void => {
    count++;
    size += 8;
    if (size > 1048576) fail("value.size");
    if (depth > 32 || count > 20000) fail("value.depth");
    if (Array.isArray(value)) {
      if (value.length > 1000) fail("collection.size");
      value.forEach((child) => visit(child, depth + 1));
    } else if (record(value)) {
      if (![Object.prototype, null].includes(Object.getPrototypeOf(value)))
        fail("value.json");
      for (const [key, child] of Object.entries(value)) {
        size += bytes(key);
        if (!safe(key)) fail("field.unsafe");
        visit(child, depth + 1);
      }
    } else if (typeof value === "string") {
      const length = bytes(value);
      size += length;
      if (size > 1048576) fail("value.size");
      if (length > 65536) fail("string.size");
    } else if (typeof value === "number") finite(value);
    else if (value !== null && typeof value !== "boolean") fail("value.json");
  };
  visit(value, 0);
  if (bytes(canonicalJSON(value)) > 1048576) fail("value.size");
}

export function validateExpression(expression: unknown): void {
  let count = 0;
  const visit = (raw: unknown, depth: number): void => {
    if (++count > 4096 || depth > 32) fail("expression.limit");
    if (!record(raw)) return fail("expression.object");
    const op = raw.op;
    if (typeof op !== "string") return fail("expression.op");
    const allowed = ["op"];
    let arity = -1;
    switch (op) {
      case "literal":
        allowed.push("value");
        if (!own(raw, "value")) fail("literal.value");
        checkValue(raw.value);
        break;
      case "state":
      case "data":
        allowed.push("name");
        if (
          typeof raw.name !== "string" ||
          !/^[A-Za-z_][A-Za-z_0-9]{0,63}$/.test(raw.name) ||
          !safe(raw.name)
        )
          fail("reference.name");
        break;
      case "item":
        break;
      case "field":
        allowed.push("input", "field");
        if (
          typeof raw.field !== "string" ||
          !raw.field ||
          bytes(raw.field) > 128 ||
          !safe(raw.field)
        )
          fail("field.unsafe");
        visit(raw.input, depth + 1);
        break;
      case "count":
      case "sum":
      case "min":
      case "max":
      case "avg":
        allowed.push("input");
        visit(raw.input, depth + 1);
        break;
      case "map":
      case "filter":
      case "sort":
        allowed.push("input", "body");
        if (op === "sort") {
          allowed.push("descending");
          if (own(raw, "descending") && typeof raw.descending !== "boolean")
            fail("sort.direction");
        }
        visit(raw.input, depth + 1);
        visit(raw.body, depth + 1);
        break;
      case "format":
        allowed.push("input", "style", "digits");
        if (!["decimal", "percent", "text"].includes(raw.style as string))
          fail("format.style");
        if (
          own(raw, "digits") &&
          (typeof raw.digits !== "number" ||
            !Number.isInteger(raw.digits) ||
            raw.digits < 0 ||
            raw.digits > 6)
        )
          fail("format.digits");
        visit(raw.input, depth + 1);
        break;
      case "not":
        arity = 1;
        break;
      case "if":
        arity = 3;
        break;
      case "add":
      case "sub":
      case "mul":
      case "div":
      case "mod":
      case "eq":
      case "ne":
      case "lt":
      case "lte":
      case "gt":
      case "gte":
      case "and":
      case "or":
      case "coalesce":
      case "take":
      case "dateDiff":
        arity = 2;
        break;
      default:
        fail("expression.unknown");
    }
    if (arity >= 0) {
      allowed.push("args");
      if (!Array.isArray(raw.args) || raw.args.length !== arity)
        return fail("expression.arity");
      raw.args.forEach((arg) => visit(arg, depth + 1));
    }
    if (Object.keys(raw).some((key) => !allowed.includes(key)))
      fail("expression.property");
  };
  visit(expression, 0);
}

function scalarOrder(a: string, b: string): number {
  const left = Array.from(a),
    right = Array.from(b);
  for (let i = 0; i < Math.min(left.length, right.length); i++) {
    const difference = left[i].codePointAt(0)! - right[i].codePointAt(0)!;
    if (difference) return Math.sign(difference);
  }
  return Math.sign(left.length - right.length);
}
function compare(a: unknown, b: unknown): number {
  if (a === null) return b === null ? 0 : 1;
  if (b === null) return -1;
  if (typeof a === "number" && typeof b === "number")
    return a < b ? -1 : a > b ? 1 : 0;
  if (typeof a === "string" && typeof b === "string") return scalarOrder(a, b);
  return fail("comparison.type");
}
function equal(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (Array.isArray(a))
    return (
      Array.isArray(b) &&
      a.length === b.length &&
      a.every((value, index) => equal(value, b[index]))
    );
  if (record(a) && record(b)) {
    const keys = Object.keys(a);
    return (
      keys.length === Object.keys(b).length &&
      keys.every((key) => own(b, key) && equal(a[key], b[key]))
    );
  }
  return false;
}

// Match encoding/json's ordered keys and HTML-safe string escaping.
export function canonicalJSON(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonicalJSON).join(",")}]`;
  if (record(value))
    return `{${Object.keys(value)
      .sort(scalarOrder)
      .map((key) => `${canonicalJSON(key)}:${canonicalJSON(value[key])}`)
      .join(",")}}`;
  return JSON.stringify(value).replace(
    /[<>&\u2028\u2029]/g,
    (char) => `\\u${char.charCodeAt(0).toString(16).padStart(4, "0")}`,
  );
}

// Round the exact binary64 value to nearest, ties to even, matching Go's 'f'.
function fixed(value: number, digits: number): string {
  const view = new DataView(new ArrayBuffer(8));
  view.setFloat64(0, Math.abs(value));
  const bits = view.getBigUint64(0);
  const exponent = Number((bits >> 52n) & 2047n);
  const mantissa = (bits & ((1n << 52n) - 1n)) | (exponent ? 1n << 52n : 0n);
  const shift = (exponent || 1) - 1075;
  let numerator = mantissa * 10n ** BigInt(digits),
    denominator = 1n;
  if (shift >= 0) numerator <<= BigInt(shift);
  else denominator <<= BigInt(-shift);
  let rounded = numerator / denominator;
  const remainder = numerator % denominator;
  if (
    remainder * 2n > denominator ||
    (remainder * 2n === denominator && rounded % 2n === 1n)
  )
    rounded++;
  let text = rounded.toString().padStart(digits + 1, "0");
  if (digits) text = `${text.slice(0, -digits)}.${text.slice(-digits)}`;
  return (value < 0 ? "-" : "") + text;
}
export function parseUITimestamp(value: unknown): {
  seconds: number;
  nanos: number;
} {
  if (typeof value !== "string") return fail("date.type");
  const match =
    /^(\d{4})-(\d{2})-(\d{2})T(\d{1,2}):(\d{2}):(\d{2})(?:[.,](\d+))?(Z|([+-])(\d{2}):(\d{2}))$/.exec(
      value,
    );
  if (!match) return fail("date.format");
  const [
    ,
    year,
    month,
    day,
    hour,
    minute,
    second,
    fraction,
    zone,
    sign,
    zoneHour,
    zoneMinute,
  ] = match;
  const date = new Date(0);
  date.setUTCFullYear(+year, +month - 1, +day);
  date.setUTCHours(+hour, +minute, +second, 0);
  if (
    date.getUTCFullYear() !== +year ||
    date.getUTCMonth() !== +month - 1 ||
    date.getUTCDate() !== +day ||
    +hour > 23 ||
    +minute > 59 ||
    +second > 59
  )
    return fail("date.format");
  if (zone !== "Z" && (+zoneHour > 24 || +zoneMinute > 60))
    return fail("date.format");
  const offset =
    zone === "Z"
      ? 0
      : (sign === "+" ? 1 : -1) * (+zoneHour * 3600 + +zoneMinute * 60);
  return {
    seconds: date.getTime() / 1000 - offset,
    nanos: Number((fraction ?? "").slice(0, 9).padEnd(9, "0")),
  };
}

export function evaluateExpression(
  expression: unknown,
  environment: EvaluationEnvironment,
): unknown {
  validateExpression(expression);
  return evaluateValidatedExpression(expression, environment);
}

// Keep an owned definition so callers cannot change an already-validated tree.
// Results are copied because a literal may otherwise expose part of that tree.
export function compileExpression(
  expression: unknown,
): (environment: EvaluationEnvironment) => unknown {
  validateExpression(expression);
  const definition = structuredClone(expression);
  return (environment) =>
    structuredClone(evaluateValidatedExpression(definition, environment));
}

function evaluateValidatedExpression(
  expression: unknown,
  environment: EvaluationEnvironment,
): unknown {
  // Match Go UIEnvironment's omitted zero-value item fields. Explicit false
  // or null fields must not consume bytes absent from the capture envelope.
  const envelope: EvaluationEnvironment = { ...environment };
  if (envelope.hasItem === false || envelope.hasItem === undefined)
    delete envelope.hasItem;
  if (envelope.item == null) delete envelope.item;
  checkValue(envelope);
  let steps = 0;
  const tick = (depth = 0) => {
    if (++steps > 20000 || depth > 32) fail("evaluation.budget");
  };
  const evaluate = (
    raw: unknown,
    item: unknown,
    bound: boolean,
    depth: number,
  ): unknown => {
    tick(depth);
    const node = raw as Record<string, unknown>,
      op = node.op;
    const run = (value: unknown) => evaluate(value, item, bound, depth + 1);
    if (op === "literal") return node.value;
    if (op === "state" || op === "data") {
      const source = environment[op],
        name = node.name as string;
      if (!source || !own(source, name)) return fail("reference.missing");
      return source[name];
    }
    if (op === "item") return bound ? item : fail("reference.item");
    if (op === "field") {
      const value = run(node.input);
      if (value === null) return null;
      if (!record(value)) return fail("field.type");
      return own(value, node.field as string)
        ? value[node.field as string]
        : null;
    }
    if (
      ["map", "filter", "sort", "count", "sum", "min", "max", "avg"].includes(
        op as string,
      )
    ) {
      const values = run(node.input);
      if (!Array.isArray(values)) return fail("collection.type");
      if (values.length > 1000) return fail("collection.size");
      if (op === "count") return values.length;
      if (["sum", "min", "max", "avg"].includes(op as string)) {
        if (!values.length) return op === "sum" ? 0 : null;
        let total = 0;
        values.forEach((value, index) => {
          tick();
          const n = number(value);
          if (op === "min") total = index === 0 || n < total ? n : total;
          else if (op === "max") total = index === 0 || n > total ? n : total;
          else total += n;
          finite(total);
        });
        return op === "avg" ? total / values.length : total;
      }
      const rows: { value: unknown; key: unknown }[] = [];
      const result: unknown[] = [];
      for (const value of values) {
        const key = evaluate(node.body, value, true, depth + 1);
        if (op === "map") result.push(key);
        else if (op === "filter") {
          if (boolean(key)) result.push(value);
        } else rows.push({ value, key });
      }
      if (op !== "sort") return result;
      let sample: unknown = null;
      for (const row of rows)
        if (row.key !== null) {
          compare(row.key, row.key);
          if (sample !== null) compare(sample, row.key);
          sample = row.key;
        }
      // Identical bottom-up merge order on server and browser keeps budget
      // accounting independent of each platform's built-in sort algorithm.
      const scratch = rows.slice();
      for (let width = 1; width < rows.length; width *= 2) {
        for (let start = 0; start < rows.length; start += 2 * width) {
          const middle = Math.min(start + width, rows.length);
          const end = Math.min(start + 2 * width, rows.length);
          let left = start,
            right = middle;
          for (let at = start; at < end; at++) {
            let before = false;
            if (left < middle && right < end) {
              tick();
              const a = rows[left].key,
                b = rows[right].key;
              let order = compare(a, b);
              if (node.descending && a !== null && b !== null) order = -order;
              before = order <= 0;
            }
            scratch[at] =
              right >= end || (left < middle && before)
                ? rows[left++]
                : rows[right++];
          }
        }
        for (let at = 0; at < rows.length; at++) rows[at] = scratch[at];
      }
      return rows.map((row) => row.value);
    }
    if (op === "format") {
      const value = run(node.input);
      if (node.style === "text") {
        checkValue(value);
        return value === null
          ? "Unavailable"
          : typeof value === "string"
            ? value
            : canonicalJSON(value);
      }
      const n = finite(number(value) * (node.style === "percent" ? 100 : 1));
      return (
        fixed(n, (node.digits as number) ?? 0) +
        (node.style === "percent" ? "%" : "")
      );
    }
    const args = node.args as unknown[],
      left = run(args[0]);
    if (op === "if") return run(args[boolean(left) ? 1 : 2]);
    if (op === "coalesce") return left === null ? run(args[1]) : left;
    if (op === "not") return !boolean(left);
    if (op === "and" || op === "or") {
      const value = boolean(left);
      return (op === "and" && !value) || (op === "or" && value)
        ? value
        : boolean(run(args[1]));
    }
    const right = run(args[1]);
    if (op === "eq" || op === "ne") return equal(left, right) === (op === "eq");
    if (["lt", "lte", "gt", "gte"].includes(op as string)) {
      const order = compare(left, right);
      return op === "lt"
        ? order < 0
        : op === "lte"
          ? order <= 0
          : op === "gt"
            ? order > 0
            : order >= 0;
    }
    if (op === "take") {
      if (
        !Array.isArray(left) ||
        typeof right !== "number" ||
        !Number.isInteger(right) ||
        right < 0 ||
        right > 1000
      )
        return fail("take.type");
      return left.slice(0, right);
    }
    if (op === "dateDiff") {
      if (typeof left !== "string" || typeof right !== "string")
        return fail("date.type");
      const a = parseUITimestamp(left),
        b = parseUITimestamp(right);
      return a.seconds - b.seconds + (a.nanos - b.nanos) / 1e9;
    }
    const a = number(left),
      b = number(right);
    if ((op === "div" || op === "mod") && b === 0)
      return fail("number.zeroDivision");
    switch (op) {
      case "add":
        return finite(a + b);
      case "sub":
        return finite(a - b);
      case "mul":
        return finite(a * b);
      case "div":
        return finite(a / b);
      case "mod":
        return finite(a % b);
      default:
        return fail("expression.unknown");
    }
  };
  const result = evaluate(
    expression,
    environment.item,
    environment.hasItem === true,
    0,
  );
  checkValue(result);
  return result;
}

export function expressionDependencies(expression: unknown): string[] {
  validateExpression(expression);
  const dependencies = new Set<string>();
  const walk = (raw: unknown): void => {
    if (!record(raw) || raw.op === "literal") return;
    if (raw.op === "state" || raw.op === "data")
      dependencies.add(`${raw.op}.${raw.name}`);
    if (Array.isArray(raw.args)) raw.args.forEach(walk);
    walk(raw.input);
    walk(raw.body);
  };
  walk(expression);
  return [...dependencies].sort();
}

// Collection bodies bind their own item. Only references outside those bodies
// depend on the surrounding repeated row.
export function expressionUsesItem(expression: unknown): boolean {
  if (!record(expression) || expression.op === "literal") return false;
  if (expression.op === "item") return true;
  if (expressionUsesItem(expression.input)) return true;
  if (
    Array.isArray(expression.args) &&
    expression.args.some(expressionUsesItem)
  )
    return true;
  return (
    !["map", "filter", "sort"].includes(String(expression.op)) &&
    expressionUsesItem(expression.body)
  );
}
