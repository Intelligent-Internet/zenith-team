import { useEffect, useRef, useState, type CSSProperties } from "react";
import type { AttachmentPreviewFile } from "@/features/chat-panel/components/artifacts/attachment-file-view";
import { TurnArtifactCard } from "@/features/chat-panel/components/artifacts/turn-artifact-card";
import {
  createArtifactFilePreview,
  maximumArtifactPreviewBytes,
} from "@/features/chat-panel/model/artifact-file-access";
import { attachmentService } from "@/shared/api/attachment.service";
import {
  attachmentKind,
  formatAttachmentSize,
} from "@/shared/chat/attachment-preview";
import { ImageIcon } from "@/shared/ui/icons";
import { useDiscussionPreview } from "./discussion-preview";
import { useDiscussionRuntime } from "./discussion-provider";

export function DiscussionAttachments({
  files,
}: {
  files: readonly AttachmentPreviewFile[];
}) {
  const { session } = useDiscussionRuntime();
  const { openAttachment } = useDiscussionPreview();
  const images = files.filter(isPreviewableImage);
  const otherFiles = files.filter((file) => !isPreviewableImage(file));
  if (!files.length) return null;

  return (
    <div
      className="flex min-w-0 flex-col [align-items:start] gap-[8px] m-[8px_0]"
      aria-label="Message attachments"
    >
      {files.length > 1 ? (
        <span className="text-[var(--discussion-muted)] text-[length:13px] leading-[20px]">
          {files.length} attachments
        </span>
      ) : null}
      {images.length ? (
        <div
          className="discussion-image-gallery"
          data-multiple={images.length > 1}
        >
          {images.map((file) => (
            <DiscussionImageAttachment
              key={`${session.companyId}:${file.id}`}
              companyId={session.companyId}
              file={file}
              inGallery={images.length > 1}
              onOpen={() => openAttachment(file)}
            />
          ))}
        </div>
      ) : null}
      {otherFiles.length ? (
        <div className="flex flex-wrap max-w-full [align-items:start] gap-[8px]">
          {otherFiles.map((file) => (
            <DiscussionAttachmentCard
              key={file.id}
              file={file}
              onOpen={() => openAttachment(file)}
            />
          ))}
        </div>
      ) : null}
    </div>
  );
}

function isPreviewableImage(file: AttachmentPreviewFile) {
  return (
    file.sizeBytes > 0 &&
    file.sizeBytes <= maximumArtifactPreviewBytes &&
    attachmentKind({
      id: file.id,
      name: file.filename,
      mediaType: file.contentType,
    }) === "image"
  );
}

function DiscussionAttachmentCard({
  file,
  onOpen,
}: {
  file: AttachmentPreviewFile;
  onOpen: () => void;
}) {
  const detail = [
    file.filename.split(".").pop()?.toUpperCase(),
    formatAttachmentSize(file.sizeBytes),
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <TurnArtifactCard
      title={file.filename}
      subtitle={detail}
      ariaLabel={`Preview ${file.filename}`}
      files={[{ fileName: file.filename, contentType: file.contentType }]}
      onClick={onOpen}
    />
  );
}

function DiscussionImageAttachment({
  companyId,
  file,
  inGallery,
  onOpen,
}: {
  companyId: string;
  file: AttachmentPreviewFile;
  inGallery: boolean;
  onOpen: () => void;
}) {
  const containerRef = useRef<HTMLButtonElement>(null);
  const [preview, setPreview] = useState<{ url: string } | { error: true }>();
  const [aspectRatio, setAspectRatio] = useState(1);

  useEffect(() => {
    setPreview(undefined);
    setAspectRatio(1);
    const controller = new AbortController();
    let observer: IntersectionObserver | undefined;
    let objectUrl: string | undefined;
    let started = false;

    const load = async () => {
      if (started || controller.signal.aborted) return;
      started = true;
      observer?.disconnect();
      try {
        const blob = await attachmentService.download(
          companyId,
          file.id,
          controller.signal,
        );
        if (controller.signal.aborted) return;
        const image = await createArtifactFilePreview(blob, {
          ...file,
          title: file.filename,
        });
        if (controller.signal.aborted) return;
        if (image.format !== "image") {
          setPreview({ error: true });
          return;
        }
        objectUrl = URL.createObjectURL(image.blob);
        setPreview({ url: objectUrl });
      } catch {
        if (!controller.signal.aborted) setPreview({ error: true });
      }
    };

    if (!containerRef.current || !("IntersectionObserver" in window)) {
      void load();
    } else {
      observer = new IntersectionObserver(
        (entries) => {
          if (entries.some((entry) => entry.isIntersecting)) void load();
        },
        { rootMargin: "100px" },
      );
      observer.observe(containerRef.current);
    }

    return () => {
      controller.abort();
      observer?.disconnect();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [companyId, file.id, file.filename, file.contentType, file.sizeBytes]);

  const failed = preview && "error" in preview;
  if (failed && !inGallery) {
    return <DiscussionAttachmentCard file={file} onOpen={onOpen} />;
  }

  return (
    <button
      ref={containerRef}
      type="button"
      className="discussion-image-attachment"
      style={{ "--discussion-image-ratio": aspectRatio } as CSSProperties}
      aria-label={`Preview ${file.filename}`}
      title={file.filename}
      onClick={onOpen}
    >
      <span className="discussion-image-attachment-preview">
        {preview && "url" in preview ? (
          <img
            src={preview.url}
            alt={file.filename}
            decoding="async"
            onLoad={(event) => {
              const image = event.currentTarget;
              if (image.naturalWidth && image.naturalHeight)
                setAspectRatio(image.naturalWidth / image.naturalHeight);
            }}
            onError={() => setPreview({ error: true })}
          />
        ) : (
          <span
            className="flex w-[320px] max-w-full h-[180px] items-center justify-center gap-[8px] text-[var(--discussion-muted)] text-[length:12px]"
            role="status"
          >
            <ImageIcon aria-hidden size={24} />
            {failed ? "Image unavailable" : "Loading image…"}
          </span>
        )}
      </span>
    </button>
  );
}
