import * as HoverCard from "@radix-ui/react-hover-card";
import { useId, useState, type ReactElement, type ReactNode } from "react";
import { cn } from "./core/cn";
import "./tooltip.css";

export function Tooltip({
  children,
  label,
  disabled = false,
  open: controlledOpen,
  onOpenChange,
  contentClassName,
  closeDelay = 0,
  openDelay = 250,
  side = "bottom",
}: {
  children: ReactElement;
  label: ReactNode;
  disabled?: boolean;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  contentClassName?: string;
  closeDelay?: number;
  openDelay?: number;
  /** Where it opens; it flips to the other side when there is no room. */
  side?: "top" | "right" | "bottom" | "left";
}) {
  const id = useId();
  const [internalOpen, setInternalOpen] = useState(false);
  const open = controlledOpen ?? internalOpen;
  const setOpen = (nextOpen: boolean) => {
    if (controlledOpen === undefined) setInternalOpen(nextOpen);
    onOpenChange?.(nextOpen);
  };

  return (
    <HoverCard.Root
      open={open && !disabled}
      onOpenChange={setOpen}
      openDelay={openDelay}
      closeDelay={closeDelay}
    >
      <HoverCard.Trigger
        asChild
        aria-describedby={open && !disabled ? id : undefined}
        onFocus={(event) => {
          // Prevent HoverCard's delayed focus-open from reopening after Escape.
          event.preventDefault();
          setOpen(true);
        }}
        onBlur={() => setOpen(false)}
        onPointerDown={() => setOpen(false)}
        onKeyDown={(event) => {
          if (event.key === "Escape") setOpen(false);
        }}
      >
        {children}
      </HoverCard.Trigger>
      <HoverCard.Portal>
        <HoverCard.Content
          id={id}
          role="tooltip"
          className={cn("workspace-tooltip", contentClassName)}
          side={side}
          sideOffset={8}
          collisionPadding={12}
        >
          {label}
        </HoverCard.Content>
      </HoverCard.Portal>
    </HoverCard.Root>
  );
}
