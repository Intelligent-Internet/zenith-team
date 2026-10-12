import { avatarPalettes } from "./fold-avatar";

/**
 * The looks an Agent can be given: one per Fold palette. The key is what the
 * API stores in `avatarKey` (`fold-<palette>`), so a look chosen once shows
 * everywhere a Fold avatar renders. Any other key — including the retired
 * illustration keys — means "no chosen look" and the palette comes from the
 * Agent's id.
 */
export type FoldAvatarVariant = {
  background: string;
  color: string;
  key: string;
  name: string;
  paletteIndex: number;
};

export const foldAvatarVariants: readonly FoldAvatarVariant[] =
  avatarPalettes.map((palette, paletteIndex) => ({
    background: palette.background,
    color: palette.color.toUpperCase(),
    key: `fold-${palette.name.toLowerCase()}`,
    name: palette.name,
    paletteIndex,
  }));

export function foldVariantForKey(
  key?: string | null,
): FoldAvatarVariant | undefined {
  if (!key) return undefined;
  return foldAvatarVariants.find((variant) => variant.key === key);
}

export function foldPaletteIndexForKey(
  key?: string | null,
): number | undefined {
  return foldVariantForKey(key)?.paletteIndex;
}
