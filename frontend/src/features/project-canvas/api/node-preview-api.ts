import { runDeliverableService } from "@/features/run-deliverables/api/run-deliverable.service";
import { attachmentService } from "@/shared/api/attachment.service";
import type { PreviewResponse } from "../previews/preview-worker.types";
import { zenithTeamApi } from "@/shared/api/zenith-team-api";
import type {
  NodePreviewData,
  NodePreviewRequest,
  NodePreviewResponse,
} from "../previews/node-preview.types";

// Visible cards subscribe to previews at every zoom level.
// Bound downloads and decoding so panning cannot start hundreds of image jobs.
let active = 0;
const waiting = new Set<() => void>();
async function acquire(signal: AbortSignal) {
  while (active >= 2) {
    await new Promise<void>((resolve, reject) => {
      const wake = () => {
        cleanup();
        resolve();
      };
      const abort = () => {
        cleanup();
        reject(signal.reason);
      };
      const cleanup = () => {
        waiting.delete(wake);
        signal.removeEventListener("abort", abort);
      };
      waiting.add(wake);
      signal.addEventListener("abort", abort, { once: true });
      if (signal.aborted) abort();
    });
  }
  signal.throwIfAborted();
  active++;
  return () => {
    active--;
    waiting.values().next().value?.();
  };
}
type NodePreviewInput = {
  companyId: string;
  attachmentId?: string;
  outputId?: string;
  ordinal?: number;
  sizeBytes: number;
  contentType: string;
};
function prepare(
  blob: Blob,
  signal: AbortSignal,
  format: "image" | "text" | "docx" | "html",
): Promise<NodePreviewData> {
  return new Promise((resolve, reject) => {
    const worker =
      format === "docx" || format === "html"
        ? new Worker(
            new URL("../previews/preview.worker.ts", import.meta.url),
            {
              type: "module",
            },
          )
        : new Worker(
            new URL("../previews/node-preview.worker.ts", import.meta.url),
            {
              type: "module",
            },
          );
    let settled = false;
    const finish = () => {
      settled = true;
      worker.terminate();
      clearTimeout(timer);
      signal.removeEventListener("abort", abort);
    };
    const abort = () => {
      finish();
      reject(signal.reason);
    };
    const timer = setTimeout(() => {
      finish();
      reject(new Error("Preview timed out"));
    }, 10_000);
    worker.onmessage = ({
      data,
    }: MessageEvent<NodePreviewResponse | PreviewResponse>) => {
      finish();
      if ("error" in data) reject(new Error(data.error));
      else if ("content" in data) resolve({ value: data.content });
      else resolve(data);
    };
    worker.onerror = () => {
      finish();
      reject(new Error("Preview unavailable"));
    };
    signal.addEventListener("abort", abort, { once: true });
    if (signal.aborted) abort();
    else if (format === "docx" || format === "html") {
      void blob
        .arrayBuffer()
        .then((buffer) => {
          if (!settled) worker.postMessage({ requestId: 1, buffer, format });
        })
        .catch((error) => {
          finish();
          reject(error);
        });
    } else worker.postMessage({ blob, format } satisfies NodePreviewRequest);
  });
}
async function downloadBlob(
  input: NodePreviewInput,
  signal: AbortSignal,
  limit: number,
): Promise<Blob> {
  if (input.sizeBytes > limit) throw new Error("Open this artifact to read it");
  const blob =
    input.outputId && input.ordinal !== undefined
      ? await runDeliverableService.downloadArtifactFile(
          input.companyId,
          input.outputId,
          input.ordinal,
          signal,
        )
      : input.attachmentId
        ? await attachmentService.download(
            input.companyId,
            input.attachmentId,
            signal,
          )
        : undefined;
  if (!blob || blob.size > limit) throw new Error("Preview unavailable");
  signal.throwIfAborted();
  return blob.slice(0, blob.size, input.contentType);
}
async function download(
  input: NodePreviewInput,
  signal: AbortSignal,
  format: "image" | "text" | "docx" | "html" | "pdf",
) {
  const limit =
    format === "text" || format === "html" ? 1024 * 1024 : 16 * 1024 * 1024;
  const blob = await downloadBlob(input, signal, limit);
  if (format === "pdf") {
    const { pdfThumbnail } = await import("../previews/node-pdf-thumbnail");
    return pdfThumbnail(blob, signal);
  }
  return prepare(blob, signal, format);
}

