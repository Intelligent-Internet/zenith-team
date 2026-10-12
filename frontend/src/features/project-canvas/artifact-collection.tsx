import { useState } from "react";
import { Virtuoso } from "react-virtuoso";
import { ArrowUpRight, MessageSquare, Plus } from "@/shared/ui/icons";
import { artifactFormat } from "./canvas-nodes";
import { NodePreview, type ImagePreviewInfo } from "./node-preview";
import { NodeComments } from "./node-comments";
import type { CanvasArtifact, CanvasScope } from "./canvas-api";

export function ArtifactState({ artifact }: { artifact: CanvasArtifact }) {
  return (
    <span className="pc-review-state">
      {artifact.pageId ? "Page" : "Saved Output"}
    </span>
  );
}
export type ArtifactCollectionProps = {
  artifacts: CanvasArtifact[];
  scope: CanvasScope;
  onOpen: (
    artifact: CanvasArtifact,
    versionId?: string,
    threadId?: string,
  ) => void;
  onPlace?: (artifact: CanvasArtifact) => void;
};
export function ArtifactCollection({
  artifacts,
  scope,
  onOpen,
  onPlace,
}: ArtifactCollectionProps) {
  if (!artifacts.length)
    return <div className="pc-view-empty">No artifacts here yet.</div>;
  return (
    <Virtuoso
      className="nodrag nopan nowheel"
      data={artifacts}
      style={{ height: "100%" }}
      computeItemKey={(_, row) => row.id}
      itemContent={(_, artifact) => (
        <ArtifactRow
          artifact={artifact}
          scope={scope}
          onOpen={onOpen}
          onPlace={onPlace}
        />
      )}
    />
  );
}
export function ArtifactRow({
  artifact,
  onOpen,
  onPlace,
}: Omit<ArtifactCollectionProps, "artifacts"> & { artifact: CanvasArtifact }) {
  const { Icon, label, tone } = artifactFormat(artifact.latestVersion);
  return (
    <div className={`pc-file-row pc-tone-${tone}`}>
      <span className="pc-format-icon">
        <Icon size={21} />
      </span>
      <button className="pc-file-name" onClick={() => onOpen(artifact)}>
        <strong>{artifact.title}</strong>
        <span>
          {label} · v{artifact.latestVersion.number} · {artifact.creatorName}
        </span>
      </button>
      <ArtifactState artifact={artifact} />
      <span className="pc-file-comments" title="Open comments">
        <MessageSquare size={14} />
        {artifact.openComments}
      </span>
      <time dateTime={artifact.updatedAt}>
        {new Date(artifact.updatedAt).toLocaleDateString(undefined, {
          month: "short",
          day: "numeric",
        })}
      </time>
      {onPlace && (
        <button
          className="pc-icon-button"
          title="Add to free canvas"
          aria-label={`Add ${artifact.title} to free canvas`}
          onClick={() => onPlace(artifact)}
        >
          <Plus size={16} />
        </button>
      )}
      <button
        className="pc-icon-button"
        aria-label={`Open ${artifact.title}`}
        onClick={() => onOpen(artifact)}
      >
        <ArrowUpRight size={17} />
      </button>
    </div>
  );
}
export function ArtifactTile({
  artifact,
  scope,
  onOpen,
}: Omit<ArtifactCollectionProps, "artifacts"> & { artifact: CanvasArtifact }) {
  const [imageInfo, setImageInfo] = useState<ImagePreviewInfo>();
  const { Icon, label, tone } = artifactFormat(artifact.latestVersion);
  return (
    <article className={`pc-collection-tile pc-tone-${tone}`}>
      <header>
        <span className="pc-format-icon">
          <Icon size={17} />
        </span>
        <span>{label}</span>
        <small>v{artifact.latestVersion.number}</small>
        <NodeComments
          version={artifact.latestVersion}
          title={artifact.title}
          onOpen={(threadId) => onOpen(artifact, undefined, threadId)}
        />
      </header>
      <div
        className="pc-collection-preview"
        onDoubleClick={() => onOpen(artifact)}
      >
        <NodePreview
          scope={scope}
          version={artifact.latestVersion}
          excerpt={artifact.latestVersion.excerpt}
          reading={false}
          detailed
          onImageInfo={setImageInfo}
        />
      </div>
      <button className="pc-collection-title" onClick={() => onOpen(artifact)}>
        <strong>{artifact.title}</strong>
        <ArrowUpRight size={17} />
      </button>
      <footer>
        <ArtifactState artifact={artifact} />
        <span>
          {imageInfo?.dimensions
            ? `${imageInfo.dimensions.width} × ${imageInfo.dimensions.height}`
            : artifact.creatorName}
        </span>
      </footer>
    </article>
  );
}
