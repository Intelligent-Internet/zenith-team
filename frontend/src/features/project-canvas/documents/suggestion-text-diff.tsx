import type { Block } from "./block-editor-schema";

export function suggestionText(block?: Block | null): string | null {
  if (
    !block ||
    !["paragraph", "heading", "codeBlock"].includes(block.type ?? "")
  )
    return null;
  if (
    block.content?.some(
      (child) => !["text", "hardBreak"].includes(child.type ?? ""),
    )
  )
    return null;
  return (block.content ?? [])
    .map((child) => (child.type === "hardBreak" ? "\n" : (child.text ?? "")))
    .join("");
}

export function hasTextDiff(before?: Block | null, after?: Block | null) {
  const a = suggestionText(before),
    b = suggestionText(after);
  return a !== null && b !== null && a !== b && before?.type === after?.type;
}

type Part = { type: "same" | "add" | "remove"; text: string };
// Bound the comparison for large rewrites. Ordinary passage changes retain
// unchanged words; whitespace is a token so inserted line breaks stay visible.
function changes(before: string, after: string): Part[] {
  const tokenize = (text: string) =>
    text.match(/\s+|[\p{L}\p{N}_]+|[^\s\p{L}\p{N}_]/gu) ?? [];
  const a = tokenize(before),
    b = tokenize(after);
  let prefix = 0;
  while (prefix < a.length && prefix < b.length && a[prefix] === b[prefix])
    prefix++;
  let endA = a.length,
    endB = b.length;
  while (endA > prefix && endB > prefix && a[endA - 1] === b[endB - 1]) {
    endA--;
    endB--;
  }
  const parts: Part[] = [];
  const append = (type: Part["type"], text: string) => {
    if (!text) return;
    const last = parts.at(-1);
    if (last?.type === type) last.text += text;
    else parts.push({ type, text });
  };
  append("same", a.slice(0, prefix).join(""));
  const n = endA - prefix,
    m = endB - prefix;
  if (n * m > 250000) {
    append("remove", a.slice(prefix, endA).join(""));
    append("add", b.slice(prefix, endB).join(""));
  } else {
    const lengths = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1));
    for (let i = n - 1; i >= 0; i--)
      for (let j = m - 1; j >= 0; j--)
        lengths[i][j] =
          a[prefix + i] === b[prefix + j]
            ? 1 + lengths[i + 1][j + 1]
            : Math.max(lengths[i + 1][j], lengths[i][j + 1]);
    let i = 0,
      j = 0;
    while (i < n || j < m) {
      if (i < n && j < m && a[prefix + i] === b[prefix + j]) {
        append("same", a[prefix + i++]);
        j++;
      } else if (i < n && (j === m || lengths[i + 1][j] >= lengths[i][j + 1]))
        append("remove", a[prefix + i++]);
      else append("add", b[prefix + j++]);
    }
  }
  append("same", a.slice(endA).join(""));
  return parts;
}

export function SuggestionTextDiff({
  before,
  after,
}: {
  before: Block;
  after: Block;
}) {
  return (
    <div
      className="whitespace-pre-wrap break-words"
      aria-label="Suggested text changes"
    >
      {changes(suggestionText(before)!, suggestionText(after)!).map(
        (part, index) =>
          part.type === "remove" ? (
            <del key={index} className="bg-red-500/10 decoration-red-600">
              {part.text}
            </del>
          ) : part.type === "add" ? (
            <ins key={index} className="bg-green-500/10 no-underline">
              {part.text}
            </ins>
          ) : (
            <span key={index}>{part.text}</span>
          ),
      )}
    </div>
  );
}
