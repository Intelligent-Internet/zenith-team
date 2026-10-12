import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { ArrowLeft, X } from "@/shared/ui/icons";
import { Icon } from "@/shared/ui/icon";

/** Shared right-panel chrome for saved outputs, uploads and workspace files. */
export function FileViewerShell({
  title,
  label = `${title} file preview`,
  backLabel = "Back to artifacts",
  closeLabel = "Close file preview",
  children,
  actions,
  onBack,
  onClose,
  type,
}: {
  title: string;
  label?: string;
  backLabel?: string;
  closeLabel?: string;
  children: ReactNode;
  actions?: ReactNode;
  onBack?: () => void;
  onClose?: () => void;
  type?: string;
}) {
  const [fullscreen, setFullscreen] = useState(false);
  const slotRef = useRef<HTMLDivElement>(null);
  const sectionRef = useRef<HTMLElement>(null);
  const [host] = useState(() => {
    if (typeof document === "undefined") return null;
    const element = document.createElement("div");
    element.style.display = "contents";
    return element;
  });
  useLayoutEffect(() => {
    if (!host) return;
    const parent = fullscreen ? document.body : slotRef.current;
    parent?.append(host);
    sectionRef.current
      ?.querySelector<HTMLButtonElement>("[data-fullscreen-toggle]")
      ?.focus({ preventScroll: true });
    return () => host.remove();
  }, [fullscreen, host]);
  const viewer = (
    <section
      ref={sectionRef}
      aria-label={label}
      className={`artifact-viewer${fullscreen ? " file-viewer--fullscreen" : ""}`}
      data-type={type}
      role={fullscreen ? "dialog" : undefined}
      aria-modal={fullscreen || undefined}
      onKeyDown={(event) => {
        // Menus and dialogs may be React children but have their own portals.
        if (
          !event.currentTarget.contains(event.target as Node) ||
          event.defaultPrevented
        )
          return;
        if (fullscreen && event.key === "Tab") {
          event.stopPropagation();
          const controls = Array.from(
            event.currentTarget.querySelectorAll<HTMLElement>(
              'button:not(:disabled), a[href], input, select, textarea, summary, [tabindex="0"]',
            ),
          ).filter((element) => element.getClientRects().length > 0);
          const first = controls[0];
          const last = controls.at(-1);
          if (event.shiftKey && document.activeElement === first) {
            event.preventDefault();
            last?.focus();
          } else if (!event.shiftKey && document.activeElement === last) {
            event.preventDefault();
            first?.focus();
          }
          return;
        }
        if (event.key !== "Escape" || document.fullscreenElement) return;
        event.preventDefault();
        event.stopPropagation();
        if (fullscreen) setFullscreen(false);
        else (onBack ?? onClose)?.();
      }}
    >
      <header className="artifact-viewer__header">
        {onBack ? (
          <button
            aria-label={backLabel}
            title={backLabel}
            className="artifacts-icon-button"
            onClick={onBack}
            type="button"
          >
            <ArrowLeft aria-hidden size={18} />
          </button>
        ) : null}
        <div className="artifact-viewer__heading">
          <h2 title={title}>{title}</h2>
        </div>
        {actions}
        <button
          data-fullscreen-toggle
          className="artifacts-icon-button"
          aria-label={fullscreen ? "Exit fullscreen" : "View file fullscreen"}
          title={fullscreen ? "Exit fullscreen" : "View file fullscreen"}
          onClick={() => setFullscreen((value) => !value)}
          type="button"
        >
          <Icon
            aria-hidden
            name={fullscreen ? "minimize" : "expand"}
            size={18}
          />
        </button>
        {onClose ? (
          <button
            aria-label={closeLabel}
            title={closeLabel}
            className="artifacts-icon-button"
            onClick={onClose}
            type="button"
          >
            <X aria-hidden size={18} />
          </button>
        ) : null}
      </header>
      {children}
    </section>
  );
  // Move one portal host rather than remounting the preview: PDF page, zoom,
  // playback and loaded workspace bytes survive fullscreen transitions.
  return host ? (
    <>
      <div ref={slotRef} className="file-viewer-slot" />
      {createPortal(viewer, host)}
    </>
  ) : (
    viewer
  );
}
