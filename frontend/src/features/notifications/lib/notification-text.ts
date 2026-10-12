export function compactNotificationText(value: string) {
  return value.replace(/\s+/g, " ").trim();
}

export function truncateNotificationText(value: string, limit: number) {
  if (value.length <= limit) return value;
  // Electron limits UTF-16 units. Keep emoji sequences and combining marks
  // together while reserving one unit for the visible ellipsis.
  let result = "";
  for (const { segment } of new Intl.Segmenter(undefined, {
    granularity: "grapheme",
  }).segment(value)) {
    if (result.length + segment.length > limit - 1) break;
    result += segment;
  }
  return `${result.trimEnd()}…`;
}
