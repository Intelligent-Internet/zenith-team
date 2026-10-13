import type {
  DesktopGlobalLike,
  DesktopRunPreviewFrame,
} from "../../../electron/shared/desktop-bridge.types";
import { getBoardlyDesktopBridge } from "@/shared/desktop/desktop-environment.service";

export type LocalRunPreviewFrame = DesktopRunPreviewFrame;

export function subscribeToLocalRunPreview(
  listener: (frame: LocalRunPreviewFrame) => void,
  globalScope: DesktopGlobalLike = globalThis,
): () => void {
  return (
    getBoardlyDesktopBridge(globalScope)?.onRunPreview?.(listener) ?? (() => {})
  );
}
