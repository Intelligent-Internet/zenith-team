import { useState } from "react";
import { useListCompanyAgentsQuery } from "@/features/agent-setup/api/ceo-agent-api";
import { X } from "@/shared/ui/icons";
import {
  useListWikiRevisionsQuery,
  WIKI_REVISION_LIMIT,
  type WikiPageDetail,
  type WikiRevision,
} from "./wiki-api";
import { wikiAuthorLabel } from "./wiki-author-label";

export function WikiHistoryPanel({
  companyId,
  userId,
  page,
  canRestore,
  editing,
  onRestore,
  onClose,
}: {
  companyId: string;
  userId: string | undefined;
  page: WikiPageDetail;
  /** Publisher viewing an active page. */
  canRestore: boolean;
  editing: boolean;
  onRestore: (revision: WikiRevision) => void;
  onClose: () => void;
}) {
  const [offset, setOffset] = useState(0);
  const history = useListWikiRevisionsQuery(
    { companyId, pageId: page.id, offset },
    { skip: !companyId },
  );
  const roster = useListCompanyAgentsQuery(companyId, { skip: !companyId });
  const total = history.data?.total ?? 0;

  return (
    <aside className="zen-wiki__history">
      <div>
        <strong>Page history</strong>
        <button aria-label="Close history" onClick={onClose}>
          <X size={16} />
        </button>
      </div>
      {history.isLoading && <p>Loading history…</p>}
      {history.isError && (
        <p className="zen-wiki__error" role="alert">
          History could not be loaded.{" "}
          <button
            className="zen-wiki__retry"
            onClick={() => void history.refetch()}
          >
            Retry
          </button>
        </p>
      )}
      {history.data?.items.length === 0 && (
        <p className="zen-wiki__muted">No revisions yet.</p>
      )}
      {history.data?.items.map((revision) => (
        <div className="zen-wiki__revision" key={revision.id}>
          <b>Revision {revision.number}</b>
          <small>
            {new Date(revision.createdAt).toLocaleString()} ·{" "}
            {wikiAuthorLabel({
              agentId: revision.authorAgentId,
              agentName: roster.currentData?.items.find(
                (agent) => agent.id === revision.authorAgentId,
              )?.name,
              authorUserId: revision.authorUserId,
              viewerUserId: userId,
            })}
          </small>
          <p>{revision.title}</p>
          {revision.id === page.currentRevisionId ? (
            <span className="zen-wiki__revision-current">Current revision</span>
          ) : canRestore ? (
            <button
              disabled={editing}
              title={
                editing
                  ? "Wait for draft changes to save before restoring"
                  : undefined
              }
              onClick={() => onRestore(revision)}
            >
              Restore to draft
            </button>
          ) : null}
        </div>
      ))}
      {total > WIKI_REVISION_LIMIT && (
        <div className="zen-wiki__pagination">
          <button
            disabled={offset === 0}
            onClick={() => setOffset(Math.max(0, offset - WIKI_REVISION_LIMIT))}
          >
            Newer
          </button>
          <button
            disabled={offset + WIKI_REVISION_LIMIT >= total}
            onClick={() => setOffset(offset + WIKI_REVISION_LIMIT)}
          >
            Older
          </button>
        </div>
      )}
    </aside>
  );
}
