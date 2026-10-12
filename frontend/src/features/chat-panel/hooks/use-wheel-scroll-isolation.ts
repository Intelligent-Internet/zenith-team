import { useCallback, useEffect, useRef, type RefCallback } from "react";

const scrollEdgeTolerance = 1;
const externalScrollLockReleaseDelayMs = 180;

export function useWheelScrollIsolation<T extends HTMLElement>() {
  const boundaryRef = useRef<T | null>(null);
  const cleanupRef = useRef<(() => void) | null>(null);

  const setBoundaryRef = useCallback<RefCallback<T>>((boundary) => {
    if (boundaryRef.current === boundary) {
      return;
    }

    cleanupRef.current?.();
    cleanupRef.current = null;
    boundaryRef.current = boundary;

    if (!boundary) {
      return;
    }

    let externalScrollLockCleanup: (() => void) | null = null;
    let externalScrollLockReleaseTimer: number | null = null;
    const releaseExternalScrollLock = () => {
      if (externalScrollLockReleaseTimer !== null) {
        window.clearTimeout(externalScrollLockReleaseTimer);
        externalScrollLockReleaseTimer = null;
      }
      externalScrollLockCleanup?.();
      externalScrollLockCleanup = null;
    };
    const holdExternalScrollLock = () => {
      externalScrollLockCleanup ??=
        lockExternalScrollableElements(boundary);
      if (externalScrollLockReleaseTimer !== null) {
        window.clearTimeout(externalScrollLockReleaseTimer);
      }
      externalScrollLockReleaseTimer = window.setTimeout(
        releaseExternalScrollLock,
        externalScrollLockReleaseDelayMs,
      );
    };
    const handleWheel = (event: WheelEvent) => {
      event.stopPropagation();
      holdExternalScrollLock();

      if (!canConsumeWheelWithinBoundary(event, boundary)) {
        event.preventDefault();
      }
    };

    boundary.addEventListener("wheel", handleWheel, { passive: false });
    cleanupRef.current = () => {
      boundary.removeEventListener("wheel", handleWheel);
      releaseExternalScrollLock();
    };
  }, []);

  useEffect(
    () => () => {
      cleanupRef.current?.();
      cleanupRef.current = null;
      boundaryRef.current = null;
    },
    [],
  );

  return setBoundaryRef;
}

function canConsumeWheelWithinBoundary(
  event: WheelEvent,
  boundary: HTMLElement,
) {
  const useHorizontalAxis =
    Math.abs(event.deltaX) > Math.abs(event.deltaY) ||
    (event.shiftKey && event.deltaX === 0);
  const delta = useHorizontalAxis
    ? event.deltaX || event.deltaY
    : event.deltaY;
  const axis = useHorizontalAxis ? "horizontal" : "vertical";
  let element =
    event.target instanceof Element ? event.target : boundary;

  while (element instanceof HTMLElement) {
    if (canElementConsumeWheel(element, axis, delta)) {
      return true;
    }
    if (element === boundary) {
      break;
    }
    element = element.parentElement ?? boundary;
  }

  return false;
}

function canElementConsumeWheel(
  element: HTMLElement,
  axis: "horizontal" | "vertical",
  delta: number,
) {
  if (delta === 0) {
    return false;
  }

  const style = window.getComputedStyle(element);
  const overflow =
    axis === "horizontal" ? style.overflowX : style.overflowY;
  if (!isScrollableOverflow(overflow)) {
    return false;
  }

  const current =
    axis === "horizontal" ? element.scrollLeft : element.scrollTop;
  const clientSize =
    axis === "horizontal" ? element.clientWidth : element.clientHeight;
  const scrollSize =
    axis === "horizontal" ? element.scrollWidth : element.scrollHeight;
  const maximum = scrollSize - clientSize;

  if (maximum <= scrollEdgeTolerance) {
    return false;
  }

  return delta < 0
    ? current > scrollEdgeTolerance
    : current < maximum - scrollEdgeTolerance;
}

function isScrollableOverflow(overflow: string) {
  return overflow === "auto" || overflow === "scroll" || overflow === "overlay";
}

function lockExternalScrollableElements(boundary: HTMLElement) {
  const lockedElements = Array.from(
    boundary.ownerDocument.querySelectorAll<HTMLElement>("*"),
  ).flatMap((element) => {
    if (boundary.contains(element)) {
      return [];
    }

    const style = window.getComputedStyle(element);
    const canScrollHorizontally =
      isScrollableOverflow(style.overflowX) &&
      element.scrollWidth - element.clientWidth > scrollEdgeTolerance;
    const canScrollVertically =
      isScrollableOverflow(style.overflowY) &&
      element.scrollHeight - element.clientHeight > scrollEdgeTolerance;
    if (!canScrollHorizontally && !canScrollVertically) {
      return [];
    }

    const initialScrollLeft = element.scrollLeft;
    const initialScrollTop = element.scrollTop;
    const overflowX = readInlineStyle(element, "overflow-x");
    const overflowY = readInlineStyle(element, "overflow-y");
    const restoreScrollPosition = () => {
      if (canScrollHorizontally) {
        element.scrollLeft = initialScrollLeft;
      }
      if (canScrollVertically) {
        element.scrollTop = initialScrollTop;
      }
    };

    element.addEventListener("scroll", restoreScrollPosition, {
      passive: true,
    });
    if (canScrollHorizontally) {
      element.style.setProperty("overflow-x", "hidden", "important");
    }
    if (canScrollVertically) {
      element.style.setProperty("overflow-y", "hidden", "important");
    }

    return [
      () => {
        element.removeEventListener("scroll", restoreScrollPosition);
        restoreInlineStyle(element, "overflow-x", overflowX);
        restoreInlineStyle(element, "overflow-y", overflowY);
        restoreScrollPosition();
      },
    ];
  });

  return () => {
    for (const unlockElement of lockedElements) {
      unlockElement();
    }
  };
}

function readInlineStyle(element: HTMLElement, property: string) {
  return {
    priority: element.style.getPropertyPriority(property),
    value: element.style.getPropertyValue(property),
  };
}

function restoreInlineStyle(
  element: HTMLElement,
  property: string,
  inlineStyle: ReturnType<typeof readInlineStyle>,
) {
  if (inlineStyle.value) {
    element.style.setProperty(
      property,
      inlineStyle.value,
      inlineStyle.priority,
    );
    return;
  }

  element.style.removeProperty(property);
}
