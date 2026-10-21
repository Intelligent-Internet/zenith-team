# Iconsax assets

Icons by [Iconsax](https://iconsax.io). These Free icons are integrated into
Zenith Team source code under the
[Iconsax Free License for Developers](https://docs.iconsax.io/license-and-terms/license).
Keep this notice with the integrated assets. They are not a standalone icon pack.

Source: the bundled Free SVG data in the official `iconsax@0.1.1` npm package,
downloaded on 2026-09-14. The package metadata lists MIT for the package;
the icon artwork has the Iconsax license linked above.

- [Official package documentation](https://docs.iconsax.io/npm/web-component)
- [Package source](https://github.com/iconsax/iconsax-npm)
- [Outline catalog](https://app.iconsax.io/?style=outline)

The default style is Outline. `bookmark-bold` and `star-bold` use the official Bold artwork for saved indicators. Colors are normalized to
`currentColor`; redundant full-viewBox clipping is removed to avoid duplicate
SVG IDs. The original 24 × 24 viewBox is retained.

The following local variants reuse Iconsax geometry: `3-dots-more` uses the
original Outline outer contours as solid dots, omitting the inner cutouts.
Each dot is scaled to 80% around its original center to balance the filled
weight with adjacent Outline icons. Workspace styles soften their fill to 80%
opacity except in high-contrast mode.
`stop-filled` keeps the Outline `stop` outer contour and omits its inner cutout
for the chat composer's Stop action.
`add` extends the arms of
the original glyph; `close` rotates the same cross with longer arms to balance
its diagonal footprint. Both preserve the original line thickness and rounded
ends. Other variants include:
`more-vertical` rotates `3-dots-more`; `arrow-up-right` rotates `arrow-up-01`;
`sidebar-left-open` reverses only the chevron in `sidebar-left`, preserving the left panel divider;
`checkmark` enlarges the check path from `tick-circle`; `selection-dot` uses the
outer circle from `record`; `grip-vertical` arranges six scaled selection dots
as a drag handle; `double-chevron-down` repeats
`arrow-down-02`; `minimize` reverses the two arrows from `maximize-4`;
`text-strikethrough`, `wifi-off`, and `link-off` combine their base glyph with
`minus`. `user-minus` is `user-add` with the plus sign's vertical bar removed,
leaving the minus (Team › Project access, "Remove access", 2026-10-07).
`login-01` is `logout-01` with its arc mirrored to the right and its
arrow moved 6 units left, so the arrow enters the arc (Team › Project
access, "Join", 2026-10-08; the lock there means "Beyond your access"). `menu-lines` selects `menu` from the Essential category because the
package manifest resolves that shared name to the Settings category instead.

Use `shared/ui/icons.tsx` for named components and `shared/ui/icon.tsx` for
persisted product icon names. Use the official Free catalog for new Iconsax
assets, add them here, and register them in the shared components. If the
catalog lacks a suitable glyph, follow the authored-symbol workflow below. Preserve
each SVG's fill and stroke geometry: set `color` or a text-color class, and size
with `size` or CSS dimensions; do not add stroke overrides. Refresh actions use
`refresh-arrow-01` through both shared components. Brand marks live in
`assets/brands`.

The pushpin, @, and puzzle-piece symbols live separately in `assets/ui-symbols`;
they are authored UI symbols matching Iconsax's style, not official Iconsax
artwork. They may be reused, and new symbols may be authored when the catalog
lacks a suitable glyph. Follow the [mandatory icon instructions](../../../AGENTS.md)
and [UI-symbol guidelines](../ui-symbols/NOTICE.md).

`video-play` (Outline), exposed as `Studio`, was added from the same official
`iconsax@0.1.1` package on 2026-09-28 after review in the Outline catalog.

`headphone` (Outline), exposed as `Headphones`, was added from the official
Outline catalog for Huddle entry actions. The original geometry is preserved;
the color is normalized to `currentColor` and the full-viewBox clip is removed.

`document-copy` (Outline), exposed as `Files`, was added from the official Free
Outline catalog on 2026-10-05 for the conversation Artifacts button. The SVG was
obtained from the catalog's SVG code panel. Its path geometry and 24 × 24 viewBox
are preserved; color is normalized to `currentColor` and the full-viewBox clip is
removed.

`message-bubble` and `message-add` (Outline) were added from the official Free
Outline catalog on 2026-10-09 for document comments. The SVGs were obtained
from the catalog's SVG code panel. Their path geometry and 24 × 24 viewBox are
preserved; colors are normalized to `currentColor` and full-viewBox clips are
removed.
