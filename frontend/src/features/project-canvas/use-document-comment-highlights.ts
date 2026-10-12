import { useId, useLayoutEffect, useState, type RefObject } from "react";
import type { ReviewAnchor } from "./annotation-overlay";
import { quoteRange } from "./quote-highlight";

/** Highlights connect the sequential comment list to its document passages. */
export function useDocumentCommentHighlights(
  content: HTMLDivElement | null,
  rail: RefObject<HTMLElement | null>,
  anchors: { id: string; anchor: ReviewAnchor }[],
  open: boolean,
  onSelect: (id: string) => void,
  activeId: string,
  connectorLayer: RefObject<SVGSVGElement | null>,
) {
  const highlightName = `page-comments-${useId().replace(/[^a-zA-Z0-9-]/g, "")}`;
  const selectedHighlightName = `${highlightName}-selected`;
  const [connectors, setConnectors] = useState<{ id: string; path: string }[]>(
    [],
  );
  useLayoutEffect(() => {
    const root = content;
    const aside = rail.current;
    const list = aside?.querySelector<HTMLElement>(".po-page-comment-cards");
    if (!open || !root || !aside || !list) {
      setConnectors((previous) => (previous.length ? [] : previous));
      return;
    }
    let frame = 0;
    let passages: { id: string; range: Range }[] = [];
    const updateConnectors = () => {
      const layer = connectorLayer?.current;
      if (!layer) return;
      if (!activeId) {
        setConnectors((previous) => (previous.length ? [] : previous));
        return;
      }
      const origin = layer.getBoundingClientRect();
      const viewport = root
        .closest(".po-project-home, .po-page-workspace, .pc-review-body")
        ?.getBoundingClientRect();
      const top = Math.max(0, viewport?.top ?? 0);
      const bottom = Math.min(
        window.innerHeight,
        viewport?.bottom ?? window.innerHeight,
      );
      const left = Math.max(0, viewport?.left ?? 0);
      const right = Math.min(
        window.innerWidth,
        viewport?.right ?? window.innerWidth,
      );
      const inView = (rect: DOMRect) =>
        rect.bottom > top &&
        rect.top < bottom &&
        rect.right > left &&
        rect.left < right;
      const listBounds = list.getBoundingClientRect();
      const next = layer.getClientRects().length
        ? passages
            .filter(({ id }) => id === activeId)
            .flatMap(({ id, range }) => {
              const card = list.querySelector<HTMLElement>(
                `[data-page-comment="${CSS.escape(id)}"]`,
              );
              // Use an actual highlighted line, not the empty area of a wrapped range.
              const quote = Array.from(range.getClientRects())
                .filter((rect) => rect.width && rect.height && inView(rect))
                .sort((a, b) => b.right - a.right)[0];
              if (!card || !quote) return [];
              const target = card.getBoundingClientRect();
              if (
                !inView(quote) ||
                !inView(target) ||
                target.bottom <= listBounds.top ||
                target.top >= listBounds.bottom
              )
                return [];
              const x1 = quote.right - origin.left + 4;
              const y1 = quote.top + quote.height / 2 - origin.top;
              const x2 = target.left - origin.left - 4;
              const y2 =
                target.top + Math.min(24, target.height / 2) - origin.top;
              if (x2 <= x1) return [];
              const bend = (x1 + x2) / 2;
              return [
                {
                  id,
                  path: `M ${x1} ${y1} C ${bend} ${y1}, ${bend} ${y2}, ${x2} ${y2}`,
                },
              ];
            })
        : [];
      setConnectors((previous) =>
        previous.length === next.length &&
        previous.every(
          (line, index) =>
            line.id === next[index].id && line.path === next[index].path,
        )
          ? previous
          : next,
      );
    };
    const measure = () => {
      passages = anchors.flatMap(({ id, anchor }) => {
        if (anchor.type !== "textQuote" && anchor.type !== "block") return [];
        const block = anchor.blockId
          ? root.querySelector<HTMLElement>(
              `[data-block-id="${CSS.escape(anchor.blockId)}"]`,
            )
          : null;
        if (anchor.blockId && !block) return [];
        let range: Range | undefined;
        if (anchor.type === "textQuote" && anchor.exact)
          range = quoteRange(block ?? root, anchor.exact);
        else if (block) {
          range = document.createRange();
          range.selectNodeContents(block);
        }
        return range ? [{ id, range }] : [];
      });
      updateConnectors();
      if (typeof Highlight === "undefined" || !CSS.highlights) return;
      CSS.highlights.set(
        highlightName,
        new Highlight(...passages.map(({ range }) => range)),
      );
      const selected = new Highlight(
        ...passages
          .filter(({ id }) => id === activeId)
          .map(({ range }) => range),
      );
      selected.priority = 1;
      CSS.highlights.set(selectedHighlightName, selected);
    };
    const schedule = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(measure);
    };
    const onScroll = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(updateConnectors);
    };
    const resize = new ResizeObserver(schedule);
    resize.observe(root);
    resize.observe(aside);
    resize.observe(list);
    list
      .querySelectorAll("[data-page-comment]")
      .forEach((card) => resize.observe(card));
    document.addEventListener("scroll", onScroll, true);
    window.addEventListener("resize", schedule);
    const clearSelectionOutside = (event: PointerEvent) => {
      if (!(event.target instanceof Element)) return;
      const card = event.target.closest("[data-page-comment]");
      if (card && aside.contains(card)) return;
      // Clear only the selected thread, leaving unfinished comments intact.
      // A subsequent click on a highlighted passage selects its thread.
      onSelect("");
      const focused = document.activeElement;
      if (focused instanceof HTMLElement && aside.contains(focused))
        focused.blur();
    };
    const selectPassage = (event: MouseEvent) => {
      if (!window.getSelection()?.isCollapsed) return;
      const passage = passages.find(({ range }) =>
        Array.from(range.getClientRects()).some(
          (rect) =>
            event.clientX >= rect.left &&
            event.clientX <= rect.right &&
            event.clientY >= rect.top &&
            event.clientY <= rect.bottom,
        ),
      );
      if (passage && passage.id !== "draft") onSelect(passage.id);
    };
    root.addEventListener("click", selectPassage);
    document.addEventListener("pointerdown", clearSelectionOutside, true);
    const mutation = new MutationObserver(schedule);
    mutation.observe(root, {
      subtree: true,
      childList: true,
      characterData: true,
    });
    measure();
    return () => {
      cancelAnimationFrame(frame);
      root.removeEventListener("click", selectPassage);
      document.removeEventListener("pointerdown", clearSelectionOutside, true);
      CSS.highlights?.delete(highlightName);
      CSS.highlights?.delete(selectedHighlightName);
      mutation.disconnect();
      resize.disconnect();
      document.removeEventListener("scroll", onScroll, true);
      window.removeEventListener("resize", schedule);
    };
  }, [
    anchors,
    content,
    open,
    rail,
    highlightName,
    selectedHighlightName,
    onSelect,
    activeId,
    connectorLayer,
  ]);
  return { highlightName, selectedHighlightName, connectors };
}
