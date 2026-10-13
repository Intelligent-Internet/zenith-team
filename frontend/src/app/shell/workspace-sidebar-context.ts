import {
  createContext,
  useContext,
  type Dispatch,
  type SetStateAction,
} from "react";

export type WorkspaceSidebarId = "chat" | "wiki";

type PreviewHost = { id: WorkspaceSidebarId; element: HTMLDivElement } | null;
export type SidebarContext = {
  desktop: boolean;
  viewport: HTMLElement | null;
  collapsed: boolean;
  activePage: { id: WorkspaceSidebarId; label: string } | undefined;
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
