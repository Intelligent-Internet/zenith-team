import { apiClient } from "@/lib/api-client";
import type {
  AttachmentDto,
  ResourceAttachmentSnapshot,
  ResourceSummary,
  ResourceUploadResponse,
} from "@/shared/api/types";
import { generateUuidV7 } from "@/shared/lib/uuid-v7";
import type { FileTransferHandler } from "@/shared/chat/attachment-preview";

const MINIMUM_UPLOAD_BYTES_PER_SECOND = 128 * 1024;
const UPLOAD_BASE_TIMEOUT_MS = 2 * 60 * 1000;

function attachmentUploadTimeoutMs(sizeBytes: number) {
  return (
    UPLOAD_BASE_TIMEOUT_MS +
    Math.ceil(sizeBytes / MINIMUM_UPLOAD_BYTES_PER_SECOND) * 1000
  );
}

class AttachmentService {
  async uploadFiles(
    companyId: string,
    files: File[],
    onFileTransfer?: FileTransferHandler,
  ): Promise<ResourceUploadResponse> {
    const results = await Promise.allSettled(
      files.map(async (file, index) => {
        onFileTransfer?.(index, { status: "uploading", progress: 0 });
        try {
          const attachment = await this.uploadFile(
            companyId,
            file,
            (progress) =>
              onFileTransfer?.(index, { status: "uploading", progress }),
          );
          onFileTransfer?.(index, { status: "uploaded", progress: 100 });
          return attachment;
        } catch (error) {
          onFileTransfer?.(index, {
            status: "error",
            error: "Upload failed. Try again.",
          });
          throw error;
        }
      }),
    );
    const failure = results.find((result) => result.status === "rejected");
    if (failure) throw failure.reason;
    const attachments = results.map(
      (result) => (result as PromiseFulfilledResult<AttachmentDto>).value,
    );
    return {
      attachments: attachments.map(toResourceAttachmentSnapshot),
      resources: attachments.map(attachmentResource),
    };
  }

  async uploadFile(
    companyId: string,
    file: File,
    onProgress?: (percent: number | undefined) => void,
    requestKey = generateUuidV7(),
  ): Promise<AttachmentDto> {
    const metadata = {
      companyId,
      contentType: file.type || "application/octet-stream",
      filename: file.name || "attachment",
      requestKey,
      sizeBytes: file.size,
    };
    const form = new FormData();
    form.append(
      "metadata",
      new Blob([JSON.stringify(metadata)], { type: "application/json" }),
    );
    form.append("file", file, metadata.filename);
    const initiated = await apiClient.post<AttachmentDto>(
      "/attachments/initiate",
      form,
      {
        timeout: attachmentUploadTimeoutMs(file.size),
        onUploadProgress: (event) =>
          onProgress?.(
            event.total
              ? Math.min(99, Math.round((event.loaded / event.total) * 100))
              : undefined,
          ),
      },
    );
    return apiClient.post<AttachmentDto>(
      `/attachments/${encodeURIComponent(initiated.id)}/complete`,
      { companyId },
      { timeout: attachmentUploadTimeoutMs(file.size) },
    );
  }

  async download(
    companyId: string,
    attachmentId: string,
    signal?: AbortSignal,
  ): Promise<Blob> {
    return apiClient.get<Blob>(
      `/attachments/${encodeURIComponent(attachmentId)}/download`,
      { params: { companyId }, responseType: "blob", signal },
    );
  }
}

export const attachmentService = new AttachmentService();

export function toResourceAttachmentSnapshot(
  attachment: AttachmentDto,
): ResourceAttachmentSnapshot {
  return {
    asset_type: assetType(attachment.contentType),
    content_type: attachment.contentType,
    created_at: attachment.createdAt,
    file_name: attachment.filename,
    file_size: attachment.sizeBytes,
    id: attachment.id,
    is_public: false,
    source: "user_upload",
    url: `/attachments/${encodeURIComponent(attachment.id)}/download?companyId=${encodeURIComponent(attachment.companyId)}`,
  };
}

function attachmentResource(attachment: AttachmentDto): ResourceSummary {
  const previewKind = assetType(attachment.contentType);
  return {
    agent_id: null,
    content: {},
    content_url: `/attachments/${encodeURIComponent(attachment.id)}/download?companyId=${encodeURIComponent(attachment.companyId)}`,
    created_at: attachment.createdAt,
    description: null,
    file_size: humanFileSize(attachment.sizeBytes),
    file_type: attachment.contentType,
    id: attachment.id,
    kind: attachment.contentType.startsWith("image/") ? "image" : "file",
    metadata: { attachment_id: attachment.id },
    mime_type: attachment.contentType,
    preview_kind: previewKind === "other" ? "unknown" : previewKind,
    preview_status: "ready",
    preview_url: null,
    resource_id: attachment.id,
    size_bytes: attachment.sizeBytes,
    source_conversation_id: null,
    source_message_id: null,
    source_tool_call_id: null,
    source_type: "user_upload",
    status: "active",
    thumbnail_url: null,
    title: attachment.filename,
    updated_at: attachment.updatedAt,
    version: attachment.revision,
  };
}

function assetType(contentType: string) {
  if (contentType.startsWith("image/")) return "image" as const;
  if (contentType.startsWith("video/")) return "video" as const;
  if (contentType.startsWith("audio/")) return "audio" as const;
  if (contentType.includes("json") || contentType.includes("javascript")) {
    return "code" as const;
  }
  if (contentType.startsWith("text/") || contentType.includes("pdf")) {
    return "document" as const;
  }
  return "other" as const;
}

function humanFileSize(size: number) {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}
