import { useMemo } from "react";
import { useLocation, useNavigate } from "react-router";
import { ArrowLeft, Maximize2, X } from "@/shared/ui/icons";
import { WhiteboardEditor } from "./board-editor";
import type { BoardLeaves } from "./board-save-feedback";
import "./whiteboards.css";

const searchParam = (search: string, key: string) =>
  new URLSearchParams(search).get(key);

/**
 * A Whiteboard Artifact, edited in the side panel of the chat it was made in
 * or on its own Artifact page. Its id is the Board's.
 */
export default function BoardArtifact({
  companyId,
  artifactId,
  title,
  layout,
  onBack,
  backLabel = "Back",
  onClose,
}: {
  companyId: string;
  artifactId: string;
  title: string;
  layout: "panel" | "page";
  onBack?: () => void;
  backLabel?: string;
  onClose?: () => void;
}) {
  const navigate = useNavigate();
  const location = useLocation();
  const scope = useMemo(
    () => ({ companyId, boardId: artifactId }),
    [companyId, artifactId],
  );
  const panel = layout === "panel";
  // The panel shows the Artifact named by `artifactId` in this chat; another
  // Artifact, another chat or closing the panel leaves the Board.
  const leavesPanel = useMemo<BoardLeaves>(
    () => (current, next) =>
      current.pathname !== next.pathname ||
      searchParam(next.search, "artifactId") !== artifactId ||
      searchParam(next.search, "conversationId") !==
        searchParam(current.search, "conversationId"),
    [artifactId],
  );
  return (
    <WhiteboardEditor
      key={`${companyId}:${artifactId}`}
      scope={scope}
      title={title}
      compact={panel}
      leading={
        onBack ? (
          <button
            type="button"
            className="wb-icon-button"
            aria-label={backLabel}
            title={backLabel}
            onClick={onBack}
          >
            <ArrowLeft size={18} aria-hidden />
          </button>
        ) : null
      }
      actions={
        panel ? (
          <>
            <button
              type="button"
              className="wb-icon-button"
              aria-label="Open full view"
              title="Open full view"
              onClick={() =>
                navigate(
                  `/companies/${encodeURIComponent(companyId)}/artifacts/${encodeURIComponent(artifactId)}`,
                  {
                    // Back on the full view returns to this chat and panel;
                    // a browser stays in the browser, as any in-app link.
                    state: {
                      artifactInBrowser: true,
                      canvasReturnTo: `${location.pathname}${location.search}`,
                    },
                  },
                )
              }
            >
              <Maximize2 size={18} aria-hidden />
            </button>
            {onClose ? (
              <button
                type="button"
                className="wb-icon-button"
                aria-label="Close whiteboard"
                title="Close"
                onClick={onClose}
              >
                <X size={18} aria-hidden />
              </button>
            ) : null}
          </>
        ) : null
      }
      leaves={panel ? leavesPanel : undefined}
    />
  );
}
