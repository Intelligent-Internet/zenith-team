import { PageContributions } from "./page-contributions";
import type { Editor } from "@tiptap/react";
import { PageComments } from "./page-comments";
import type { ReviewAnchor } from "../review/annotation-overlay";
import { useMemo, useState } from "react";
import { Link, useParams } from "react-router";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { Button } from "@/shared/ui/button";
import { ArrowLeft, FileText } from "@/shared/ui/icons";
import {
  useReadablePagesQuery,
  useLiveArtifactAccessQuery,
} from "../api/artifact-access-api";
import { ArtifactSharing } from "./artifact-sharing";
import { LiveDocument } from "./live-document";
import { PageDocument } from "./page-document";
import { DocumentHistoryPanel } from "./document-history";
import { CanvasDialog } from "../canvas/canvas-dialog";

export function PageLibrary() {
  const session = useSessionSnapshot();
  const companyId = session.company?.id ?? "";
  const pages = useReadablePagesQuery(companyId, {
    skip: !companyId,
    refetchOnMountOrArgChange: true,
    refetchOnFocus: true,
  });
  const [search, setSearch] = useState("");
  const visible = (pages.isError ? undefined : pages.currentData)?.items.filter(
    (page) =>
      page.title.toLocaleLowerCase().includes(search.toLocaleLowerCase()),
  );
  return (
    <main className="mx-auto w-full max-w-5xl space-y-6 overflow-auto p-6">
      <header>
        <h1 className="text-2xl font-semibold">Pages</h1>
        <p className="mt-1 text-sm text-workspace-muted">
          Your shared work, across Projects and conversations.
        </p>
      </header>
      <input
        aria-label="Search Pages"
        placeholder="Find a Page…"
        className="w-full rounded-workspace-control border border-border bg-background px-3 py-2"
        value={search}
        onChange={(event) => setSearch(event.target.value)}
      />
      {pages.isLoading && <p role="status">Loading Pages…</p>}
      {pages.isError && (
        <p role="alert">
          Pages could not be loaded.{" "}
          <Button variant="outline" onClick={() => void pages.refetch()}>
            Retry
          </Button>
        </p>
      )}
      {visible && (
        <ul className="divide-y divide-border">
          {visible.map((page) => (
            <li key={page.id}>
              <Link
                className="flex items-center gap-3 rounded-workspace-control px-2 py-4 hover:bg-muted focus-visible:outline-2"
                to={`/companies/${companyId}/pages/${page.id}`}
              >
                <FileText size={20} />
                <span className="min-w-0 flex-1 truncate">{page.title}</span>
                <span className="text-sm text-workspace-muted">
                  {page.projectId ? "Project Page" : "Standalone Page"}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
      {visible?.length === 0 && (
        <p className="text-workspace-muted">
          {search
            ? "No Pages match your search."
            : "Pages you create in Projects or open from delivered Markdown appear here."}
        </p>
      )}
    </main>
  );
}

export function PageWorkspace() {
  const { companyId = "", pageId = "" } = useParams();
  return (
    <PageWorkspaceContent
      key={`${companyId}:${pageId}`}
      companyId={companyId}
      pageId={pageId}
    />
  );
}
function PageWorkspaceContent({
  companyId,
  pageId,
}: {
  companyId: string;
  pageId: string;
}) {
  const access = useLiveArtifactAccessQuery(
    { companyId, artifactId: pageId },
    { refetchOnMountOrArgChange: true, refetchOnFocus: true },
  );
  const [documentEditor, setDocumentEditor] = useState<Editor | null>(null);
  const [requestedThread, setRequestedThread] = useState("");
  const [commentsOpen, setCommentsOpen] = useState(false);
  const [commentAnchor, setCommentAnchor] = useState<ReviewAnchor>({
    type: "version",
  });
  const [content, setContent] = useState<HTMLDivElement | null>(null);
  const [commentsToolbar, setCommentsToolbar] = useState<HTMLDivElement | null>(
    null,
  );
  const [actions, setActions] = useState<HTMLDivElement | null>(null);
  const [history, setHistory] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [reload, setReload] = useState(0);
  const scope = useMemo(() => ({ companyId, pageId }), [companyId, pageId]);
  const data = access.isError ? undefined : access.currentData;
  return (
    <main className="flex min-h-0 flex-1 flex-col overflow-auto">
      <header className="flex flex-wrap items-center gap-3 border-b border-border px-6 py-3">
        <Link
          to="/pages"
          className="mr-auto inline-flex items-center gap-2 text-sm"
        >
          <ArrowLeft size={16} />
          Pages
        </Link>
        <div ref={setActions} />
        {!data?.projectId && <div ref={setCommentsToolbar} />}
        {data && <ArtifactSharing scope={{ companyId, artifactId: pageId }} />}
        {data?.ownership === "person" && (
          <Button
            variant="outline"
            size="sm"
            disabled={dirty}
            onClick={() => setHistory(true)}
          >
            History
          </Button>
        )}
      </header>
      {access.isLoading && (
        <p role="status" className="p-6">
          Opening Page…
        </p>
      )}
      {access.isError && (
        <p role="alert" className="p-6">
          This Page is unavailable or your access changed.{" "}
          <Button variant="outline" onClick={() => void access.refetch()}>
            Retry
          </Button>
        </p>
      )}
      {data &&
        (data.projectId ? (
          <PageDocument
            scope={{ ...scope, projectId: data.projectId }}
            actionContainer={actions}
          />
        ) : (
          <div className="po-page-discussion-layout p-6">
            <div className="min-w-0" ref={setContent}>
              <LiveDocument
                key={reload}
                scope={scope}
                versionId={pageId}
                title={data.title}
                onEditor={setDocumentEditor}
                onAgentThread={(id) => {
                  setRequestedThread(id);
                  setCommentsOpen(true);
                }}
                onDirtyChange={setDirty}
                onCommentSelection={(anchor) => {
                  setCommentAnchor(anchor);
                  setCommentsOpen(true);
                }}
              />
              <PageContributions
                scope={scope}
                editor={documentEditor}
                canEdit={data.canEdit}
                dirty={dirty}
              />
            </div>
            <div className="po-page-comments-rail">
              <PageComments
                requestedThreadId={requestedThread}
                scope={scope}
                canComment
                open={commentsOpen}
                onOpenChange={setCommentsOpen}
                anchor={commentAnchor}
                onAnchorChange={setCommentAnchor}
                toolbar={commentsToolbar}
                content={content}
              />
            </div>
          </div>
        ))}
      {history && (
        <CanvasDialog
          title="Page history"
          description="Automatically saved changes to this Page."
          onClose={() => setHistory(false)}
        >
          <DocumentHistoryPanel
            scope={scope}
            versionId={pageId}
            disabled={dirty}
            onRestored={() => {
              setReload((value) => value + 1);
              setHistory(false);
            }}
          />
        </CanvasDialog>
      )}
    </main>
  );
}
