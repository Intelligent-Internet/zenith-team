import type {
  DesktopWebsitePreviewBridge,
  DesktopWebsitePreviewState,
} from "../../../../electron/shared/desktop-bridge.types";
import {
  ArrowLeft,
  ArrowRight,
  Globe,
  LoaderCircle,
  RotateCw,
} from "@/shared/ui/icons";
import { useEffect, useRef, useState, type ReactNode } from "react";

export function DesktopWebsitePreview({
  bridge,
  url,
  title,
  onEscape,
  onUrlChange,
  actions,
}: {
  actions?: ReactNode;
  bridge: DesktopWebsitePreviewBridge;
  url: string;
  title: string;
  onEscape: () => void;
  onUrlChange: (url: string) => void;
}) {
  const hostRef = useRef<HTMLDivElement>(null);
  const previewIdRef = useRef<string | null>(null);
  const connectedRef = useRef(false);
  const callbacksRef = useRef({ onEscape, onUrlChange });
  callbacksRef.current = { onEscape, onUrlChange };
  const syncBoundsRef = useRef(() => {});
  const [state, setState] = useState<DesktopWebsitePreviewState | null>(null);
  const [attempt, setAttempt] = useState(0);
  const errorRef = useRef<string | null>(null);
  errorRef.current = state?.error ?? null;

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    const id = crypto.randomUUID();
    previewIdRef.current = id;
    connectedRef.current = false;
    errorRef.current = null;
    setState(null);
    let cancelled = false;
    let receivedState = false;
    const update = (next: DesktopWebsitePreviewState) => {
      if (cancelled) return;
      setState(next);
      callbacksRef.current.onUrlChange(next.url);
    };
    const unsubscribe = bridge.onEvent((event) => {
      if (event.type === "escape" && event.id === id)
        callbacksRef.current.onEscape();
      if (event.type === "state" && event.state.id === id) {
        receivedState = true;
        update(event.state);
      }
    });
    const observer = observeWebsitePreviewBounds(host, (bounds) => {
      if (connectedRef.current)
        bridge.setBounds(id, errorRef.current ? null : bounds);
    });
    syncBoundsRef.current = observer.sync;

    const failed = (reason: string) =>
      update({
        id,
        url,
        title,
        loading: false,
        error: reason,
        canGoBack: false,
        canGoForward: false,
      });
    void bridge
      .open({ id, url })
      .then((result) => {
        if (cancelled) return;
        if (!result.ok) {
          failed(result.reason);
          return;
        }
        connectedRef.current = true;
        if (!receivedState) update(result.state);
        observer.sync();
      })
      .catch(() =>
        failed("The website preview could not be opened. Try again."),
      );

    return () => {
      cancelled = true;
      connectedRef.current = false;
      unsubscribe();
      observer.dispose();
      bridge.close(id);
      previewIdRef.current = null;
    };
  }, [bridge, url, title, attempt]);

  useEffect(() => syncBoundsRef.current(), [state?.error]);

  const command = (action: "back" | "forward" | "reload" | "focus") => {
    const id = previewIdRef.current;
    if (connectedRef.current && id) bridge.command(id, action);
    else if (action === "reload") setAttempt((value) => value + 1);
  };
  const loading = !state || state.loading;
  const currentUrl = state?.url ?? url;
  return (
    <div className="desktop-website-preview">
      <div className="desktop-website-preview__address-bar flex min-h-[42px] shrink-0 items-center gap-[7px] [border-bottom:1px_solid_var(--workspace-divider,_#dedbd2)] p-[5px_10px] text-[var(--workspace-native-text-muted,_#697168)]">
        <NavigationButton
          label="Go back"
          disabled={!state?.canGoBack}
          onClick={() => command("back")}
        >
          <ArrowLeft aria-hidden size={15} />
        </NavigationButton>
        <NavigationButton
          label="Go forward"
          disabled={!state?.canGoForward}
          onClick={() => command("forward")}
        >
          <ArrowRight aria-hidden size={15} />
        </NavigationButton>
        {loading ? (
          <LoaderCircle aria-hidden className="artifact-spinner" size={15} />
        ) : (
          <Globe aria-hidden size={15} />
        )}
        <span title={currentUrl}>{currentUrl}</span>
        <NavigationButton
          label="Reload website preview"
          onClick={() => command("reload")}
        >
          <RotateCw aria-hidden size={15} />
        </NavigationButton>
        {actions}
      </div>
      <div
        aria-label={`Website preview: ${title}`}
        aria-busy={loading}
        className="desktop-website-preview__native"
        onFocus={() => {
          if (!state?.error) command("focus");
        }}
        ref={hostRef}
        role="region"
        tabIndex={state?.error ? -1 : 0}
      >
        {state?.error ? (
          <div className="desktop-website-preview__error" role="alert">
            <Globe aria-hidden size={24} />
            <strong>Website unavailable</strong>
            <p>{state.error}</p>
            <button
              className="artifact-action-button"
              onClick={() => command("reload")}
              type="button"
            >
              <RotateCw aria-hidden size={15} /> Retry
            </button>
          </div>
        ) : loading ? (
          <span className="sr-only" role="status">
            Loading website preview…
          </span>
        ) : null}
      </div>
    </div>
  );
}

