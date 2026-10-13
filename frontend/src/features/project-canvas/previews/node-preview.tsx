import presentationStyles from "../canvas/canvas-presentation.module.css";
import { previewContentType } from "./preview-content-type";
import { useEffect } from "react";
import { skipToken } from "@reduxjs/toolkit/query";
import { useGetArtifactQuery } from "@/features/run-deliverables/api/run-deliverables-api";
import { Image, LoaderCircle } from "@/shared/ui/icons";
import {
  useCanvasPlaybackQuery,
  useCanvasThumbnailQuery,
  useCanvasCardTextQuery,
  useCanvasPdfThumbnailQuery,
} from "../api/node-preview-api";
import { MarkdownView } from "./markdown-view";
import { BlockDocument } from "../documents/block-editor-schema";
import {
  useCanvasBodyQuery,
  useCanvasDocumentQuery,
  type CanvasScope,
  type CanvasVersion,
} from "../api/canvas-api";
import type { NodePreviewData } from "./node-preview.types";

export type ImagePreviewInfo = Pick<
  CanvasVersion,
  "filename" | "contentType" | "sizeBytes"
> & {
  versionId: string;
  dimensions?: NodePreviewData["dimensions"];
};

export function NodePreview({
  scope,
  version,
  excerpt,
  reading: selected,
  documentPreview = false,
  detailed,
  onImageInfo,
  blockId,
  draft,
}: {
  scope: CanvasScope;
  version: CanvasVersion;
  excerpt?: string;
  reading: boolean;
  documentPreview?: boolean;
  detailed: boolean;
  onImageInfo: (info: ImagePreviewInfo | undefined) => void;
  blockId?: string;
  draft?: boolean;
}) {
  const reading = selected && !documentPreview;
  const output = useGetArtifactQuery(
    version.outputId
      ? { companyId: scope.companyId, artifactId: version.outputId }
      : skipToken,
  );
  const files = !output.isError
    ? output.currentData?.result.output?.items
    : undefined;
  const primary = files?.find((file) => file.role === "primary") ?? files?.[0];
  const contentType = previewContentType(
    primary?.contentType ?? version.contentType,
    primary?.displayName ?? version.filename,
  );
  const isImage = contentType.startsWith("image/");
  const image = isImage ? primary : undefined;
  const fileInput = {
    companyId: scope.companyId,
    attachmentId: version.outputId
      ? undefined
      : (version.attachmentId ?? undefined),
    outputId: version.outputId ?? undefined,
    ordinal: primary?.ordinal,
    sizeBytes: primary?.sizeBytes ?? version.sizeBytes ?? 0,
    contentType,
  };
  const wiki = useCanvasBodyQuery(
    detailed && !draft && version.sourceType === "wiki"
      ? { ...scope, versionId: version.id }
      : skipToken,
  );
  const live = useCanvasDocumentQuery(
    detailed && draft && version.sourceType === "wiki"
      ? { ...scope, versionId: version.id, blockId, preview: !reading }
      : skipToken,
  );
  const text = useCanvasCardTextQuery(
    detailed &&
      version.sourceType !== "wiki" &&
      /^text\/(plain|html|(?:x-)?markdown)|wordprocessingml/.test(contentType)
      ? fileInput
      : skipToken,
  );
  const document =
    !wiki.isError && wiki.currentData
      ? wiki.currentData.body
      : !text.isError
        ? text.currentData
        : undefined;
  const body =
    document !== undefined
      ? reading
        ? document
        : document.trim().slice(0, 2400)
      : excerpt;
  const loading = wiki.isFetching || text.isFetching || output.isLoading;
  const media = useCanvasPdfThumbnailQuery(
    detailed && contentType === "application/pdf" ? fileInput : skipToken,
  );
  const playback = useCanvasPlaybackQuery(
    detailed && selected && /^(video|audio)\//.test(contentType)
      ? fileInput
      : skipToken,
  );
  const thumbnail = useCanvasThumbnailQuery(isImage ? fileInput : skipToken);
  const filename = primary?.displayName ?? version.filename;
  const sizeBytes = primary?.sizeBytes ?? version.sizeBytes;
  const dimensions = !thumbnail.isError
    ? thumbnail.currentData?.dimensions
    : undefined;
  useEffect(() => {
    onImageInfo(
      isImage
        ? {
            versionId: version.id,
            filename,
            contentType,
            sizeBytes,
            dimensions,
          }
        : undefined,
    );
  }, [
    onImageInfo,
    isImage,
    version.id,
    filename,
    contentType,
    sizeBytes,
    dimensions,
  ]);
  if (isImage)
    return (
      <div className="pc-node-image">
        {thumbnail.currentData && !thumbnail.isError ? (
          <img
            src={thumbnail.currentData.value}
            alt={image?.displayName ?? version.filename ?? "Artifact preview"}
            draggable={false}
            decoding="async"
          />
        ) : (
          <div
            className="h-full flex flex-col items-center justify-center gap-[12px] p-[24px] text-[var(--pc-muted)] text-[length:12px] text-center wrap-anywhere"
            role="status"
          >
            {thumbnail.isFetching ? (
              <LoaderCircle size={24} className="pc-spin" />
            ) : (
              <Image size={28} />
            )}
            <span>
              {thumbnail.isError ? "Open to view image" : "Loading image…"}
            </span>
          </div>
        )}
      </div>
    );
  if (contentType === "application/pdf")
    return media.currentData && !media.isError ? (
      <img
        className={presentationStyles["pc-node-media"]}
        src={media.currentData.value}
        alt={filename ?? "PDF first page"}
        draggable={false}
      />
    ) : (
      <div
        className="h-full flex flex-col items-center justify-center gap-[12px] p-[24px] text-[var(--pc-muted)] text-[length:12px] text-center wrap-anywhere"
        role="status"
      >
        {media.isFetching ? "Loading preview…" : "Open to preview this file"}
      </div>
    );
  if (/^(video|audio)\//.test(contentType)) {
    const source = playback.currentData;
    if (selected && source?.status === "ready")
      return contentType.startsWith("video/") ? (
        <video
          className={`${presentationStyles["pc-node-media"]} nodrag nopan`}
          src={source.value}
          controls
          preload="metadata"
          playsInline
          aria-label={filename ?? "Video preview"}
        />
      ) : (
        <div className={`${presentationStyles["pc-node-audio"]} nodrag nopan`}>
          <span>{filename ?? "Audio"}</span>
          <audio
            src={source.value}
            controls
            preload="metadata"
            aria-label={filename ?? "Audio preview"}
          />
        </div>
      );
    return (
      <div
        className="h-full flex flex-col items-center justify-center gap-[12px] p-[24px] text-[var(--pc-muted)] text-[length:12px] text-center wrap-anywhere"
        role="status"
      >
        <span>{filename ?? "Media"}</span>
        <span>
          {!selected
            ? "Select to preview"
            : source?.status === "error"
              ? "Open to preview this file"
              : "Loading preview…"}
        </span>
      </div>
    );
  }
  if (contentType === "text/html" && text.currentData && !text.isError)
    return (
      <iframe
        className={presentationStyles["pc-node-html"]}
        sandbox=""
        referrerPolicy="no-referrer"
        title={filename ?? "HTML preview"}
        srcDoc={`<!doctype html><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:; form-action 'none'; base-uri 'none'">${text.currentData}`}
        tabIndex={-1}
      />
    );
  const structured = draft
    ? !live.isError
      ? live.currentData?.content
      : undefined
    : !wiki.isError
      ? wiki.currentData?.content
      : undefined;
  if (structured?.type === "doc")
    return (
      <div
        className={`pc-node-document ${documentPreview ? presentationStyles["is-paper-preview"] : "nodrag nopan nowheel"}`}
        onDoubleClick={
          documentPreview ? undefined : (event) => event.stopPropagation()
        }
      >
        <BlockDocument
          scope={scope}
          content={structured}
          blockId={blockId}
          live={draft && !documentPreview}
        />
      </div>
    );
  if (draft)
    return (
      <p>{live.isError ? "Live source unavailable" : "Loading live blocks…"}</p>
    );
  if (body !== undefined && (body || document !== undefined))
    return (
      <div
        className={`pc-node-document ${documentPreview ? presentationStyles["is-paper-preview"] : "nodrag nopan nowheel"}`}
        tabIndex={documentPreview ? undefined : 0}
        aria-label={reading ? "Document content" : "Document excerpt"}
        onDoubleClick={
          documentPreview ? undefined : (event) => event.stopPropagation()
        }
      >
        {contentType === "text/plain" ? (
          <pre className="m-0 whitespace-pre-wrap wrap-anywhere [font-family:var(--font-mono)] text-[length:11px] leading-[1.6] [user-select:text]">
            {body || "This document is empty."}
          </pre>
        ) : (
          <MarkdownView>{body || "This document is empty."}</MarkdownView>
        )}
        <span className="pc-card-reader-hint">
          {wiki.isFetching || text.isFetching
            ? "Loading document…"
            : reading && document !== undefined
              ? "End of document"
              : wiki.isError || text.isError
                ? "Open the artifact to read this document"
                : "Select this card to read the document"}
        </span>
      </div>
    );
  return (
    <div
      className="h-full flex flex-col items-center justify-center gap-[12px] p-[24px] text-[var(--pc-muted)] text-[length:12px] text-center wrap-anywhere"
      role="status"
    >
      {loading && <LoaderCircle size={24} className="pc-spin" />}
      <span>
        {loading
          ? "Loading preview…"
          : text.isError || wiki.isError
            ? "Open the artifact to read this document"
            : version.filename || "Open to explore"}
      </span>
    </div>
  );
}
