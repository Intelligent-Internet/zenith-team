import { useState } from "react";
import { useLocation, useNavigate, useNavigationType } from "react-router";
import { ArrowLeft, ArrowRight } from "@/shared/ui/icons";
import { WorkspaceSidebarToggle } from "./workspace-page-sidebar";

export function WorkspaceHistoryControls() {
  const location = useLocation();
  const navigate = useNavigate();
  const navigationType = useNavigationType();
  // Track only this mounted company's route history, so Back never leaves the app.
  const [history, setHistory] = useState({ keys: [location.key], index: 0 });
  if (history.keys[history.index] !== location.key) {
    if (navigationType === "PUSH") {
      setHistory({
        keys: [...history.keys.slice(0, history.index + 1), location.key],
        index: history.index + 1,
      });
    } else if (navigationType === "REPLACE") {
      setHistory({
        ...history,
        keys: history.keys.map((key, index) =>
          index === history.index ? location.key : key,
        ),
      });
    } else {
      const index = history.keys.indexOf(location.key);
      setHistory(
        index < 0 ? { keys: [location.key], index: 0 } : { ...history, index },
      );
    }
  }
  return (
    <div
      className="workspace-history-controls shrink-0 flex items-center gap-[3px] [-webkit-app-region:no-drag]"
      role="group"
      aria-label="Workspace navigation"
    >
      <button
        type="button"
        className="workspace-icon-button"
        aria-label="Go back"
        title="Back"
        disabled={history.index === 0}
        onClick={() => void navigate(-1)}
      >
        <ArrowLeft size={19} />
      </button>
      <button
        type="button"
        className="workspace-icon-button"
        aria-label="Go forward"
        title="Forward"
        disabled={history.index >= history.keys.length - 1}
        onClick={() => void navigate(1)}
      >
        <ArrowRight size={19} />
      </button>
      <WorkspaceSidebarToggle />
    </div>
  );
}
