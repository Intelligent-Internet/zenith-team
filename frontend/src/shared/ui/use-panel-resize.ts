import { useEffect, useRef, useState, type RefObject } from "react";
import type { KeyboardEvent, PointerEvent } from "react";

export function usePanelResize(
  panelRef: RefObject<HTMLDivElement | null>,
  {
    side,
    containerRef,
    mode = "panel",
    visible = true,
    defaultWidth,
    minWidth,
    remainingWidth,
    widthLimit = Infinity,
    label,
  }: {
    side: "left" | "right";
    containerRef?: RefObject<HTMLElement | null>;
    mode?: string;
    visible?: boolean;
    defaultWidth: number | ((containerWidth: number) => number);
    minWidth: number;
    remainingWidth: number;
    widthLimit?: number;
    label: string;
  },
) {
  const [widths, setWidths] = useState<Record<string, number | undefined>>({});
  const [containerWidth, setContainerWidth] = useState(0);
  const [resizing, setResizing] = useState(false);
  const drag = useRef<{ pointerId: number; x: number; width: number } | null>(
    null,
  );
  const direction = side === "left" ? 1 : -1;
  const maxWidth = Math.max(
    minWidth,
    Math.min(widthLimit, containerWidth - remainingWidth),
  );
  const clamp = (value: number) =>
    Math.min(maxWidth, Math.max(minWidth, value));
  const width = clamp(
    widths[mode] ??
      (typeof defaultWidth === "function"
        ? defaultWidth(containerWidth)
        : defaultWidth),
  );

  useEffect(() => {
    const container = containerRef?.current ?? panelRef.current?.parentElement;
    if (!container) return;
    const observer = new ResizeObserver(() => {
      setContainerWidth(container.clientWidth);
    });
    observer.observe(container);
    return () => observer.disconnect();
  }, [containerRef, panelRef, visible]);

  useEffect(() => {
    if (!resizing) return;
    const { cursor, userSelect } = document.body.style;
    document.body.style.cursor = "col-resize";
    document.body.style.userSelect = "none";
    return () => {
      document.body.style.cursor = cursor;
      document.body.style.userSelect = userSelect;
    };
  }, [resizing]);

  useEffect(() => {
    drag.current = null;
    setResizing(false);
  }, [visible, mode]);

  const updateWidth = (value: number) => {
    setWidths((current) => ({ ...current, [mode]: clamp(value) }));
  };
  const endDrag = (event: PointerEvent<HTMLDivElement>) => {
    if (drag.current?.pointerId !== event.pointerId) return;
    drag.current = null;
    setResizing(false);
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
  };

  return {
    width,
    resizing,
    separatorProps: {
      role: "separator",
      tabIndex: 0,
      "aria-label": label,
      "aria-orientation": "vertical" as const,
      "aria-valuemin": minWidth,
      "aria-valuemax": Math.round(maxWidth),
      "aria-valuenow": Math.round(width),
      "aria-valuetext": `${Math.round(width)} pixels wide`,
      onPointerDown: (event: PointerEvent<HTMLDivElement>) => {
        if (event.button !== 0 || !event.isPrimary) return;
        event.preventDefault();
        event.currentTarget.focus();
        event.currentTarget.setPointerCapture(event.pointerId);
        drag.current = { pointerId: event.pointerId, x: event.clientX, width };
        setResizing(true);
      },
      onPointerMove: (event: PointerEvent<HTMLDivElement>) => {
        const session = drag.current;
        if (session?.pointerId !== event.pointerId) return;
        updateWidth(session.width + direction * (event.clientX - session.x));
      },
      onPointerUp: endDrag,
      onPointerCancel: endDrag,
      onLostPointerCapture: endDrag,
      onKeyDown: (event: KeyboardEvent<HTMLDivElement>) => {
        const step = event.shiftKey ? 50 : 10;
        const nextWidth = {
          ArrowLeft: width - direction * step,
          ArrowRight: width + direction * step,
          Home: minWidth,
          End: maxWidth,
        }[event.key];
        if (nextWidth === undefined) return;
        event.preventDefault();
        event.stopPropagation();
        updateWidth(nextWidth);
      },
      onDoubleClick: () => {
        setWidths((current) => ({ ...current, [mode]: undefined }));
      },
    },
  };
}
