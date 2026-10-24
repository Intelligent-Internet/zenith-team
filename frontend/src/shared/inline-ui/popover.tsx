import * as Popover from "@radix-ui/react-popover";
import {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";

const Context = createContext<{
  open: boolean;
  setOpen: (open: boolean) => void;
  enter: () => void;
  leave: () => void;
  cancel: () => void;
} | null>(null);

export function InlinePopover({
  open: controlled,
  showOnHover,
  hoverOpenDelay = 200,
  interactive,
  onOpenChange,
  children,
}: {
  open?: boolean;
  showOnHover?: boolean;
  hoverOpenDelay?: number;
  interactive: boolean;
  onOpenChange?: (open: boolean) => void;
  children: ReactNode;
}) {
  const [local, setLocal] = useState(controlled ?? false);
  useEffect(() => {
    if (controlled !== undefined) setLocal(controlled);
  }, [controlled]);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const cancel = () => clearTimeout(timer.current);
  useEffect(() => () => clearTimeout(timer.current), []);
  const open = onOpenChange && controlled !== undefined ? controlled : local;
  const setOpen = (next: boolean) => {
    if (interactive) {
      setLocal(next);
      onOpenChange?.(next);
    }
  };
  const enter = () => {
    cancel();
    if (interactive && showOnHover)
      timer.current = setTimeout(
        () => setOpen(true),
        Math.max(0, Math.min(5000, hoverOpenDelay)),
      );
  };
  const leave = () => {
    cancel();
    if (showOnHover) timer.current = setTimeout(() => setOpen(false), 160);
  };
  return (
    <Context.Provider value={{ open, setOpen, enter, leave, cancel }}>
      <Popover.Root open={open} onOpenChange={setOpen}>
        {children}
      </Popover.Root>
    </Context.Provider>
  );
}

export function InlinePopoverTrigger({
  children,
  interactive,
  block,
  onClick,
  compound,
}: {
  children: ReactNode;
  interactive: boolean;
  block?: boolean;
  onClick: () => void;
  compound: boolean;
}) {
  const context = useContext(Context);
  const events = {
    onClick: () => {
      if (interactive) {
        onClick();
        context?.setOpen(!context.open);
      }
    },
    onMouseEnter: () => context?.enter(),
    onMouseLeave: () => context?.leave(),
    onFocus: () => context?.cancel(),
  };
  return (
    <Popover.Anchor asChild>
      {compound ? (
        <span {...events} className={block ? "flex w-full" : "inline-flex"}>
          {children}
        </span>
      ) : (
        <button
          {...events}
          type="button"
          disabled={!interactive}
          aria-haspopup="dialog"
          aria-expanded={context?.open}
          className={`${block ? "flex w-full" : "inline-flex"} min-h-9 items-center rounded-workspace-control px-3 py-2 text-sm focus-visible:outline-2 focus-visible:outline-workspace-accent disabled:opacity-40`}
        >
          {children}
        </button>
      )}
    </Popover.Anchor>
  );
}

export function InlinePopoverContent({
  children,
  side = "bottom",
  align = "center",
  sideOffset = 8,
  showCloseButton,
  interactive,
}: {
  children: ReactNode;
  side?: "top" | "right" | "bottom" | "left";
  align?: "start" | "center" | "end";
  sideOffset?: number;
  showCloseButton?: boolean;
  interactive: boolean;
}) {
  const context = useContext(Context);
  return (
    <Popover.Content
      side={side}
      align={align}
      sideOffset={sideOffset}
      onOpenAutoFocus={(event) => event.preventDefault()}
      onMouseEnter={() => context?.cancel()}
      onMouseLeave={(event) => {
        if (!event.currentTarget.contains(document.activeElement))
          context?.leave();
      }}
      onFocus={() => context?.cancel()}
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget))
          context?.leave();
      }}
      className="z-50 max-h-[80vh] max-w-[min(90vw,32rem)] overflow-auto rounded-workspace-overlay border border-workspace-divider bg-workspace-panel p-4 text-workspace-text shadow-lg"
    >
      {children}
      {showCloseButton && (
        <button
          type="button"
          disabled={!interactive}
          onClick={() => context?.setOpen(false)}
          className="mt-3 rounded-workspace-control border border-workspace-divider px-3 py-1 text-sm focus-visible:outline-2 focus-visible:outline-workspace-accent"
        >
          Close
        </button>
      )}
    </Popover.Content>
  );
}
