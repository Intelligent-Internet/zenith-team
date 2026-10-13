import { CommentConnectors } from "../documents/comment-connectors";
import type { Editor } from "@tiptap/react";
import { DocumentFormatToolbar } from "../documents/document-format-toolbar";
import { PageDocumentLoading } from "../documents/page-document-loading";
import { DocumentHistoryPanel } from "../documents/document-history";
import { skipToken } from "@reduxjs/toolkit/query";
import { type OutputDocumentScope } from "../documents/document-scope";
import type { ArtifactScope } from "../previews/artifact-viewer";
import {
  useOutputReviewQuery,
  useOutputReviewCommandMutation,
  useOpenOutputDocumentMutation,
} from "../api/output-review-api";
import { useDocumentComments } from "../documents/use-document-comments";
import {
  DocumentCommentControls,
  DocumentCommentHighlights,
  DocumentCommentsPanel,
} from "../documents/document-comments";
import "./floating-comments.css";
import { PageContributions } from "../documents/page-contributions";
import {
  usePageThreadsQuery,
  useProjectPageQuery,
  useUpdatePageMetadataMutation,
} from "../api/page-api";
import { SelectionActions } from "./selection-actions";
import { quoteRange } from "./quote-highlight";
import { scrollToReviewAnchor } from "./review-navigation";
import { type ReviewAnchor, type MediaReview } from "./annotation-overlay";
import { WikiDocumentWorkspace } from "@/features/company-wiki/wiki-document-workspace";
import { useCan } from "@/shared/permissions";
import {
  lazy,
  Suspense,
  useEffect,
  useCallback,
  useMemo,
  useRef,
  useState,
} from "react";
import { artifactFormat } from "../canvas/canvas-nodes";
import {
  reviewDate as date,
  type ReviewThreadData as Thread,
} from "./review-comments";
import {
  Pencil,
  FileText,
  MoreHorizontal,
  X,
  Check,
  Clock3,
  Copy,
  Link2,
} from "@/shared/ui/icons";
import {
  useCanvasHistoryQuery,
  useCanvasThreadsQuery,
  type CanvasArtifact,
  type CanvasItem,
} from "../api/canvas-api";
import { CanvasDialog } from "../canvas/canvas-dialog";
import { type ExecuteCanvasCommand } from "../canvas/add-artifact";
import { LiveDocument } from "../documents/live-document";
const ArtifactViewer = lazy(() =>
  import("../previews/artifact-viewer").then((module) => ({
    default: module.ArtifactViewer,
  })),
);
type Anchor = ReviewAnchor;
export function ArtifactReview({
  scope,
  artifact,
  item,
  versionId: initialVersion,
  threadId: initialThread,
  blockId,
  canEdit,
  canComment = canEdit,
  outputId,
  canEditDocuments,
  execute,
  onClose,
  embedded = false,
  reviewOnly = false,
  initialOutputItemId,
  initialSharedView = false,
}: {
  scope: ArtifactScope;
  artifact: CanvasArtifact;
  item?: CanvasItem;
  versionId?: string;
  threadId?: string;
  blockId?: string;
  canEdit: boolean;
  canComment?: boolean;
  outputId?: string;
  canEditDocuments: boolean;
  execute: ExecuteCanvasCommand;
  onClose: () => void;
  embedded?: boolean;
  reviewOnly?: boolean;
  initialOutputItemId?: string;
  initialSharedView?: boolean;
}) {
  const projectScope = "projectId" in scope ? scope : undefined;
  const [outputDocumentItem, setOutputDocumentItem] = useState<string>();
  const projectHistory = useCanvasHistoryQuery(
    projectScope ? { ...projectScope, artifactId: artifact.id } : skipToken,
    { skip: !!outputId },
  );
  const outputReview = useOutputReviewQuery(
    outputId ? { companyId: scope.companyId, outputId } : skipToken,
    {
      skip: !outputId,
      refetchOnMountOrArgChange: true,
      refetchOnReconnect: true,
      refetchOnFocus: true,
      pollingInterval: 5000,
      skipPollingIfUnfocused: true,
    },
  );
  const history = outputId
    ? {
        ...projectHistory,
        isError: outputReview.isError,
        data: outputReview.currentData
          ? {
              versions: outputReview.currentData.versions,
              decisions: [],
            }
          : undefined,
      }
    : projectHistory;
  const [versionId, setVersionId] = useState(
    initialVersion || artifact.latestVersionId,
  );
  const [compareId, setCompareId] = useState("");
  const [documentEditor, setDocumentEditor] = useState<Editor | null>(null);
  const [documentStatusContainer, setDocumentStatusContainer] =
    useState<HTMLDivElement | null>(null);
  const [readyDraftKey, setReadyDraftKey] = useState("");
  const [documentActionsContainer, setDocumentActionsContainer] =
    useState<HTMLDivElement | null>(null);
  const [commentsOpen, setCommentsOpen] = useState(true);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [documentReload, setDocumentReload] = useState(0);
  const [documentStage, setDocumentStage] = useState<HTMLDivElement | null>(
    null,
  );
  const [editButtonContainer, setEditButtonContainer] =
    useState<HTMLDivElement | null>(null);
  const [editing, setEditing] = useState(
    !initialThread && !reviewOnly && (!outputId || initialSharedView),
  );
  const [documentEditing, setDocumentEditing] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [anchor, setAnchor] = useState<Anchor>({ type: "version" });
  const [navigation, setNavigation] = useState<MediaReview["navigation"]>();
  const draftRange = useRef<Range | undefined>(undefined);
  const navigationId = useRef(0);
  const openedThread = useRef("");
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState("");
  const viewer = useRef<HTMLDivElement>(null);
  const [viewerSize, setViewerSize] = useState({ width: 0, height: 0 });
  useEffect(() => {
    const element = viewer.current;
    if (!element) return;
    const observer = new ResizeObserver(() =>
      setViewerSize({
        width: element.clientWidth,
        height: element.clientHeight,
      }),
    );
    observer.observe(element);
    return () => observer.disconnect();
  }, [editing, compareId, versionId]);
  const version = history.isError
    ? undefined
    : (history.data?.versions.find((row) => row.id === versionId) ??
      (!history.data && artifact.latestVersionId === versionId
        ? artifact.latestVersion
        : undefined));
  const documentItemId = outputDocumentItem ?? initialOutputItemId;
  const workingVersion = version?.wikiPageId
    ? version
    : history.data?.versions.find(
        (row) =>
          row.wikiPageId &&
          (!documentItemId || row.outputItemId === documentItemId),
      );
  const [updatePageMetadata] = useUpdatePageMetadataMutation();
  const workingPageId =
    artifact.pageId && !workingVersion?.sourceOutputId
      ? workingVersion?.wikiPageId
      : undefined;
  const page = useProjectPageQuery(
    projectScope && workingPageId
      ? { ...projectScope, pageId: workingPageId }
      : skipToken,
    { skip: !workingPageId },
  );
  const companyWikiPageId =
    !artifact.pageId && !workingVersion?.sourceOutputId
      ? workingVersion?.wikiPageId
      : undefined;
  const canPublishWiki = useCan("wiki.publish");
  const [openOutputDocument] = useOpenOutputDocumentMutation();
  const selectedOutputDocument = outputReview.currentData?.documents.find(
    (doc) =>
      workingVersion?.wikiPageId
        ? doc.pageId === workingVersion.wikiPageId
        : documentItemId
          ? doc.outputItemId === documentItemId
          : true,
  );
  const outputDocumentScope: OutputDocumentScope | undefined =
    outputId && selectedOutputDocument
      ? {
          companyId: scope.companyId,
          outputId,
          outputItemId: selectedOutputDocument.outputItemId,
        }
      : undefined;
  const historyDocumentScope =
    outputDocumentScope ??
    (projectScope &&
    workingVersion?.wikiPageId &&
    (artifact.pageId || workingVersion.sourceOutputId)
      ? projectScope
      : undefined);
  const historyVersionId = workingVersion?.id ?? versionId;
  // Default to shared content, but keep explicit saved-version selections.
  // Document editing controls write access independently of this view choice.
  const liveEditing =
    editing &&
    (companyWikiPageId
      ? canPublishWiki
      : !!outputDocumentScope || !!workingVersion?.wikiPageId);
  const draftKey = `${historyVersionId}:${selectedOutputDocument?.pageId ?? ""}:${documentReload}`;
  const handleDraftReady = useCallback(
    () => setReadyDraftKey(draftKey),
    [draftKey],
  );
  useEffect(() => {
    if (!liveEditing) {
      setReadyDraftKey("");
      setDocumentEditing(false);
    }
  }, [liveEditing]);
  const compare = history.isError
    ? undefined
    : history.data?.versions.find((row) => row.id === compareId);
  const discussionOutputId = outputId ?? workingVersion?.sourceOutputId;
  const documentReview = useOutputReviewQuery(
    discussionOutputId && !outputId
      ? { companyId: scope.companyId, outputId: discussionOutputId }
      : skipToken,
    {
      refetchOnMountOrArgChange: true,
      refetchOnReconnect: true,
      refetchOnFocus: true,
      pollingInterval: 5000,
      skipPollingIfUnfocused: true,
    },
  );
  const [commentOnOutput] = useOutputReviewCommandMutation();
  const discussionPageId = outputDocumentScope
    ? selectedOutputDocument?.pageId
    : (workingPageId ??
      (discussionOutputId ? workingVersion?.wikiPageId : undefined));
  const pageDiscussion = liveEditing && !!discussionPageId;
  const versionThreads = useCanvasThreadsQuery(
    projectScope ? { ...projectScope, versionId } : skipToken,
    {
      skip: pageDiscussion || !!discussionOutputId,
      refetchOnReconnect: true,
      refetchOnMountOrArgChange: true,
    },
  );
  const pageThreads = usePageThreadsQuery(
    projectScope && workingPageId
      ? { ...projectScope, pageId: workingPageId }
      : skipToken,
    { skip: !pageDiscussion, refetchOnMountOrArgChange: true },
  );
  const threads = outputId
    ? outputReview
    : discussionOutputId
      ? documentReview
      : pageDiscussion
        ? pageThreads
        : versionThreads;
  const threadsReady = !threads.isError && !!threads.currentData;
  const openingDraft =
    liveEditing &&
    !companyWikiPageId &&
    (readyDraftKey !== draftKey ||
      (commentsOpen && !threadsReady && !threads.isError));
  const visibleThreads = useMemo(
    () =>
      threadsReady
        ? threads.currentData!.items.filter(
            (thread) =>
              !discussionOutputId ||
              (pageDiscussion
                ? thread.pageId === discussionPageId
                : !thread.pageId && thread.versionId === versionId),
          )
        : [],
    [
      threadsReady,
      threads.currentData,
      discussionOutputId,
      pageDiscussion,
      discussionPageId,
      versionId,
    ],
  );
  const comments = useDocumentComments({
    threads: threadsReady ? visibleThreads : undefined,
    content: documentStage,
    open: commentsOpen,
    onOpenChange: setCommentsOpen,
    anchor,
    onAnchorChange: setAnchor,
    canCompose: canComment && (!liveEditing || pageDiscussion),
    initialThread,
  });
  const {
    active: activeThread,
    setActive: setActiveThread,
    hovered: hoveredThread,
    setHovered: setHoveredThread,
    setComposing,
    start: focusComposer,
    reveal: revealThread,
  } = comments;
  useEffect(() => {
    if (anchor.type === "version") draftRange.current = undefined;
  }, [anchor]);
  const {
    Icon: FormatIcon,
    label: formatLabel,
    tone,
  } = artifactFormat(version);
  const cancelSelection = () => {
    comments.cancel();
    setError("");
    draftRange.current = undefined;
    window.getSelection()?.removeAllRanges();
  };
  const navigateThread = (thread: Thread) => {
    if (busy || (!pageDiscussion && dirty)) return;
    if (anchor.type !== "version") {
      setError(
        "Post your comment or cancel the selection before jumping to another location.",
      );
      return;
    }
    setError("");
    if (pageDiscussion) {
      revealThread(thread);
      if (viewer.current) scrollToReviewAnchor(viewer.current, thread.anchor);
      return;
    }
    setEditing(false);
    revealThread(thread);
    setHoveredThread("");
    setNavigation({
      id: ++navigationId.current,
      threadId: thread.id,
      anchor: thread.anchor,
    });
  };
  useEffect(() => {
    const root = viewer.current;
    if (!root || !navigation || navigation.anchor.type === "media" || editing)
      return;
    let frame = 0;
    const jump = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        if (scrollToReviewAnchor(root, navigation.anchor))
          observer.disconnect();
      });
    };
    // A node's comment can open the review before its document has loaded.
    // Retry on rendered content changes, then stop observing after the jump.
    const observer = new MutationObserver(jump);
    observer.observe(root, {
      childList: true,
      subtree: true,
      characterData: true,
    });
    jump();
    return () => {
      observer.disconnect();
      cancelAnimationFrame(frame);
    };
  }, [navigation, versionId, editing, compareId]);
  useEffect(() => {
    if (!initialThread || openedThread.current === initialThread) return;
    const thread = visibleThreads?.find((row) => row.id === initialThread);
    if (!thread) return;
    openedThread.current = initialThread;
    navigateThread(thread);
  }, [initialThread, visibleThreads]);
  const highlighted = visibleThreads?.find(
    (row) => row.id === (hoveredThread || activeThread),
  );
  const quote =
    anchor.type === "textQuote"
      ? anchor.exact
      : highlighted?.anchor.type === "textQuote"
        ? highlighted.anchor.exact
        : "";
  useEffect(() => {
    const root = viewer.current;
    if (!root || !("highlights" in CSS) || typeof Highlight === "undefined")
      return;
    CSS.highlights.delete("canvas-comment");
    if (!quote) return;
    const paint = () => {
      CSS.highlights.delete("canvas-comment");
      if (!quote) return;
      const range =
        anchor.type === "textQuote" &&
        draftRange.current?.startContainer.isConnected
          ? draftRange.current
          : quoteRange(root, quote);
      if (range) CSS.highlights.set("canvas-comment", new Highlight(range));
    };
    paint();
    const observer = new MutationObserver(paint);
    observer.observe(root, { childList: true, subtree: true });
    return () => {
      observer.disconnect();
      CSS.highlights.delete("canvas-comment");
    };
  }, [quote, anchor.type, versionId, liveEditing]);
  async function action(command: Parameters<ExecuteCanvasCommand>[0]) {
    setBusy(true);
    setError("");
    try {
      if (
        discussionOutputId &&
        !outputId &&
        [
          "comment",
          "reply",
          "resolve",
          "edit_comment",
          "delete_comment",
        ].includes(command.type)
      ) {
        const result = await commentOnOutput({
          companyId: scope.companyId,
          outputId: discussionOutputId,
          command: {
            ...command,
            requestKey: command.requestKey ?? crypto.randomUUID(),
          },
        }).unwrap();
        await documentReview.refetch().unwrap();
        return result;
      }
      return await execute({
        ...command,
        versionId: command.pageId
          ? undefined
          : (command.versionId ?? versionId),
        artifactId: command.artifactId ?? artifact.id,
      });
    } catch (reason) {
      setError(
        reason instanceof Error
          ? reason.message
          : "The change could not be saved. Refresh and try again.",
      );
      throw reason;
    } finally {
      setBusy(false);
    }
  }
  useEffect(() => {
    if (anchor.type !== "media") return;
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [anchor.type]);
  const selectVersion = (id: string) => {
    if (anchor.type === "media" || busy) return;
    setNavigation(undefined);
    setVersionId(id);
    setEditing(false);
    setAnchor({ type: "version" });
    setActiveThread("");
    setHoveredThread("");
    draftRange.current = undefined;
  };
  const openSavedVersion = async (id: string) => {
    let fresh = await history.refetch().unwrap();
    // A notification refresh may have started before this version committed.
    if (!fresh.versions.some((row) => row.id === id))
      fresh = await history.refetch().unwrap();
    if (!fresh.versions.some((row) => row.id === id))
      throw new Error("The saved version is unavailable. Please refresh.");
    selectVersion(id);
  };
  const readPoint = (value: Anchor) =>
    value.type === "point" &&
    typeof value.x === "number" &&
    typeof value.y === "number"
      ? value
      : null;
  return (
    <>
      <DocumentCommentHighlights comments={comments} />
      <CanvasDialog
        wide
        embedded={embedded}
        className="pc-document-workspace"
        title={artifact.title}
        leading={
          <span className={`pc-review-format pc-tone-${tone}`}>
            <FormatIcon size={21} />
          </span>
        }
        headerActions={
          <>
            <div
              className="pc-review-status-slot"
              ref={setDocumentStatusContainer}
            />
            <DocumentCommentControls
              comments={comments}
              className="pc-button pc-discussion-toggle"
            />
            {historyDocumentScope && (
              <button
                className="pc-icon-button"
                aria-label="Document history"
                title="Document history"
                aria-pressed={historyOpen}
                onClick={() => setHistoryOpen(!historyOpen)}
              >
                <Clock3 size={18} />
              </button>
            )}
          </>
        }
        description={
          liveEditing
            ? workingVersion?.branchTitle &&
              workingVersion.branchTitle !== "Main"
              ? workingVersion.branchTitle
              : undefined
            : `${formatLabel}${version ? ` · ${date(version.createdAt)}` : ""} · Saved artifact`
        }
        onClose={onClose}
        closeHint={
          anchor.type === "media"
            ? "Post or discard your marks before closing"
            : undefined
        }
        canClose={!dirty && !busy && anchor.type !== "media"}
      >
        <div className="pc-review-toolbar">
          {companyWikiPageId && (
            <div className="pc-review-view-modes" aria-label="Document version">
              {workingVersion && canPublishWiki && (
                <button
                  aria-pressed={liveEditing}
                  disabled={dirty || busy || anchor.type === "media"}
                  onClick={() => {
                    cancelSelection();
                    setActiveThread("");
                    setVersionId(workingVersion.id);
                    setEditing(true);
                    setCompareId("");
                  }}
                >
                  <Pencil size={15} aria-hidden />
                  Wiki draft
                </button>
              )}
              <button
                aria-pressed={!liveEditing}
                disabled={dirty || anchor.type === "media" || busy}
                onClick={() => {
                  cancelSelection();
                  setActiveThread("");
                  setEditing(false);
                }}
              >
                <FileText size={15} aria-hidden /> Published Wiki
              </button>
            </div>
          )}
          {!liveEditing && !historyDocumentScope && (
            <>
              <select
                disabled={busy || anchor.type === "media"}
                aria-label="Submitted output"
                value={versionId}
                onChange={(event) => selectVersion(event.target.value)}
              >
                {(history.data?.versions ?? [artifact.latestVersion]).map(
                  (row) => (
                    <option key={row.id} value={row.id}>
                      {row.branchTitle || "Main"} · {date(row.createdAt)}
                    </option>
                  ),
                )}
              </select>
              <button
                className="pc-button"
                disabled={
                  busy ||
                  anchor.type === "media" ||
                  (history.data?.versions.length ?? 0) < 2
                }
                aria-pressed={!!compareId}
                onClick={() =>
                  setCompareId(
                    compareId
                      ? ""
                      : (history.data?.versions.find(
                          (row) => row.id !== versionId,
                        )?.id ?? ""),
                  )
                }
              >
                <Copy size={16} />
                Compare
              </button>
            </>
          )}
          <span className="pc-toolbar-spacer" />
          {liveEditing && !companyWikiPageId && canEditDocuments && (
            <button
              className="pc-text-button"
              disabled={dirty || busy}
              onClick={() => setDocumentEditing((value) => !value)}
            >
              <Pencil size={16} aria-hidden />
              {documentEditing ? "Done editing" : "Edit document"}
            </button>
          )}
          {version?.sourceOutputId && version.wikiPageId && (
            <button
              className="pc-text-button"
              disabled={dirty || busy}
              onClick={() => {
                const original = history.data?.versions.find(
                  (row) => row.outputId === version.sourceOutputId,
                );
                if (original) selectVersion(original.id);
              }}
            >
              Original files
            </button>
          )}
          {liveEditing && !companyWikiPageId && (
            <details className="pc-document-menu pc-review-document-menu">
              <summary
                className="pc-icon-button"
                aria-label="Document options"
                title="Document options"
              >
                <MoreHorizontal size={18} aria-hidden />
              </summary>
              <div
                className="pc-document-menu-panel"
                ref={setDocumentActionsContainer}
              />
            </details>
          )}
          <button
            className="pc-icon-button"
            title={
              copied
                ? "Link copied"
                : liveEditing
                  ? "Copy document link"
                  : "Copy submitted output link"
            }
            aria-label={
              liveEditing ? "Copy document link" : "Copy submitted output link"
            }
            onClick={() => {
              const url = new URL(
                outputId || (embedded && version?.sourceOutputId)
                  ? `/companies/${scope.companyId}/artifacts/${outputId ?? version?.sourceOutputId}`
                  : `/projects/${projectScope?.projectId}/artifacts?view=canvas`,
                window.location.origin,
              );
              if (!embedded || !version?.sourceOutputId)
                url.searchParams.set("artifactId", artifact.id);
              if (!liveEditing) url.searchParams.set("versionId", versionId);
              if (outputId) url.searchParams.set("view", "canvas");
              url.searchParams.delete("mode");
              void navigator.clipboard
                .writeText(url.href)
                .then(() => setCopied(true))
                .catch(() => setError("The review link could not be copied."));
            }}
          >
            {copied ? <Check size={16} /> : <Link2 size={16} />}
          </button>
        </div>
        <div
          className="pc-review-formatting"
          data-open={
            liveEditing &&
            documentEditing &&
            canEditDocuments &&
            !companyWikiPageId
          }
        >
          {liveEditing &&
            documentEditing &&
            canEditDocuments &&
            !companyWikiPageId && (
              <DocumentFormatToolbar editor={documentEditor} />
            )}
        </div>
        <div className="pc-review-content" aria-busy={openingDraft}>
          {openingDraft && (
            <div className="pc-review-loading-cover">
              <PageDocumentLoading toolbar={false} />
            </div>
          )}
          <div
            className="pc-review-layout"
            data-loading={openingDraft || undefined}
            inert={openingDraft}
            aria-hidden={openingDraft || undefined}
          >
            {error && (
              <p className="pc-inline-warning" role="alert">
                {error}
              </p>
            )}
            <div
              className={`pc-review-body ${liveEditing ? "is-editing" : ""} ${!commentsOpen && !(historyOpen && historyDocumentScope) ? "is-focused" : ""}`}
            >
              <div
                className="pc-review-floating-actions"
                ref={setEditButtonContainer}
              >
                {!liveEditing &&
                  version?.sourceType !== "output" &&
                  workingVersion?.wikiPageId &&
                  (canEditDocuments ||
                    page.currentData?.canEdit ||
                    (companyWikiPageId && canPublishWiki)) && (
                    <button
                      className="pc-document-edit-button"
                      disabled={dirty || busy || anchor.type === "media"}
                      title="Edit the shared document."
                      onClick={() => {
                        setDocumentEditing(true);
                        cancelSelection();
                        setActiveThread("");
                        setVersionId(workingVersion.id);
                        setEditing(true);
                        setCompareId("");
                      }}
                    >
                      <Pencil size={16} aria-hidden /> Edit
                    </button>
                  )}
              </div>
              <CommentConnectors
                layer={comments.connectorLayer}
                paths={comments.connectors}
                activeId={comments.hovered || comments.active}
                open={commentsOpen}
                className="pc-review-connectors"
              />
              <div className="pc-review-stage" ref={setDocumentStage}>
                {liveEditing ? (
                  <div
                    ref={viewer}
                    className="pc-review-document pc-page-theme"
                    data-page-theme={page.currentData?.theme}
                  >
                    {outputDocumentScope ? (
                      <LiveDocument
                        onReady={handleDraftReady}
                        onEditor={setDocumentEditor}
                        actionsContainer={documentActionsContainer}
                        statusContainer={documentStatusContainer}
                        loadingContent={<PageDocumentLoading toolbar={false} />}
                        key={draftKey}
                        scope={outputDocumentScope}
                        versionId={selectedOutputDocument!.pageId}
                        title={selectedOutputDocument!.title}
                        onCommentSelection={
                          canComment
                            ? (selected) => {
                                setAnchor(selected);
                                setActiveThread("");
                                setComposing(true);
                                setCommentsOpen(true);
                              }
                            : undefined
                        }
                        readOnly={!canEditDocuments || !documentEditing}
                        onDirtyChange={setDirty}
                      />
                    ) : companyWikiPageId ? (
                      <WikiDocumentWorkspace
                        key={`${scope.companyId}:${companyWikiPageId}`}
                        companyId={scope.companyId}
                        pageId={companyWikiPageId}
                        onDirtyChange={setDirty}
                        onPublished={() => setEditing(false)}
                      />
                    ) : projectScope ? (
                      <LiveDocument
                        onReady={handleDraftReady}
                        onEditor={setDocumentEditor}
                        actionsContainer={documentActionsContainer}
                        statusContainer={documentStatusContainer}
                        loadingContent={<PageDocumentLoading toolbar={false} />}
                        key={draftKey}
                        scope={
                          workingVersion?.sourceOutputId &&
                          workingVersion.outputItemId
                            ? {
                                companyId: scope.companyId,
                                outputId: workingVersion.sourceOutputId,
                                outputItemId: workingVersion.outputItemId,
                              }
                            : projectScope
                        }
                        versionId={
                          workingVersion?.sourceOutputId &&
                          workingVersion.outputItemId
                            ? workingVersion.wikiPageId!
                            : historyVersionId
                        }
                        title={page.currentData?.title ?? artifact.title}
                        titleMaxLength={artifact.pageId ? 200 : undefined}
                        onRename={
                          canEdit && canEditDocuments && documentEditing
                            ? async (title) => {
                                if (page.currentData) {
                                  await updatePageMetadata({
                                    ...projectScope!,
                                    pageId: page.currentData.pageId,
                                    input: {
                                      sequence: page.currentData.sequence,
                                      title,
                                    },
                                  }).unwrap();
                                  return;
                                }
                                await action({
                                  type: "rename",
                                  artifactId: artifact.id,
                                  versionId,
                                  expectedRevision: artifact.revision,
                                  title,
                                });
                              }
                            : undefined
                        }
                        readOnly={!canEditDocuments || !documentEditing}
                        focusId={
                          blockId ||
                          (typeof item?.viewConfig.blockId === "string"
                            ? item.viewConfig.blockId
                            : undefined)
                        }
                        onDirtyChange={setDirty}
                        onPlaceBlock={async (blockId) => {
                          await action({
                            type: "place",
                            versionId,
                            versionPolicy: "draft",
                            viewConfig: { blockId },
                            title: artifact.title,
                            x: 80,
                            y: 80,
                            width: 480,
                            height: 340,
                          });
                        }}
                        onAgentThread={(id) => {
                          setActiveThread(id);
                          setCommentsOpen(true);
                        }}
                        onCommentSelection={
                          pageDiscussion && canComment
                            ? (selected) => {
                                setAnchor(selected);
                                setActiveThread("");
                                focusComposer();
                              }
                            : undefined
                        }
                      />
                    ) : null}
                  </div>
                ) : (
                  <div
                    className={`pc-review-comparison ${compare ? "is-comparing" : ""}`}
                  >
                    {compare && (
                      <section className="pc-compare-pane">
                        <div className="pc-compare-label">
                          <span>Compare with</span>
                          <select
                            aria-label="Comparison snapshot"
                            value={compareId}
                            onChange={(event) =>
                              setCompareId(event.target.value)
                            }
                          >
                            {history.data?.versions
                              .filter((row) => row.id !== versionId)
                              .map((row) => (
                                <option key={row.id} value={row.id}>
                                  {date(row.createdAt)}
                                </option>
                              ))}
                          </select>
                        </div>
                        <Suspense
                          fallback={
                            <p className="pc-viewer-loading">
                              Opening comparison…
                            </p>
                          }
                        >
                          <ArtifactViewer scope={scope} version={compare} />
                        </Suspense>
                      </section>
                    )}
                    <section className="pc-compare-pane">
                      {compare && (
                        <div className="pc-compare-label">
                          Reviewing version {version?.number}
                        </div>
                      )}
                      <div
                        ref={viewer}
                        className="pc-review-viewer"
                        onMouseUp={(event) => {
                          if (!canComment || anchor.type === "media") return;
                          if (
                            !(event.target as HTMLElement).closest(
                              ".pc-document-prose, .pc-block-editor, .pc-source-code",
                            )
                          )
                            return;
                          const selection = window.getSelection();
                          const text = selection?.toString().trim();
                          if (
                            text &&
                            text.length <= 1000 &&
                            selection?.anchorNode &&
                            viewer.current?.contains(selection.anchorNode) &&
                            selection.focusNode &&
                            viewer.current?.contains(selection.focusNode) &&
                            selection.rangeCount
                          ) {
                            draftRange.current = selection
                              .getRangeAt(0)
                              .cloneRange();
                            setActiveThread("");
                            setHoveredThread("");
                            setNavigation(undefined);
                            const block = (
                              selection.anchorNode instanceof Element
                                ? selection.anchorNode
                                : selection.anchorNode.parentElement
                            )?.closest<HTMLElement>("[data-block-id]");
                            setAnchor({
                              type: "textQuote",
                              exact: text,
                              blockId: block?.dataset.blockId,
                            });
                          }
                        }}
                      >
                        {version ? (
                          <Suspense
                            fallback={<PageDocumentLoading toolbar={false} />}
                          >
                            <ArtifactViewer
                              key={version.id}
                              scope={scope}
                              version={version}
                              title={artifact.title}
                              autoEdit={false}
                              initialOutputItemId={initialOutputItemId}
                              editButtonContainer={editButtonContainer}
                              onEditDocument={
                                canEditDocuments
                                  ? async (outputItemId, filename) => {
                                      if (outputId) {
                                        const documentScope = {
                                          companyId: scope.companyId,
                                          outputId,
                                          outputItemId,
                                        };
                                        const opened =
                                          await openOutputDocument(
                                            documentScope,
                                          ).unwrap();
                                        const refreshed = await outputReview
                                          .refetch()
                                          .unwrap();
                                        const version = refreshed.versions.find(
                                          (row) =>
                                            row.wikiPageId === opened.pageId,
                                        );
                                        if (version) setVersionId(version.id);
                                        setOutputDocumentItem(outputItemId);
                                        setDocumentEditing(true);
                                        setEditing(true);
                                        return;
                                      }
                                      const result = await action({
                                        type: "import_output_document",
                                        artifactId: artifact.id,
                                        versionId,
                                        outputItemId,
                                        title: Array.from(filename)
                                          .slice(0, 240)
                                          .join(""),
                                      });
                                      await openSavedVersion(result.id);
                                      setDocumentEditing(true);
                                      setEditing(true);
                                    }
                                  : undefined
                              }
                              focusId={
                                blockId ||
                                (typeof item?.viewConfig.blockId === "string"
                                  ? item.viewConfig.blockId
                                  : undefined)
                              }
                              review={{
                                anchor,
                                onChange: (next) => {
                                  setAnchor(next);
                                  setError("");
                                  setActiveThread("");
                                  setHoveredThread("");
                                  setNavigation(undefined);
                                },
                                threads: visibleThreads ?? [],
                                activeThread,
                                hoveredThread,
                                onHover: setHoveredThread,
                                onCompose: focusComposer,
                                onSelect: (id) => {
                                  const thread = visibleThreads.find(
                                    (row) => row.id === id,
                                  );
                                  if (thread) revealThread(thread);
                                },
                                navigation,
                                canEdit: canComment,
                                busy,
                              }}
                            />
                          </Suspense>
                        ) : history.isError ? (
                          <p className="pc-viewer-loading" role="alert">
                            This version is unavailable or access has changed.
                          </p>
                        ) : (
                          <PageDocumentLoading toolbar={false} />
                        )}
                        {anchor.type !== "version" &&
                          anchor.type !== "media" && (
                            <SelectionActions
                              label={
                                anchor.type === "textQuote"
                                  ? "Selected text"
                                  : "Pinned view"
                              }
                              busy={busy}
                              onComment={focusComposer}
                              onCancel={cancelSelection}
                            />
                          )}
                        <div className="pc-review-pins">
                          {visibleThreads?.map((thread, index) => {
                            const point = readPoint(thread.anchor);
                            return point &&
                              thread.status === "open" &&
                              Math.abs(
                                (point.viewportWidth ?? 0) - viewerSize.width,
                              ) < 4 &&
                              Math.abs(
                                (point.viewportHeight ?? 0) - viewerSize.height,
                              ) < 4 ? (
                              <button
                                key={thread.id}
                                className={
                                  activeThread === thread.id ||
                                  hoveredThread === thread.id
                                    ? "is-active"
                                    : ""
                                }
                                title={`Open comment ${index + 1}`}
                                aria-label={`Open comment ${index + 1}`}
                                style={{
                                  left: `${(point.x ?? 0) * 100}%`,
                                  top: `${(point.y ?? 0) * 100}%`,
                                }}
                                onClick={() => {
                                  revealThread(thread);
                                }}
                              >
                                {index + 1}
                              </button>
                            ) : null;
                          })}
                        </div>
                      </div>
                    </section>
                  </div>
                )}
              </div>
              <DocumentCommentsPanel
                suggestions={
                  liveEditing && page.currentData ? (
                    <PageContributions
                      onEditPage={
                        canEditDocuments
                          ? () => setDocumentEditing(true)
                          : undefined
                      }
                      editor={documentEditor}
                      key={page.currentData.pageId}
                      scope={{
                        ...projectScope!,
                        pageId: page.currentData.pageId,
                      }}
                      canEdit={page.currentData.canReviewSuggestions}
                      dirty={dirty}
                    />
                  ) : undefined
                }
                agentScope={
                  workingPageId && canComment && "projectId" in scope
                    ? scope
                    : undefined
                }
                comments={comments}
                layout="canvas"
                page={pageDiscussion}
                canComment={canComment}
                ready={threadsReady}
                failed={threads.isError}
                retry={threads.refetch}
                busy={busy}
                execute={action}
                target={
                  pageDiscussion && discussionPageId
                    ? { pageId: discussionPageId }
                    : { versionId }
                }
                onNavigate={navigateThread}
                onCancel={cancelSelection}
              />
              <aside
                className="pc-review-rail"
                aria-label="History"
                hidden={!historyOpen || !historyDocumentScope}
              >
                <header className="pc-floating-comments-heading">
                  <strong>Document history</strong>
                  <button
                    className="pc-icon-button"
                    aria-label="Close history"
                    onClick={() => setHistoryOpen(false)}
                  >
                    <X size={16} />
                  </button>
                </header>
                {historyOpen && historyDocumentScope && (
                  <DocumentHistoryPanel
                    key={`${workingVersion?.wikiPageId ?? selectedOutputDocument?.pageId}`}
                    scope={historyDocumentScope}
                    versionId={historyVersionId}
                    disabled={dirty || busy}
                    onReviewSuggestion={() => setHistoryOpen(false)}
                    onRestored={() => {
                      setDocumentReload((value) => value + 1);
                      if (workingVersion) setVersionId(workingVersion.id);
                      setEditing(true);
                    }}
                  />
                )}
              </aside>
            </div>
          </div>
        </div>
      </CanvasDialog>
    </>
  );
}
