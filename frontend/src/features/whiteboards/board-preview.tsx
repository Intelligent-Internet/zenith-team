import { useEffect, useRef, useState } from "react";
import { apiClient } from "@/lib/api-client";
import { LayoutDashboard } from "@/shared/ui/icons";
import { boardPath, onBoardChange, type BoardScope } from "./board-api";

/**
 * A Whiteboard Artifact's card shows a small image of its Board, loaded once
 * the card nears the view and again shortly after the Board changes. The
 * browser revalidates it by Board sequence, so an unchanged Board is not drawn
 * again. An empty Board, or one that cannot be drawn, keeps the placeholder.
 */
export function BoardPreview({ companyId, boardId }: BoardScope) {
  const containerRef = useRef<HTMLSpanElement>(null);
  const [url, setUrl] = useState<string>();
  useEffect(() => {
    const scope = { companyId, boardId };
    const path = `${boardPath(scope)}/preview`;
    setUrl(undefined);
    let controller: AbortController | undefined;
    let observer: IntersectionObserver | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let objectUrl: string | undefined;
    let started = false;
    const show = (next?: string) => {
      if (objectUrl) URL.revokeObjectURL(objectUrl);
      objectUrl = next;
      setUrl(next);
    };
    const load = async () => {
      started = true;
      observer?.disconnect();
      controller?.abort();
      const current = new AbortController();
      controller = current;
      try {
        const blob = await apiClient.get<Blob | null>(path, {
          responseType: "blob",
          signal: current.signal,
        });
        if (current.signal.aborted) return;
        show(blob?.size ? URL.createObjectURL(blob) : undefined);
      } catch {
        // The placeholder stays; the Board itself opens from the card.
      }
    };
    const stop = onBoardChange(scope, () => {
      if (!started) return;
      clearTimeout(timer);
      timer = setTimeout(() => void load(), 2000);
    });
    if (!containerRef.current || !("IntersectionObserver" in window)) {
      void load();
    } else {
      observer = new IntersectionObserver(
        (entries) => {
          if (entries.some((entry) => entry.isIntersecting)) void load();
        },
        { rootMargin: "100px" },
      );
      observer.observe(containerRef.current);
    }
    return () => {
      stop();
      clearTimeout(timer);
      controller?.abort();
      observer?.disconnect();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [companyId, boardId]);
  return (
    <span
      className="artifact-row__whiteboard"
      data-preview={url ? "" : undefined}
      ref={containerRef}
    >
      {url ? (
        <img
          alt=""
          draggable={false}
          onError={() => setUrl(undefined)}
          src={url}
        />
      ) : (
        <>
          <LayoutDashboard aria-hidden size={24} />
          <span>Whiteboard</span>
        </>
      )}
    </span>
  );
}
