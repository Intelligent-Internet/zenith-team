import type { CSSProperties, ReactNode } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { XIcon } from "@/shared/ui/icons";
import { cn } from "@/shared/ui/core/cn";
import "@/features/common/styles/personal-tools.css";

export function PersonalToolsDialog({
  title,
  description,
  onClose,
  children,
  className,
  overlayClassName,
  style,
  headingActions,
}: {
  title: string;
  description: string;
  onClose: () => void;
  children: ReactNode;
  className?: string;
  overlayClassName?: string;
  style?: CSSProperties;
  headingActions?: ReactNode;
}) {
  return (
    <Dialog.Root
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <Dialog.Portal
        container={document.querySelector(".boardly-workspace-theme")}
      >
        <Dialog.Overlay
          className={cn("personal-dialog-overlay", overlayClassName)}
        />
        <Dialog.Content
          className={cn("personal-dialog", className)}
          style={style}
        >
          <header className="personal-dialog-heading">
            <div>
              <Dialog.Title>{title}</Dialog.Title>
              <Dialog.Description>{description}</Dialog.Description>
            </div>
            {headingActions}
            <Dialog.Close className="personal-icon-button" aria-label="Close">
              <XIcon size={20} />
            </Dialog.Close>
          </header>
          {children}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
