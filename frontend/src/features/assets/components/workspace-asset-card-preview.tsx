import type { DesktopWorkspaceAssetItem } from "../../../../electron/shared/desktop-bridge.types";
import type { ReactNode } from "react";

import { useLazyWorkspaceAssetPreview } from "@/features/assets/hooks/use-lazy-workspace-asset-preview";
import { cx } from "@/shared/lib/cx";

type WorkspaceAssetCardPreviewDensity = "compact" | "micro" | "regular";

const contentClassNames: Record<WorkspaceAssetCardPreviewDensity, string> = {
  compact:
    "inset-x-3 bottom-11 top-3 text-[7px] leading-[9px] text-[#0f233f]/58",
  micro: "inset-2 text-[5px] leading-[7px] text-[#0f233f]/58",
  regular:
    "inset-x-4 bottom-14 top-4 text-[8px] leading-[10px] text-[#0f233f]/58",
};

const filenameClassNames: Record<WorkspaceAssetCardPreviewDensity, string> = {
  compact: "inset-x-3 bottom-3 text-[8px] leading-[11px]",
  micro: "inset-x-2 bottom-2 text-[6px] leading-[8px]",
  regular: "inset-x-4 bottom-4 text-[12px] leading-[18px]",
};

export function WorkspaceAssetCardPreview({
  className,
  companyId,
  density = "regular",
  fallback,
  item,
  scope,
  showFilename = false,
}: {
  className?: string;
  companyId: string;
  density?: WorkspaceAssetCardPreviewDensity;
  fallback?: ReactNode;
  item: DesktopWorkspaceAssetItem;
  scope: string;
  showFilename?: boolean;
}) {
  const { preview, previewRef } = useLazyWorkspaceAssetPreview<HTMLSpanElement>(
    { companyId, item, scope },
  );
  const documentPreview =
    preview?.format === "markdown" || preview?.format === "text";
  const imageSource =
    preview?.format === "image" && preview.contentBase64
      ? `data:${preview.mimeType};base64,${preview.contentBase64}`
      : null;
  const { base, extension } = splitAssetFilename(item.filename);

  return (
    <span
      ref={previewRef}
      className={cx(
        "relative block size-full overflow-hidden",
        documentPreview && "bg-[#fffdf8]",
        className,
      )}
    >
      {imageSource ? (
        <img
          alt=""
          className="absolute inset-0 size-full object-cover"
          draggable={false}
          src={imageSource}
        />
      ) : documentPreview ? (
        <span
          className={cx(
            "absolute overflow-hidden whitespace-pre-wrap break-words text-left font-mono font-normal",
            contentClassNames[density],
          )}
        >
          {preview.content}
        </span>
      ) : (
        fallback
      )}

      {documentPreview && showFilename ? (
        <span className="pointer-events-none absolute inset-x-0 bottom-0 h-[34%] bg-gradient-to-b from-[#fffdf8]/0 via-[#fffdf8]/92 to-[#fffdf8]" />
      ) : null}
      {showFilename ? (
        <span
          className={cx(
            "pointer-events-none absolute max-h-[44px] overflow-hidden [overflow-wrap:anywhere] text-left font-mono font-bold text-[#0f233f]",
            filenameClassNames[density],
          )}
        >
          {base}
          <span className="font-normal text-[#0f233f]/45">{extension}</span>
        </span>
      ) : null}
    </span>
  );
}

function splitAssetFilename(filename: string) {
  const extensionIndex = filename.lastIndexOf(".");
  if (extensionIndex <= 0) return { base: filename, extension: "" };
  return {
    base: filename.slice(0, extensionIndex),
    extension: filename.slice(extensionIndex),
  };
}
