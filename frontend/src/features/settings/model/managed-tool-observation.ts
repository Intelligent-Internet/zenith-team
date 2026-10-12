import type { RuntimeDto } from "@/shared/api/types";

// Installation is shared; authentication is meaningful only for the exact Agent.
export function managedToolObservation(
  tools: RuntimeDto["tools"],
  key: string,
  agentId: string,
) {
  const scoped = tools?.find(
    (item) => item.key === key && item.agentId === agentId,
  );
  if (agentId && scoped) return scoped;
  const installation = tools?.find((item) => item.key === key && !item.agentId);
  return installation
    ? {
        ...installation,
        authState:
          installation.authState === "not_required"
            ? ("not_required" as const)
            : ("unknown" as const),
      }
    : undefined;
}
