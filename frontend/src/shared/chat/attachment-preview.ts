import { attachmentLanguage } from "./attachment-language";
import type { FileUIPart } from "ai";

// File presentation and transfer state belong to the client, not the API model.
export type ChatAttachment = {
  id: string;
  name: string;
  url?: string;
  mediaType?: string;
  sizeBytes?: number;
  content?: string;
  detail?: string;
};

export type ChatInputFile = FileUIPart & { sizeBytes?: number };

export type FileTransfer = {
  status: "preparing" | "ready" | "uploading" | "uploaded" | "error";
  progress?: number;
  error?: string;
};

export type FileTransferHandler = (
  index: number,
  transfer: FileTransfer,
) => void;

export function attachmentKind(file: ChatAttachment) {
  const mime = file.mediaType?.split(";", 1)[0].toLowerCase() ?? "";
  const extension = file.name.split(".").pop()?.toLowerCase();
  if (
    extension === "pptx" ||
    mime ===
      "application/vnd.openxmlformats-officedocument.presentationml.presentation"
  )
    return "presentation";
  if (
    extension === "xlsx" ||
    extension === "csv" ||
    extension === "tsv" ||
    mime === "text/csv" ||
    mime === "text/tab-separated-values" ||
    mime === "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
  )
    return "spreadsheet";
  if (mime.startsWith("image/")) return "image";
  if (mime.startsWith("video/")) return "video";
  if (mime.startsWith("audio/")) return "audio";
  if (mime === "application/pdf") return "pdf";
  if (
    mime.includes("markdown") ||
    extension === "md" ||
    extension === "markdown"
  )
    return "markdown";
  if (
    mime.startsWith("text/") ||
    ["application/json", "application/xml"].includes(mime)
  )
    return "text";
  if (attachmentLanguage(file.name, file.mediaType)) return "text";
  if (mime && mime !== "application/octet-stream") return "file";
  if (
    ["png", "jpg", "jpeg", "gif", "webp", "avif", "svg", "bmp"].includes(
      extension ?? "",
    )
  )
    return "image";
  if (["mp4", "webm", "mov", "m4v"].includes(extension ?? "")) return "video";
  if (["mp3", "wav", "ogg", "m4a", "flac", "aac"].includes(extension ?? ""))
    return "audio";
  if (extension === "pdf") return "pdf";
  if (
    [
      "txt",
      "csv",
      "tsv",
      "json",
      "log",
      "yaml",
      "yml",
      "xml",
      "js",
      "ts",
      "tsx",
      "jsx",
      "css",
      "html",
      "py",
      "go",
      "sh",
      "sql",
    ].includes(extension ?? "")
  )
    return "text";
  return "file";
}

export function attachmentFromInput(
  file: ChatInputFile,
  id: string,
): ChatAttachment {
  return {
    id,
    name: file.filename || "Attachment",
    url: file.url,
    mediaType: file.mediaType,
    sizeBytes: file.sizeBytes,
  };
}

export function formatAttachmentSize(bytes?: number) {
  if (bytes === undefined) return "";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export function attachmentUrl(value?: string): string | undefined {
  if (!value) return undefined;
  if (value.startsWith("blob:") || /^data:[^,]*;base64,/i.test(value))
    return value;
  try {
    const url = new URL(value, window.location.href);
    return ["http:", "https:"].includes(url.protocol) &&
      !url.username &&
      !url.password
      ? url.href
      : undefined;
  } catch {
    return undefined;
  }
}

export async function downloadChatAttachment(file: ChatAttachment) {
  const url = attachmentUrl(file.url);
  let blob: Blob;
  if (url) {
    const response = await fetch(url, { credentials: "omit" });
    if (!response.ok)
      throw new Error("The file could not be downloaded. Please try again.");
    blob = await response.blob();
  } else if (file.content !== undefined) {
    blob = new Blob([file.content], { type: file.mediaType || "text/plain" });
  } else {
    throw new Error("This file is no longer available.");
  }
  const objectUrl = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = objectUrl;
  anchor.download = file.name;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(objectUrl), 30_000);
}
