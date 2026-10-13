import { Link } from "react-router";
import { Icon } from "@/shared/ui/icon";
import { useProjectPagesQuery } from "../api/page-api";
import type { CanvasScope } from "../api/canvas-api";
import "./task-pages.css";

export function TaskPages({
  companyId,
  projectId,
  taskId,
}: CanvasScope & { taskId: string }) {
  const query = useProjectPagesQuery({ companyId, projectId, taskId });
  return (
    <section
      className="task-working-pages m-[20px_0] text-[var(--workspace-native-text,_inherit)] text-[length:13px]"
      aria-label="Pages"
    >
      <h3>Pages</h3>
      <p>
        Pages this Task created or updated. Submitted snapshots appear with its
        results.
      </p>
      {query.isLoading ? (
        <p role="status">Loading Pages…</p>
      ) : query.isError ? (
        <p role="alert">
          Pages could not be loaded.{" "}
          <button onClick={() => void query.refetch()}>Retry</button>
        </p>
      ) : query.currentData?.length ? (
        <div>
          {query.currentData.map((page) => (
            <Link
              key={page.id}
              to={`/projects/${projectId}/pages?pageId=${page.pageId}`}
            >
              <Icon name="document-text" className="size-4" />
              <span>
                {page.title}
                <small>Page</small>
              </span>
              <Icon name="arrow-up-right" className="size-4" />
            </Link>
          ))}
        </div>
      ) : (
        <p className="task-working-pages-empty">No working Pages yet.</p>
      )}
    </section>
  );
}
