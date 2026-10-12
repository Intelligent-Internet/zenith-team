// A runtime is always private (ADR 0014): it belongs to the member who
// registered it, nobody else can see it in a roster or start work on it, and
// there is no sharing decision to render. What is left of the old visibility
// model is this one question, which a settings screen asks before it offers
// to rename, refresh or remove a machine.
export function viewerIsRegistrant(
  runtime: { owner?: { id: string } | null },
  viewerUserId: string | undefined,
): boolean {
  return Boolean(viewerUserId && runtime.owner?.id === viewerUserId);
}
