import * as Dialog from "@radix-ui/react-dialog";
import { X } from "@/shared/ui/icons";
import type { ReactNode } from "react";
export function CanvasDialog({
  title,
  description,
  children,
  onClose,
  wide = false,
  canClose = true,
  closeHint,
  headerActions,
  leading,
  embedded = false,
  className = "",
}: {
  title: string;
  description?: string;
  children: ReactNode;
  onClose: () => void;
  wide?: boolean;
  canClose?: boolean;
  closeHint?: string;
  headerActions?: ReactNode;
  leading?: ReactNode;
  embedded?: boolean;
  className?: string;
}) {
  if (embedded)
    return (
      <section
        className={`pc-dialog pc-review-dialog pc-embedded-document ${className}`}
        aria-label={title}
        onKeyDown={(event) => {
          if (event.key === "Escape" && !event.defaultPrevented && canClose) {
            event.preventDefault();
            event.stopPropagation();
            onClose();
          }
        }}
      >
        <header className="pc-dialog-heading">
          {leading}
          <div className="pc-dialog-title">
            <h2>{title}</h2>
            {description && <p>{description}</p>}
          </div>
          {headerActions}
          <button
            className="pc-icon-button"
            aria-label="Close document"
            disabled={!canClose}
            onClick={onClose}
          >
            <X size={20} />
          </button>
        </header>
        {children}
      </section>
    );
  return (
    <Dialog.Root
      open
      onOpenChange={(open) => {
        if (!open && canClose) onClose();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="pc-overlay" />
        <Dialog.Content
          {...(!description ? { "aria-describedby": undefined } : {})}
          className={`pc-dialog ${wide ? "pc-review-dialog" : ""} ${className}`}
          onInteractOutside={(event) => event.preventDefault()}
          onEscapeKeyDown={(event) => {
            if (!canClose) event.preventDefault();
          }}
        >
          <header className="pc-dialog-heading">
            {leading}
            <div className="pc-dialog-title">
              <Dialog.Title>{title}</Dialog.Title>
              {description && (
                <Dialog.Description>{description}</Dialog.Description>
              )}
            </div>
            {headerActions}
            <button
              className="pc-icon-button"
              aria-label="Close"
              title={
                canClose ? "Close" : (closeHint ?? "Waiting for edits to save")
              }
              disabled={!canClose}
              onClick={onClose}
            >
              <X size={20} />
            </button>
          </header>
          {children}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
