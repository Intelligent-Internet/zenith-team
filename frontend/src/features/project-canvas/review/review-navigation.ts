import type { components } from "@/shared/api/generated/api.generated";
import { quoteRange } from "./quote-highlight";

type ReviewAnchor = components["schemas"]["CanvasAnchor"];

// Move only the reading pane, keeping the comment rail and dialog in place.
export function centerReviewRegion(container: HTMLElement, region: DOMRect) {
  const bounds = container.getBoundingClientRect();
  container.scrollTo({
    top:
      container.scrollTop +
      region.top -
      bounds.top -
      Math.max(24, (container.clientHeight - region.height) / 2),
    left:
      container.scrollLeft +
      region.left -
      bounds.left -
      Math.max(24, (container.clientWidth - region.width) / 2),
    behavior: "instant",
  });
}

export function scrollToReviewAnchor(root: HTMLElement, anchor: ReviewAnchor) {
  if (anchor.type === "media" || root.querySelector(".pc-viewer-loading"))
    return false;
  // The image camera owns version-level navigation as well as media regions.
  if (anchor.type === "version" && root.querySelector(".pc-image-viewport"))
    return true;
  let container =
    root.querySelector<HTMLElement>(".pc-media-stage") ??
    root.querySelector<HTMLElement>(
      ".pc-file-content, .pc-reader-scroll, .pc-block-scroll",
    );
  // Page/Overview editors grow with their content; Canvas editors scroll inside
  // the document. Navigate the actual scroll owner in either layout.
  while (
    container &&
    !/auto|scroll/.test(getComputedStyle(container).overflowY)
  ) {
    container = container.parentElement;
  }
  if (!container) return false;
  if (anchor.type === "block") {
    if (!anchor.blockId) return false;
    const block = root.querySelector<HTMLElement>(
      `[data-block-id="${CSS.escape(anchor.blockId)}"]`,
    );
    if (!block) return false;
    centerReviewRegion(container, block.getBoundingClientRect());
  } else if (anchor.type === "textQuote") {
    if (!anchor.exact) return false;
    const block = anchor.blockId
      ? root.querySelector<HTMLElement>(
          `[data-block-id="${CSS.escape(anchor.blockId)}"]`,
        )
      : null;
    if (anchor.blockId && !block) return false;
    const range = quoteRange(block ?? root, anchor.exact);
    const region = range?.getClientRects()[0];
    if (!region) return false;
    centerReviewRegion(container, region);
  } else {
    container.scrollTo({
      top: anchor.type === "point" ? (anchor.scrollTop ?? 0) : 0,
      behavior: "instant",
    });
  }
  return true;
}

const pendingReveals = new WeakMap<HTMLElement, number>();

/** Reveal a card inside the sidebar without ever scrolling the document. */
export function revealMarginComment(rail: HTMLElement | null, id: string) {
  if (!rail) return;
  const pending = pendingReveals.get(rail);
  if (pending !== undefined) cancelAnimationFrame(pending);
  pendingReveals.set(
    rail,
    requestAnimationFrame(() => {
      pendingReveals.delete(rail);
      if (!rail.isConnected || rail.hidden) return;
      const list = rail.querySelector<HTMLElement>(".po-page-comment-cards");
      const card = list?.querySelector<HTMLElement>(
        `[data-page-comment="${CSS.escape(id)}"]`,
      );
      if (!list || !card) return;
      const bounds = list.getBoundingClientRect();
      const target = card.getBoundingClientRect();
      const top = bounds.top + list.clientTop + 8;
      const bottom = bounds.top + list.clientHeight - 8;
      const offset =
        target.top < top
          ? target.top - top
          : target.bottom > bottom
            ? Math.max(0, Math.min(target.top - top, target.bottom - bottom))
            : 0;
      if (offset) list.scrollBy({ top: offset, behavior: "instant" });
    }),
  );
}
