import type { components } from "@/shared/api/generated/api.generated";
export type ResourceReference =
  | components["schemas"]["MessageSkillReference"]
  | components["schemas"]["MessageConnectorReference"];

// Keep selected identities through edits outside their labels; editing any
// part of a label turns it back into ordinary text, never a different resource.
export function moveResourceReferences(
  before: string,
  after: string,
  refs: ResourceReference[],
): ResourceReference[] {
  let start = 0;
  while (
    start < before.length &&
    start < after.length &&
    before[start] === after[start]
  )
    start++;
  let oldEnd = before.length;
  let newEnd = after.length;
  while (
    oldEnd > start &&
    newEnd > start &&
    before[oldEnd - 1] === after[newEnd - 1]
  ) {
    oldEnd--;
    newEnd--;
  }
  const delta = newEnd - oldEnd;
  return refs.flatMap((ref) => {
    if (ref.end <= start) return [ref];
    if (ref.start >= oldEnd)
      return [{ ...ref, start: ref.start + delta, end: ref.end + delta }];
    return [];
  });
}
