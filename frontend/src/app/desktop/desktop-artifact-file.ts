import {
  getBoardlyDesktopBridge,
  type DesktopArtifactFileReference,
} from "./desktop-environment.service";

// The main process owns credentials, disk access and permission revalidation.
// Web clients and files above the desktop cache limit use the normal API path.
export async function downloadDesktopArtifactFile(
  reference: DesktopArtifactFileReference,
  signal?: AbortSignal,
): Promise<Blob | null> {
  signal?.throwIfAborted();
  const bridge = getBoardlyDesktopBridge();
  if (!bridge?.readArtifactFile) return null;
  const requestId = crypto.randomUUID();
  const cancel = () => {
    void bridge.cancelArtifactFile?.(requestId).catch(() => {});
  };
  signal?.addEventListener("abort", cancel, { once: true });
  try {
    const result = await bridge.readArtifactFile({ ...reference, requestId });
    signal?.throwIfAborted();
    if (!result.ok) {
      if (result.code === "uncacheable") return null;
      if (result.code === "canceled")
        throw new DOMException("Download canceled", "AbortError");
      throw new Error(
        result.code === "session_missing"
          ? "Please sign in again to open this artifact."
          : "Unable to load this artifact. Please try again.",
      );
    }
    return new Blob([new Uint8Array(result.bytes)], {
      type: result.contentType,
    });
  } finally {
    signal?.removeEventListener("abort", cancel);
  }
}
