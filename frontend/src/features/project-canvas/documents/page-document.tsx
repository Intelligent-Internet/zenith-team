import type { Editor } from "@tiptap/react";
import { DocumentFormatToolbar } from "./document-format-toolbar";
import { createPortal } from "react-dom";
import { Pencil } from "@/shared/ui/icons";
import { DocumentHistoryPanel } from "./document-history";
import { PageDocumentLoading } from "./page-document-loading";
import { useAppDispatch } from "@/state";
import * as Popover from "@radix-ui/react-popover";
import { PageComments } from "./page-comments";
import type { ReviewAnchor } from "../review/annotation-overlay";
import { PagePdfExport } from "./page-pdf";
import { useEffect, useState, type ReactNode } from "react";
import { useSearchParams } from "react-router";
import { Icon } from "@/shared/ui/icon";
import { apiClient } from "@/lib/api-client";
import { LiveDocument } from "./live-document";
import {
  BlockDocument,
  SnapshotDocuments,
  type Block,
} from "./block-editor-schema";
import { CanvasDialog } from "../canvas/canvas-dialog";
import { PageContributions } from "./page-contributions";
import { downloadDocument } from "./document-export";
import {
  useProjectPageQuery,
  useUpdatePageMetadataMutation,
  useProjectPresentationQuery,
  presentationPath,
  type PageScope,
  type Presentation,
  pageApi,
} from "../api/page-api";
import type { CanvasScope } from "../api/canvas-api";
import "../canvas/project-canvas.css";
import "./document-workspace.css";
import "./page-document.css";

