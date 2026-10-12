import type { components } from "@/shared/api/generated/api.generated";
import { Link } from "react-router";
import {
  ChevronRight,
  File,
  FileText,
  Globe2,
  ImageIcon,
  Music,
  Video,
} from "@/shared/ui/icons";
import type { ArtifactType } from "@/features/run-deliverables/model/run-deliverable";

import "../../styles/turn-artifact-card.css";

export function TurnArtifactCard({
  title,
  type,
  files,
  subtitle,
  stale,
  ariaLabel,
  onClick,
  to,
}: {
  title: string;
  type?: ArtifactType;
  files: components["schemas"]["DiscussionArtifactFile"][];
  subtitle?: string;
  stale?: boolean;
  ariaLabel?: string;
  onClick?: () => void;
  to?: string;
}) {
  const multiple = files.length > 1;
  const fileName = files[0]?.fileName;
  const variant =
    type === "app" ? "app" : fileVariant(files[0]?.contentType, type);
  const Icon = fileIcons[variant];
  const content = (
    <>
      {!multiple ? (
        <span className="turn-artifact-card__icon" aria-hidden>
          <Icon size={18} />
        </span>
      ) : null}
      <span className="turn-artifact-card__body">
        <span className="turn-artifact-card__title">{title}</span>
        {stale && (
          <small title="The shared content or suggestions have changed since this delivery.">
            Stale · changes since delivery
          </small>
        )}
        {multiple ? (
          <span className="turn-artifact-card__files">
            {files.slice(0, 3).map((file, index) => {
              const variant = fileVariant(file.contentType);
              const FileIcon = fileIcons[variant];
              return (
                <span className="turn-artifact-card__file" key={index}>
                  <FileIcon aria-hidden size={14} />
                  <span
                    className="turn-artifact-card__filename"
                    title={file.fileName}
                  >
                    {file.fileName}
                  </span>
                </span>
              );
            })}
            {files.length > 3 ? (
              <span className="turn-artifact-card__filename">
                +{files.length - 3} more files
              </span>
            ) : null}
          </span>
        ) : subtitle || (fileName && fileName !== title) ? (
          <span
            className="turn-artifact-card__filename"
            title={subtitle ?? fileName}
          >
            {subtitle ?? fileName}
          </span>
        ) : null}
      </span>
      <ChevronRight
        size={16}
        aria-hidden
        className="turn-artifact-card__arrow"
      />
    </>
  );
  return to ? (
    <Link
      to={to}
      className="turn-artifact-card"
      data-artifact-type={variant}
      title={title}
      aria-label={ariaLabel}
    >
      {content}
    </Link>
  ) : (
    <button
      type="button"
      onClick={onClick}
      className="turn-artifact-card"
      data-artifact-type={variant}
      title={title}
      aria-label={ariaLabel}
    >
      {content}
    </button>
  );
}

const fileIcons = {
  app: Globe2,
  media: ImageIcon,
  image: ImageIcon,
  audio: Music,
  video: Video,
  text: FileText,
  file: File,
};

function fileVariant(contentType?: string, fallback: ArtifactType = "file") {
  const mime = contentType?.split(";")[0].trim().toLowerCase();
  if (mime?.startsWith("image/")) return "image";
  if (mime?.startsWith("audio/")) return "audio";
  if (mime?.startsWith("video/")) return "video";
  if (
    mime?.startsWith("text/") ||
    mime === "application/pdf" ||
    mime?.includes("document") ||
    mime === "application/json"
  )
    return "text";
  return fallback;
}
