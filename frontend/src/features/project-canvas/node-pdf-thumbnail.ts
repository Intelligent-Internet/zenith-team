import {
  getDocument,
  GlobalWorkerOptions,
} from "pdfjs-dist/legacy/build/pdf.mjs";
import workerUrl from "pdfjs-dist/legacy/build/pdf.worker.min.mjs?url";
import type { NodePreviewData } from "./node-preview.types";

GlobalWorkerOptions.workerSrc = workerUrl;

export async function pdfThumbnail(
  blob: Blob,
  signal: AbortSignal,
): Promise<NodePreviewData> {
  const data = await blob.arrayBuffer();
  signal.throwIfAborted();
  const task = getDocument({
    data,
    useSystemFonts: true,
  });
  const abort = () => {
    void task.destroy().catch(() => undefined);
  };
  signal.addEventListener("abort", abort, { once: true });
  const timer = setTimeout(abort, 10_000);
  try {
    const pdf = await task.promise;
    const page = await pdf.getPage(1);
    const natural = page.getViewport({ scale: 1 });
    const viewport = page.getViewport({
      scale: Math.min(1.5, 1000 / natural.width, 1400 / natural.height),
    });
    const canvas = document.createElement("canvas");
    canvas.width = Math.ceil(viewport.width);
    canvas.height = Math.ceil(viewport.height);
    await page.render({ canvas, viewport }).promise;
    signal.throwIfAborted();
    return { value: canvas.toDataURL("image/webp", 0.85) };
  } finally {
    clearTimeout(timer);
    signal.removeEventListener("abort", abort);
    await task.destroy();
  }
}
