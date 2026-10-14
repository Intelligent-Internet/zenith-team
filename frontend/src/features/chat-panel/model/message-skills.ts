import type { components } from "@/shared/api/generated/api.generated";
type SkillReference = components["schemas"]["MessageSkillReference"];

// Keep selected identities through edits outside their labels; editing any
// part of a label turns it back into ordinary text, never a different skill.
export function moveSkillReferences(
  before: string,
  after: string,
  refs: SkillReference[],
): SkillReference[] {
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
