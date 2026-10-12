import {
  createContext,
  useContext,
  type Dispatch,
  type SetStateAction,
} from "react";
import type {
  workspaceSidebarPages,
  WorkspaceSidebarId,
} from "./workspace-sidebar-pages";

type PreviewHost = { id: WorkspaceSidebarId; element: HTMLDivElement } | null;
export type SidebarContext = {
  desktop: boolean;
  viewport: HTMLElement | null;
  collapsed: boolean;
  activePage: (typeof workspaceSidebarPages)[number] | undefined;
  previewId: WorkspaceSidebarId | null;
  previewHost: PreviewHost;
  setPreviewHost: Dispatch<SetStateAction<PreviewHost>>;
  setPreviewId: Dispatch<SetStateAction<WorkspaceSidebarId | null>>;
  toggle: () => void;
};

export const WorkspaceSidebarContext = createContext<SidebarContext | null>(
  null,
);

export function useWorkspaceSidebar() {
  const context = useContext(WorkspaceSidebarContext);
  if (!context) throw new Error("WorkspaceSidebarProvider is required.");
  return context;
}
