import { useState } from "react";
import type { Editor, JSONContent } from "@tiptap/react";
import { useNavigate } from "react-router";
import type { components } from "@/shared/api/generated/api.generated";
import { Icon } from "@/shared/ui/icon";
import {
  useGetCanvasQuery,
  useCanvasCommandMutation,
  type CanvasScope,
} from "../api/canvas-api";
import { CanvasDialog } from "../canvas/canvas-dialog";

export type SourceState = components["schemas"]["PageSourceState"];

export function DocumentBlockDetails({
  editor,
  scope,
  blockId,
  sources,
  onClose,
}: {
  editor: Editor;
  scope: CanvasScope;
  blockId: string;
  sources: SourceState[];
  onClose: () => void;
}) {
  const board = useGetCanvasQuery(scope);
  const navigate = useNavigate();
  let node: JSONContent | undefined;
  let position = 0;
  editor.state.doc.descendants((current, pos) => {
    if (current.attrs.blockId === blockId) {
      node = current.toJSON();
      position = pos;
      return false;
    }
  });
  if (!node)
    return (
      <aside className="pc-block-details w-[min(420px,_100%)] m-[24px_auto] p-[20px] [background:var(--pc-paper)] [border:1px_solid_var(--pc-line)] rounded-[10px] text-[var(--pc-ink)] text-[length:13px]">
        <p>This content was removed.</p>
        <button onClick={onClose}>Close</button>
      </aside>
    );
  const selected = (node.attrs?.sourceVersionIds ?? []) as string[];
  const update = (ids: string[]) => {
    editor.view.dispatch(
      editor.state.tr.setNodeMarkup(position, undefined, {
        ...node?.attrs,
        sourceVersionIds: ids,
      }),
    );
  };
  return (
    <aside
      className="pc-block-details w-[min(420px,_100%)] m-[24px_auto] p-[20px] [background:var(--pc-paper)] [border:1px_solid_var(--pc-line)] rounded-[10px] text-[var(--pc-ink)] text-[length:13px]"
      aria-label="Sources and editing permissions"
    >
      <header>
        <strong>Sources and permissions</strong>
        <button onClick={onClose} aria-label="Close details">
          <Icon name="close-circle" className="size-4" />
        </button>
      </header>
      <small>
        {node.type === "heading"
          ? "Heading and its following section"
          : "Selected content"}
      </small>
      <p>
        Declare the exact evidence used for this writing. New source versions
        flag it for review and preserve your words.
      </p>
      <label>
        <input
          type="checkbox"
          checked={!!node.attrs?.agentProtected}
          disabled={!editor.isEditable}
          onChange={(event) =>
            editor.view.dispatch(
              editor.state.tr.setNodeMarkup(position, undefined, {
                ...node?.attrs,
                agentProtected: event.target.checked,
              }),
            )
          }
        />{" "}
        Protect from direct Agent edits
      </label>
      {selected.map((id) => {
        const state = sources.find((source) => source.versionId === id);
        const current = board.currentData?.artifacts.find(
          (artifact) => artifact.latestVersionId === id,
        );
        const name = state?.title || current?.title || "Exact source version";
        const status = state?.status ?? (current ? "current" : "unknown");
        return (
          <div
            className="pc-source-detail flex gap-[8px] flex-wrap [border-top:1px_solid_var(--pc-line)] p-[12px_0]"
            key={id}
          >
            <span>
              <strong>{name}</strong>
              <small>
                {status === "needs_refresh"
                  ? "Needs refresh · newer evidence available"
                  : status === "unavailable"
                    ? "Source unavailable with your access"
                    : status === "current"
                      ? "Current source version"
                      : "Version retained · open the Overview to check freshness"}
              </small>
            </span>
            {(state?.artifactId || current?.id) && (
              <button
                onClick={() =>
                  navigate(
                    `/projects/${scope.projectId}/artifacts?view=canvas&artifactId=${state?.artifactId || current?.id}&versionId=${id}`,
                  )
                }
              >
                Open source
              </button>
            )}
            {editor.isEditable && (
              <button
                aria-label={`Remove source ${name}`}
                onClick={() =>
                  update(selected.filter((source) => source !== id))
                }
              >
                Remove
              </button>
            )}
            {editor.isEditable &&
              state?.status === "needs_refresh" &&
              state.currentVersionId && (
                <button
                  onClick={() =>
                    update(
                      selected.map((source) =>
                        source === id ? state.currentVersionId! : source,
                      ),
                    )
                  }
                >
                  I reviewed the current evidence
                </button>
              )}
          </div>
        );
      })}
      {editor.isEditable && (
        <label>
          Add evidence
          <select
            aria-label="Declare source evidence"
            value=""
            disabled={selected.length >= 16 || board.isFetching}
            onChange={(event) => {
              if (event.target.value) update([...selected, event.target.value]);
            }}
          >
            <option value="">Choose a Page, file or Artifact…</option>
            {board.currentData?.artifacts
              .filter(
                (artifact) => !selected.includes(artifact.latestVersionId),
              )
              .map((artifact) => (
                <option key={artifact.id} value={artifact.latestVersionId}>
                  {artifact.title} · v{artifact.latestVersion.number}
                </option>
              ))}
          </select>
        </label>
      )}
      {board.isError && (
        <p role="alert">
          Sources could not be loaded.{" "}
          <button onClick={() => void board.refetch()}>Retry</button>
        </p>
      )}
      <small>
        Only declared sources are checked. This does not verify untracked
        claims.
      </small>
    </aside>
  );
}

