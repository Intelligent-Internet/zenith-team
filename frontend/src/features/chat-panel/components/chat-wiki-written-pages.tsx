import { Link } from "react-router";
import type { WikiWrittenPage } from "@/features/chat-panel/model/wiki-tool-activity";

const STATUS_LABELS: Record<string, string> = {
  created: "created",
  unchanged: "unchanged",
  updated: "updated",
};

/**
 * The Company Wiki pages one wiki_write call saved, each a link into the Wiki.
 * A page known only from the call's input has no id yet and stays plain text.
 */
export function ChatWikiWrittenPages({ pages }: { pages: WikiWrittenPage[] }) {
  if (pages.length === 0) {
    return null;
  }
  return (
    <ul aria-label="Saved wiki pages" className="chat-runtime-wiki-pages">
      {pages.map((page, index) => {
        const status = page.status
          ? (STATUS_LABELS[page.status] ?? page.status)
          : undefined;
        return (
          <li key={page.pageId ?? `${index}:${page.title}`}>
            {page.pageId ? (
              <Link
                className="chat-runtime-wiki-pages__link"
                title={`Open “${page.title}” in the Wiki`}
                to={`/wiki/pages/${page.pageId}`}
              >
                {page.title}
              </Link>
            ) : (
              <span className="chat-runtime-wiki-pages__title">
                {page.title}
              </span>
            )}
            {status ? (
              <span className="chat-runtime-revision">{status}</span>
            ) : null}
          </li>
        );
      })}
    </ul>
  );
}
