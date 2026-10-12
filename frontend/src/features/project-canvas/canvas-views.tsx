import type { ArtifactCollectionProps } from "./artifact-collection";
import type { CanvasSnapshot } from "./canvas-api";
import { PlanArtifactCanvas } from "./plan-artifact-canvas";
import type { ViewCamera } from "./graph-controls";
import {
  FilesView,
  ArtifactTimeline,
  RelationshipMap,
  PresentationView,
} from "./artifact-views";

export default function CanvasViews({
  mode,
  snapshot,
  scope,
  onOpen,
  onPlace,
  planCamera,
  relationshipCamera,
  expandedTask,
  onExpand,
  onTask,
  onStart,
  onCanvas,
  onAdd,
  canEditDocuments,
  canManageFiles,
}: Omit<ArtifactCollectionProps, "artifacts"> & {
  mode: string;
  snapshot?: CanvasSnapshot;
  planCamera: ViewCamera;
  relationshipCamera: ViewCamera;
  expandedTask: string;
  onExpand: (id: string) => void;
  onTask: (id: string) => void;
  onStart: (index: number) => void;
  onCanvas: () => void;
  onAdd?: (initial: "document" | "upload", taskId?: string) => void;
  canEditDocuments: boolean;
  canManageFiles: boolean;
}) {
  const common = {
    scope,
    artifacts: snapshot?.artifacts ?? [],
    onOpen,
    onPlace,
  };
  if (mode === "plan") {
    return (
      <PlanArtifactCanvas
        {...common}
        camera={planCamera}
        expanded={expandedTask}
        onExpand={onExpand}
        onTask={onTask}
        onAdd={onAdd}
        canEditDocuments={canEditDocuments}
        canManageFiles={canManageFiles}
      />
    );
  }
  if (!snapshot) return null;
  switch (mode) {
    case "files":
      return <FilesView {...common} />;
    case "timeline":
      return <ArtifactTimeline {...common} />;
    case "relationships":
      return (
        <RelationshipMap
          {...common}
          snapshot={snapshot}
          camera={relationshipCamera}
        />
      );
    case "presentation":
      return (
        <PresentationView
          frames={snapshot.items
            .filter((row) => row.type === "frame")
            .sort(
              (a, b) =>
                a.stackOrder - b.stackOrder ||
                a.y - b.y ||
                a.x - b.x ||
                a.id.localeCompare(b.id),
            )}
          items={snapshot.items}
          artifacts={snapshot.artifacts}
          onStart={onStart}
          onCanvas={onCanvas}
        />
      );
    default:
      return null;
  }
}
