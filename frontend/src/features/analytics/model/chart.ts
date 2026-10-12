export function chartDataIndex(params: unknown): number | null {
  if (!params || typeof params !== "object") return null;
  const candidate = params as { dataIndex?: unknown };
  return typeof candidate.dataIndex === "number" ? candidate.dataIndex : null;
}
