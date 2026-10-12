import type { RefObject } from "react";
import { usePanelResize } from "@/shared/ui/use-panel-resize";

export function useArtifactsResize(
  asideRef: RefObject<HTMLDivElement | null>,
  viewer: boolean,
  visible: boolean,
) {
  return usePanelResize(asideRef, {
    side: "right",
    mode: viewer ? "viewer" : "list",
    visible,
    defaultWidth: viewer ? (width) => width * 0.56 : 360,
    minWidth: 280,
    remainingWidth: 320,
    label: "Resize artifacts panel",
  });
}
