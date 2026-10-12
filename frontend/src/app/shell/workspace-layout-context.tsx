import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  type Dispatch,
  type ReactNode,
  type SetStateAction,
} from "react";
export type WorkspaceLayoutOptions = {
  agentDock?: {
    className?: string;
    hidden?: boolean;
  };
};

type WorkspaceLayoutContextValue = {
  setWorkspaceLayoutOptions: Dispatch<SetStateAction<WorkspaceLayoutOptions>>;
  canvasFullscreen: boolean;
  toggleCanvasFullscreen: () => void;
};

const WorkspaceLayoutContext =
  createContext<WorkspaceLayoutContextValue | null>(null);

export function WorkspaceLayoutProvider({
  children,
  setWorkspaceLayoutOptions,
  canvasFullscreen,
  toggleCanvasFullscreen,
}: {
  children: ReactNode;
  setWorkspaceLayoutOptions: Dispatch<SetStateAction<WorkspaceLayoutOptions>>;
  canvasFullscreen: boolean;
  toggleCanvasFullscreen: () => void;
}) {
  const contextValue = useMemo(
    () => ({
      setWorkspaceLayoutOptions,
      canvasFullscreen,
      toggleCanvasFullscreen,
    }),
    [setWorkspaceLayoutOptions, canvasFullscreen, toggleCanvasFullscreen],
  );

  return (
    <WorkspaceLayoutContext.Provider value={contextValue}>
      {children}
    </WorkspaceLayoutContext.Provider>
  );
}

export function useCanvasFullscreen() {
  const context = useContext(WorkspaceLayoutContext);
  if (!context) throw new Error("WorkspaceLayoutProvider is required.");
  return {
    active: context.canvasFullscreen,
    toggle: context.toggleCanvasFullscreen,
  };
}

export function useWorkspaceLayoutOptions(options: WorkspaceLayoutOptions) {
  const context = useContext(WorkspaceLayoutContext);

  useEffect(() => {
    if (!context) {
      return;
    }

    context.setWorkspaceLayoutOptions(options);

    return () => {
      context.setWorkspaceLayoutOptions({});
    };
  }, [context, options]);
}
