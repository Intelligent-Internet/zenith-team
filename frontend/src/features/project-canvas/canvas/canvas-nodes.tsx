import presentationStyles from "./canvas-presentation.module.css";
import { previewContentType } from "../previews/preview-content-type";
import { NodePreview, type ImagePreviewInfo } from "../previews/node-preview";
import { NodeComments } from "../documents/node-comments";
import { memo, useEffect, useState } from "react";
import {
  Handle,
  NodeResizer,
  NodeResizeControl,
  Position,
  useReactFlow,
  type Node,
  type NodeProps,
} from "@xyflow/react";
import {
  FileText,
  FileCode2,
  Image,
  File,
  Layers,
  ArrowUpRight,
} from "@/shared/ui/icons";
import type {
  CanvasArtifact,
  CanvasItem,
  CanvasVersion,
  CanvasScope,
} from "../api/canvas-api";

export function artifactFormat(version?: CanvasVersion) {
  if (!version) return { label: "Frame", Icon: Layers, tone: "neutral" };
  const type = previewContentType(version.contentType, version.filename),
    name = version.filename ?? "";
  if (
    version.sourceType === "wiki" ||
    /markdown/.test(type) ||
    /\.md$/i.test(name)
  )
    return { label: "Document", Icon: FileText, tone: "amber" };
  if (/html/.test(type) || /\.html?$/i.test(name))
    return { label: "HTML design", Icon: FileCode2, tone: "violet" };
  if (type.startsWith("image/"))
    return { label: "Image", Icon: Image, tone: "rose" };
  if (/pdf/.test(type)) return { label: "PDF", Icon: FileText, tone: "rose" };
  if (/wordprocessingml/.test(type) || /\.docx$/i.test(name))
    return { label: "Word document", Icon: FileText, tone: "blue" };
  if (version.sourceType === "output")
    return { label: "Saved output", Icon: Layers, tone: "violet" };
  return { label: "File", Icon: File, tone: "blue" };
}
export type CanvasNodeData = {
  scope: CanvasScope;
  item: CanvasItem;
  artifact?: CanvasArtifact;
  canEdit: boolean;
  commentsOpen: boolean;
  commentsDocked?: boolean;
  onCommentsOpenChange: (id: string | null, open: boolean) => void;
  onOpen: (item: CanvasItem, threadId?: string) => void;
  onInteractionStart: () => void;
  onResize: (
    id: string,
    bounds: { x: number; y: number; width: number; height: number },
  ) => void;
};
export type CanvasNode = Node<CanvasNodeData, "artifact" | "frame">;
export const ArtifactNode = memo(function ArtifactNode({
  data,
  selected,
  dragging,
}: NodeProps<CanvasNode>) {
  const { updateNode } = useReactFlow();
  const [imageInfo, setImageInfo] = useState<ImagePreviewInfo>();
  const { item, artifact } = data;
  const version = item.version ?? artifact?.latestVersion;
  const isLatest = !!item.version;
  const previewInfo =
    imageInfo?.versionId === version?.id ? imageInfo : undefined;
  const { label } = artifactFormat(
    version && previewInfo
      ? {
          ...version,
          contentType: previewInfo.contentType,
          filename: previewInfo.filename,
        }
      : version,
  );
  const imageCard = label === "Image";
  const documentCard =
    label === "Document" || label === "Word document" || label === "PDF";
  const dimensions = imageCard ? previewInfo?.dimensions : undefined;
  useEffect(() => {
    if (!dimensions?.width || !dimensions.height) return;
    const scale = Math.min(
      item.width / dimensions.width,
      item.height / dimensions.height,
    );
    const width = dimensions.width * scale;
    const height = dimensions.height * scale;
    updateNode(item.id, { width, height, style: { width, height } });
  }, [dimensions, item, artifact, updateNode]);
  const title = item.title || artifact?.title || "Artifact";
  const excerpt =
    typeof item.viewConfig.excerpt === "string"
      ? item.viewConfig.excerpt
      : isLatest
        ? version?.excerpt
        : "";
  return (
    <article
      className={`pc-artifact-card ${presentationStyles.resizeGrips} ${imageCard ? "is-image-card" : ""} ${documentCard ? presentationStyles["is-document-card"] : ""}`}
      aria-label={title}
      data-dragging={dragging || undefined}
    >
      {selected &&
        data.canEdit &&
        (["top", "right", "bottom", "left"] as const).map((edge) => (
          <NodeResizeControl
            key={edge}
            position={edge}
            className={presentationStyles.resizeGrip}
            keepAspectRatio={imageCard}
            minWidth={imageCard ? 120 : 240}
            minHeight={imageCard ? 80 : 160}
            onResizeStart={data.onInteractionStart}
            onResizeEnd={(_event, bounds) => data.onResize(item.id, bounds)}
          >
            <span title={`Drag to resize from the ${edge}`} />
          </NodeResizeControl>
        ))}
      <Handle
        type="target"
        position={Position.Left}
        isConnectable={data.canEdit}
      />
      <div className="pc-card-preview">
        {version && (
          <NodePreview
            scope={data.scope}
            version={version}
            excerpt={excerpt ?? undefined}
            reading={!!selected}
            documentPreview
            detailed
            blockId={
              typeof item.viewConfig.blockId === "string"
                ? item.viewConfig.blockId
                : undefined
            }
            draft={item.versionPolicy === "draft"}
            onImageInfo={setImageInfo}
          />
        )}
      </div>
      <footer
        className={presentationStyles["pc-card-legend"]}
        data-comments-open={data.commentsOpen || undefined}
      >
        <span title={title}>{title}</span>
        {version && (
          <NodeComments
            version={version}
            title={title}
            open={data.commentsOpen}
            onOpenChange={
              data.commentsDocked
                ? (open) => data.onCommentsOpenChange(item.id, open)
                : undefined
            }
            onOpen={(threadId) => data.onOpen(item, threadId)}
          />
        )}
        <button
          className="nodrag nopan"
          aria-label={`Open ${title}`}
          onClick={() => data.onOpen(item)}
        >
          <ArrowUpRight size={15} />
        </button>
      </footer>
      <Handle
        type="source"
        position={Position.Right}
        isConnectable={data.canEdit}
      />
    </article>
  );
});

export const FrameNode = memo(function FrameNode({
  data,
  selected,
}: NodeProps<CanvasNode>) {
  return (
    <section className="pc-frame-node">
      <NodeResizer
        isVisible={!!selected && data.canEdit}
        minWidth={360}
        minHeight={280}
        onResizeStart={data.onInteractionStart}
        onResizeEnd={(_event, bounds) => data.onResize(data.item.id, bounds)}
      />
      <div className="pc-frame-label">
        <Layers size={16} />
        <span>{data.item.title || "Untitled frame"}</span>
      </div>
    </section>
  );
});
export const canvasNodeTypes = { artifact: ArtifactNode, frame: FrameNode };