type PlaybackPreview = {
  status: "loading" | "ready" | "error";
  value: string;
};

export const nodePreviewApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    canvasThumbnail: builder.query<NodePreviewData, NodePreviewInput>({
      keepUnusedDataFor: 10,
      providesTags: (_r, _e, input) =>
        input.outputId
          ? [
              { type: "Artifact", id: input.outputId },
              { type: "Deliverable", id: input.outputId },
            ]
          : [],
      queryFn: async (input, { signal }) => {
        let release: (() => void) | undefined;
        try {
          release = await acquire(signal);
          return { data: await download(input, signal, "image") };
        } catch {
          return {
            error: {
              status: null,
              message: "Open the artifact to view this image.",
            },
          };
        } finally {
          release?.();
        }
      },
    }),
    canvasPlayback: builder.query<PlaybackPreview, NodePreviewInput>({
      keepUnusedDataFor: 0,
      queryFn: () => ({ data: { status: "loading", value: "" } }),
      // The cache lifetime owns the blob; a focus refetch must not reset it.
      merge: () => {},
      async onCacheEntryAdded(
        input,
        { cacheDataLoaded, cacheEntryRemoved, updateCachedData },
      ) {
        try {
          await cacheDataLoaded;
        } catch {
          return;
        }
        const controller = new AbortController();
        let objectUrl: string | undefined;
        const loading = (async () => {
          let release: (() => void) | undefined;
          try {
            release = await acquire(controller.signal);
            const blob = await downloadBlob(
              input,
              controller.signal,
              16 * 1024 * 1024,
            );
            controller.signal.throwIfAborted();
            const url = URL.createObjectURL(blob);
            objectUrl = url;
            updateCachedData((draft) => {
              draft.status = "ready";
              draft.value = url;
            });
          } catch {
            if (!controller.signal.aborted)
              updateCachedData((draft) => {
                draft.status = "error";
              });
          } finally {
            release?.();
          }
        })();
        await cacheEntryRemoved;
        controller.abort();
        await loading;
        if (objectUrl) URL.revokeObjectURL(objectUrl);
      },
    }),
    canvasPdfThumbnail: builder.query<NodePreviewData, NodePreviewInput>({
      keepUnusedDataFor: 10,
      providesTags: (_r, _e, input) =>
        input.outputId
          ? [
              { type: "Artifact", id: input.outputId },
              { type: "Deliverable", id: input.outputId },
            ]
          : [],
      queryFn: async (input, { signal }) => {
        let release: (() => void) | undefined;
        try {
          release = await acquire(signal);
          return { data: await download(input, signal, "pdf") };
        } catch {
          return {
            error: {
              status: null,
              message: "Open the artifact to preview this file.",
            },
          };
        } finally {
          release?.();
        }
      },
    }),
    canvasCardText: builder.query<string, NodePreviewInput>({
      keepUnusedDataFor: 10,
      providesTags: (_r, _e, input) =>
        input.outputId ? [{ type: "Deliverable", id: input.outputId }] : [],
      queryFn: async (input, { signal }) => {
        let release: (() => void) | undefined;
        try {
          release = await acquire(signal);
          return {
            data: (
              await download(
                input,
                signal,
                /wordprocessingml/.test(input.contentType)
                  ? "docx"
                  : /html/.test(input.contentType)
                    ? "html"
                    : "text",
              )
            ).value,
          };
        } catch {
          return {
            error: {
              status: null,
              message: "Open the artifact to read this document.",
            },
          };
        } finally {
          release?.();
        }
      },
    }),
  }),
});
export const {
  useCanvasPlaybackQuery,
  useCanvasThumbnailQuery,
  useCanvasCardTextQuery,
  useCanvasPdfThumbnailQuery,
} = nodePreviewApi;
