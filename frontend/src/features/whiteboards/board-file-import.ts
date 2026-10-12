import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type DragEvent,
} from "react";
import { attachmentService } from "@/shared/api/attachment.service";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import { toast } from "@/shared/ui/sonner";
import {
  useAttachWhiteboardFileMutation,
  type BoardCommand,
  type BoardScope,
} from "./board-api";

type Point = { x: number; y: number };
type Placement = Point & { anchor: "top-left" | "center" };

const imageTypes: Record<string, string> = {
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
};

// Images render inline; every other file becomes a file card.
function boardFile(file: File) {
  const type =
    file.type ||
    imageTypes[file.name.split(".").at(-1)?.toLowerCase() ?? ""] ||
    "application/octet-stream";
  const typed = file.type ? file : new File([file], file.name, { type });
  return { file: typed, image: Object.values(imageTypes).includes(type) };
}

export const fileCardSize = { width: 280, height: 88 };

async function imageSize(file: File) {
  if (file.size > 32 * 1024 * 1024)
    throw new Error("Images must be 32 MB or smaller.");
  const url = URL.createObjectURL(file);
  try {
    const image = new Image();
    image.src = url;
    await image.decode();
    if (image.naturalWidth * image.naturalHeight > 32 * 1024 * 1024)
      throw new Error("This image has too many pixels. Use a smaller image.");
    const scale = Math.max(
      0.1 / Math.min(image.naturalWidth, image.naturalHeight),
      Math.min(1, 480 / image.naturalWidth, 360 / image.naturalHeight),
    );
    return {
      width: image.naturalWidth * scale,
      height: image.naturalHeight * scale,
    };
  } finally {
    URL.revokeObjectURL(url);
  }
}

export function useBoardFileImport({
  scope,
  enabled,
  toBoardPosition,
  onInsert,
}: {
  scope: BoardScope;
  enabled: boolean;
  toBoardPosition: (point: Point) => Point | undefined;
  onInsert: (commands: BoardCommand[]) => Promise<void>;
}) {
  const [attach] = useAttachWhiteboardFileMutation();
  const [dragging, setDragging] = useState(false);
  const [progress, setProgress] = useState("");
  const depth = useRef(0);
  const pending = useRef<Promise<void> | null>(null);
  const mounted = useRef(true);
  const current = useRef({ enabled, onInsert });
  current.current = { enabled, onInsert };
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  useEffect(() => {
    if (!enabled) {
      depth.current = 0;
      setDragging(false);
    }
  }, [enabled]);
  const acceptsFiles = (event: DragEvent) =>
    event.dataTransfer.types.includes("Files");
  const upload = async (
    files: ReturnType<typeof boardFile>[],
    position: Placement,
  ) => {
    const commands: BoardCommand[] = [];
    for (const [index, { file, image }] of files.entries()) {
      if (!mounted.current || !current.current.enabled) break;
      try {
        setProgress(`Adding file ${index + 1} of ${files.length}…`);
        if (file.size > 100 * 1024 * 1024)
          throw new Error("Files must be 100 MB or smaller.");
        const size = image ? await imageSize(file) : fileCardSize;
        if (!mounted.current || !current.current.enabled) break;
        const uploaded = await attachmentService.uploadFile(
          scope.companyId,
          file,
          (percent) => {
            setProgress(
              `Uploading file ${index + 1} of ${files.length}${percent === undefined ? "…" : ` · ${percent}%`}`,
            );
          },
        );
        if (!mounted.current || !current.current.enabled) break;
        const attached = await attach({
          ...scope,
          input: { attachmentId: uploaded.id },
        }).unwrap();
        commands.push({
          type: "create_item",
          draftId: crypto.randomUUID(),
          itemType: image ? "image" : "file",
          geometry: {
            x:
              position.x +
              index * 32 -
              (position.anchor === "center" ? size.width / 2 : 0),
            y:
              position.y +
              index * 32 -
              (position.anchor === "center" ? size.height / 2 : 0),
            ...size,
            rotation: 0,
            stackOrder: 0,
            parentFrameId: null,
            readingOrder: 0,
          },
          properties: {
            attachmentId: attached.attachmentId,
            alt: image ? file.name : undefined,
            locked: false,
          },
        });
      } catch (error) {
        toast.error(
          `${file.name}: ${getBoardlyQueryErrorMessage(error, "This file could not be added. Try again.")}`,
        );
      }
    }
    if (commands.length && mounted.current && current.current.enabled) {
      setProgress("Saving files…");
      await current.current.onInsert(commands);
    }
  };
  const importFiles = (input: File[], placement: Placement) => {
    if (!enabled) return;
    if (pending.current) {
      toast.error("Wait for the current files to finish uploading.");
      return;
    }
    const files = input.map(boardFile);
    if (!files.length) return;
    if (files.length > 100) {
      toast.error("Add up to 100 files at a time.");
      return;
    }
    const position = toBoardPosition(placement);
    if (!position) return;
    pending.current = upload(files, { ...position, anchor: placement.anchor })
      .catch((error) => {
        toast.error(getBoardlyQueryErrorMessage(error));
      })
      .finally(() => {
        pending.current = null;
        if (mounted.current) setProgress("");
      });
  };
  const hasPendingUpload = useCallback(() => pending.current !== null, []);
  const flushUploads = useCallback(
    () => pending.current ?? Promise.resolve(),
    [],
  );
  return {
    dragging: dragging && enabled,
    progress,
    isUploading: !!progress,
    hasPendingUpload,
    flushUploads,
    importFiles,
    handlers: {
      onDragEnter: (event: DragEvent) => {
        if (!acceptsFiles(event)) return;
        event.preventDefault();
        depth.current++;
        if (enabled && !pending.current) setDragging(true);
      },
      onDragOver: (event: DragEvent) => {
        if (!acceptsFiles(event)) return;
        event.preventDefault();
        event.dataTransfer.dropEffect =
          enabled && !pending.current ? "copy" : "none";
      },
      onDragLeave: (event: DragEvent) => {
        if (!acceptsFiles(event)) return;
        depth.current = Math.max(0, depth.current - 1);
        if (!depth.current) setDragging(false);
      },
      onDrop: (event: DragEvent) => {
        if (!acceptsFiles(event)) return;
        event.preventDefault();
        event.stopPropagation();
        depth.current = 0;
        setDragging(false);
        event.currentTarget instanceof HTMLElement &&
          event.currentTarget.focus({ preventScroll: true });
        importFiles(Array.from(event.dataTransfer.files), {
          x: event.clientX,
          y: event.clientY,
          anchor: "top-left",
        });
      },
    },
  };
}