function sectionAt(editor: Editor, blockId: string) {
  const nodes: JSONContent[] = [];
  let start = -1,
    end = -1,
    level = 0,
    headingSize = 0;
  editor.state.doc.forEach((node, pos) => {
    if (end >= 0) return;
    if (start < 0) {
      if (node.attrs.blockId !== blockId || node.type.name !== "heading")
        return;
      start = pos;
      level = Number(node.attrs.level);
      headingSize = node.nodeSize;
    } else if (
      node.type.name === "heading" &&
      Number(node.attrs.level) <= level
    ) {
      end = pos;
      return;
    }
    nodes.push(node.toJSON());
  });
  return start < 0
    ? null
    : {
        start,
        end: end < 0 ? editor.state.doc.content.size : end,
        headingSize,
        nodes,
      };
}

export function PromoteSectionDialog({
  editor,
  scope,
  versionId,
  blockId,
  flush,
  onClose,
  onNotice,
}: {
  editor: Editor;
  scope: CanvasScope;
  versionId: string;
  blockId: string;
  flush: () => Promise<boolean>;
  onClose: () => void;
  onNotice: (message: string) => void;
}) {
  const [section] = useState(() => sectionAt(editor, blockId));
  const [title, setTitle] = useState(
    () =>
      section?.nodes[0].content?.map((node) => node.text ?? "").join("") ||
      "Supporting Page",
  );
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [command] = useCanvasCommandMutation();
  const board = useGetCanvasQuery(scope);
  const [requestKey] = useState(() => crypto.randomUUID());
  async function promote() {
    if (!section || !title.trim() || busy) return;
    setBusy(true);
    setError("");
    try {
      if (!(await flush()))
        throw new Error("Save your latest changes before creating the Page.");
      const current = sectionAt(editor, blockId);
      if (
        !current ||
        JSON.stringify(current.nodes) !== JSON.stringify(section.nodes)
      )
        throw new Error(
          "This section changed. Close this dialog and review the current section first.",
        );
      const content: JSONContent = {
        type: "doc",
        content: structuredClone(section.nodes.slice(1)),
      };
      if (!content.content?.length) content.content = [{ type: "paragraph" }];
      function clearIds(node: JSONContent) {
        if (node.attrs) delete node.attrs.blockId;
        node.content?.forEach(clearIds);
      }
      clearIds(content);
      const result = await command({
        ...scope,
        command: {
          requestKey,
          type: "create_document",
          title: title.trim(),
          versionId,
          content: content as components["schemas"]["CanvasBlock"],
        },
      }).unwrap();
      const snapshot = await board.refetch().unwrap();
      const page = snapshot.artifacts.find(
        (artifact) => artifact.latestVersionId === result.id,
      );
      const latest = sectionAt(editor, blockId);
      if (
        !editor.isEditable ||
        !latest ||
        JSON.stringify(latest.nodes) !== JSON.stringify(section.nodes)
      ) {
        onNotice(
          `“${title.trim()}” was created in Pages. The section changed, so its content has been kept. Add the Page reference when ready.`,
        );
      } else {
        editor
          .chain()
          .focus()
          .insertContentAt(
            { from: latest.start + latest.headingSize, to: latest.end },
            {
              type: "artifactReference",
              attrs: {
                artifactId: page?.id ?? "",
                versionId: result.id,
                label: title.trim(),
                display: "card",
                format: "pinned",
              },
            },
          )
          .run();
      }
      onClose();
    } catch (reason) {
      setError(
        reason instanceof Error
          ? reason.message
          : "The Page could not be created. Your section is still here.",
      );
    } finally {
      setBusy(false);
    }
  }
  return (
    <CanvasDialog
      title="Turn section into a Page"
      description="The Page gets its own identity and collaboration. This heading stays here with a reference to the new Page."
      onClose={onClose}
      canClose={!busy}
    >
      <form
        className="pc-promotion-form p-[24px] leading-[1.6]"
        onSubmit={(event) => {
          event.preventDefault();
          void promote();
        }}
      >
        <label>
          Page title
          <input
            value={title}
            maxLength={240}
            autoFocus
            onChange={(event) => setTitle(event.target.value)}
          />
        </label>
        <p>
          {Math.max(0, (section?.nodes.length ?? 1) - 1)} sections will move
          into the new Page.
        </p>
        {error && <p role="alert">{error}</p>}
        <button disabled={busy || !section || !title.trim()}>
          {busy ? "Creating Page…" : "Create Page and insert reference"}
        </button>
      </form>
    </CanvasDialog>
  );
}
