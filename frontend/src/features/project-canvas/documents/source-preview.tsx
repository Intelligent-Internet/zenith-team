import { measureUIFrame, uiNow } from "./intelligent-ui/performance";
import {
  IntelligentPreviewInteractionContext,
  InlineStreamingContext,
} from "./intelligent-ui/document-context";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@/shared/ui/core/dialog";
import {
  usePageSourcePreviewQuery,
  type PageReviewScope,
} from "../api/page-api";
import type { components } from "@/shared/api/generated/api.generated";
import { BlockDocument } from "./block-editor-schema";

type Generation = components["schemas"]["PagePreviewGeneration"];

type Block = components["schemas"]["CanvasBlock"];
// Preview-only keys isolate runtime scopes from saved Page block identities.
function previewKeys(node: Block, generation: string, path = "root"): Block {
  return {
    ...node,
    attrs: { ...node.attrs, blockId: `preview:${generation}:${path}` },
    content: node.content?.map((child, index) =>
      previewKeys(child, generation, `${path}.${index}`),
    ),
  };
}

export function SourcePreview({
  scope,
  generationId,
}: {
  scope: PageReviewScope;
  generationId: string;
}) {
  const query = usePageSourcePreviewQuery({ ...scope, generationId });
  const [expanded, setExpanded] = useState(false);
  const expandButton = useRef<HTMLButtonElement>(null);
  const dialogContent = useRef<HTMLDivElement>(null);
  // The portal target stays fixed while its DOM parent changes. React therefore
  // retains the editor and local inline component state across expansion.
  const [previewContainer] = useState(() => document.createElement("div"));
  const mountPreview = useCallback(
    (host: HTMLDivElement | null) => {
      if (host) host.appendChild(previewContainer);
    },
    [previewContainer],
  );
  const [retained, setRetained] = useState<Generation>();
  const [now, setNow] = useState(Date.now);
  const current = query.currentData;
  useEffect(() => {
    if (query.isError || current?.discarded) setRetained(undefined);
    else if (current?.preview.content) setRetained(current);
  }, [current, query.isError]);
  useEffect(() => {
    if (!current) return;
    const remaining = Date.parse(current.expiresAt) - Date.now();
    const timer = window.setTimeout(
      () => setNow(Date.now()),
      Math.max(0, remaining),
    );
    return () => clearTimeout(timer);
  }, [current]);
  // Never render a previous query argument's cached content or an expired buffer.
  const valid =
    current?.id === generationId &&
    Date.parse(current.expiresAt) > Math.max(now, Date.now());
  const visible = !query.isError && valid && !current.discarded;
  const content = visible
    ? (current.preview.content ??
      (retained?.id === generationId ? retained.preview.content : undefined))
    : undefined;
  const displayContent = useMemo(
    () => (content ? previewKeys(content, generationId) : undefined),
    [content, generationId],
  );
  const stale = !!content && !current?.preview.content;
  const previewTiming = useRef({
    generationId,
    started: uiNow(),
    measured: false,
  });
  if (previewTiming.current.generationId !== generationId)
    previewTiming.current = { generationId, started: uiNow(), measured: false };
  useEffect(() => {
    const timing = previewTiming.current;
    if (!displayContent || stale || timing.measured) return;
    return measureUIFrame(
      "first-useful-preview",
      timing.started,
      () => previewTiming.current === timing,
      () => {
        timing.measured = true;
      },
    );
  }, [displayContent, stale, generationId]);

  const message = query.isError
    ? "This preview is unavailable. It may have expired or access may have changed."
    : current?.discarded
      ? "The Agent discarded this preview."
      : current && !valid
        ? "This temporary preview has expired."
        : !current
          ? "Loading preview…"
          : stale
            ? "Showing the previous valid preview while the Agent revises it."
            : current.preview.complete
              ? "Draft preview — try the controls locally. Changes still need Page review."
              : "The Agent is still writing. Completed content appears below.";
  const body = displayContent ? (
    <IntelligentPreviewInteractionContext.Provider value={!stale}>
      <InlineStreamingContext.Provider value={!current?.preview.complete}>
        <BlockDocument
          content={displayContent}
          scope={scope}
          mode="comparison"
        />
      </InlineStreamingContext.Provider>
    </IntelligentPreviewInteractionContext.Provider>
  ) : null;
  return (
    <section
      className="my-3 min-w-0 rounded-workspace-control border border-workspace-divider p-3"
      aria-label="Temporary Page preview"
    >
      <header className="mb-2 flex items-start justify-between gap-3">
        <div>
          <h4 className="text-sm font-medium">Page preview</h4>
          <p className="text-xs text-workspace-muted" role="status">
            {message}
          </p>
        </div>
        {content && (
          <button
            ref={expandButton}
            type="button"
            className="shrink-0 rounded-workspace-control px-2 py-1 text-sm underline focus-visible:outline-2"
            onClick={() => setExpanded(true)}
          >
            Expand
          </button>
        )}
      </header>
      {query.isError && (
        <button
          type="button"
          className="text-sm underline"
          onClick={() => void query.refetch()}
        >
          Try again
        </button>
      )}
      <div
        className="max-h-96 overflow-auto"
        ref={expanded ? undefined : mountPreview}
      />
      {createPortal(
        <div
          onKeyDown={(event) => {
            if (
              !expanded ||
              event.key !== "Tab" ||
              event.defaultPrevented ||
              !dialogContent.current
            )
              return;
            // Portal events follow their React owner, so relay Tab to the
            // existing dialog focus scope for its first/last-element handling.
            const tab = new KeyboardEvent("keydown", {
              key: event.key,
              code: event.code,
              shiftKey: event.shiftKey,
              altKey: event.altKey,
              ctrlKey: event.ctrlKey,
              metaKey: event.metaKey,
              bubbles: true,
              cancelable: true,
            });
            dialogContent.current.dispatchEvent(tab);
            if (tab.defaultPrevented) event.preventDefault();
            event.stopPropagation();
          }}
        >
          {body}
        </div>,
        previewContainer,
      )}
      {visible && current.preview.diagnostics.length > 0 && (
        <p className="mt-2 text-xs text-workspace-muted">
          Some content is waiting for the Agent to finish or repair it.
        </p>
      )}
      <Dialog open={expanded} onOpenChange={setExpanded}>
        <DialogContent
          ref={dialogContent}
          className="max-h-[90dvh] overflow-auto sm:max-w-5xl"
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            expandButton.current?.focus();
          }}
          onInteractOutside={(event) => {
            // React portal events follow the stable inline owner. The content
            // still belongs to the modal when its actual DOM is inside it.
            if (
              event.target instanceof Node &&
              previewContainer.contains(event.target)
            )
              event.preventDefault();
          }}
        >
          <DialogTitle>Page preview</DialogTitle>
          <DialogDescription>{message}</DialogDescription>
          <div ref={expanded ? mountPreview : undefined} />
        </DialogContent>
      </Dialog>
    </section>
  );
}
