import { intelligentDefinitions } from "./catalog.generated";

// Version 1 is the legacy omitted catalog version. Future interpreters must
// dispatch by saved version rather than widening this check to accept anything.
export function supportsReactiveVersion(
  type: string,
  attrs: Record<string, unknown>,
) {
  const definition = intelligentDefinitions[type];
  return (
    !!definition &&
    (attrs.catalogVersion === undefined ? 1 : attrs.catalogVersion) ===
      definition.version
  );
}

export function supportsCaptureVersions(evaluations: unknown) {
  if (evaluations === undefined || evaluations === null) return true;
  if (typeof evaluations !== "object" || Array.isArray(evaluations))
    return false;
  return Object.values(evaluations).every((capture: unknown) => {
    if (!capture || typeof capture !== "object" || Array.isArray(capture))
      return false;
    const value = capture as Record<string, unknown>;
    return (
      value.schemaVersion === 2 &&
      value.languageVersion === 1 &&
      value.catalogVersion === 1
    );
  });
}
