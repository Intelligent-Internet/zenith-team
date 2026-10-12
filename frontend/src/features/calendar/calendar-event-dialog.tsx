import * as Popover from "@radix-ui/react-popover";
import {
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
} from "react";
import { XIcon } from "@/shared/ui/icons";
import { cn } from "@/shared/ui/core/cn";

export type CalendarEventAnchor = {
  contextElement: HTMLElement;
  getBoundingClientRect: () => DOMRect;
};

export function calendarEventAnchor(
  element: HTMLElement,
  offsetY = 0,
  height?: number,
): CalendarEventAnchor {
  const initialRect = element.getBoundingClientRect();
  return {
    contextElement: element,
    getBoundingClientRect: () => {
      const rect = element.isConnected
        ? element.getBoundingClientRect()
        : initialRect;
      return new DOMRect(
        rect.x,
        rect.y + offsetY,
        rect.width,
        height ?? rect.height,
      );
    },
  };
}

export function CalendarEventDialog({
  anchor,
  title,
  description,
  onClose,
  children,
  className,
  style,
  headingActions,
  busy = false,
}: {
  anchor: CalendarEventAnchor;
  title: string;
  description: string;
  onClose: () => void;
  children: ReactNode;
  className?: string;
  style?: CSSProperties;
  headingActions?: ReactNode;
  busy?: boolean;
}) {
  const titleId = useId();
  const descriptionId = useId();
  const virtualRef = useMemo(() => ({ current: anchor }), [anchor]);
  const interactedOutside = useRef(false);
  const [content, setContent] = useState<HTMLDivElement | null>(null);
  const [side, setSide] = useState<"right" | "bottom">("right");
  useLayoutEffect(() => {
    const element = anchor.contextElement;
    element.setAttribute("data-calendar-selected", "true");
    return () => element.removeAttribute("data-calendar-selected");
  }, [anchor]);
  useLayoutEffect(() => {
    if (!content) return;
    const updateSide = () => {
      const rect = anchor.getBoundingClientRect();
      const availableWidth = Math.max(
        rect.left,
        window.innerWidth - rect.right,
      );
      setSide(availableWidth >= content.offsetWidth + 28 ? "right" : "bottom");
    };
    updateSide();
    const observer = new ResizeObserver(updateSide);
    observer.observe(content);
    observer.observe(anchor.contextElement);
    window.addEventListener("resize", updateSide);
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", updateSide);
    };
  }, [anchor, content]);
  return (
    <Popover.Root
      open
      modal={busy}
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
    >
      <Popover.Anchor virtualRef={virtualRef} />
      <Popover.Portal
        container={document.querySelector(".boardly-workspace-theme")}
      >
        <Popover.Content
          ref={setContent}
          className={cn("personal-dialog calendar-event-dialog", className)}
          style={style}
          side={side}
          align={side === "right" ? "start" : "center"}
          sideOffset={12}
          collisionPadding={16}
          sticky="always"
          aria-labelledby={titleId}
          aria-describedby={descriptionId}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            // Editing mounts a new popover; keep its newly focused input.
            if (
              !interactedOutside.current &&
              (document.activeElement === document.body ||
                content?.contains(document.activeElement))
            ) {
              const trigger = anchor.contextElement.isConnected
                ? anchor.contextElement
                : document.querySelector<HTMLButtonElement>(".calendar-create");
              trigger?.focus({ preventScroll: true });
            }
          }}
          onEscapeKeyDown={(event) => {
            if (busy) event.preventDefault();
          }}
          onInteractOutside={(event) => {
            if (busy) event.preventDefault();
            else interactedOutside.current = true;
          }}
        >
          <header className="personal-dialog-heading">
            <div>
              <h2 id={titleId}>{title}</h2>
              <p id={descriptionId}>{description}</p>
            </div>
            {headingActions}
            <Popover.Close
              className="personal-icon-button"
              aria-label="Close"
              disabled={busy}
            >
              <XIcon size={20} />
            </Popover.Close>
          </header>
          {children}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
