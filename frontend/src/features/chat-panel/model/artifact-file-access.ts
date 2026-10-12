import { runDeliverableService } from "@/features/run-deliverables/api/run-deliverable.service";
import type {
  ArtifactRunResult,
  RunOutputItem,
} from "@/features/run-deliverables/model/run-deliverable";
import { BOARDLY_API_BASE_URL } from "@/lib/api-client";
import { attachmentKind } from "@/shared/chat/attachment-preview";

export type ArtifactFileSelection = {
  id: string;
  runId: string | null;
  artifactId: string;
  ordinal: RunOutputItem["ordinal"];
  title: string;
  filename: string;
  contentType: string;
  sizeBytes: number;
};

export type ArtifactPreviewFile = Pick<
  ArtifactFileSelection,
  "id" | "title" | "filename" | "contentType" | "sizeBytes"
>;

export type ArtifactFilePreview =
  | { format: "image"; blob: Blob }
  | { format: "video"; blob: Blob }
  | { format: "audio"; blob: Blob }
  | { format: "pdf"; blob: Blob }
  | { format: "presentation"; blob: Blob }
  | {
      format: "spreadsheet";
      truncated?: boolean;
      blob: Blob;
      spreadsheetFormat: "xlsx" | "csv" | "tsv";
    }
  | { format: "markdown"; content: string; truncated: boolean }
  | { format: "text"; content: string; truncated: boolean }
  | { format: "unsupported"; reason: string };

export const maximumArtifactPreviewBytes = 100 * 1024 * 1024;
const maximumArtifactTextBytes = 1024 * 1024;

export function artifactFileSelection(
  runId: string | null,
  artifactId: string,
  item: RunOutputItem,
): ArtifactFileSelection {
  return {
    id: item.id,
    runId,
    artifactId,
    ordinal: item.ordinal,
    title: item.label,
    filename: item.displayName,
    contentType: item.contentType,
    sizeBytes: item.sizeBytes,
  };
}

export function downloadArtifactFile(
  companyId: string,
  file: ArtifactFileSelection,
  signal?: AbortSignal,
): Promise<Blob> {
  return runDeliverableService.downloadItem(
    companyId,
    file.runId,
    file.artifactId,
    file.ordinal,
    signal,
  );
}

export function shareArtifactFile(
  companyId: string,
  file: ArtifactFileSelection,
  signal?: AbortSignal,
) {
  return runDeliverableService.shareItem(
    companyId,
    file.runId,
    file.artifactId,
    file.ordinal,
    signal,
  );
}

export async function createArtifactFilePreview(
  blob: Blob,
  file: ArtifactPreviewFile,
): Promise<ArtifactFilePreview> {
  if (blob.size > maximumArtifactPreviewBytes) {
    return {
      format: "unsupported",
      reason:
        "This file is larger than the 100 MB preview limit. Download it to view the complete file.",
    };
  }

  const responseType = normalizeContentType(blob.type);
  const declaredType = normalizeContentType(file.contentType);
  const contentType =
    responseType && responseType !== "application/octet-stream"
      ? responseType
      : declaredType;
  const signature = new Uint8Array(await blob.slice(0, 16).arrayBuffer());
  if (
    attachmentKind({
      id: file.id,
      name: file.filename,
      mediaType: contentType,
    }) === "presentation"
  ) {
    return { format: "presentation", blob };
  }
  const imageType = rasterImageType(signature);
  if (imageType) {
    return { format: "image", blob: new Blob([blob], { type: imageType }) };
  }
  if (contentType === "image/svg+xml" || /\.svg$/i.test(file.filename)) {
    return {
      format: "image",
      blob: new Blob([blob], { type: "image/svg+xml" }),
    };
  }
  if (contentType.startsWith("video/")) {
    return { format: "video", blob: new Blob([blob], { type: contentType }) };
  }
  if (
    attachmentKind({
      id: file.id,
      name: file.filename,
      mediaType: contentType,
    }) === "audio"
  ) {
    return {
      format: "audio",
      blob: new Blob([blob], {
        type: contentType.startsWith("audio/") ? contentType : "",
      }),
    };
  }
  if (
    contentType === "application/pdf" &&
    startsWithBytes(signature, "%PDF-")
  ) {
    return {
      format: "pdf",
      blob: new Blob([blob], { type: "application/pdf" }),
    };
  }
  const spreadsheetExtension = file.filename.split(".").pop()?.toLowerCase();
  if (
    spreadsheetExtension === "xlsx" ||
    spreadsheetExtension === "csv" ||
    spreadsheetExtension === "tsv"
  ) {
    return {
      format: "spreadsheet",
      blob,
      spreadsheetFormat: spreadsheetExtension,
    };
  }
  if (isTextContentType(contentType, file.filename)) {
    const sample = await blob.slice(0, maximumArtifactTextBytes).arrayBuffer();
    let content: string;
    try {
      content = new TextDecoder("utf-8", { fatal: true }).decode(sample, {
        stream: blob.size > maximumArtifactTextBytes,
      });
    } catch {
      return {
        format: "unsupported",
        reason:
          "This file cannot be displayed as UTF-8 text. Download it to open it in another application.",
      };
    }
    if (content.includes("\u0000")) {
      return {
        format: "unsupported",
        reason:
          "This file contains binary data. Download it to open it in another application.",
      };
    }
    return {
      format:
        contentType === "text/markdown" ||
        contentType === "text/x-markdown" ||
        /\.(?:md|mdown|markdown)$/i.test(file.filename)
          ? "markdown"
          : "text",
      content,
      truncated: blob.size > maximumArtifactTextBytes,
    };
  }
  return {
    format: "unsupported",
    reason:
      "Preview is not available for this file type. Download it to open it in another application.",
  };
}

