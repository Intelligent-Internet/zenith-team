import { publicAssetPath } from "@/shared/lib/public-assets";

/**
 * The one illustration still in use: the fallback picture behind a person's
 * profile control in the title bars. Agents are never drawn with it — an
 * Agent's face is its Fold avatar (`fold-avatar.tsx`, `fold-variants.ts`).
 */
export const ceoAvatarSrc = publicAssetPath("images/agents/ceo.webp");
