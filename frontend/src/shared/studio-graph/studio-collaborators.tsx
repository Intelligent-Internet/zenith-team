import { memo, useEffect, useRef, type CSSProperties } from "react";
import {
  useReactFlow,
  useStore,
  useViewport,
  ViewportPortal,
} from "@xyflow/react";
import type {
  StudioCollaboratorCursor,
  StudioCursorUpdate,
} from "@/shared/api/studio-collaboration";
import { MousePointer } from "@/shared/ui/icons";
import "./studio-collaborators.css";

function cursorColor(userId: string) {
  let hash = 0;
  for (const char of userId) hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  return `hsl(${hash % 360} 68% 44%)`;
}

function initials(name: string) {
  const words = name.trim().split(/\s+/);
  return words
    .slice(0, 2)
    .map((word) => word.slice(0, 1))
    .join("")
    .toLocaleUpperCase();
}

export const StudioCollaborators = memo(function StudioCollaborators({
  cursors,
  emitCursor,
  connected,
}: {
  cursors: StudioCollaboratorCursor[];
  emitCursor: (cursor: StudioCursorUpdate) => boolean;
  connected: boolean;
}) {
  const { screenToFlowPosition } = useReactFlow();
  const canvas = useStore((state) => state.domNode);
  const { x, y, zoom } = useViewport();
  const scheduleRef = useRef<(() => void) | null>(null);

  useEffect(() => {
    if (!canvas || !connected) return;
    let pointer: { x: number; y: number } | null = null;
    let visible = false;
    let lastEmittedAt = 0;
    let trailingTimer: ReturnType<typeof setTimeout> | undefined;

    const publish = () => {
      trailingTimer = undefined;
      if (!pointer || document.hidden || !document.hasFocus()) return;
      const position = screenToFlowPosition(pointer, { snapToGrid: false });
      if (
        emitCursor({
          x: Math.round(position.x * 10) / 10,
          y: Math.round(position.y * 10) / 10,
          visible: true,
        })
      ) {
        visible = true;
        lastEmittedAt = performance.now();
      }
    };
    const schedule = () => {
      if (!pointer || trailingTimer !== undefined) return;
      const remaining = 65 - (performance.now() - lastEmittedAt);
      if (remaining <= 0) publish();
      else trailingTimer = setTimeout(publish, remaining);
    };
    const hide = () => {
      clearTimeout(trailingTimer);
      trailingTimer = undefined;
      pointer = null;
      if (visible) emitCursor({ visible: false });
      visible = false;
    };
    const move = (event: PointerEvent) => {
      const target = event.target;
      if (
        event.pointerType === "touch" ||
        (target instanceof Element &&
          target.closest(
            ".react-flow__panel, .react-flow__minimap, .studio-graph__context",
          ))
      ) {
        hide();
        return;
      }
      pointer = { x: event.clientX, y: event.clientY };
      schedule();
    };
    const visibility = () => {
      if (document.hidden) hide();
    };

    scheduleRef.current = schedule;
    canvas.addEventListener("pointermove", move, true);
    canvas.addEventListener("pointerleave", hide);
    window.addEventListener("blur", hide);
    document.addEventListener("visibilitychange", visibility);
    // Keep a stationary collaborator visible without storing presence as data.
    const heartbeat = setInterval(schedule, 4_000);

    return () => {
      hide();
      clearInterval(heartbeat);
      scheduleRef.current = null;
      canvas.removeEventListener("pointermove", move, true);
      canvas.removeEventListener("pointerleave", hide);
      window.removeEventListener("blur", hide);
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [canvas, connected, emitCursor, screenToFlowPosition]);

  useEffect(() => {
    // A stationary screen pointer changes canvas coordinates while panning.
    scheduleRef.current?.();
  }, [x, y, zoom]);

  if (!connected || !cursors.length) return null;

  return (
    <ViewportPortal>
      {cursors.map((cursor) => {
        const name = cursor.name || "Collaborator";
        const style = {
          "--studio-cursor-color": cursorColor(cursor.userId),
          transform: `translate(${cursor.x}px, ${cursor.y}px)`,
        } as CSSProperties;
        return (
          <div
            key={cursor.cursorId}
            className="studio-collaborator-cursor nodrag nopan nowheel"
            style={style}
            aria-hidden="true"
          >
            <div
              className="studio-collaborator-cursor__content"
              style={{ transform: `scale(${1 / Math.max(zoom, 0.1)})` }}
            >
              <MousePointer
                className="studio-collaborator-cursor__icon"
                size={23}
              />
              <span className="studio-collaborator-cursor__label">
                <span className="studio-collaborator-cursor__avatar">
                  {cursor.avatar ? (
                    <img src={cursor.avatar} alt="" draggable={false} />
                  ) : (
                    initials(name)
                  )}
                </span>
                <span className="studio-collaborator-cursor__name">{name}</span>
              </span>
            </div>
          </div>
        );
      })}
    </ViewportPortal>
  );
});
