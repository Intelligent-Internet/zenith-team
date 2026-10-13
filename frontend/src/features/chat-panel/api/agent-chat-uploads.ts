import type { FileTransferHandler } from "@/shared/chat/attachment-preview";
import type { ChatMessage } from "@/shared/chat/types";
import { cockpitService } from "@/shared/api";
import { requireActiveCompanyId } from "@/shared/api/active-company";
import type { ResourceAttachmentSnapshot } from "@/shared/api/types";

// Retry receipts live only as long as the draft's file objects. They keep a
// partially successful upload from creating another copy on the next send.
const uploadedDraftFiles = new WeakMap<
  NonNullable<ChatMessage["files"]>[number],
  {
    companyId: string;
    conversationId: string;
    attachments: ResourceAttachmentSnapshot[];
  }
>();

export async function uploadMessageResources({
  clientMessageId,
  conversationId,
  files,
  onFileTransfer,
}: {
  clientMessageId: string;
  conversationId: string;
  files: ChatMessage["files"];
  onFileTransfer?: FileTransferHandler;
}) {
  if (!files?.length) {
    return [];
  }
  const companyId = requireActiveCompanyId();
  const results = await Promise.allSettled(
    files.map(async (file, index) => {
      const cached = uploadedDraftFiles.get(file);
      if (
        cached?.companyId === companyId &&
        cached.conversationId === conversationId
      ) {
        onFileTransfer?.(index, { status: "uploaded", progress: 100 });
        return cached.attachments;
      }
      try {
        const uploadFile = await fileFromPromptInputPart(file);
        const response = await cockpitService.uploadResources({
          clientMessageId,
          conversationId,
          companyId,
          files: [uploadFile],
          onFileTransfer: (_index, transfer) =>
            onFileTransfer?.(index, transfer),
        });
        const attachments = response.attachments?.length
          ? response.attachments
          : response.resources.map((resource) =>
              cockpitService.resourceAttachmentSnapshot(resource),
            );
        uploadedDraftFiles.set(file, {
          companyId,
          conversationId,
          attachments,
        });
        return attachments;
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
  return results.flatMap(
    (result) =>
      (result as PromiseFulfilledResult<ResourceAttachmentSnapshot[]>).value,
  );
}

async function fileFromPromptInputPart(
  file: NonNullable<ChatMessage["files"]>[number],
): Promise<File> {
  const filename = file.filename || "attachment";
  const contentType = file.mediaType || "application/octet-stream";
  if (!file.url) {
    return fileFromBlob(new Blob([], { type: contentType }), filename);
  }
  const response = await fetch(file.url);
  const blob = await response.blob();
  return fileFromBlob(
    blob,
    filename,
    file.mediaType || blob.type || contentType,
  );
}

function fileFromBlob(
  blob: Blob,
  filename: string,
  contentType = blob.type,
): File {
  if (typeof File === "function") {
    return new File([blob], filename, {
      type: contentType || "application/octet-stream",
    });
  }

  const fallback = blob as File;
  Object.defineProperties(fallback, {
    lastModified: {
      configurable: true,
      value: Date.now(),
    },
    name: {
      configurable: true,
      value: filename,
    },
  });
  return fallback;
}
