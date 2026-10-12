import { useState } from "react";
import { CanvasDialog } from "./canvas-dialog";
import {
  useCanvasLibraryQuery,
  type CanvasScope,
  type CanvasSnapshot,
} from "./canvas-api";
import type { ExecuteCanvasCommand } from "./add-artifact";
import { Trash2, RotateCw, Plus, Layers } from "@/shared/ui/icons";

export function CanvasLibrary({
  scope,
  snapshot,
  execute,
  onClose,
  view,
  onView,
}: {
  scope: CanvasScope;
  snapshot: CanvasSnapshot;
  execute: ExecuteCanvasCommand;
  onClose: () => void;
  view: {
    mode: string;
    search: string;
    camera: { x: number; y: number; zoom: number };
    filters?: {
      review: string;
      type: string;
      contributor: string;
      task: string;
    };
    collapsed?: string[];
  };
  onView: (config: Record<string, unknown>) => void;
}) {
  const query = useCanvasLibraryQuery(scope);
  const [tab, setTab] = useState<"templates" | "views" | "removed">(
    "templates",
  );
  const [title, setTitle] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const act = async (work: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await work();
      await query.refetch();
    } catch {
      setError(
        "The operation could not be completed. Refresh and try again; source access may have changed.",
      );
    } finally {
      setBusy(false);
    }
  };
  const template = () => {
    const rows = snapshot.items;
    const indexes = new Map(rows.map((row, index) => [row.id, index]));
    const originX = Math.min(...rows.map((row) => row.x), 0),
      originY = Math.min(...rows.map((row) => row.y), 0);
    return {
      items: rows.map((row) => ({
        type: row.type,
        title:
          row.title ||
          snapshot.artifacts.find((a) => a.id === row.artifactId)?.title ||
          "Frame",
        versionId: row.version?.id,
        versionPolicy: "pinned",
        representation: row.representation,
        viewConfig: row.viewConfig,
        parent: row.parentFrameId ? indexes.get(row.parentFrameId) : undefined,
        x: row.x - originX,
        y: row.y - originY,
        width: row.width,
        height: row.height,
      })),
      edges: snapshot.edges
        .filter((edge) => indexes.has(edge.source) && indexes.has(edge.target))
        .map((edge) => ({
          source: indexes.get(edge.source),
          target: indexes.get(edge.target),
          label: edge.label,
        })),
    };
  };
  return (
    <CanvasDialog
      title="Canvas library"
      description="Reusable layouts, your views, and recoverable placements"
      onClose={onClose}
      canClose={!busy}
    >
      <div className="pc-add-tabs" role="tablist">
        {(["templates", "views", "removed"] as const).map((value) => (
          <button
            key={value}
            role="tab"
            aria-selected={tab === value}
            onClick={() => setTab(value)}
          >
            {value === "removed"
              ? "Recovery"
              : value === "views"
                ? "My views"
                : "Templates"}
          </button>
        ))}
      </div>
      <div className="pc-add-form">
        {error && <p role="alert">{error}</p>}
        {query.isLoading && <p>Loading library…</p>}
        {query.isError && (
          <p role="alert">
            The library is unavailable.{" "}
            <button onClick={() => void query.refetch()}>Retry</button>
          </p>
        )}
        {tab === "templates" && snapshot.canEditDocuments && (
          <section>
            <h3>Frozen handoff</h3>
            <p>
              Create a document referencing the exact versions currently placed
              on this canvas.{" "}
              {snapshot.artifacts.filter((row) => row.openComments > 0).length}{" "}
              artifacts have open comments.
            </p>
            <button
              className="pc-button"
              disabled={busy || !snapshot.items.some((row) => row.version)}
              onClick={() =>
                void act(() =>
                  execute({
                    type: "create_document",
                    title: title.trim() || "Project handoff",
                    content: {
                      type: "doc",
                      content: [
                        {
                          type: "heading",
                          attrs: { level: 1 },
                          content: [
                            {
                              type: "text",
                              text: title.trim() || "Project handoff",
                            },
                          ],
                        },
                        {
                          type: "paragraph",
                          content: [
                            {
                              type: "text",
                              text: "Exact version references captured from the canvas. Source access remains required. Review outstanding comments before release.",
                            },
                          ],
                        },
                        ...snapshot.items
                          .filter((row) => row.version)
                          .map((row) => ({
                            type: "artifactReference",
                            attrs: {
                              artifactId: row.artifactId,
                              versionId: row.version!.id,
                              blockTargetId: row.viewConfig.blockId || "",
                              label:
                                row.title ||
                                snapshot.artifacts.find(
                                  (a) => a.id === row.artifactId,
                                )?.title ||
                                "Deliverable",
                            },
                          })),
                      ],
                    },
                  }),
                )
              }
            >
              Create frozen handoff document
            </button>
          </section>
        )}
        {tab !== "removed" && snapshot.canEdit && (
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void act(() =>
                execute({
                  type: tab === "templates" ? "save_template" : "save_view",
                  title,
                  viewConfig: tab === "templates" ? template() : view,
                }),
              );
            }}
          >
            <label>
              Name
              <input
                required
                maxLength={240}
                value={title}
                onChange={(event) => setTitle(event.target.value)}
                placeholder={
                  tab === "templates"
                    ? "Reusable review layout"
                    : "My focused view"
                }
              />
            </label>
            <button
              className="pc-button"
              disabled={
                busy ||
                !title.trim() ||
                (tab === "templates" &&
                  (!snapshot.items.length || snapshot.items.length > 100))
              }
            >
              <Plus size={16} />
              {tab === "templates"
                ? "Save current canvas as template"
                : "Save my current view"}
            </button>
            {tab === "templates" && (
              <small>
                Up to 100 placements. Saved templates pin reviewed versions;
                live draft edits are not copied.
              </small>
            )}
          </form>
        )}
        {tab === "templates" && (
          <>
            <h3>Built-in layouts</h3>
            {[
              "Research → Synthesis → Decision",
              "Draft → Comments → Saved",
              "Idea → Experiment → Results",
              "Problem → Evidence → Options → Delivery plan",
              "Audience → Message → Concepts → Channel assets",
              "Deliverables → Decisions → Instructions → Open questions",
            ].map((name) => (
              <button
                className="pc-button"
                key={name}
                disabled={busy || !snapshot.canEdit}
                onClick={() =>
                  void act(async () => {
                    for (const [i, label] of name.split(" → ").entries())
                      await execute({
                        type: "frame",
                        title: label,
                        x: i * 650,
                        y: 0,
                        width: 600,
                        height: 500,
                      });
                  })
                }
              >
                <Layers size={16} />
                {name}
              </button>
            ))}
            <h3>Shared templates</h3>
            {query.data?.templates.map((row) => (
              <div className="pc-library-row" key={row.id}>
                <span>{row.title}</span>
                <button
                  disabled={busy || !snapshot.canEdit}
                  onClick={() =>
                    void act(() =>
                      execute({
                        type: "use_template",
                        id: row.id,
                        x: 80,
                        y: 80,
                      }),
                    )
                  }
                >
                  Use template
                </button>
                <button
                  aria-label={`Delete ${row.title}`}
                  disabled={busy || !snapshot.canEdit}
                  onClick={() =>
                    void act(() =>
                      execute({ type: "delete_template", id: row.id }),
                    )
                  }
                >
                  <Trash2 size={16} />
                </button>
              </div>
            ))}
            {!query.data?.templates.length && <p>No shared templates yet.</p>}
          </>
        )}
        {tab === "views" && (
          <>
            {query.data?.views.map((row) => (
              <div className="pc-library-row" key={row.id}>
                <button
                  onClick={() => {
                    onView(row.config);
                    onClose();
                  }}
                >
                  {row.title}
                </button>
                <button
                  disabled={busy || !snapshot.canEdit}
                  aria-label={`Delete ${row.title}`}
                  onClick={() =>
                    void act(() => execute({ type: "delete_view", id: row.id }))
                  }
                >
                  <Trash2 size={16} />
                </button>
              </div>
            ))}
            {!query.data?.views.length && (
              <p>
                Save a view to return to its filters, display and camera. These
                views are private to you.
              </p>
            )}
          </>
        )}
        {tab === "removed" && (
          <>
            <p>
              Restore a placement without changing the underlying artifact.
              Removed connections are not restored.
            </p>
            {query.data?.removed.map((row) => (
              <div className="pc-library-row" key={row.id}>
                <span>{row.title || "Canvas placement"}</span>
                <button
                  disabled={busy || !snapshot.canEdit}
                  onClick={() =>
                    void act(() =>
                      execute({
                        type: "restore_item",
                        id: row.id,
                        expectedRevision: row.revision,
                      }),
                    )
                  }
                >
                  <RotateCw size={16} /> Restore
                </button>
              </div>
            ))}
            {!query.data?.removed.length && (
              <p>No removed placements to recover.</p>
            )}
          </>
        )}
      </div>
    </CanvasDialog>
  );
}
