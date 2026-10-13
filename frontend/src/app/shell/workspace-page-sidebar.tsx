import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { PanelLeft, PanelLeftOpen } from "@/shared/ui/icons";
import { Tooltip } from "@/shared/ui/tooltip";
import { cx } from "@/shared/lib/cx";
import {
  useWorkspaceSidebar,
  type WorkspaceSidebarId,
} from "./workspace-sidebar-context";
import "./workspace-page-sidebar.css";

export function WorkspaceSidebarToggle() {
  const { activePage, collapsed, desktop, toggle } = useWorkspaceSidebar();
  if (!desktop || !activePage) return null;
  const label = `${collapsed ? "Expand" : "Collapse"} ${activePage.label} sidebar`;
  return (
    <Tooltip label={label}>
      <button
        type="button"
        className="workspace-icon-button"
        aria-label={label}
        aria-expanded={!collapsed}
        aria-controls={`workspace-page-sidebar-${activePage.id}`}
        onClick={toggle}
      >
        {collapsed ? (
          <PanelLeftOpen size={19} aria-hidden="true" />
        ) : (
          <PanelLeft size={19} aria-hidden="true" />
        )}
      </button>
    </Tooltip>
  );
}

/** Move the same mounted sidebar between the page and its hover popup. */
export function WorkspacePageSidebar({
  id,
  className,
  children,
}: {
  id: WorkspaceSidebarId;
  className?: string;
  children: ReactNode;
}) {
  const { collapsed, previewHost } = useWorkspaceSidebar();
  const dock = useRef<HTMLDivElement>(null);
  const [container] = useState(() => document.createElement("div"));
  useLayoutEffect(() => {
    container.id = `workspace-page-sidebar-${id}`;
    container.className = cx("workspace-page-sidebar__content", className);
  }, [className, container, id]);
  useLayoutEffect(() => {
    const host =
      collapsed && previewHost?.id === id ? previewHost.element : dock.current;
    host?.appendChild(container);
    return () => container.remove();
  }, [collapsed, container, id, previewHost]);
  return (
    <>
      <div
        ref={dock}
        className="workspace-page-sidebar__dock"
        hidden={collapsed}
      />
      {createPortal(children, container)}
    </>
  );
}
