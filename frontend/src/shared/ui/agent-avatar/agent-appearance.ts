import { foldVariantForKey } from "@/shared/ui/agent-avatar/fold-variants";

type AgentAppearanceSource = {
  accentColor?: string | null;
  avatarKey?: string | null;
  id: string;
  reportsTo?: { id: string } | null;
};

/** The palette an Agent without its own `accentColor` draws from. */
export const defaultAgentAccentColors = [
  "#FF6B57",
  "#A78BFA",
  "#58B8F2",
  "#FF7AC8",
  "#6EE787",
  "#31D6C6",
  "#FFB85C",
] as const;

/** The CEO Agent's accent matches its Tangerine Fold look. */
export const ceoAccentColor = "#EE8B42";

/**
 * The Agent's own accent colour; else the colour of the Fold look its
 * `avatarKey` names; else a stable pick from the default palette. The root
 * Agent always gets the CEO colour.
 */
export function agentAccentColor(
  agent: Pick<
    AgentAppearanceSource,
    "accentColor" | "avatarKey" | "id" | "reportsTo"
  >,
): string {
  const own = agent.accentColor?.trim();
  if (own) return own;
  const look = foldVariantForKey(agent.avatarKey);
  if (look) return look.color;
  if (agent.reportsTo === null) return ceoAccentColor;
  return defaultAgentAccentColors[
    stableIndex(agent.id, defaultAgentAccentColors.length)
  ];
}

function stableIndex(value: string, size: number) {
  let hash = 0;
  for (const character of value) {
    hash = (hash * 31 + character.codePointAt(0)!) >>> 0;
  }
  return hash % size;
}
