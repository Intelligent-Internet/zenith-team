import { toResourceAttachmentSnapshot } from "@/shared/api/attachment.service";
import type {
  AttachmentDto,
  ConversationDisplayItemSummary,
  ResourceAttachmentSnapshot,
} from "@/shared/api/types";

export function createTaskPanelAttachments(
  items: ConversationDisplayItemSummary[],
): {
  criteria: ResourceAttachmentSnapshot[];
  deliverables: ResourceAttachmentSnapshot[];
} {
  const criteria = new Map<string, ResourceAttachmentSnapshot>();
  const deliverables = new Map<string, ResourceAttachmentSnapshot>();

  for (const item of items) {
    const attachments = item.payload.attachments;
    if (!Array.isArray(attachments)) {
      continue;
    }

    const target = item.role === "user" ? criteria : deliverables;
    for (const value of attachments) {
      if (!isAttachment(value)) {
        continue;
      }
      const snapshot = toResourceAttachmentSnapshot(value);
      target.set(snapshot.id, snapshot);
    }
  }

  return {
    criteria: [...criteria.values()],
    deliverables: [...deliverables.values()],
  };
}

function isAttachment(value: unknown): value is AttachmentDto {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const record = value as Record<string, unknown>;
  return (
    typeof record.id === "string" &&
    typeof record.companyId === "string" &&
    typeof record.uploadedByUserId === "string" &&
    typeof record.filename === "string" &&
    typeof record.contentType === "string" &&
    typeof record.sizeBytes === "number" &&
    (record.status === "pending" || record.status === "ready") &&
    typeof record.createdAt === "string" &&
    typeof record.updatedAt === "string" &&
    typeof record.revision === "number"
  );
}
