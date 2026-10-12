import { Link } from "react-router";
import { Icon } from "@/shared/ui/icon";
import { artifactFormat } from "./canvas-nodes";
import { blockText } from "./block-editor-schema";
import {
  useCanvasDocumentQuery,
  type CanvasArtifact,
  type CanvasScope,
} from "./canvas-api";

export function ProjectResultCard({
  scope,
  artifact,
  onOpen,
}: {
  scope: CanvasScope;
  artifact: CanvasArtifact;
  onOpen: () => void;
}) {
  const version = artifact.latestVersion;
  const page = !!version.wikiPageId && !version.sourceOutputId;
  const document = useCanvasDocumentQuery(
    { ...scope, versionId: version.id, preview: true },
    { skip: !page },
  );
  const { Icon: FormatIcon, label } = artifactFormat(version);
  const text = page
    ? document.currentData?.content && !document.isError
      ? blockText(document.currentData.content)
      : ""
    : version.excerpt;
  return (
    <article className="po-result-card">
      <button className="po-result-open" onClick={onOpen}>
        <span className="po-result-kind">
          <FormatIcon size={18} /> {page ? "Page" : label}
        </span>
        <h3>{artifact.title}</h3>
        <p
          className={text ? "po-result-excerpt" : "po-result-excerpt is-empty"}
        >
          {text ||
            (document.isLoading
              ? "Loading page…"
              : page
                ? "Open this page to explore the work."
                : "Open to view the result.")}
        </p>
        <span className="po-result-state">
          {page ? <span>Working page</span> : <span>Saved Output</span>}
          <Icon name="arrow-up-right" className="size-4" />
        </span>
      </button>
      <footer>
        {artifact.taskId ? (
          <Link
            className="po-result-origin"
            to={`/projects/${scope.projectId}?taskId=${artifact.taskId}`}
            title="Open source Task"
          >
            <Icon name="task-square" className="size-4" />
            <span>Source Task</span>
          </Link>
        ) : (
          <span className="po-result-origin">
            {artifact.creatorName || "Project page"}
          </span>
        )}
      </footer>
    </article>
  );
}
