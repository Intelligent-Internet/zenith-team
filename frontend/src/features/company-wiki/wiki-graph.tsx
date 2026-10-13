import { useEffect, useMemo, useRef, useState } from "react";
import {
  Focus,
  Plus,
  RefreshCw,
  Search,
  Workflow,
  ZoomIn,
  ZoomOut,
} from "@/shared/ui/icons";
import type {
  WikiCollection,
  WikiGraphData,
  WikiPageSummary,
} from "./wiki-api";
import { WikiGraphScene, type GraphLink } from "./wiki-graph-scene";
import "./wiki-graph.css";

const unfiledColor = "var(--workspace-native-text-faint)";

export function WikiGraph({
  graph,
  collections,
  onOpenPage,
  onCreatePage,
}: {
  graph: WikiGraphData;
  collections: WikiCollection[];
  onOpenPage: (page: WikiPageSummary) => void;
  /** Present only for members who can publish; shows the empty-state CTA. */
  onCreatePage?: () => void;
}) {
  const [search, setSearch] = useState("");
  const [scene, setScene] = useState<WikiGraphScene | null>(null);
  const normalizedSearch = search.trim().toLowerCase();
  const stageRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const focusViewRef = useRef<() => void>(() => {});
  const openPageRef = useRef<(pageId: string) => void>(() => {});
  const hasPages = graph.pages.length > 0;
  const pageById = useMemo(
    () => new Map(graph.pages.map((page) => [page.id, page])),
    [graph.pages],
  );
  // Golden-angle hues keep every collection distinct however many exist; the
  // mid lightness reads on light and dark stages alike.
  const collectionColors = useMemo(
    () =>
      new Map(
        collections.map((collection, index) => [
          collection.id,
          `oklch(0.64 0.11 ${(330 + index * 137.508) % 360})`,
        ]),
      ),
    [collections],
  );
  // Links are undirected in the graph: one edge per linked pair of pages.
  const links = useMemo(() => {
    const pairs = new Map<string, GraphLink>();
    for (const link of graph.links) {
      if (!pageById.has(link.fromPageId) || !pageById.has(link.toPageId))
        continue;
      const [source, target] = [link.fromPageId, link.toPageId].sort();
      if (source !== target)
        pairs.set(`${source}→${target}`, { source, target });
    }
    return [...pairs.values()];
  }, [graph.links, pageById]);
  const matchingPages = graph.pages.filter((page) =>
    page.title.toLowerCase().includes(normalizedSearch),
  );

  useEffect(() => {
    openPageRef.current = (pageId) => {
      const page = pageById.get(pageId);
      if (page) onOpenPage(page);
    };
  }, [pageById, onOpenPage]);

  useEffect(() => {
    const stage = stageRef.current;
    const canvas = canvasRef.current;
    if (!hasPages || !stage || !canvas) return;
    const created = new WikiGraphScene(canvas, stage, {
      onOpen: (pageId) => openPageRef.current(pageId),
      // The viewport moves only on mount, on a stage resize, or when the
      // search changes; data refetches never snap the reader's pan and zoom.
      onResize: () => focusViewRef.current(),
    });
    setScene(created);
    return () => {
      created.destroy();
      setScene(null);
    };
  }, [hasPages]);

  useEffect(() => {
    scene?.setGraph(
      graph.pages.map((page) => ({
        id: page.id,
        title: page.title,
        color:
          (page.collectionId && collectionColors.get(page.collectionId)) ||
          unfiledColor,
        dimmed:
          Boolean(normalizedSearch) &&
          !page.title.toLowerCase().includes(normalizedSearch),
      })),
      links,
    );
  }, [scene, graph.pages, links, collectionColors, normalizedSearch]);

  useEffect(() => {
    focusViewRef.current = () => {
      const match = normalizedSearch
        ? graph.pages.find((page) =>
            page.title.toLowerCase().includes(normalizedSearch),
          )
        : undefined;
      if (match) scene?.centerOn(match.id, 1, 220);
      else scene?.fit(220);
    };
  }, [scene, normalizedSearch, graph.pages]);

  useEffect(() => {
    if (!scene) return;
    const frame = requestAnimationFrame(() => focusViewRef.current());
    return () => cancelAnimationFrame(frame);
  }, [scene, normalizedSearch]);

  return (
    <div className="w-full max-w-[1400px] m-[0_auto] min-w-0">
      <div className="[background:var(--wk-panel)] [border:1px_solid_var(--wk-border)] rounded-[12px] [box-shadow:0_12px_38px_color-mix(in_srgb,_var(--workspace-native-text)_4%,_transparent)] min-w-0 overflow-hidden">
        <div className="min-h-[58px] p-[10px_15px] flex items-center justify-between gap-[12px] [border-bottom:1px_solid_var(--wk-border)]">
          <span className="text-[var(--wk-muted)] text-[length:11px]">
            {graph.pages.length} pages
            {graph.truncated ? " · showing first 500" : ""}
          </span>
          <label className="wk-graph-search flex items-center gap-[7px] text-[var(--wk-muted)] [border:1px_solid_var(--wk-border)] rounded-[7px] p-[6px_9px] min-w-[150px] max-w-[210px]">
            <Search size={15} />
            <input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder="Find a page…"
              aria-label="Find a page in graph"
            />
          </label>
        </div>
        <div
          ref={stageRef}
          className={`wk-graph-stage ${graph.pages.length <= 3 ? "is-sparse" : ""}`}
          role="group"
          aria-label="Wiki page graph"
        >
          {!hasPages ? (
            <div className="zen-wiki__empty-state grid [justify-items:center] gap-[7px] p-[60px_20px] text-center text-[var(--wk-muted)] h-full [align-content:center]">
              <Workflow size={32} />
              <h2>No pages to map yet</h2>
              <p>Pages and the links between them appear here.</p>
              {onCreatePage && (
                <button className="zen-wiki__primary" onClick={onCreatePage}>
                  <Plus size={16} /> New page
                </button>
              )}
            </div>
          ) : (
            <>
              <canvas
                ref={canvasRef}
                className="absolute inset-0 block w-full h-full cursor-grab [touch-action:none]"
                aria-hidden="true"
              />
              {/* The canvas is pointer-only; these buttons give keyboard and
                  screen-reader users the same pages, highlighted on focus. */}
              <ul className="sr-only">
                {graph.pages.map((page) => (
                  <li key={page.id}>
                    <button
                      type="button"
                      onFocus={() => scene?.setFocus(page.id)}
                      onBlur={() => scene?.setFocus(null)}
                      onClick={() => onOpenPage(page)}
                    >
                      Open {page.title}
                    </button>
                  </li>
                ))}
              </ul>
              <span className="absolute z-[5] top-[13px] left-[14px] p-[5px_8px] [border:1px_solid_var(--wk-border)] rounded-[6px] [background:color-mix(in_srgb,_var(--wk-panel)_86%,_transparent)] text-[var(--wk-muted)] text-[length:10px] pointer-events-none">
                Drag to explore · scroll or use + / − to zoom
              </span>
              {normalizedSearch && matchingPages.length === 0 && (
                <span
                  className="absolute z-[5] top-[50%] left-[50%] w-[min(240px,_80%)] [transform:translate(-50%,_-50%)] p-[12px] [border:1px_solid_var(--wk-border)] rounded-[8px] [background:var(--wk-panel)] text-[var(--wk-muted)] text-center text-[length:11px] [box-shadow:var(--workspace-overlay-shadow)]"
                  role="status"
                >
                  No matching page. Try another title.
                </span>
              )}
              <div
                className="wk-graph-controls absolute z-[5] left-[14px] bottom-[14px] flex flex-col [border:1px_solid_var(--wk-border)] rounded-[8px] overflow-hidden [background:var(--wk-panel)] [box-shadow:0_5px_15px_color-mix(in_srgb,_var(--workspace-native-text)_8%,_transparent)]"
                aria-label="Graph controls"
              >
                <button
                  aria-label="Auto arrange"
                  title="Auto arrange"
                  onClick={() => scene?.rearrange()}
                >
                  <RefreshCw size={17} />
                </button>
                <button
                  aria-label="Zoom in"
                  title="Zoom in"
                  onClick={() => scene?.zoomBy(1.2, 180)}
                >
                  <ZoomIn size={17} />
                </button>
                <button
                  aria-label="Zoom out"
                  title="Zoom out"
                  onClick={() => scene?.zoomBy(1 / 1.2, 180)}
                >
                  <ZoomOut size={17} />
                </button>
                <button
                  aria-label="Fit graph"
                  title="Fit graph"
                  onClick={() => scene?.fit(180)}
                >
                  <Focus size={17} />
                </button>
              </div>
            </>
          )}
        </div>
        <div className="wk-graph-legend">
          {collections
            .filter((collection) =>
              graph.pages.some((page) => page.collectionId === collection.id),
            )
            .map((collection) => (
              <span key={collection.id}>
                <i
                  style={{ background: collectionColors.get(collection.id) }}
                />
                {collection.name}
              </span>
            ))}
          {graph.pages.some(
            (page) =>
              !page.collectionId || !collectionColors.has(page.collectionId),
          ) && (
            <span>
              <i style={{ background: unfiledColor }} />
              Unfiled
            </span>
          )}
        </div>
      </div>
    </div>
  );
}
