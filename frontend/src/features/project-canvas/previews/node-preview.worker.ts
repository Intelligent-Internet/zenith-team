import type {
  NodePreviewRequest,
  NodePreviewResponse,
} from "./node-preview.types";

self.onmessage = async ({ data }: MessageEvent<NodePreviewRequest>) => {
  let bitmap: ImageBitmap | undefined;
  try {
    if (data.format === "text") {
      if (data.blob.size > 1024 * 1024)
        throw new Error("Document exceeds preview limit");
      postMessage({
        value: await data.blob.text(),
      } satisfies NodePreviewResponse);
      return;
    }
    if (data.blob.type === "image/svg+xml" && data.blob.size <= 1024 * 1024) {
      postMessage({
        value: await dataURL(data.blob),
      } satisfies NodePreviewResponse);
      return;
    }
    bitmap = await createImageBitmap(data.blob);
    const scale = Math.min(1, 640 / Math.max(bitmap.width, bitmap.height));
    const canvas = new OffscreenCanvas(
      Math.max(1, Math.round(bitmap.width * scale)),
      Math.max(1, Math.round(bitmap.height * scale)),
    );
    const context = canvas.getContext("2d");
    if (!context) throw new Error("Preview unavailable");
    context.drawImage(bitmap, 0, 0, canvas.width, canvas.height);
    const blob = await canvas.convertToBlob({
      type: "image/webp",
      quality: 0.8,
    });
    const url = await dataURL(blob);
    postMessage({
      value: url,
      dimensions: { width: bitmap.width, height: bitmap.height },
    } satisfies NodePreviewResponse);
  } catch {
    postMessage({
      error: "Image preview unavailable",
    } satisfies NodePreviewResponse);
  } finally {
    bitmap?.close();
  }
};

function dataURL(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(blob);
  });
}
