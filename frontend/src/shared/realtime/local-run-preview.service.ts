import {
  getBoardlyDesktopBridge,
  type DesktopGlobalLike,
  type DesktopRunPreviewFrame,
} from "@/app/desktop/desktop-environment.service";

export type LocalRunPreviewFrame = DesktopRunPreviewFrame;

export function subscribeToLocalRunPreview(
  listener: (frame: LocalRunPreviewFrame) => void,
  globalScope: DesktopGlobalLike = globalThis,
): () => void {
  return (
    getBoardlyDesktopBridge(globalScope)?.onRunPreview?.(listener) ?? (() => {})
  );
}
