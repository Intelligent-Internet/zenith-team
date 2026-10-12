import * as HoverCard from "@radix-ui/react-hover-card";
import { useId, useRef, useState, type ReactNode } from "react";
import "./canvas-tool-guide.css";

export type CanvasToolDescription = {
  label: string;
  title?: string;
  description: string;
  shortcut?: string;
};

export function CanvasToolGuide({
  tool,
  preview,
  open,
  onOpenChange,
  disabledReason,
  pressed,
  expanded,
  onClick,
  children,
  containerSelector,
  side = "top",
  sideOffset = 18,
}: {
  tool: CanvasToolDescription;
  preview?: ReactNode;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  disabledReason?: string;
  pressed?: boolean;
  expanded?: boolean;
  onClick: () => void;
  children: ReactNode;
  containerSelector: string;
  side?: "top" | "right";
  sideOffset?: number;
}) {
  const descriptionId = useId();
  const trigger = useRef<HTMLSpanElement>(null);
  const dismissed = useRef(false);
  const [container, setContainer] = useState<HTMLElement>();
  const dismiss = () => {
    dismissed.current = true;
    onOpenChange(false);
  };

  return (
    <HoverCard.Root
      open={open}
      openDelay={350}
      closeDelay={120}
      onOpenChange={(next) => {
        if (next && dismissed.current) return;
        if (next)
          setContainer(
            trigger.current?.closest<HTMLElement>(containerSelector) ??
              undefined,
          );
        onOpenChange(next);
      }}
    >
      <HoverCard.Trigger
        asChild
        onPointerEnter={() => {
          dismissed.current = false;
        }}
        onFocus={(event) => {
          // Clicking performs the action; keyboard focus also explains the tool.
          if (
            !(event.target instanceof Element) ||
            !event.target.matches(":focus-visible")
          )
            event.preventDefault();
          else dismissed.current = false;
        }}
        onBlur={dismiss}
        onPointerDown={dismiss}
        onTouchStartCapture={(event) => event.stopPropagation()}
        onKeyDown={(event) => {
          if (event.key === "Escape" && open) {
            event.preventDefault();
            event.stopPropagation();
            dismiss();
          }
        }}
      >
        <span ref={trigger} className="canvas-tool-guide__trigger">
          <button
            type="button"
            aria-label={tool.label}
            aria-describedby={open ? descriptionId : undefined}
            aria-description={disabledReason}
            aria-pressed={pressed}
            aria-expanded={expanded}
            disabled={Boolean(disabledReason)}
            onClick={() => {
              dismiss();
              onClick();
            }}
          >
            {children}
          </button>
        </span>
      </HoverCard.Trigger>
      <HoverCard.Portal container={container}>
        <HoverCard.Content
          id={descriptionId}
          role="tooltip"
          className="canvas-tool-guide"
          side={side}
          sideOffset={sideOffset}
          collisionPadding={12}
          collisionBoundary={container}
          onEscapeKeyDown={(event) => {
            event.preventDefault();
            event.stopPropagation();
            dismiss();
          }}
          onPointerDownOutside={dismiss}
        >
          {preview}
          <div className="canvas-tool-guide__copy">
            <div className="canvas-tool-guide__heading">
              <strong>{tool.title ?? tool.label}</strong>
              {tool.shortcut && <kbd>{tool.shortcut}</kbd>}
            </div>
            <p>{tool.description}</p>
            {disabledReason && <small>{disabledReason}</small>}
          </div>
        </HoverCard.Content>
      </HoverCard.Portal>
    </HoverCard.Root>
  );
}
