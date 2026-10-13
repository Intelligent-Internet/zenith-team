import { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { Editor } from "@tiptap/react";
import { Plugin, PluginKey } from "@tiptap/pm/state";
import { FileText } from "@/shared/ui/icons";
import { useGetCanvasQuery, type CanvasScope } from "../api/canvas-api";
import "./document-mentions.css";

type MentionRange = { from: number; to: number; query: string };

function mentionAtCursor(editor: Editor): MentionRange | null {
  const { selection } = editor.state;
  if (
    !editor.isEditable ||
    !selection.empty ||
    editor.view.composing ||
    editor.isActive("codeBlock") ||
    editor.isActive("code") ||
    editor.isActive("mermaid") ||
    editor.isActive("link")
  )
    return null;
  const text = selection.$from.parent.textBetween(
    0,
    selection.$from.parentOffset,
    "\0",
    "\0",
  );
  // A boundary keeps email addresses and mid-word @ signs as ordinary text.
  const match =
    /(?:^|[\s(])(@([^@\[\]\n\0]{0,80}))$|\[\[([^\[\]\n\0]{0,80})$/.exec(text);
  if (!match) return null;
  const query = match[2] ?? match[3];
  return {
    from: selection.from - query.length - (match[1] ? 1 : 2),
    to: selection.from,
    query,
  };
}

export function DocumentMentions({
  editor,
  scope,
}: {
  editor: Editor;
  scope: CanvasScope;
}) {
  const id = useId();
  const [range, setRange] = useState<MentionRange | null>(null);
  const [choice, setChoice] = useState(0);
  const [position, setPosition] = useState({ top: 0, left: 0 });
  const dismissed = useRef<number | null>(null);
  const menu = useRef<HTMLDivElement>(null);
  const query = useGetCanvasQuery(scope, { skip: !range });
  const search = range?.query.trim().toLocaleLowerCase() ?? "";
  const documents = (query.isError ? [] : (query.currentData?.artifacts ?? []))
    .filter(
      (artifact) =>
        artifact.latestVersion.sourceType === "wiki" &&
        artifact.title.toLocaleLowerCase().includes(search),
    )
    .sort(
      (a, b) =>
        Number(b.title.toLocaleLowerCase().startsWith(search)) -
          Number(a.title.toLocaleLowerCase().startsWith(search)) ||
        a.title.localeCompare(b.title) ||
        a.id.localeCompare(b.id),
    )
    .slice(0, 20);
  const selected = Math.min(choice, Math.max(0, documents.length - 1));
  const dismiss = () => {
    dismissed.current = range?.from ?? null;
    setRange(null);
  };
  const pick = (index: number) => {
    const document = documents[index];
    const current = mentionAtCursor(editor);
    if (
      !document ||
      !range ||
      !current ||
      current.from !== range.from ||
      current.to !== range.to ||
      current.query !== range.query
    )
      return;
    const pageId = document.latestVersion.sourceOutputId
      ? null
      : document.pageId;
    const href = new URL(
      `/projects/${scope.projectId}/${pageId ? "pages" : "artifacts"}`,
      window.location.origin,
    );
    href.searchParams.set(
      pageId ? "pageId" : "artifactId",
      pageId || document.id,
    );
    const following = editor.state.selection.$from.parent.textBetween(
      editor.state.selection.$from.parentOffset,
      editor.state.selection.$from.parentOffset +
        Math.min(
          2,
          editor.state.selection.$from.parent.content.size -
            editor.state.selection.$from.parentOffset,
        ),
    );
    const brackets =
      editor.state.doc.textBetween(range.from, range.from + 2) === "[[";
    setRange(null);
    editor
      .chain()
      .focus()
      .insertContentAt(
        {
          from: range.from,
          to: range.to + (brackets && following === "]]" ? 2 : 0),
        },
        [
          {
            type: "text",
            text: document.title,
            marks: [{ type: "link", attrs: { href: href.href } }],
          },
          { type: "text", text: " ", marks: [] },
        ],
      )
      .unsetLink()
      .run();
  };
  const actions = useRef({ range, documents, selected, pick, dismiss });
  actions.current = { range, documents, selected, pick, dismiss };

  useEffect(() => {
    const update = () => {
      const next = mentionAtCursor(editor);
      if (!next) dismissed.current = null;
      setRange((previous) => {
        if (next?.from === dismissed.current) return null;
        if (
          previous?.from === next?.from &&
          previous?.to === next?.to &&
          previous?.query === next?.query
        )
          return previous;
        return next;
      });
    };
    const blur = () => actions.current.dismiss();
    const key = new PluginKey("documentMentions");
    editor.registerPlugin(
      new Plugin({
        key,
        props: {
          handleKeyDown: (_view, event) => {
            const current = actions.current;
            if (!current.range || event.isComposing) return false;
            if (event.key === "Escape") {
              current.dismiss();
              return true;
            }
            if (event.key === "ArrowDown" || event.key === "ArrowUp") {
              setChoice(
                (value) =>
                  (value +
                    (event.key === "ArrowDown" ? 1 : -1) +
                    current.documents.length) %
                  Math.max(1, current.documents.length),
              );
              return true;
            }
            if (
              (event.key === "Enter" || event.key === "Tab") &&
              current.documents.length
            ) {
              current.pick(current.selected);
              return true;
            }
            return false;
          },
        },
      }),
    );
    editor.on("update", update);
    editor.on("selectionUpdate", update);
    editor.on("blur", blur);
    return () => {
      editor.unregisterPlugin(key);
      editor.off("update", update);
      editor.off("selectionUpdate", update);
      editor.off("blur", blur);
    };
  }, [editor]);

  useEffect(() => {
    setChoice(0);
  }, [range?.query, range?.from]);
  useEffect(() => {
    if (!range) return;
    const reposition = () => {
      const cursor = editor.view.coordsAtPos(editor.state.selection.from);
      const width = Math.min(320, window.innerWidth - 24);
      const height = Math.min(
        menu.current?.offsetHeight ?? 300,
        window.innerHeight - 24,
      );
      setPosition({
        left: Math.max(
          12,
          Math.min(cursor.left, window.innerWidth - width - 12),
        ),
        top: Math.max(
          12,
          Math.min(
            cursor.bottom + height + 8 <= window.innerHeight - 12
              ? cursor.bottom + 8
              : cursor.top - height - 8,
            window.innerHeight - height - 12,
          ),
        ),
      });
    };
    reposition();
    window.addEventListener("scroll", reposition, true);
    window.addEventListener("resize", reposition);
    return () => {
      window.removeEventListener("scroll", reposition, true);
      window.removeEventListener("resize", reposition);
    };
  }, [editor, range, documents.length, query.isFetching, query.isError]);
  useEffect(() => {
    const dom = editor.view.dom;
    if (range) {
      dom.setAttribute("aria-controls", id);
      dom.setAttribute("aria-haspopup", "listbox");
      if (documents.length)
        dom.setAttribute("aria-activedescendant", `${id}-${selected}`);
      else dom.removeAttribute("aria-activedescendant");
      menu.current
        ?.querySelector('[aria-selected="true"]')
        ?.scrollIntoView({ block: "nearest" });
    }
    return () => {
      dom.removeAttribute("aria-controls");
      dom.removeAttribute("aria-haspopup");
      dom.removeAttribute("aria-activedescendant");
    };
  }, [editor, range, id, selected, documents.length]);

  if (!range) return null;
  return createPortal(
    <div
      className="pc-document-mentions fixed z-[100] w-[min(320px,_calc(100vw_-_24px))] max-h-[calc(100vh_-_24px)] overflow-auto p-[6px] [border:1px_solid_var(--workspace-divider,_var(--border))] rounded-[10px] [background:var(--workspace-overlay-bg,_var(--popover))] text-[var(--workspace-native-text,_var(--foreground))] [box-shadow:var(--workspace-overlay-shadow,_0_8px_30px_#0002)] text-[length:13px]"
      style={position}
      ref={menu}
      onMouseDown={(event) => event.preventDefault()}
    >
      <div className="pc-mention-heading">
        Link a document <span>This project</span>
      </div>
      <div id={id} role="listbox" aria-label="Documents">
        {query.isError ? (
          <p role="status">
            Couldn’t load documents.{" "}
            <button onClick={() => void query.refetch()}>Retry</button>
          </p>
        ) : documents.length ? (
          documents.map((document, index) => (
            <button
              key={document.id}
              id={`${id}-${index}`}
              type="button"
              role="option"
              aria-selected={selected === index}
              tabIndex={-1}
              onPointerMove={() => setChoice(index)}
              onClick={() => pick(index)}
            >
              <FileText size={16} aria-hidden="true" />
              <span>{document.title}</span>
            </button>
          ))
        ) : (
          <p role="status">
            {query.isFetching ? "Loading documents…" : "No matching documents"}
          </p>
        )}
      </div>
      <div className="pc-mention-footer">
        Arrow keys to choose · Enter to link · Esc to close
      </div>
    </div>,
    document.body,
  );
}
