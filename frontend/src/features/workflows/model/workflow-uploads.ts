import type { WorkflowFiles } from "@/features/workflows/model/workflow-template";
import type { WorkflowPage } from "@/shared/api/types";
import type { WorkflowDraftFile } from "@/state/slice/workflow-drafts";

/** One file a person added to a field that takes files, from pick to upload. */
export type WorkflowUpload = {
  /** Stable while it uploads; the Attachment id arrives once it is ready. */
  localId: string;
  filename: string;
  contentType: string;
  sizeBytes: number;
  status: "uploading" | "uploaded" | "error";
  /** Percent sent, while uploading; absent when the size is unknown. */
  progress?: number;
  attachmentId?: string;
};

/** Field key → the files added to it, in the order they were added. */
export type WorkflowUploads = Record<string, readonly WorkflowUpload[]>;

/** The uploads that finished, as the brief and the launch carry them. */
export function uploadedWorkflowFiles(uploads: WorkflowUploads): WorkflowFiles {
  const files: Record<string, { id: string; filename: string }[]> = {};
  for (const [key, entries] of Object.entries(uploads)) {
    const ready = entries.flatMap((entry) =>
      entry.status === "uploaded" && entry.attachmentId
        ? [{ id: entry.attachmentId, filename: entry.filename }]
        : [],
    );
    if (ready.length) files[key] = ready;
  }
  return files;
}

/** Every file added to the form, whatever its state. */
export function workflowUploadCount(uploads: WorkflowUploads): number {
  return Object.values(uploads).reduce(
    (count, entries) => count + entries.length,
    0,
  );
}

export function hasWorkflowUploadIn(
  uploads: WorkflowUploads,
  status: WorkflowUpload["status"],
): boolean {
  return Object.values(uploads).some((entries) =>
    entries.some((entry) => entry.status === status),
  );
}

/**
 * A filename two files of the form share, compared without case across every
 * field, or undefined. The brief names each file only by its name under its
 * answer, and the Run renames a clash by upload order, so two files with one
 * name could not be told apart. A failed upload is not counted.
 */
export function duplicateWorkflowUploadName(
  uploads: WorkflowUploads,
): string | undefined {
  const seen = new Set<string>();
  for (const entries of Object.values(uploads)) {
    for (const entry of entries) {
      if (entry.status === "error") continue;
      const name = entry.filename.trim().toLowerCase();
      if (seen.has(name)) return entry.filename;
      seen.add(name);
    }
  }
  return undefined;
}

/** The finished uploads a draft keeps when the person leaves to connect an app. */
export function draftFilesFromUploads(
  uploads: WorkflowUploads,
): Record<string, WorkflowDraftFile[]> {
  const files: Record<string, WorkflowDraftFile[]> = {};
  for (const [key, entries] of Object.entries(uploads)) {
    const kept = entries.flatMap((entry) =>
      entry.status === "uploaded" && entry.attachmentId
        ? [
            {
              attachmentId: entry.attachmentId,
              filename: entry.filename,
              contentType: entry.contentType,
              sizeBytes: entry.sizeBytes,
            },
          ]
        : [],
    );
    if (kept.length) files[key] = kept;
  }
  return files;
}

/**
 * The uploads a saved draft restores: only to fields the form still has that
 * still take files.
 */
export function uploadsFromDraft(
  pages: readonly WorkflowPage[],
  files: Record<string, WorkflowDraftFile[]> | undefined,
): WorkflowUploads {
  if (!files) return {};
  const uploads: Record<string, WorkflowUpload[]> = {};
  for (const field of pages.flatMap((page) => page.fields)) {
    const saved = files[field.key];
    if (!field.acceptsFiles || !saved?.length) continue;
    uploads[field.key] = saved.map((file) => ({
      localId: file.attachmentId,
      filename: file.filename,
      contentType: file.contentType,
      sizeBytes: file.sizeBytes,
      status: "uploaded",
      attachmentId: file.attachmentId,
    }));
  }
  return uploads;
}