export function artifactUsesLocalAppPreview(
  result: ArtifactRunResult,
): boolean {
  const artifact = result.artifact;
  // Command-based apps have no uploaded items or URL until the daemon starts them.
  return (
    artifact.type === "app" &&
    (Boolean(artifact.url && artifactAppUrl(artifact.url)) ||
      (!result.publication && artifact.items.length === 0))
  );
}

export function artifactAppUrl(value: string): string | null {
  try {
    const url = new URL(value);
    if (
      (url.protocol !== "http:" && url.protocol !== "https:") ||
      url.username ||
      url.password
    ) {
      return null;
    }
    return url.href;
  } catch {
    return null;
  }
}

export function artifactAppPreviewReason(url: string): string | null {
  const app = new URL(url);
  const apiOrigin = new URL(BOARDLY_API_BASE_URL, window.location.href).origin;
  if (app.origin === window.location.origin || app.origin === apiOrigin) {
    return "Open this app in your browser to view it.";
  }
  if (window.location.protocol === "https:" && app.protocol === "http:") {
    return "This app uses HTTP. Open it in your browser to view it.";
  }
  return null;
}

function normalizeContentType(contentType: string | undefined) {
  return contentType?.split(";", 1)[0].trim().toLowerCase() ?? "";
}

function isTextContentType(contentType: string, filename: string) {
  return (
    contentType.startsWith("text/") ||
    /^application\/(?:json|[\w.+-]+\+json|javascript|xml|[\w.+-]+\+xml|x-yaml|yaml)$/.test(
      contentType,
    ) ||
    ((!contentType || contentType === "application/octet-stream") &&
      /\.(?:md|mdown|markdown|txt|csv|tsv|json|jsonl|yaml|yml|xml|html?|css|js|jsx|ts|tsx|go|py|rb|rs|sh|sql|toml|log)$/i.test(
        filename,
      ))
  );
}

function rasterImageType(bytes: Uint8Array): string | null {
  if (
    bytes[0] === 0x89 &&
    startsWithBytes(bytes.subarray(1), "PNG\r\n\u001a\n")
  ) {
    return "image/png";
  }
  if (bytes[0] === 0xff && bytes[1] === 0xd8 && bytes[2] === 0xff) {
    return "image/jpeg";
  }
  if (startsWithBytes(bytes, "GIF87a") || startsWithBytes(bytes, "GIF89a")) {
    return "image/gif";
  }
  if (
    startsWithBytes(bytes, "RIFF") &&
    startsWithBytes(bytes.subarray(8), "WEBP")
  ) {
    return "image/webp";
  }
  if (
    startsWithBytes(bytes.subarray(4), "ftypavif") ||
    startsWithBytes(bytes.subarray(4), "ftypavis")
  ) {
    return "image/avif";
  }
  return null;
}

function startsWithBytes(bytes: Uint8Array, prefix: string) {
  return [...prefix].every(
    (character, index) => bytes[index] === character.charCodeAt(0),
  );
}
