// Normalize UUID wire identities without depending on a particular UUID version.
export function canonicalUuid(value: unknown): string | undefined {
  if (
    typeof value !== "string" ||
    !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
      value,
    )
  )
    return undefined;
  const id = value.toLowerCase();
  return id === "00000000-0000-0000-0000-000000000000" ? undefined : id;
}
