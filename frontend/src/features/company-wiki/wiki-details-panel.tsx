import { useEffect, useState } from "react";
import { Archive, ChevronRight, FileText, X } from "@/shared/ui/icons";
import { toast } from "@/shared/ui/sonner";
import {
  useGetWikiPageLinksQuery,
  useSetWikiArchiveMutation,
  type WikiPageDetail,
} from "./wiki-api";
import { WikiPageAgentReads } from "./agent-reads/wiki-page-agent-reads";

type OutlineHeading = { element: HTMLElement; text: string };

/** Tracks the h2/h3 headings rendered inside `prose`. */
function useRenderedOutline(prose: HTMLElement | null) {
  const [headings, setHeadings] = useState<OutlineHeading[]>([]);
  useEffect(() => {
    if (!prose) {
      setHeadings([]);
      return;
    }
    const collect = () =>
      setHeadings(
        [...prose.querySelectorAll<HTMLElement>("h2, h3")].map((element) => ({
          element,
          text: element.textContent?.trim() ?? "",
        })),
      );
    collect();
    // The reader re-renders in place when the page changes remotely.
    const observer = new MutationObserver(collect);
    observer.observe(prose, {
      childList: true,
      subtree: true,
      characterData: true,
    });
    return () => observer.disconnect();
  }, [prose]);
  return headings;
}

export function WikiDetailsPanel({
  companyId,
  page,
  prose,
  canPublish,
  editing,
  onOpenPage,
  onClose,
}: {
  companyId: string;
  page: WikiPageDetail;
  /** Rendered page body the outline is read from; null while editing. */
  prose: HTMLElement | null;
  canPublish: boolean;
  editing: boolean;
  onOpenPage: (pageId: string) => void;
  onClose: () => void;
}) {
  const links = useGetWikiPageLinksQuery(
    { companyId, pageId: page.id },
    { skip: !companyId },
  );
  const [setArchive, archiveState] = useSetWikiArchiveMutation();
  const outline = useRenderedOutline(prose);

  async function toggleArchive() {
    const archive = page.status === "active";
    const result = await setArchive({
      companyId,
      pageId: page.id,
      expectedRevision: page.revision,
      archive,
    });
    if ("error" in result)
      toast.error(result.error?.message ?? "Could not change page status.");
    else toast.success(archive ? "Page archived." : "Page restored.");
  }

  return (
    <aside className="zen-wiki__details" aria-label="Page details">
      <div className="zen-wiki__panel-head">
        <strong>On this page</strong>
        <button aria-label="Close page details" onClick={onClose}>
          <X size={16} />
        </button>
      </div>
      {editing ? (
        <p className="zen-wiki__muted">
          Publish the draft to update the page outline and links.
        </p>
      ) : outline.length ? (
        <nav aria-label="Page outline" className="zen-wiki__outline">
          {outline.map((heading, index) => (
            <button
              key={`${index}-${heading.text}`}
              onClick={() =>
                heading.element.scrollIntoView({
                  behavior: "smooth",
                  block: "start",
                })
              }
            >
              {heading.text}
            </button>
          ))}
        </nav>
      ) : (
        <p className="zen-wiki__muted">No headings yet.</p>
      )}
      <div className="zen-wiki__detail-section">
        <div className="zen-wiki__detail-heading">
          <strong>Backlinks</strong>
          <span>{links.data?.incoming.length ?? 0}</span>
        </div>
        {links.isLoading && <p className="zen-wiki__muted">Loading links…</p>}
        {links.isError && (
          <p className="zen-wiki__error" role="alert">
            Links could not be loaded.
          </p>
        )}
        {links.data?.incoming.map((linked) => (
          <button
            className="zen-wiki__detail-link"
            key={linked.id}
            onClick={() => onOpenPage(linked.id)}
          >
            <FileText size={15} />
            <span>{linked.title}</span>
            <ChevronRight size={14} />
          </button>
        ))}
        {links.data?.incoming.length === 0 && (
          <p className="zen-wiki__muted">No pages link here.</p>
        )}
        {links.data?.incomingTruncated && (
          <p className="zen-wiki__muted">Showing the first 200 backlinks.</p>
        )}
      </div>
      <div className="zen-wiki__detail-section">
        <div className="zen-wiki__detail-heading">
          <strong>Linked pages</strong>
          <span>{links.data?.outgoing.length ?? 0}</span>
        </div>
        {links.data?.outgoing.map((linked) => (
          <button
            className="zen-wiki__detail-link"
            key={linked.id}
            onClick={() => onOpenPage(linked.id)}
          >
            <FileText size={15} />
            <span>{linked.title}</span>
            <ChevronRight size={14} />
          </button>
        ))}
        {links.data?.outgoing.length === 0 && (
          <p className="zen-wiki__muted">No linked pages yet.</p>
        )}
        {links.data?.outgoingTruncated && (
          <p className="zen-wiki__muted">Showing the first 200 linked pages.</p>
        )}
      </div>
      <WikiPageAgentReads companyId={companyId} pageId={page.id} />
      {canPublish && (
        <div className="zen-wiki__detail-section zen-wiki__detail-actions">
          <button
            disabled={archiveState.isLoading || editing}
            onClick={() => void toggleArchive()}
          >
            <Archive size={16} />
            {page.status === "active" ? "Archive page" : "Restore page"}
          </button>
        </div>
      )}
    </aside>
  );
}
