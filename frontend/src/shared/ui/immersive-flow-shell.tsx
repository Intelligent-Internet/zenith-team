import type { CSSProperties, ReactNode } from "react";
import { publicAssetCssUrl } from "@/shared/lib/public-assets";
import "@/shared/styles/immersive-flow.css";

type ImmersiveFlowShellProps = {
  children: ReactNode;
  hero: ReactNode;
  label: string;
};

const immersiveFlowShellStyle = {
  "--immersive-canvas-dark-bg-image": publicAssetCssUrl(
    "images/ui/dark-2.webp",
  ),
} as CSSProperties;

export function ImmersiveFlowShell({
  children,
  hero,
  label,
}: ImmersiveFlowShellProps) {
  return (
    <div
      className="immersive-canvas-texture immersive-canvas-dark flex h-dvh w-screen flex-col overflow-x-hidden overflow-y-auto min-[861px]:h-screen min-[861px]:overflow-hidden"
      style={immersiveFlowShellStyle}
    >
      <main
        className="grid flex-none gap-4 overflow-visible p-10 grid-cols-[minmax(0,664fr)_minmax(0,1024fr)] min-[861px]:min-h-0 min-[861px]:flex-1 min-[861px]:overflow-hidden max-[860px]:min-h-max max-[860px]:grid-cols-1 max-[860px]:grid-rows-[minmax(220px,40vh)_auto] max-[860px]:p-5 max-[520px]:grid-rows-[minmax(220px,38vh)_auto]"
        aria-label={label}
      >
        {hero}
        {children}
      </main>
    </div>
  );
}
