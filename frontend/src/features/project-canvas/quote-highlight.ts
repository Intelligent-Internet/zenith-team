// Text stays selectable: CSS highlights paint ranges without replacing reader DOM.
export function quoteRange(
  root: HTMLElement,
  quote: string,
): Range | undefined {
  const reader = root.matches(
    "[data-block-id], .tiptap, .pc-document-prose, .pc-source-code",
  )
    ? root
    : root.querySelector(".tiptap, .pc-document-prose, .pc-source-code");
  if (!reader) return;
  const walker = document.createTreeWalker(reader, NodeFilter.SHOW_TEXT);
  const segments: { node: Text; start: number; end: number }[] = [];
  let text = "";
  let node: Node | null;
  let previousBlock: Element | null = null;
  while ((node = walker.nextNode())) {
    if (node.parentElement?.closest("[data-suggestion-preview]")) continue;
    if (text.length > 1_048_576) return;
    const value = node.textContent ?? "";
    const block =
      node.parentElement?.closest(
        "p, li, h1, h2, h3, h4, h5, h6, pre, td, blockquote",
      ) ?? null;
    if (previousBlock && block && previousBlock !== block) text += "\n";
    previousBlock = block;
    segments.push({
      node: node as Text,
      start: text.length,
      end: text.length + value.length,
    });
    text += value;
  }
  const pattern = quote
    .trim()
    .split(/\s+/)
    .map((word) => word.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"))
    .join("\\s+");
  if (!pattern) return;
  const expression = new RegExp(pattern, "g");
  const match = expression.exec(text);
  if (!match) return;
  // Legacy anchors lack an occurrence offset; never jump to a different copy.
  expression.lastIndex = match.index + 1;
  if (expression.exec(text)) return;
  const start = match.index;
  const end = start + match[0].length;
  const first = segments.find(
    (part) => part.start <= start && part.end > start,
  );
  const last = segments.find((part) => part.start < end && part.end >= end);
  if (!first || !last) return;
  const range = document.createRange();
  range.setStart(first.node, start - first.start);
  range.setEnd(last.node, end - last.start);
  return range;
}
