import { Link } from "react-router";
import { FileText, ArrowUpRight } from "@/shared/ui/icons";
import type { CanvasArtifact } from "./canvas-api";

export function ProjectPageList({
  pages,
  projectId,
  params,
}: {
  pages: CanvasArtifact[];
  projectId: string;
  params: URLSearchParams;
}) {
  return (
    <div className="po-pages-list">
      <div className="po-pages-columns" aria-hidden="true">
        <span>Name</span>
        <span>Created by</span>
        <span>Last edited</span>
        <span />
      </div>
      <ul>
        {pages.map((page) => {
          const next = new URLSearchParams(params);
          for (const key of [
            "template",
            "artifactId",
            "pageId",
            "presentationId",
            "versionId",
            "blockId",
          ])
            next.delete(key);
          next.set(
            page.pageId ? "pageId" : "artifactId",
            page.pageId || page.id,
          );
          return (
            <li key={page.id}>
              <Link
                className="po-pages-row"
                to={`/projects/${projectId}/pages?${next}`}
              >
                <span className="po-pages-name">
                  <FileText size={18} aria-hidden="true" />
                  <span title={page.title}>{page.title || "Untitled"}</span>
                </span>
                <span className="po-pages-author" title={page.creatorName}>
                  <span className="sr-only">Created by </span>
                  {page.creatorName}
                </span>
                <time
                  dateTime={page.updatedAt}
                  title={new Date(page.updatedAt).toLocaleString()}
                >
                  <span className="sr-only">Last edited </span>
                  {new Date(page.updatedAt).toLocaleDateString(undefined, {
                    month: "short",
                    day: "numeric",
                    year: "numeric",
                  })}
                </time>
                <ArrowUpRight
                  className="po-pages-open"
                  size={15}
                  aria-hidden="true"
                />
              </Link>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
