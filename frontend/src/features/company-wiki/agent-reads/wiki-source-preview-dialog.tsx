import { Link } from "react-router";
import { ExternalLink } from "@/shared/ui/icons";
import { RestorePreview, WikiDialog } from "../wiki-dialogs";
import {
  useGetWikiPageQuery,
  useListWikiCollectionsQuery,
  useListWikiRevisionsQuery,
} from "../wiki-api";
import type { RunWikiRead } from "./agent-reads-api";
import "../wiki-page.css";

/**
 * The exact page revision an Agent read in a Run, beside the page as it is
 * now. Revisions are listed newest first, so the revision read sits
 * `currentRevision - revisionNumber` rows from the top.
 */
export function WikiSourcePreviewDialog({
  companyId,
  read,
  onClose,
}: {
  companyId: string;
  read: RunWikiRead;
  onClose: () => void;
}) {
  const pageId = read.pageId ?? "";
  const revisionNumber = read.revisionNumber ?? 0;
  const page = useGetWikiPageQuery(
    { companyId, pageId },
    { skip: !companyId || !pageId },
  );
  const current = page.currentData;
  const collections = useListWikiCollectionsQuery(companyId, {
    skip: !companyId,
  });
  const revisions = useListWikiRevisionsQuery(
    {
      companyId,
      pageId,
      offset: Math.max(0, (current?.contentRevision ?? 0) - revisionNumber),
    },
    { skip: !current },
  );
  const version = revisions.currentData?.items.find(
    (revision) => revision.number === revisionNumber,
  );
  const collectionName = (collectionId: string | null) =>
    collections.currentData?.items.find(
      (collection) => collection.id === collectionId,
    )?.name ?? "Unfiled";
  const title = read.title.trim() || "Wiki source";
  const changed =
    current !== undefined && current.contentRevision > revisionNumber;
  const archived = current?.status === "archived";
  const failed = page.isError || revisions.isError;
  const loading = !failed && (!current || (!version && revisions.isFetching));

  return (
    <WikiDialog
      wide
      title={`${title} · v${revisionNumber}`}
      description="The version the Agent read in this run, compared with the page today. This preview is read-only."
      onClose={onClose}
    >
      {archived ? (
        <p
          className="zen-wiki__restore-stale m-0 rounded-[8px] p-[9px_12px] [background:var(--workspace-approval-soft)] text-[var(--workspace-native-approval)]"
          role="status"
        >
          This page is archived, so Agents can no longer find or read it. You
          can restore it from the Wiki.
        </p>
      ) : null}
      {failed ? (
        <p className="text-workspace-danger p-[0_10px]" role="alert">
          The page could not be loaded.{" "}
          <button
            type="button"
            className="zen-wiki__retry"
            onClick={() => {
              void page.refetch();
              if (current) void revisions.refetch();
            }}
          >
            Retry
          </button>
        </p>
      ) : loading ? (
        <p
          className="p-[40px] text-center text-[var(--wk-muted)]"
          role="status"
        >
          Loading the version the Agent read…
        </p>
      ) : !version || !current ? (
        <p className="text-[var(--wk-muted)] p-[0_10px]" role="status">
          Revision {revisionNumber} of this page is no longer available.
        </p>
      ) : (
        <div
          className={
            changed
              ? "zen-wiki__restore-comparison"
              : "zen-wiki__restore-comparison zen-wiki__restore-comparison--single"
          }
        >
          <RestorePreview
            companyId={companyId}
            label={`Read by the Agent · revision ${version.number}`}
            title={version.title}
            collection={collectionName(version.collectionId)}
            summary={version.summary}
            body={version.body}
            pageId={current.id}
            content={version.content}
          />
          {changed ? (
            <RestorePreview
              companyId={companyId}
              label={`Now · revision ${current.contentRevision}`}
              title={current.title}
              collection={collectionName(current.collectionId)}
              summary={current.summary}
              body={current.body}
              pageId={current.id}
              content={current.content}
            />
          ) : null}
        </div>
      )}
      {!changed && current && version && !archived ? (
        <p className="text-[var(--wk-muted)] p-[0_10px]">
          This is still the current version of the page.
        </p>
      ) : null}
      <div className="flex justify-end gap-[8px]">
        <button type="button" className="zen-wiki__button" onClick={onClose}>
          Close
        </button>
        {pageId ? (
          <Link
            className="zen-wiki__button zen-wiki__primary"
            to={`/wiki/pages/${pageId}`}
          >
            <ExternalLink size={15} aria-hidden /> Open in Wiki
          </Link>
        ) : null}
      </div>
    </WikiDialog>
  );
}
