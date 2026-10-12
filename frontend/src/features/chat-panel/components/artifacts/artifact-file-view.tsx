import { useId, useMemo, useRef, useState } from "react";

import type { ConversationArtifact } from "@/features/chat-panel/api/conversation-artifacts-api";
import { ArtifactFileActions } from "@/features/chat-panel/components/artifacts/artifact-file-actions";
import {
  ArtifactFilePreview,
  type ArtifactDocumentPosition,
} from "@/features/chat-panel/components/artifacts/artifact-file-preview";
import { ArtifactFileTabs } from "@/features/chat-panel/components/artifacts/artifact-file-tabs";
import { artifactFileSelection } from "@/features/chat-panel/model/artifact-file-access";
import { isStudioCanvasItem } from "@/features/run-deliverables/model/studio-canvas";

export function ArtifactFileView({
  artifact,
  companyId,
  initialFileId,
}: {
  artifact: ConversationArtifact;
  companyId: string;
  initialFileId?: string;
}) {
  const files = useMemo(
    () =>
      [...artifact.artifact.items]
        .filter(
          (item) =>
            item.role !== "supporting" &&
            (artifact.artifact.type !== "media" || !isStudioCanvasItem(item)),
        )
        .sort(
          (left, right) =>
            Number(right.role === "primary") - Number(left.role === "primary"),
        )
        .map((item) =>
          artifactFileSelection(artifact.runId, artifact.artifact.id, item),
        ),
    [artifact],
  );
  const [selectedId, setSelectedId] = useState(
    initialFileId ?? files[0]?.id ?? "",
  );
  const tabsId = useId();
  const positions = useRef(new Map<string, ArtifactDocumentPosition>());
  const selected = files.find((file) => file.id === selectedId) ?? files[0];
  if (!selected) return null;
  return (
    <div className="artifact-file-view">
      <div className="artifact-file-toolbar">
        <ArtifactFileTabs
          id={tabsId}
          files={files}
          selectedId={selected.id}
          onSelect={setSelectedId}
        />
        <ArtifactFileActions
          companyId={companyId}
          file={selected}
          output={artifact.artifact}
        />
      </div>
      <div
        aria-labelledby={`${tabsId}-${selected.id}`}
        className="artifact-file-preview-pane"
        id={`${tabsId}-panel`}
        role="tabpanel"
        tabIndex={0}
      >
        <ArtifactFilePreview
          key={selected.id}
          companyId={companyId}
          file={selected}
          documentPosition={positions.current.get(selected.id)}
          onDocumentScroll={(position) =>
            positions.current.set(selected.id, position)
          }
        />
      </div>
    </div>
  );
}
