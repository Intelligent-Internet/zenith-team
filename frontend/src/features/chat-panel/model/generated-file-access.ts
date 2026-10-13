import type {
  DesktopGeneratedFileActionResult,
  DesktopGeneratedFilePreview,
  DesktopWorkspaceAssetPreview,
  DesktopWorkspaceAssetReference,
} from "../../../../electron/shared/desktop-bridge.types";
import { readSessionSnapshot } from "@/shared/session/session";
import {
  previewDesktopGeneratedFile,
  previewDesktopWorkspaceAsset,
  revealDesktopGeneratedFile,
  revealDesktopWorkspaceAsset,
  saveDesktopGeneratedFile,
  saveDesktopWorkspaceAsset,
} from "@/shared/desktop/desktop-environment.service";
import type { ChatTeamId } from "@/shared/chat/chat-team";
import type { ChatGeneratedFile } from "@/shared/chat/types";

export type ChatGeneratedFileSelection = {
  file: Pick<ChatGeneratedFile, "path">;
  runId: string;
  workspaceReference?: DesktopWorkspaceAssetReference;
};

export type ChatGeneratedFilePreview =
  DesktopGeneratedFilePreview | DesktopWorkspaceAssetPreview;

export type ChatGeneratedFilePreviewResult =
  | { ok: true; value: ChatGeneratedFilePreview }
  | { code: string; ok: false; reason: string };

export function createChatGeneratedFileSelection({
  file,
  runId,
}: {
  file: ChatGeneratedFile;
  runId?: string;
  /** The lane the file was shown in; files resolve to the Company workspace regardless. */
  team?: ChatTeamId;
}): ChatGeneratedFileSelection {
  const workspaceReference = workspaceReferenceForGeneratedFile(file);
  return {
    file,
    runId: runId === undefined ? "" : String(runId),
    ...(workspaceReference ? { workspaceReference } : {}),
  };
}

/** Generated files live in the Company workspace whichever Agent wrote them. */
export function workspaceReferenceForGeneratedFile(
  file: ChatGeneratedFile,
): DesktopWorkspaceAssetReference | undefined {
  const companyId = readSessionSnapshot().company?.id;
  if (!companyId) return undefined;
  return {
    companyId,
    path: file.path,
    scope: "company",
  };
}

export async function previewChatGeneratedFile(
  selection: ChatGeneratedFileSelection,
): Promise<ChatGeneratedFilePreviewResult> {
  if (selection.runId) {
    const runPreview = await previewDesktopGeneratedFile({
      path: selection.file.path,
      runId: selection.runId,
    });
    if (
      runPreview.ok ||
      !selection.workspaceReference ||
      runPreview.code !== "unsupported_file"
    ) {
      return runPreview;
    }
  }

  if (!selection.workspaceReference) {
    return generatedFileUnavailable();
  }
  const workspaceResult = await previewDesktopWorkspaceAsset(
    selection.workspaceReference,
  );
  if (!workspaceResult.ok) {
    return workspaceResult;
  }
  return workspaceResult;
}

export async function saveChatGeneratedFile(
  selection: ChatGeneratedFileSelection,
): Promise<DesktopGeneratedFileActionResult> {
  if (selection.runId) {
    return saveDesktopGeneratedFile({
      path: selection.file.path,
      runId: selection.runId,
    });
  }
  return selection.workspaceReference
    ? saveDesktopWorkspaceAsset(selection.workspaceReference)
    : generatedFileUnavailable();
}

export async function revealChatGeneratedFile(
  selection: ChatGeneratedFileSelection,
): Promise<DesktopGeneratedFileActionResult> {
  if (selection.runId) {
    return revealDesktopGeneratedFile({
      path: selection.file.path,
      runId: selection.runId,
    });
  }
  return selection.workspaceReference
    ? revealDesktopWorkspaceAsset(selection.workspaceReference)
    : generatedFileUnavailable();
}

function generatedFileUnavailable() {
  return {
    code: "file_unavailable",
    ok: false as const,
    reason: "This generated file is no longer available.",
  };
}