function NavigationButton({
  children,
  disabled,
  label,
  onClick,
}: {
  children: ReactNode;
  disabled?: boolean;
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      aria-label={label}
      className="artifacts-icon-button"
      disabled={disabled}
      onClick={onClick}
      title={label}
      type="button"
    >
      {children}
    </button>
  );
}

function observeWebsitePreviewBounds(
  host: HTMLElement,
  onBounds: (
    bounds: { x: number; y: number; width: number; height: number } | null,
  ) => void,
) {
  let frame = 0;
  let previous = "";
  function measure() {
    frame = 0;
    const rect = host.getBoundingClientRect();
    const x = Math.max(0, rect.left);
    const y = Math.max(0, rect.top);
    const right = Math.min(window.innerWidth, rect.right);
    const bottom = Math.min(window.innerHeight, rect.bottom);
    let visible =
      !document.hidden &&
      host.checkVisibility() &&
      !host.closest('[inert], [aria-hidden="true"]') &&
      right > x &&
      bottom > y;
    // A native child view sits above the renderer. Hide it while React overlays
    // cover its slot, so dialogs and menus remain visible and interactive.
    if (visible) {
      const overlays = document.querySelectorAll(
        '[role="dialog"], [role="menu"], [data-radix-popper-content-wrapper]',
      );
      visible = !Array.from(overlays).some((overlay) => {
        if (overlay.contains(host) || !overlay.checkVisibility()) return false;
        const bounds = overlay.getBoundingClientRect();
        return (
          bounds.left < right &&
          bounds.right > x &&
          bounds.top < bottom &&
          bounds.bottom > y
        );
      });
    }
    if (visible) {
      // Rounded workspace corners clip the extreme pixels without covering
      // the preview. Probe inside the slot; overlay rectangles are checked above.
      const insetX = Math.min(16, (right - x) / 4);
      const insetY = Math.min(16, (bottom - y) / 4);
      visible = [
        [x + insetX, y + insetY],
        [right - insetX, y + insetY],
        [(x + right) / 2, (y + bottom) / 2],
        [right - insetX, bottom - insetY],
      ].every(([left, top]) => {
        const element = document.elementFromPoint(left, top);
        return element !== null && host.contains(element);
      });
    }
    const bounds = visible
      ? { x, y, width: right - x, height: bottom - y }
      : null;
    const serialized = JSON.stringify(bounds);
    if (serialized !== previous) {
      previous = serialized;
      onBounds(bounds);
    }
  }
  function schedule() {
    if (!frame) frame = requestAnimationFrame(measure);
  }
  const resize = new ResizeObserver(schedule);
  resize.observe(host);
  const mutations = new MutationObserver(schedule);
  mutations.observe(document.body, {
    childList: true,
    subtree: true,
    attributes: true,
    attributeFilter: [
      "style",
      "class",
      "hidden",
      "inert",
      "aria-hidden",
      "open",
      "data-state",
    ],
  });
  window.addEventListener("resize", schedule);
  window.addEventListener("scroll", schedule, true);
  document.addEventListener("visibilitychange", schedule);
  document.addEventListener("transitionend", schedule, true);
  return {
    sync() {
      previous = "";
      schedule();
    },
    dispose() {
      cancelAnimationFrame(frame);
      resize.disconnect();
      mutations.disconnect();
      window.removeEventListener("resize", schedule);
      window.removeEventListener("scroll", schedule, true);
      document.removeEventListener("visibilitychange", schedule);
      document.removeEventListener("transitionend", schedule, true);
    },
  };
}
