import { useEffect, useRef, useState } from "react";
import { ImageIcon, Music } from "@/shared/ui/icons";

import {
  createArtifactFilePreview,
  downloadArtifactFile,
  maximumArtifactPreviewBytes,
  type ArtifactFileSelection,
} from "@/features/chat-panel/model/artifact-file-access";

export function ArtifactThumbnail({
  companyId,
  file,
}: {
  companyId: string;
  file: ArtifactFileSelection;
}) {
  const containerRef = useRef<HTMLSpanElement>(null);
  const [url, setUrl] = useState<string>();
  const [format, setFormat] = useState<"image" | "video">("image");
  const fileKey = JSON.stringify(file);
  useEffect(() => {
    setUrl(undefined);
    if (
      file.sizeBytes <= 0 ||
      file.sizeBytes > maximumArtifactPreviewBytes ||
      !/^(image|video)\//.test(file.contentType)
    ) {
      return;
    }
    const controller = new AbortController();
    let observer: IntersectionObserver | undefined;
    let objectUrl: string | undefined;
    let started = false;
    const load = async () => {
      if (started || controller.signal.aborted) return;
      started = true;
      observer?.disconnect();
      try {
        const blob = await downloadArtifactFile(
          companyId,
          file,
          controller.signal,
        );
        const preview = await createArtifactFilePreview(blob, file);
        if (
          controller.signal.aborted ||
          (preview.format !== "image" && preview.format !== "video")
        )
          return;
        setFormat(preview.format);
        objectUrl = URL.createObjectURL(preview.blob);
        setUrl(objectUrl);
      } catch {
        // Thumbnails are optional; the full viewer exposes recovery actions.
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
  }, [companyId, fileKey]);
  return (
    <span className="artifact-thumbnail" ref={containerRef}>
      {url && format === "video" ? (
        <video
          aria-hidden
          muted
          playsInline
          preload="auto"
          tabIndex={-1}
          onLoadedMetadata={(event) => {
            const video = event.currentTarget;
            if (Number.isFinite(video.duration) && video.duration > 0) {
              video.currentTime = Math.min(0.1, video.duration / 2);
            }
          }}
          onError={() => setUrl(undefined)}
          src={url}
        />
      ) : url ? (
        <img
          alt=""
          draggable={false}
          onError={() => setUrl(undefined)}
          src={url}
        />
      ) : file.contentType.startsWith("audio/") ? (
        <Music aria-hidden size={20} />
      ) : (
        <ImageIcon aria-hidden size={20} />
      )}
    </span>
  );
}
