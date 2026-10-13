import * as Popover from "@radix-ui/react-popover";
import {
  Suspense,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { Link, useLocation, type LinkProps } from "react-router";
import {
  setWorkspaceSidebarCollapsed,
  useAppDispatch,
  useAppSelector,
} from "@/state";
import { workspaceSidebarPages } from "./workspace-sidebar-pages";
import {
  WorkspaceSidebarContext,
  useWorkspaceSidebar,
  type SidebarContext,
  type WorkspaceSidebarId,
} from "./workspace-sidebar-context";
import "./workspace-page-sidebar.css";

export function WorkspaceSidebarProvider({
  companyId,
  desktop,
  viewport,
  children,
}: {
  companyId: string;
  desktop: boolean;
  viewport: HTMLElement | null;
  children: ReactNode;
}) {
  const dispatch = useAppDispatch();
  const location = useLocation();
  const savedCollapsed = useAppSelector(
    (state) => state.ui.sidebarCollapsedByCompany?.[companyId] === true,
  );
  const collapsed = desktop && savedCollapsed;
  const activePage = workspaceSidebarPages.find((page) =>
    page.matches(location.pathname),
  );
  const [previewId, setPreviewId] = useState<WorkspaceSidebarId | null>(null);
  const [previewHost, setPreviewHost] =
    useState<SidebarContext["previewHost"]>(null);

  useEffect(() => {
    setPreviewId(null);
  }, [location.key, collapsed, companyId]);

  const toggle = useCallback(() => {
    setPreviewId(null);
    dispatch(
      setWorkspaceSidebarCollapsed({ companyId, collapsed: !savedCollapsed }),
    );
  }, [companyId, dispatch, savedCollapsed]);
  const value = useMemo(
    () => ({
      desktop,
      viewport,
      collapsed,
      activePage,
      previewId,
      previewHost,
      setPreviewHost,
      setPreviewId,
      toggle,
    }),
    [desktop, viewport, collapsed, activePage, previewId, previewHost, toggle],
  );
  return (
    <WorkspaceSidebarContext.Provider value={value}>
      {children}
    </WorkspaceSidebarContext.Provider>
  );
}

export function WorkspaceSidebarLink({
  sidebarId,
  ...props
}: LinkProps & { sidebarId: WorkspaceSidebarId }) {
  const sidebar = useWorkspaceSidebar();
  const page = workspaceSidebarPages.find((item) => item.id === sidebarId)!;
  const open =
    sidebar.collapsed &&
    sidebar.viewport !== null &&
    sidebar.previewId === sidebarId;
  const anchor = useMemo(
    () => ({
      current: {
        contextElement: sidebar.viewport ?? undefined,
        getBoundingClientRect: () => {
          const rect = sidebar.viewport?.getBoundingClientRect();
          return new DOMRect(
            rect?.left ?? 0,
            rect?.top ?? 0,
            0,
            rect?.height ?? 0,
          );
        },
      },
    }),
    [sidebar.viewport],
  );
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const trigger = useRef<HTMLAnchorElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const [contentElement, setContentElement] = useState<HTMLDivElement | null>(
    null,
  );
  const registerContent = useCallback((element: HTMLDivElement | null) => {
    content.current = element;
    setContentElement(element);
  }, []);
  const engaged = useRef(false);
  const keyboardOpened = useRef(false);
  const restoreFocus = useRef(false);
  const cancelTimer = () => clearTimeout(timer.current);
  const close = () => {
    cancelTimer();
    restoreFocus.current = Boolean(
      content.current?.contains(document.activeElement),
    );
    sidebar.setPreviewId((current) => (current === sidebarId ? null : current));
  };
  const scheduleClose = () => {
    cancelTimer();
    if (!engaged.current) timer.current = setTimeout(close, 220);
  };
  const registerHost = useCallback(
    (element: HTMLDivElement | null) => {
      sidebar.setPreviewHost((current) =>
        element
          ? { id: sidebarId, element }
          : current?.id === sidebarId
            ? null
            : current,
      );
    },
    [sidebar.setPreviewHost, sidebarId],
  );
  useEffect(() => () => clearTimeout(timer.current), []);
  useEffect(() => {
    if (!open) {
      engaged.current = false;
      keyboardOpened.current = false;
      clearTimeout(timer.current);
    }
  }, [open]);
  useEffect(() => {
    const element = contentElement;
    if (!open || !element) return;
    // The active page's portal has a different React parent. Listen on its
    // physical popup host so pointer and focus interactions still keep it open.
    const keepOpen = () => {
      engaged.current = true;
      clearTimeout(timer.current);
    };
    const enter = () => clearTimeout(timer.current);
    const leave = () => {
      clearTimeout(timer.current);
      if (!engaged.current) {
        timer.current = setTimeout(() => {
          sidebar.setPreviewId((current) =>
            current === sidebarId ? null : current,
          );
        }, 220);
      }
    };
    element.addEventListener("pointerenter", enter);
    element.addEventListener("pointerleave", leave);
    element.addEventListener("pointerdown", keepOpen);
    element.addEventListener("focusin", keepOpen);
    return () => {
      element.removeEventListener("pointerenter", enter);
      element.removeEventListener("pointerleave", leave);
      element.removeEventListener("pointerdown", keepOpen);
      element.removeEventListener("focusin", keepOpen);
    };
  }, [open, contentElement, sidebar.setPreviewId, sidebarId]);

  if (!sidebar.collapsed) return <Link {...props} />;
  return (
    <Popover.Root
      open={open}
      onOpenChange={(next) => {
        if (!next) close();
      }}
    >
      <Popover.Anchor virtualRef={anchor} />
      <Link
        {...props}
        ref={trigger}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={(event) => {
          props.onClick?.(event);
          if (!event.defaultPrevented) close();
        }}
        onPointerEnter={(event) => {
          cancelTimer();
          if (event.pointerType !== "mouse" || open) return;
          timer.current = setTimeout(() => {
            engaged.current = false;
            keyboardOpened.current = false;
            sidebar.setPreviewId(sidebarId);
          }, 140);
        }}
        onPointerLeave={scheduleClose}
        onKeyDown={(event) => {
          if (event.key === "ArrowRight" || event.key === "ArrowDown") {
            event.preventDefault();
            cancelTimer();
            engaged.current = true;
            keyboardOpened.current = true;
            sidebar.setPreviewId(sidebarId);
            if (open) content.current?.focus();
          } else if (event.key === "Escape") close();
        }}
      />
      <Popover.Portal>
        <Popover.Content
          ref={registerContent}
          className="boardly-workspace-theme workspace-sidebar-popup"
          aria-label={`${page.label} sidebar`}
          tabIndex={-1}
          side="right"
          align="start"
          avoidCollisions={false}
          onInteractOutside={(event) => {
            // Menus and dialogs launched by the reparented sidebar use their
            // own portals, outside both this DOM host and its React ancestry.
            if (
              event.target instanceof Element &&
              (content.current?.contains(event.target) ||
                (engaged.current &&
                  event.target.closest(
                    '[role="menu"], [role="dialog"], [role="alertdialog"], [role="listbox"]',
                  )))
            )
              event.preventDefault();
          }}
          onPointerDownCapture={() => {
            engaged.current = true;
            cancelTimer();
          }}
          onFocusCapture={() => {
            engaged.current = true;
            cancelTimer();
          }}
          onOpenAutoFocus={(event) => {
            event.preventDefault();
            if (keyboardOpened.current) content.current?.focus();
          }}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            if (
              restoreFocus.current &&
              document.activeElement === document.body
            )
              trigger.current?.focus();
            restoreFocus.current = false;
          }}
        >
          {sidebar.activePage?.id === sidebarId ? (
            <div ref={registerHost} className="workspace-sidebar-popup__host" />
          ) : (
            <Suspense
              fallback={
                <p className="workspace-sidebar-popup__loading" role="status">
                  Loading {page.label.toLowerCase()}…
                </p>
              }
            >
              <page.Preview sidebarOnly />
            </Suspense>
          )}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
