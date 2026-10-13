import { useMemo } from "react";

/** Bounded text comparison; large documents retain the full side-by-side preview. */
export function DocumentHistoryDiff({
  before,
  after,
}: {
  before: string;
  after: string;
}) {
  const lines = useMemo(() => {
    const a = before.split("\n"),
      b = after.split("\n");
    if (
      a.length > 400 ||
      b.length > 400 ||
      before.length + after.length > 100000
    )
      return null;
    const lengths = Array.from(
      { length: a.length + 1 },
      () => new Uint16Array(b.length + 1),
    );
    for (let i = a.length - 1; i >= 0; i--)
      for (let j = b.length - 1; j >= 0; j--)
        lengths[i][j] =
          a[i] === b[j]
            ? lengths[i + 1][j + 1] + 1
            : Math.max(lengths[i + 1][j], lengths[i][j + 1]);
    const result: { type: "same" | "removed" | "added"; text: string }[] = [];
    let i = 0,
      j = 0;
    while (i < a.length || j < b.length) {
      if (i < a.length && j < b.length && a[i] === b[j]) {
        result.push({ type: "same", text: a[i++] });
        j++;
      } else if (
        j < b.length &&
        (i === a.length || lengths[i][j + 1] >= lengths[i + 1][j])
      )
        result.push({ type: "added", text: b[j++] });
      else result.push({ type: "removed", text: a[i++] });
    }
    return result;
  }, [before, after]);
  if (!lines)
    return (
      <p className="text-[var(--pc-muted)] text-[length:11px]">
        This comparison is large; the complete saved states are shown below.
      </p>
    );
  if (before === after)
    return (
      <p className="text-[var(--pc-muted)] text-[length:11px]">
        No text changes between these saves.
      </p>
    );
  return (
    <details className="pc-history-diff col-[1_/_-1]" open>
      <summary>Text changes</summary>
      <pre>
        {lines.map((line, i) => (
          <span key={i} data-change={line.type}>
            {line.type === "added"
              ? "+ "
              : line.type === "removed"
                ? "− "
                : "  "}
            {line.text}
            {"\n"}
          </span>
        ))}
      </pre>
    </details>
  );
}
