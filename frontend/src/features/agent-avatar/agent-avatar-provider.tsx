import { useContext, useMemo, type ReactNode } from "react";

import {
  avatarPalettes,
  FoldAvatarPaletteContext,
} from "@/shared/ui/agent-avatar/fold-avatar";
import { foldPaletteIndexForKey } from "@/shared/ui/agent-avatar/fold-variants";

const CEO_PALETTE_INDEX = avatarPalettes.findIndex(
  (palette) => palette.name === "Tangerine",
);

/**
 * Resolves each Agent's Fold palette once for the whole shell: the look its
 * `avatarKey` names when it has one, Tangerine for the CEO Agent otherwise,
 * and the id-derived palette for everyone else (FoldAvatar's own default).
 */
export function AgentAvatarProvider({
  agents,
  ceoAgentId,
  children,
}: {
  agents?: ReadonlyArray<{ avatarKey?: string | null; id: string }>;
  ceoAgentId?: string;
  children: ReactNode;
}) {
  const inheritedPalettes = useContext(FoldAvatarPaletteContext);
  const palettes = useMemo(() => {
    const next: Record<string, number> = { ...inheritedPalettes };
    if (ceoAgentId) next[ceoAgentId] = CEO_PALETTE_INDEX;
    for (const agent of agents ?? []) {
      const paletteIndex = foldPaletteIndexForKey(agent.avatarKey);
      if (paletteIndex !== undefined) next[agent.id] = paletteIndex;
    }
    return next;
  }, [agents, ceoAgentId, inheritedPalettes]);

  return (
    <FoldAvatarPaletteContext.Provider value={palettes}>
      {children}
    </FoldAvatarPaletteContext.Provider>
  );
}
