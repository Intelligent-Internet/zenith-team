import type { ReactNode } from "react";

import { cx } from "@/shared/lib/cx";

export type StackedAssetPreviewItem = {
  content?: ReactNode;
  id: string;
  imageSrc?: string;
  surfaceClassName?: string;
  title?: string;
};

type StackedAssetPreviewProps = {
  className?: string;
  previews: StackedAssetPreviewItem[];
  size?: "compact" | "large";
};

const compactLayers = [
  {
    cardClassName: "h-[174px] w-[132px] rotate-[15deg]",
    layerClassName: "left-[56px] top-[17px]",
    previewIndex: 2,
  },
  {
    cardClassName: "h-[174px] w-[140px] -rotate-[15deg]",
    layerClassName: "left-[20px] top-[22px]",
    previewIndex: 1,
  },
  {
    cardClassName: "h-[182px] w-[148px]",
    layerClassName: "left-[32px] top-[30px]",
    previewIndex: 0,
  },
] as const;

const largeLayers = [
  {
    cardClassName: "h-[254.101px] w-[193.525px] rotate-[15deg]",
    layerClassName:
      "left-[70.3px] top-0 flex h-[295.531px] w-[252.697px] items-center justify-center",
    previewIndex: 2,
  },
  {
    cardClassName: "h-[254.101px] w-[205.254px] -rotate-[15deg]",
    layerClassName:
      "left-0 top-[10.21px] flex h-[298.567px] w-[264.026px] items-center justify-center",
    previewIndex: 1,
  },
  {
    cardClassName: "h-[266px] w-[217px]",
    layerClassName: "left-[55px] top-[44px]",
    previewIndex: 0,
  },
] as const;

/**
 * Renders the three-layer asset preview used by Home deliverables and Assets.
 * The preview is decorative; its surrounding card owns the accessible label.
 */
export function StackedAssetPreview({
  className,
  previews,
  size = "compact",
}: StackedAssetPreviewProps) {
  const layers = size === "large" ? largeLayers : compactLayers;

  return (
    <div
      aria-hidden="true"
      className={cx(
        "relative shrink-0",
        size === "large" ? "h-[310px] w-[323px]" : "h-[212px] w-[209px]",
        className,
      )}
    >
      <div
        className={cx(
          "absolute inset-0",
          size === "large" && "origin-top scale-[0.94]",
        )}
      >
        {layers.map((layer) => {
          const preview = previews[layer.previewIndex];

          return (
            <div
              className={cx(
                "pointer-events-none absolute",
                layer.layerClassName,
              )}
              key={preview?.id ?? `asset-preview-layer-${layer.previewIndex}`}
            >
              <div
                className={cx(
                  "relative overflow-hidden rounded-xl border border-[var(--workspace-divider,#e5dfe8)]",
                  layer.previewIndex === 0
                    ? "shadow-[0_6px_16px_rgba(45,38,49,0.08)]"
                    : "shadow-[0_2px_6px_rgba(45,38,49,0.04)]",
                  preview?.surfaceClassName ??
                    (layer.previewIndex === 0
                      ? "bg-[var(--workspace-panel-bg,#fff)]"
                      : "bg-[var(--workspace-control-bg,#f0edf2)]"),
                  layer.cardClassName,
                )}
              >
                {preview?.imageSrc ? (
                  <img
                    alt=""
                    className="h-full w-full object-cover"
                    draggable={false}
                    src={preview.imageSrc}
                  />
                ) : null}
                {preview?.content ? (
                  <span className="absolute inset-0 grid place-items-center">
                    {preview.content}
                  </span>
                ) : null}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
