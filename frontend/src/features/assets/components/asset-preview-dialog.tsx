import { useEffect, useState } from "react";

import {
  previewDesktopWorkspaceAsset,
  revealDesktopWorkspaceAsset,
  saveDesktopWorkspaceAsset,
  type DesktopWorkspaceAssetItem,
  type DesktopWorkspaceAssetPreview,
} from "@/app/desktop/desktop-environment.service";
import { formatWorkspaceAssetBytes } from "@/features/assets/hooks/use-workspace-asset-scopes";
import {
  FilePreviewActionButton,
  FilePreviewDialog,
  FilePreviewImage,
  FileMarkdownPreview,
  FilePreviewMessage,
  FileTextPreview,
  type FilePreviewDialogFile,
} from "@/shared/ui/file-preview-dialog";
import { Icon } from "@/shared/ui/icon";

type PreviewState =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "metadata" }
  | { reason: string; status: "error" }
  | { preview: DesktopWorkspaceAssetPreview; status: "ready" };

export function AssetPreviewDialog({
  asset,
  companyId,
  onOpenChange,
  scope,
}: {
  asset: DesktopWorkspaceAssetItem | null;
  companyId: string;
  onOpenChange: (open: boolean) => void;
  scope: string;
}) {
  const [actionBusy, setActionBusy] = useState<"export" | "reveal" | null>(
    null,
  );
  const [actionMessage, setActionMessage] = useState<string | null>(null);
  const [state, setState] = useState<PreviewState>({ status: "idle" });

  useEffect(() => {
    setActionBusy(null);
    setActionMessage(null);
    if (!asset) {
      setState({ status: "idle" });
      return;
    }
    if (asset.previewKind === "none") {
      setState({ status: "metadata" });
      return;
    }
    let cancelled = false;
    setState({ status: "loading" });
    void previewDesktopWorkspaceAsset({
      companyId,
      path: asset.path,
      scope,
    }).then((result) => {
      if (cancelled) return;
      setState(
        result.ok
          ? { preview: result.value, status: "ready" }
          : { reason: result.reason, status: "error" },
      );
    });
    return () => {
      cancelled = true;
    };
  }, [asset, companyId, scope]);

  const runAction = async (action: "export" | "reveal") => {
    if (!asset || actionBusy) return;
    setActionBusy(action);
    setActionMessage(null);
    const reference = { companyId, path: asset.path, scope };
    const result =
      action === "export"
        ? await saveDesktopWorkspaceAsset(reference)
        : await revealDesktopWorkspaceAsset(reference);
    setActionBusy(null);
    if (!result.ok) {
      setActionMessage(result.reason);
    } else if (action === "export" && !result.canceled) {
      setActionMessage("Exported successfully.");
    } else if (action === "reveal") {
      setActionMessage("A temporary local copy is ready in your file manager.");
    }
  };

  const dialogFile = asset ? assetPreviewFile(asset) : null;

  return (
    <FilePreviewDialog
      file={dialogFile}
      headerActions={
        asset ? (
          <>
            <FilePreviewActionButton
              ariaLabel="Export workspace asset"
              disabled={actionBusy !== null}
              onClick={() => void runAction("export")}
            >
              <Icon
                aria-hidden="true"
                className="size-[18px]"
                name="download"
              />
              <span className="text-[13px] font-medium leading-5">
                {actionBusy === "export" ? "Exporting…" : "Export"}
              </span>
            </FilePreviewActionButton>
            <FilePreviewActionButton
              ariaLabel="Show a local copy of this workspace asset in folder"
              disabled={actionBusy !== null}
              onClick={() => void runAction("reveal")}
            >
              <Icon
                aria-hidden="true"
                className="size-[18px]"
                name="folder-open"
              />
              <span className="text-[13px] font-medium leading-5 max-[640px]:hidden">
                {actionBusy === "reveal" ? "Preparing copy…" : "Show copy"}
              </span>
            </FilePreviewActionButton>
          </>
        ) : undefined
      }
      sidebarFooter={
        asset ? (
          <>
            {actionMessage ? (
              <p
                className="mt-6 rounded-md border border-border bg-muted p-3 text-xs leading-5 text-muted-foreground"
                role="status"
              >
                {actionMessage}
              </p>
            ) : null}
            <p className="mt-6 text-xs leading-5 text-muted-foreground">
              Export saves a copy to a location you choose. Show copy creates a
              temporary local copy. Changes to either copy do not update the
              workspace.
            </p>
          </>
        ) : undefined
      }
      onOpenChange={onOpenChange}
    >
      {asset ? <PreviewBody asset={asset} state={state} /> : null}
    </FilePreviewDialog>
  );
}

function assetPreviewFile(
  asset: DesktopWorkspaceAssetItem,
): FilePreviewDialogFile {
  return {
    description: `${asset.parentPath || "Workspace root"} · ${asset.kind}`,
    details: [
      { label: "Kind", value: asset.kind },
      { label: "MIME", value: asset.mimeType },
      { label: "Size", value: formatWorkspaceAssetBytes(asset.sizeBytes) },
      { label: "Modified", value: formatDateTime(asset.modifiedAt) },
      { label: "Relative path", value: asset.path },
    ],
    title: asset.filename,
  };
}

function PreviewBody({
  asset,
  state,
}: {
  asset: DesktopWorkspaceAssetItem;
  state: PreviewState;
}) {
  if (state.status === "loading") {
    return <FilePreviewMessage icon="refresh" title="Loading safe preview" />;
  }
  if (state.status === "error") {
    return (
      <FilePreviewMessage
        description={state.reason}
        icon="alert"
        title="Preview unavailable"
      />
    );
  }
  if (state.status === "metadata" || state.status === "idle") {
    return (
      <FilePreviewMessage
        description="Export this file or show a local copy in your file manager. A preview is not available for this file type."
        icon={kindIcon(asset.kind)}
        title="Metadata preview"
      />
    );
  }
  const preview = state.preview;
  if (preview.format === "image" && preview.contentBase64) {
    return (
      <FilePreviewImage
        alt={asset.filename}
        src={`data:${preview.mimeType};base64,${preview.contentBase64}`}
      />
    );
  }
  if (preview.format === "markdown" && preview.content !== undefined) {
    return (
      <FileMarkdownPreview
        content={preview.content}
        truncated={preview.truncated}
      />
    );
  }
  if (preview.content !== undefined) {
    return (
      <FileTextPreview
        content={preview.content}
        truncated={preview.truncated}
      />
    );
  }
  return <FilePreviewMessage icon="alert" title="Preview unavailable" />;
}

function kindIcon(kind: DesktopWorkspaceAssetItem["kind"]) {
  if (kind === "image") return "artboard";
  if (kind === "media") return "play";
  if (kind === "archive") return "box-tick";
  if (kind === "document") return "note";
  return "folder";
}

function formatDateTime(value: string) {
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
}
