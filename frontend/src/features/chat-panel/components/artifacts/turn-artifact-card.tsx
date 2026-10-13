import type { components } from "@/shared/api/generated/api.generated";
import { Link } from "react-router";
import {
  ChevronRight,
  File,
  FileText,
  Globe2,
  ImageIcon,
  LayoutDashboard,
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
  const caption =
    subtitle ||
    (type === "whiteboard"
      ? "Whiteboard"
      : fileName !== title
        ? fileName
        : undefined);
  // An app or a Whiteboard is one experience, not the files it holds.
  const variant =
    type === "app" || type === "whiteboard"
      ? type
      : fileVariant(files[0]?.contentType, type);
  const Icon = fileIcons[variant];
  const content = (
    <>
      {!multiple ? (
        <span
          className="grid [flex:0_0_32px] h-[34px] place-items-center [border:1px_solid_color-mix(in_srgb,_var(--artifact-card-tint)_14%,_transparent)] rounded-[7px] [background:color-mix(in_srgb,_var(--artifact-card-tint)_8%,_transparent)] text-[var(--artifact-card-tint)]"
          aria-hidden
        >
          <Icon size={18} />
        </span>
      ) : null}
      <span className="flex min-w-0 flex-1 flex-col gap-[2px]">
        <span className="[display:-webkit-box] overflow-hidden [-webkit-box-orient:vertical] [-webkit-line-clamp:2] wrap-anywhere text-[length:13px] font-[600] leading-[1.45]">
          {title}
        </span>
        {stale && (
          <small title="The shared content or suggestions have changed since this delivery.">
            Stale · changes since delivery
          </small>
        )}
        {multiple ? (
          <span className="flex flex-col gap-[3px] mt-[3px]">
            {files.slice(0, 3).map((file, index) => {
              const variant = fileVariant(file.contentType);
              const FileIcon = fileIcons[variant];
              return (
                <span
                  className="turn-artifact-card__file flex items-center gap-[6px] min-w-0 text-[var(--workspace-native-text-muted,_#697168)]"
                  key={index}
                >
                  <FileIcon aria-hidden size={14} />
                  <span
                    className="overflow-hidden text-[var(--workspace-native-text-muted,_#697168)] text-[length:11px] leading-[1.45] text-ellipsis whitespace-nowrap"
                    title={file.fileName}
                  >
                    {file.fileName}
                  </span>
                </span>
              );
            })}
            {files.length > 3 ? (
              <span className="overflow-hidden text-[var(--workspace-native-text-muted,_#697168)] text-[length:11px] leading-[1.45] text-ellipsis whitespace-nowrap">
                +{files.length - 3} more files
              </span>
            ) : null}
          </span>
        ) : caption ? (
          <span
            className="overflow-hidden text-[var(--workspace-native-text-muted,_#697168)] text-[length:11px] leading-[1.45] text-ellipsis whitespace-nowrap"
            title={caption}
          >
            {caption}
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
  whiteboard: LayoutDashboard,
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
