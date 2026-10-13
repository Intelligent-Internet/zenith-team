import { ArtifactReviewLoading } from "../review/artifact-review-loading";
import { ProjectPageList } from "../documents/project-page-list";
import { ResultsViewSwitch } from "../documents/results-view-switch";
import { lazy, Suspense, useState, useRef } from "react";
import { useSearchParams } from "react-router";
import { Icon } from "@/shared/ui/icon";
import {
  useCanvasCommandMutation,
  useGetCanvasQuery,
  type CanvasScope,
  type CanvasCommand,
} from "../api/canvas-api";
import {
  useCreateProjectPageMutation,
  useProjectPagesQuery,
  useProjectOverviewQuery,
} from "../api/page-api";
import { PageDocument } from "../documents/page-document";
import { documentTemplates } from "../documents/document-templates";
import { ProjectResultCard } from "./project-result-card";
import "./project-canvas.css";
import "../documents/document-workspace.css";
import "../documents/page-document.css";
import "../documents/project-pages-library.css";

const Review = lazy(() =>
  import("../review/artifact-review").then((module) => ({
    default: module.ArtifactReview,
  })),
);

export default function ProjectLibrary({
  companyId,
  projectId,
  kind,
}: {
  companyId: string;
  projectId: string;
  kind: "pages" | "artifacts";
}) {
  const scope: CanvasScope = { companyId, projectId };
  const [actionContainer, setActionContainer] = useState<HTMLDivElement | null>(
    null,
  );
  const [params, setParams] = useSearchParams();
  const selectedPageId = params.get("pageId");
  const query = useGetCanvasQuery(scope, { skip: !!selectedPageId });
  const pages = useProjectPagesQuery(scope, {
    skip: kind !== "pages" || !!selectedPageId,
  });
  const [createPage, creatingPage] = useCreateProjectPageMutation();
  const [command] = useCanvasCommandMutation();
  const selected = params.get("artifactId");
  const [search, setSearch] = useState("");
  const [sort, setSort] = useState("updated");
  const newPageButton = useRef<HTMLButtonElement>(null);
  const initialTemplate = Math.max(
    0,
    documentTemplates.findIndex(
      (entry) => entry.key === params.get("template"),
    ),
  );
  const [creating, setCreating] = useState(
    kind === "pages" && params.has("template"),
  );
  const [template, setTemplate] = useState(initialTemplate);
  const [visibleCount, setVisibleCount] = useState(12);
  const [title, setTitle] = useState("");
  const [error, setError] = useState("");
  const createKey = useRef(crypto.randomUUID());
  const board = query.currentData;
  const rows = (
    kind === "pages" ? (pages.currentData ?? []) : (board?.artifacts ?? [])
  )
    .filter((row) =>
      kind === "pages"
        ? row.latestVersion.wikiPageId && !row.latestVersion.sourceOutputId
        : row.latestVersion.outputId || row.latestVersion.sourceOutputId,
    )
    .filter((row) =>
      row.title.toLocaleLowerCase().includes(search.toLocaleLowerCase()),
    )
    .sort((a, b) =>
      kind === "pages" && sort === "title"
        ? a.title.localeCompare(b.title)
        : Date.parse(b.updatedAt) - Date.parse(a.updatedAt),
    );
  const artifact = board?.artifacts.find((row) => row.id === selected);
  const choose = (id?: string) => {
    const next = new URLSearchParams(params);
    next.delete("template");
    next.delete("pageId");
    next.delete("presentationId");
    next.delete("versionId");
    next.delete("blockId");
    const page = pages.currentData?.find((row) => row.id === id);
    if (page?.pageId) {
      next.set("pageId", page.pageId);
      next.delete("artifactId");
    } else if (id) next.set("artifactId", id);
    else next.delete("artifactId");
    setParams(next);
  };
  const closeCreate = () => {
    setCreating(false);
    setError("");
    const next = new URLSearchParams(params);
    next.delete("template");
    setParams(next, { replace: true });
    newPageButton.current?.focus();
  };
  const execute = async (
    input: Omit<CanvasCommand, "requestKey">,
    requestKey: string = crypto.randomUUID(),
  ) => command({ ...scope, command: { ...input, requestKey } }).unwrap();
  const create = async () => {
    if (creatingPage.isLoading) return;
    setError("");
    try {
      const seed = documentTemplates[template];
      const result = await createPage({
        ...scope,
        input: {
          requestKey: createKey.current,
          title:
            title.trim() || (seed.key === "blank" ? "Untitled" : seed.title),
          content: seed.content,
        },
      }).unwrap();
      createKey.current = crypto.randomUUID();
      setCreating(false);
      setTitle("");
      const next = new URLSearchParams(params);
      next.delete("template");
      next.delete("artifactId");
      next.set("pageId", result.pageId);
      setParams(next);
    } catch {
      setError(
        "The Page could not be created. Your access or connection may have changed.",
      );
    }
  };
  const pageId =
    selectedPageId ||
    (kind === "pages" && !params.has("versionId")
      ? artifact?.pageId
      : undefined);
  const overview = useProjectOverviewQuery(scope, {
    skip: kind !== "pages" || !pageId,
  });
  if (pageId)
    return (
      <div className="po-page-frame">
        <div className="pc-review-floating-actions" ref={setActionContainer} />
        <div className="po-document po-page-workspace">
          <PageDocument
            key={pageId}
            scope={{ ...scope, pageId }}
            actionContainer={actionContainer}
            onBack={() => choose()}
            isProjectOverview={overview.currentData?.pageId === pageId}
          />
        </div>
      </div>
    );
  const label = kind === "pages" ? "Pages" : "Results";
  return (
    <section
      className={`po-document po-library ${kind === "pages" ? "po-pages-library" : ""}`}
      aria-label={`Project ${label}`}
    >
      <header className="po-library-heading">
        <div>
          <h2>{label}</h2>
          <p>
            {kind === "pages"
              ? "Notes, plans and knowledge for this project."
              : "What your Tasks have produced, ready to explore and review."}
          </p>
        </div>
        {kind === "artifacts" && <ResultsViewSwitch />}
        {kind === "pages" && board?.canEditDocuments && (
          <button
            ref={newPageButton}
            className="po-primary"
            disabled={creatingPage.isLoading}
            onClick={() => {
              if (creating) closeCreate();
              else {
                setError("");
                setCreating(true);
              }
            }}
            aria-expanded={creating}
            aria-controls="project-page-create"
          >
            <Icon name="add" className="size-4" />
            New Page
          </button>
        )}
      </header>
      <div className={kind === "pages" ? "po-pages-toolbar" : undefined}>
        <label className="po-library-search">
          <Icon name="search" className="size-4" />
          <input
            aria-label={`Search ${label}`}
            placeholder={`Search ${label.toLowerCase()}…`}
            value={search}
            onChange={(event) => {
              setSearch(event.target.value);
              setVisibleCount(12);
            }}
          />
        </label>
        {kind === "pages" && (
          <label className="po-pages-sort">
            <span className="sr-only">Sort pages</span>
            <select
              value={sort}
              onChange={(event) => setSort(event.target.value)}
            >
              <option value="updated">Last edited</option>
              <option value="title">Name A–Z</option>
            </select>
          </label>
        )}
      </div>
      {creating && board?.canEditDocuments && (
        <form
          id="project-page-create"
          className="po-pages-create"
          onKeyDown={(event) => {
            if (event.key === "Escape" && !creatingPage.isLoading) {
              event.preventDefault();
              closeCreate();
            }
          }}
          onChange={() => {
            createKey.current = crypto.randomUUID();
          }}
          onSubmit={(event) => {
            event.preventDefault();
            void create();
          }}
        >
          <fieldset disabled={creatingPage.isLoading}>
            <legend className="sr-only">New Page</legend>
            <label>
              Page name
              <input
                autoFocus
                value={title}
                onChange={(event) => setTitle(event.target.value)}
                placeholder="Untitled"
                maxLength={200}
              />
            </label>
            <label>
              Start with
              <select
                value={template}
                onChange={(event) => setTemplate(Number(event.target.value))}
              >
                {documentTemplates.map((seed, index) => (
                  <option key={seed.key} value={index}>
                    {seed.title}
                  </option>
                ))}
              </select>
            </label>
          </fieldset>
          <p className="po-pages-template-description">
            {documentTemplates[template].description}
          </p>
          {error && (
            <p role="alert" className="po-error">
              {error}
            </p>
          )}
          <div className="po-options-actions">
            <button className="po-primary" disabled={creatingPage.isLoading}>
              {creatingPage.isLoading ? "Creating…" : "Create Page"}
            </button>
            <button
              type="button"
              disabled={creatingPage.isLoading}
              onClick={closeCreate}
            >
              Cancel
            </button>
          </div>
        </form>
      )}
      {error && !creating && (
        <p role="alert" className="po-error">
          {error}
        </p>
      )}
      {query.isLoading || (kind === "pages" && pages.isLoading) ? (
        <p className="po-state" role="status">
          Loading {label.toLowerCase()}…
        </p>
      ) : query.isError || (kind === "pages" && pages.isError) ? (
        <p className="po-state" role="alert">
          {label} could not be loaded.{" "}
          <button
            onClick={() => {
              void query.refetch();
              if (kind === "pages") void pages.refetch();
            }}
          >
            Retry
          </button>
        </p>
      ) : rows.length ? (
        <>
          {kind === "pages" ? (
            <ProjectPageList
              pages={rows.slice(0, visibleCount)}
              projectId={projectId}
              params={params}
            />
          ) : (
            <div className="po-result-grid">
              {rows.slice(0, visibleCount).map((row) => (
                <ProjectResultCard
                  key={row.id}
                  scope={scope}
                  artifact={row}
                  onOpen={() => choose(row.id)}
                />
              ))}
            </div>
          )}
          {kind === "pages" && (
            <p className="po-pages-count">
              {rows.length} {rows.length === 1 ? "page" : "pages"}
              {search ? " found" : ""}
            </p>
          )}
          {rows.length > visibleCount && (
            <button
              className="po-library-more"
              onClick={() => setVisibleCount((count) => count + 12)}
            >
              Show more · {rows.length - visibleCount} remaining
            </button>
          )}
        </>
      ) : (
        <div className="po-state">
          <h3>{search ? "No matches" : `No ${label.toLowerCase()} yet`}</h3>
          <p>
            {search
              ? "Try another name."
              : kind === "pages"
                ? "Keep notes, research and plans together. Create a page to get started."
                : "When a Task saves its results, they appear here with their source and review status."}
          </p>
        </div>
      )}
      {artifact && board && (
        <Suspense
          fallback={
            <ArtifactReviewLoading
              title={artifact.title}
              onClose={() => choose()}
            />
          }
        >
          <Review
            key={`${artifact.id}:${params.get("versionId") ?? "live"}`}
            scope={scope}
            artifact={artifact}
            versionId={params.get("versionId") ?? undefined}
            reviewOnly={params.has("versionId")}
            blockId={params.get("blockId") ?? undefined}
            canEdit={board.canEdit}
            canEditDocuments={board.canEditDocuments}
            execute={execute}
            onClose={() => choose()}
          />
        </Suspense>
      )}
      {selected && board && !artifact && (
        <p role="alert" className="po-error">
          This content is unavailable with your current access.{" "}
          <button onClick={() => choose()}>Close</button>
        </p>
      )}
    </section>
  );
}