export function PageDocument({
  scope,
  settingsContent,
  reportContent,
  navigationContent,
  actionContainer,
  onReady,
  onBack,
  isProjectOverview = false,
}: {
  scope: PageScope;
  settingsContent?: ReactNode;
  reportContent?: ReactNode;
  navigationContent?: ReactNode;
  actionContainer: HTMLElement | null;
  onReady?: () => void;
  onBack?: () => void;
  isProjectOverview?: boolean;
}) {
  const dispatch = useAppDispatch();
  const query = useProjectPageQuery(scope);
  const [params, setParams] = useSearchParams();
  const presentationId = params.get("presentationId");
  const [historyOpen, setHistoryOpen] = useState(false);
  const [documentReload, setDocumentReload] = useState(0);
  const saved = useProjectPresentationQuery(
    { ...scope, id: presentationId ?? "" },
    { skip: !presentationId },
  );
  useEffect(() => {
    if (query.isError || (presentationId && !saved.isLoading)) onReady?.();
  }, [query.isError, presentationId, saved.isLoading, onReady]);
  const [metadata, updating] = useUpdatePageMetadataMutation();
  const [error, setError] = useState("");
  const [dirty, setDirty] = useState(false);
  const [editing, setEditing] = useState(false);
  const [settings, setSettings] = useState(false);
  const [statusContainer, setStatusContainer] = useState<HTMLDivElement | null>(
    null,
  );
  const [actionsContainer, setActionsContainer] =
    useState<HTMLDivElement | null>(null);
  const [commentsOpen, setCommentsOpen] = useState(false);
  const [requestedThreadId, setRequestedThreadId] = useState("");
  const [commentAnchor, setCommentAnchor] = useState<ReviewAnchor>({
    type: "version",
  });
  const [documentEditor, setDocumentEditor] = useState<Editor | null>(null);
  const [commentsToolbar, setCommentsToolbar] = useState<HTMLDivElement | null>(
    null,
  );
  const [content, setContent] = useState<HTMLDivElement | null>(null);
  useEffect(() => {
    setEditing(false);
    setCommentsOpen(false);
    setRequestedThreadId("");
    setCommentAnchor({ type: "version" });
  }, [scope.companyId, scope.projectId, scope.pageId]);
  const data = query.currentData;
  const run = async (action: () => Promise<unknown>) => {
    setError("");
    try {
      await action();
    } catch (reason) {
      setError(
        (reason as { message?: string })?.message ||
          "The page changed or this action is unavailable. Refresh and try again.",
      );
    }
  };
  const choosePresentation = (id?: string) => {
    if (dirty) {
      setError(
        "Wait for your changes to save before opening a submitted document.",
      );
      return;
    }
    const next = new URLSearchParams(params);
    if (id) next.set("presentationId", id);
    else next.delete("presentationId");
    setParams(next);
  };
  if (query.isLoading) return <PageDocumentLoading />;
  if (query.isError || !data)
    return (
      <div className="po-state" role="alert">
        <h2>This Page is unavailable</h2>
        <p>Your access or connection may have changed.</p>
        <button onClick={() => void query.refetch()}>Try again</button>
        {onBack && <button onClick={onBack}>Back to Pages</button>}
      </div>
    );
  return (
    <section
      className="po-document po-page-document pc-page-theme"
      data-page-theme={presentationId ? undefined : data.theme}
      aria-label={data.title}
    >
      {actionContainer &&
        data.canEdit &&
        !editing &&
        !presentationId &&
        createPortal(
          <button
            className="pc-document-edit-button"
            onClick={() => setEditing(true)}
          >
            <Pencil size={20} aria-hidden /> Edit
          </button>,
          actionContainer,
        )}
      <div className={`po-navigation${onBack ? " po-page-navigation" : ""}`}>
        {onBack && (
          <nav className="po-page-breadcrumb" aria-label="Page breadcrumb">
            <button type="button" onClick={onBack}>
              Pages
            </button>
            <span className="po-page-breadcrumb-separator" aria-hidden="true">
              /
            </span>
            <span
              className="po-page-breadcrumb-title"
              aria-current="page"
              title={data.title}
            >
              {data.title}
            </span>
            {isProjectOverview && (
              <span className="po-page-overview-badge">Project overview</span>
            )}
          </nav>
        )}
        {navigationContent}
        {editing && !presentationId && (
          <button
            className="po-edit-done"
            disabled={dirty}
            onClick={() => setEditing(false)}
          >
            Done editing
          </button>
        )}
        {presentationId && (
          <button disabled={dirty} onClick={() => choosePresentation()}>
            <Icon name="arrow-left" className="size-4" />
            Open latest
          </button>
        )}
        <div className="po-document-status-slot" ref={setStatusContainer} />
        {!presentationId && (
          <div className="po-comments-toolbar" ref={setCommentsToolbar} />
        )}
        <Popover.Root open={settings} onOpenChange={setSettings}>
          <Popover.Trigger asChild>
            <button
              className="po-secondary"
              aria-label="Page options"
              title="Page options"
            >
              <Icon name={onBack ? "more" : "setting-2"} className="size-4" />
            </button>
          </Popover.Trigger>
          <Popover.Portal>
            <Popover.Content
              className="pc-block-settings-popup po-page-menu"
              side="bottom"
              align="end"
              sideOffset={6}
              collisionPadding={16}
              aria-label="Page options"
              onCloseAutoFocus={(event) => {
                if (document.activeElement?.closest('.tiptap, [role="dialog"]'))
                  event.preventDefault();
              }}
              onInteractOutside={(event) => {
                if (
                  event.target instanceof Element &&
                  event.target.closest('[role="dialog"]')
                )
                  event.preventDefault();
              }}
            >
              <header>
                <strong>Page options</strong>
                <Popover.Close asChild>
                  <button aria-label="Close Page options">
                    <Icon name="close" className="size-4" />
                  </button>
                </Popover.Close>
              </header>
              <div ref={setActionsContainer} />
              <button
                disabled={dirty}
                onClick={() => {
                  setSettings(false);
                  setHistoryOpen(true);
                }}
              >
                History
              </button>
              <PagePdfExport
                title={
                  presentationId
                    ? saved.currentData?.title || data.title
                    : data.title
                }
                disabled={
                  dirty ||
                  query.isFetching ||
                  (presentationId
                    ? !saved.currentData?.canExport || saved.isFetching
                    : false)
                }
                overview={!presentationId && !!reportContent}
              >
                {presentationId && saved.currentData ? (
                  <SnapshotDocuments.Provider
                    value={saved.currentData.documents as Record<string, Block>}
                  >
                    <BlockDocument
                      scope={scope}
                      content={saved.currentData.content as Block}
                      mode="snapshot"
                    />
                  </SnapshotDocuments.Provider>
                ) : (
                  <>
                    <BlockDocument
                      scope={scope}
                      content={data.content as Block}
                      live
                    />
                    {reportContent}
                  </>
                )}
              </PagePdfExport>
              {!presentationId && (
                <div className="po-page-properties">
                  <label>
                    Page theme
                    <select
                      aria-label="Page theme"
                      value={data.theme}
                      disabled={!data.canEdit || dirty || updating.isLoading}
                      onChange={(event) =>
                        void run(() =>
                          metadata({
                            ...scope,
                            input: {
                              sequence: data.sequence,
                              theme: event.target.value as
                                "default" | "editorial" | "compact",
                            },
                          }).unwrap(),
                        )
                      }
                    >
                      <option value="default">Default</option>
                      <option value="editorial">Editorial</option>
                      <option value="compact">Compact</option>
                    </select>
                  </label>
                  {settingsContent}
                  <p>
                    Changes save automatically. Updating a Task output submits
                    the saved document and its included evidence.
                  </p>
                </div>
              )}
            </Popover.Content>
          </Popover.Portal>
        </Popover.Root>
      </div>
      <div className="po-page-formatting">
        {editing && data.canEdit && !presentationId && (
          <DocumentFormatToolbar editor={documentEditor} />
        )}
      </div>
      {error && (
        <p role="alert" className="po-error">
          {error}
          <button onClick={() => setError("")}>Dismiss</button>
        </p>
      )}
      {historyOpen && (
        <CanvasDialog
          title="History"
          description="Automatically saved changes to the shared document."
          onClose={() => setHistoryOpen(false)}
        >
          <DocumentHistoryPanel
            scope={scope}
            versionId={data.versionId}
            disabled={dirty}
            onReviewSuggestion={() => setHistoryOpen(false)}
            onRestored={() => {
              setDocumentReload((value) => value + 1);
              choosePresentation();
            }}
          />
        </CanvasDialog>
      )}
      {presentationId ? (
        saved.isFetching ? (
          <p className="po-state" role="status">
            Loading submitted document…
          </p>
        ) : saved.isError || !saved.currentData ? (
          <p className="po-state" role="alert">
            This submitted document is unavailable.{" "}
            <button onClick={() => void saved.refetch()}>Retry</button>
          </p>
        ) : (
          <>
            <div className="po-capture-banner">
              <div>
                <Icon name="document-text" className="size-5" />
                <span>
                  <strong>
                    {saved.currentData.changeId
                      ? "Proposed delivery"
                      : "Saved delivery"}
                  </strong>
                  <small>
                    {new Date(saved.currentData.createdAt).toLocaleString()} ·{" "}
                    {saved.currentData.sourceCount} retained sources
                  </small>
                </span>
              </div>
              {saved.currentData.changeId && (
                <button
                  onClick={() => {
                    const next = new URLSearchParams(params);
                    next.delete("presentationId");
                    next.set("changeId", saved.currentData!.changeId!);
                    setParams(next);
                  }}
                >
                  Review changes
                </button>
              )}
              <button
                disabled={!saved.currentData.canExport}
                onClick={() =>
                  void run(async () => {
                    const response = await apiClient.get<string>(
                      `${presentationPath(scope, presentationId)}/export`,
                      { responseType: "text" },
                    );
                    downloadDocument(
                      response,
                      `${saved.currentData?.title ?? "Page"}.md`,
                      "text/markdown",
                    );
                  })
                }
              >
                <Icon name="download" className="size-4" />
                Export
              </button>
            </div>
            {saved.currentData.issues.map((issue) => (
              <p className="po-error" key={issue}>
                {issue}
              </p>
            ))}
            <PresentationPage scope={scope} presentation={saved.currentData} />
          </>
        )
      ) : (
        <div className="po-page-discussion-layout">
          <div className="po-page-content" ref={setContent}>
            <LiveDocument
              onEditor={setDocumentEditor}
              loadingContent={<PageDocumentLoading toolbar={false} />}
              onReady={onReady}
              key={`${data.pageId}:${documentReload}`}
              scope={scope}
              versionId={data.versionId}
              title={data.title}
              titleMaxLength={200}
              readOnly={!editing || !data.canEdit}
              onAgentThread={(id) => {
                setRequestedThreadId(id);
                setCommentsOpen(true);
              }}
              onCommentSelection={
                data.canEdit
                  ? (anchor) => {
                      setCommentAnchor(anchor);
                      setCommentsOpen(true);
                    }
                  : undefined
              }
              onDirtyChange={setDirty}
              quiet
              actionsContainer={actionsContainer}
              onAction={() => setSettings(false)}
              statusContainer={statusContainer}
              sources={data.sources}
              onRename={
                data.canEdit
                  ? async (title, previousTitle) => {
                      const latest = await dispatch(
                        pageApi.endpoints.projectPage.initiate(scope, {
                          subscribe: false,
                          forceRefetch: true,
                        }),
                      ).unwrap();
                      if (
                        latest.title !== previousTitle &&
                        latest.title !== title
                      ) {
                        throw new Error(
                          "Someone changed the title while you were editing. Retry to use your title.",
                        );
                      }
                      await metadata({
                        ...scope,
                        input: { sequence: latest.sequence, title },
                      }).unwrap();
                    }
                  : undefined
              }
            />
          </div>
          <div className="po-page-comments-rail">
            <PageComments
              suggestions={
                <PageContributions
                  onEditPage={data.canEdit ? () => setEditing(true) : undefined}
                  editor={documentEditor}
                  key={data.pageId}
                  scope={scope}
                  canEdit={data.canReviewSuggestions}
                  initialChangeId={params.get("changeId") ?? ""}
                  dirty={dirty}
                />
              }
              requestedThreadId={requestedThreadId}
              key={`${data.pageId}:${documentReload}`}
              scope={scope}
              canComment={data.canEdit}
              open={commentsOpen}
              onOpenChange={setCommentsOpen}
              anchor={commentAnchor}
              onAnchorChange={setCommentAnchor}
              toolbar={commentsToolbar}
              compactToolbar={!!onBack}
              content={content}
            />
          </div>
        </div>
      )}
    </section>
  );
}

export function PresentationPage({
  scope,
  presentation,
}: {
  scope: CanvasScope;
  presentation: Presentation;
}) {
  return (
    <article
      className="po-saved-page pc-page-theme"
      data-page-theme={presentation.theme}
    >
      <h1>{presentation.title}</h1>
      <SnapshotDocuments.Provider
        value={presentation.documents as Record<string, Block>}
      >
        <BlockDocument
          showOutline
          scope={scope}
          content={presentation.content as Block}
          mode="snapshot"
        />
      </SnapshotDocuments.Provider>
    </article>
  );
}
