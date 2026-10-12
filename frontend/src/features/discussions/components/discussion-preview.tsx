import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type PropsWithChildren,
} from "react";
import { useSearchParams } from "react-router";
import { useGetArtifactQuery } from "@/features/run-deliverables/api/run-deliverables-api";
import {
  AttachmentFileView,
  type AttachmentPreviewFile,
} from "@/features/chat-panel/components/artifacts/attachment-file-view";
import { ConversationArtifactViewer } from "@/features/chat-panel/components/artifacts/conversation-artifact-viewer";
import { FileViewerShell } from "@/features/chat-panel/components/artifacts/file-viewer-shell";
import { ArtifactPreviewMessage } from "@/features/chat-panel/components/artifacts/artifact-file-preview";
import { useArtifactsResize } from "@/features/chat-panel/hooks/use-artifacts-resize";
import "@/features/chat-panel/styles/conversation-artifacts.css";

type DiscussionPreview = {
  artifactId: string | null;
  fileId: string | null;
  attachment: AttachmentPreviewFile | null;
  isOpen: boolean;
  openArtifact: (id: string) => void;
  openAttachment: (file: AttachmentPreviewFile) => void;
  close: () => void;
};

const Context = createContext<DiscussionPreview | null>(null);

export function DiscussionPreviewProvider({ children }: PropsWithChildren) {
  const [params, setParams] = useSearchParams();
  const [attachment, setAttachment] = useState<AttachmentPreviewFile | null>(
    null,
  );
  const triggerRef = useRef<HTMLElement | null>(null);
  const artifactId = params.get("artifactId");

  const setArtifactId = useCallback(
    (id?: string) => {
      setParams(
        (current) => {
          const next = new URLSearchParams(current);
          for (const key of ["artifactId", "runId", "fileId"]) next.delete(key);
          if (id) next.set("artifactId", id);
          return next;
        },
        { replace: true },
      );
    },
    [setParams],
  );

  const rememberTrigger = () => {
    if (
      document.activeElement instanceof HTMLElement &&
      !document.activeElement.closest("[data-discussion-preview]")
    )
      triggerRef.current = document.activeElement;
  };

  const close = useCallback(() => {
    setAttachment(null);
    setArtifactId();
    requestAnimationFrame(() => {
      if (triggerRef.current?.isConnected)
        triggerRef.current.focus({ preventScroll: true });
    });
  }, [setArtifactId]);

  useEffect(() => {
    if (artifactId) setAttachment(null);
  }, [artifactId]);

  return (
    <Context.Provider
      value={{
        artifactId,
        fileId: params.get("fileId"),
        attachment,
        isOpen: Boolean(artifactId || attachment),
        openArtifact: (id) => {
          rememberTrigger();
          setAttachment(null);
          setArtifactId(id);
        },
        openAttachment: (file) => {
          rememberTrigger();
          setArtifactId();
          setAttachment(file);
        },
        close,
      }}
    >
      {children}
    </Context.Provider>
  );
}

export function useDiscussionPreview() {
  const preview = useContext(Context);
  if (!preview) throw new Error("DiscussionPreviewProvider is required");
  return preview;
}

export function DiscussionPreviewPanel({
  companyId,
  conversationId,
  backLabel,
}: {
  companyId: string;
  conversationId: string;
  backLabel: string;
}) {
  const preview = useDiscussionPreview();
  const panelRef = useRef<HTMLDivElement>(null);
  const resize = useArtifactsResize(panelRef, true, preview.isOpen);
  const detail = useGetArtifactQuery(
    { companyId, artifactId: preview.artifactId ?? "" },
    { skip: !preview.artifactId, refetchOnMountOrArgChange: true },
  );
  const result = detail.currentData?.result;
  const artifact =
    !detail.isError && result?.output
      ? {
          runId: detail.currentData!.runId,
          runtimeId: result.runtimeId,
          runStatus: result.runStatus,
          artifact: result.output,
          deliverable: result.deliverable,
          publication: result.publication,
        }
      : undefined;
  const selectionKey = preview.artifactId ?? preview.attachment?.id;

  useEffect(() => {
    const frame = requestAnimationFrame(() => {
      (
        panelRef.current?.querySelector<HTMLElement>(
          ".artifact-viewer__header button",
        ) ?? panelRef.current
      )?.focus();
    });
    return () => cancelAnimationFrame(frame);
  }, [selectionKey, artifact?.artifact.id]);

  const panelId = `discussion-preview-${conversationId}`;
  return (
    <div
      id={panelId}
      ref={panelRef}
      className="artifacts-aside"
      data-discussion-preview
      data-viewer
      data-resizing={resize.resizing || undefined}
      style={{ flexBasis: resize.width }}
      role="region"
      aria-label="File and artifact preview"
      tabIndex={-1}
      onKeyDown={(event) => {
        if (!event.currentTarget.contains(event.target as Node)) return;
        if (event.key === "Escape" && !event.defaultPrevented) {
          event.preventDefault();
          event.stopPropagation();
          preview.close();
        }
      }}
    >
      <div
        className="artifacts-resize-handle"
        aria-controls={panelId}
        {...resize.separatorProps}
      />
      {preview.attachment ? (
        <AttachmentFileView
          key={preview.attachment.id}
          companyId={companyId}
          file={preview.attachment}
          onClose={preview.close}
        />
      ) : artifact ? (
        <ConversationArtifactViewer
          artifact={artifact}
          companyId={companyId}
          initialFileId={preview.fileId ?? undefined}
          onBack={preview.close}
          backLabel={backLabel}
          onClose={preview.close}
        />
      ) : (
        <FileViewerShell
          title="Artifact"
          onBack={preview.close}
          onClose={preview.close}
        >
          <ArtifactPreviewMessage loading={detail.isFetching}>
            {detail.isFetching ? (
              "Loading artifact…"
            ) : (
              <>
                <p>
                  This artifact is unavailable. Check your connection and
                  access.
                </p>
                <button
                  className="artifact-action-button"
                  type="button"
                  onClick={() => void detail.refetch()}
                >
                  Try again
                </button>
              </>
            )}
          </ArtifactPreviewMessage>
        </FileViewerShell>
      )}
    </div>
  );
}
