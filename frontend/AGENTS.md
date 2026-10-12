# Zenith Team frontend agent instructions

Follow the root [AGENTS.md](../AGENTS.md) and [README.md](README.md). These
instructions apply to the web UI, Electron UI, previews and vendored components.

## Iconsax is mandatory

- Use **Iconsax** as the UI icon set; **Outline** is the default style. Authored
  symbols matching Iconsax are allowed when the catalog lacks a suitable glyph,
  following the workflow below. Use the existing official Bold artwork only
  where a selected/saved state calls for it. Horizontal and vertical ellipsis
  menus use solid dots derived from Iconsax's Outline contours, as requested
  by the owner on 2026-09-24.
- Import named components and `IconProps` / `IconComponent` from
  `@/shared/ui/icons`. For product icon names, use `Icon` from `@/shared/ui/icon`.
  Semantic export names such as `CircleAlert` and `SignalHigh` still resolve to
  Iconsax; their names do not permit imports from another package.
- Do not install, import, re-export or copy artwork from another icon library.
  This includes Lucide, Heroicons, Phosphor, React Icons, Tabler, Font Awesome,
  Material Icons, Radix Icons, Iconify, Feather and similar packages, including
  CDN imports and icon fonts. Do not add a separate Iconsax runtime package;
  use the bundled assets through the shared components.
- Authored symbols must use shared SVG assets and match Iconsax's visual style.
  Do not embed one-off SVG paths or CSS drawings in feature components or use
  emoji/Unicode glyphs in place of UI icons. Brand logos, Agent avatars,
  illustrations and data visualizations use their own assets.
- When copying a component or implementing a design, replace its icon imports
  with the shared Iconsax components. Older specs, Figma examples and library
  defaults do not override this rule. For third-party controls, use their icon
  customization API or the existing adapter. Keep Vite's Streamdown icon alias;
  it replaces that dependency's Lucide imports with our Iconsax components.

## Finding more icons

When the user asks to find more icons, explore alternatives or choose a different
icon, open the official [Iconsax Outline catalog](https://app.iconsax.io/?style=outline)
with the available browser tools. Search for the intended action or object and
related terms, keeping Outline selected. The repository bundles only a subset;
an icon missing locally may still exist in the catalog.

Inspect actual candidates before recommending them. If the user wants options,
provide a small selection with the catalog names, links or previews, and explain
which fits the UI meaning best. If the user asks to apply a replacement, proceed
within that request using the workflow below; a catalog search does not require
another confirmation.

For an icon not yet bundled, obtain the official SVG through an available source
covered by the asset license and integrate it locally through the shared icon
components. Do not install another icon package or hotlink catalog assets into
the app. If the catalog cannot be accessed, state that limitation rather than
claiming an icon was found or is absent. Authored symbols matching Iconsax remain
allowed when a suitable catalog glyph is unavailable.

## Adding an icon

1. Look for an existing export in [icons.tsx](src/shared/ui/icons.tsx) and a
   product-name mapping in [icon.tsx](src/shared/ui/icon.tsx).
2. If missing, search the [official Iconsax Outline catalog](https://app.iconsax.io/?style=outline)
   and choose an appropriate icon available under the existing Free asset license,
   bundle its SVG in `src/assets/iconsax/`, and register it in `icons.tsx` and
   `iconsaxIcons`. Add a product-name mapping only when needed. Follow the
   [asset source and license notice](src/assets/iconsax/NOTICE.md).
3. Preserve the SVG viewBox and fill/stroke geometry. Set color through
   `currentColor` / a text-color class, and dimensions through `size` or CSS.
   Do not override stroke widths to imitate another icon set.
4. If the catalog has no semantically suitable glyph, reuse or author a symbol
   that matches Iconsax: a 24 × 24 viewBox, 1.5 px outline weight, rounded ends
   and joins, comparable visual weight, and `currentColor`. Preserve the
   familiar meaning of the action rather than choosing an unrelated glyph.
5. Store authored symbols in `src/assets/ui-symbols/`, register them in
   `icons.tsx` and `uiSymbols`, and document their purpose in the
   [UI-symbol notice](src/assets/ui-symbols/NOTICE.md). Keep `data-ui-symbol`
   distinct from `data-iconsax` so authored artwork is not labelled official.
   Do not copy from another icon library to fill a catalog gap.

The existing `AtSign`, `Pin`, `PinOff` and `Puzzle` exports follow this authored
symbol approach and may be reused in new UI. The owner confirmed this approach
on 2026-09-15; additional symbols following these rules do not need separate
approval.

## Verification

Review icon imports, direct dependencies, SVG provenance, authored-symbol style
and rendered third-party controls against these instructions. Transitive lockfile
entries are not direct imports; preserve the Streamdown adapter.

Do not write, add, or restore tests anywhere under `frontend/`, including web UI,
Electron, unit, integration, snapshot, and end-to-end tests. Do not run
`pnpm test`, `pnpm run test`, `pnpm --dir frontend test`, or equivalent frontend
test commands. Validate changes
with `pnpm lint`, `pnpm typecheck`, `pnpm build`, and relevant manual UI checks.
This rule overrides general test-first guidance. The `test:commerce-kit` script
runs server-owned tests under `server/`; it is not a frontend test suite.
