import type {
  DesktopGlobalLike,
  DesktopRunPreviewFrame,
} from "../../../electron/shared/desktop-bridge.types";
import { getZenithTeamDesktopBridge } from "@/shared/desktop/desktop-environment.service";

export type LocalRunPreviewFrame = DesktopRunPreviewFrame;

export function subscribeToLocalRunPreview(
  listener: (frame: LocalRunPreviewFrame) => void,
  globalScope: DesktopGlobalLike = globalThis,
): () => void {
  return (
    getZenithTeamDesktopBridge(globalScope)?.onRunPreview?.(listener) ??
    (() => {})
  );
}
