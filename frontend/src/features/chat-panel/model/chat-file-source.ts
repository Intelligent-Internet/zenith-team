import { resolveDesktopGeneratedFileLink } from "@/shared/desktop/desktop-environment.service";
import type { FileUIPart } from "ai";
import { cockpitService } from "@/shared/api/cockpit.service";
import { attachmentUrl } from "@/shared/chat/attachment-preview";
import {
  previewChatGeneratedFile,
  saveChatGeneratedFile,
  type ChatGeneratedFileSelection,
} from "./generated-file-access";
import type { ArtifactPreviewFile } from "./artifact-file-access";

export type ChatWorkspaceFileSelection =
  ChatGeneratedFileSelection | { href: string; runId: string };

export type ChatFileSource =
  | { type: "workspace"; selection: ChatWorkspaceFileSelection }
  | { type: "upload"; file: FileUIPart };

export function chatFileDescription(
  source: ChatFileSource,
): ArtifactPreviewFile {
  const filename =
    source.type === "upload"
      ? source.file.filename || "Attachment"
      : workspaceFilename(source.selection);
  return {
    id:
      source.type === "upload"
        ? source.file.url
        : `${source.selection.runId}:${"href" in source.selection ? source.selection.href : source.selection.file.path}`,
    title: filename,
    filename,
    contentType: source.type === "upload" ? source.file.mediaType : "",
    sizeBytes: 0,
  };
}

export async function loadChatFile(
  source: ChatFileSource,
  signal: AbortSignal,
): Promise<{ blob: Blob; truncated?: boolean }> {
  if (source.type === "upload") {
    const url = source.file.url;
    if (url.startsWith("/attachments/")) {
      return { blob: await cockpitService.getResourceBlob(url) };
    }
    const safeUrl = attachmentUrl(url);
    if (!safeUrl) throw new Error("This attachment is no longer available.");
    const response = await fetch(safeUrl, { signal, credentials: "omit" });
    if (!response.ok) throw new Error("This attachment could not be loaded.");
    return { blob: await response.blob() };
  }
  const selection = await resolveWorkspaceSelection(source.selection);
  if (signal.aborted) throw new DOMException("Preview cancelled", "AbortError");
  const result = await previewChatGeneratedFile(selection);
  if (!result.ok) throw new Error(result.reason);
  if (signal.aborted) throw new DOMException("Preview cancelled", "AbortError");
  const preview = result.value;
  const content = preview.contentBase64
    ? Uint8Array.from(atob(preview.contentBase64), (value) =>
        value.charCodeAt(0),
      )
    : (preview.content ?? "");
  return {
    blob: new Blob([content], {
      type:
        preview.mimeType ||
        (preview.format === "markdown" ? "text/markdown" : "text/plain"),
    }),
    truncated: preview.truncated,
  };
}

export async function downloadChatFile(
  source: ChatFileSource,
  signal: AbortSignal,
) {
  if (source.type === "workspace") {
    const selection = await resolveWorkspaceSelection(source.selection);
    if (signal.aborted) return;
    const result = await saveChatGeneratedFile(selection);
    if (!result.ok) throw new Error(result.reason);
    return;
  }
  const { blob } = await loadChatFile(source, signal);
  if (signal.aborted) return;
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = chatFileDescription(source).filename;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}

async function resolveWorkspaceSelection(
  selection: ChatWorkspaceFileSelection,
): Promise<ChatGeneratedFileSelection> {
  if (!("href" in selection)) return selection;
  const result = await resolveDesktopGeneratedFileLink(selection);
  if (!result.ok) throw new Error(result.reason);
  return { runId: selection.runId, file: { path: result.value.path } };
}

function workspaceFilename(selection: ChatWorkspaceFileSelection) {
  const path =
    "href" in selection
      ? selection.href.split(/[?#]/, 1)[0]
      : selection.file.path;
  const filename = path.replaceAll("\\", "/").split("/").at(-1) || "File";
  if (!("href" in selection)) return filename;
  try {
    return decodeURIComponent(filename);
  } catch {
    return filename;
  }
}
