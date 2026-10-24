// Portable repair evidence shared by compilation and rendering. Diagnostics
// contain source locations and contract messages, never evaluated user values.
export type InlineDiagnostic = {
  code: string;
  phase: "parse" | "catalog" | "evaluate" | "render";
  start: number;
  component?: string;
  property?: string;
  message: string;
  repair: "omitted" | "normalized" | "closed" | "pending" | "retained";
};

export function failureMessage(error: unknown): string {
  return (
    error instanceof Error ? error.message : "This part could not be displayed."
  ).slice(0, 500);
}

export function recordDiagnostic(
  list: InlineDiagnostic[],
  diagnostic: InlineDiagnostic,
) {
  if (
    list.length < 100 &&
    !list.some(
      (item) =>
        item.code === diagnostic.code &&
        item.start === diagnostic.start &&
        item.property === diagnostic.property,
    )
  )
    list.push(diagnostic);
}

export function repairRequest(
  source: string,
  diagnostics: InlineDiagnostic[],
): string {
  const issues = diagnostics
    .slice(0, 20)
    .map(
      (item) =>
        `- ${item.code}${item.component ? ` (${item.component}${item.property ? `.${item.property}` : ""})` : ""}: ${item.message}`,
    )
    .join("\n");
  return `Repair this Intelligent UI response using the zenith-intelligent-ui skill. Preserve its intended content and interactions. Return corrected component source directly. Do not invent missing integrations or data.\n\nRenderer diagnostics:\n${issues || "The response ended before its components finished."}\n\nOriginal source${source.length > 24000 ? " (truncated; refer to the original response for the rest)" : ""}:\n${source.slice(0, 24000)}`;
}
