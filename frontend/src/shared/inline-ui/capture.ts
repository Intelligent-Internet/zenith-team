// A capture records local viewer state; it never grants access to host actions.
export type InlineCapture = {
  rendererVersion: 1;
  identity: string;
  source: string;
  theme: string;
  width: number;
  state: Record<string, unknown>;
  refs: Record<string, { current: unknown }>;
  memo: Record<string, { value: unknown; dependencies: unknown[] }>;
};
export type InlineCaptureReader = () => InlineCapture;

export function retainInlineCapture(value: InlineCapture): InlineCapture {
  let count = 0;
  const visit = (item: unknown, depth: number): void => {
    if (++count > 10000 || depth > 32)
      throw new Error("This component state exceeds the capture limit.");
    if (item === null || typeof item === "string" || typeof item === "boolean")
      return;
    if (typeof item === "number" && Number.isFinite(item)) return;
    if (!item || typeof item !== "object")
      throw new Error("This component contains state that cannot be captured.");
    if (
      !Array.isArray(item) &&
      Object.getPrototypeOf(item) !== Object.prototype &&
      Object.getPrototypeOf(item) !== null
    )
      throw new Error("This component contains state that cannot be captured.");
    for (const [key, child] of Object.entries(item)) {
      if (["__proto__", "constructor", "prototype", "invoke"].includes(key))
        throw new Error(
          "This component contains state that cannot be captured.",
        );
      visit(child, depth + 1);
    }
  };
  visit(value, 0);
  const encoded = JSON.stringify(value);
  if (new TextEncoder().encode(encoded).length > 524288)
    throw new Error("This component state exceeds the capture limit.");
  return JSON.parse(encoded) as InlineCapture;
}

export function isInlineCapture(
  value: unknown,
  source: string,
): value is InlineCapture {
  const record = (item: unknown): item is Record<string, unknown> =>
    !!item && typeof item === "object" && !Array.isArray(item);
  if (
    !record(value) ||
    value.rendererVersion !== 1 ||
    value.source !== source ||
    typeof value.identity !== "string" ||
    !value.identity ||
    value.identity.length > 200 ||
    !["light", "dark"].includes(String(value.theme)) ||
    typeof value.width !== "number" ||
    !Number.isFinite(value.width) ||
    value.width < 0 ||
    value.width > 100000 ||
    !record(value.state) ||
    !record(value.refs) ||
    !record(value.memo)
  )
    return false;
  if (
    Object.values(value.refs).some(
      (item) =>
        !record(item) ||
        !Object.hasOwn(item, "current") ||
        Object.keys(item).length !== 1,
    )
  )
    return false;
  if (
    Object.values(value.memo).some(
      (item) =>
        !record(item) ||
        !Object.hasOwn(item, "value") ||
        !Array.isArray(item.dependencies) ||
        Object.keys(item).length !== 2,
    )
  )
    return false;
  try {
    retainInlineCapture(value as InlineCapture);
    return true;
  } catch {
    return false;
  }
}
