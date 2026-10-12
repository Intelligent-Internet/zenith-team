import { useEffect, type ReactNode } from "react";
import { createPortal } from "react-dom";

let activeWorkspaceModalCount = 0;
let rootWasInert = false;

export function WorkspaceModalPortal({ children }: { children: ReactNode }) {
  useEffect(() => {
    const appRoot = document.getElementById("root");
    if (!appRoot) return undefined;

    if (activeWorkspaceModalCount === 0) {
      rootWasInert = appRoot.inert;
    }
    activeWorkspaceModalCount += 1;
    appRoot.inert = true;

    return () => {
      activeWorkspaceModalCount = Math.max(0, activeWorkspaceModalCount - 1);
      if (activeWorkspaceModalCount === 0) {
        appRoot.inert = rootWasInert;
      }
    };
  }, []);

  return createPortal(
    <div className="boardly-workspace-theme workspace-modal-portal">
      {children}
    </div>,
    document.body,
  );
}
