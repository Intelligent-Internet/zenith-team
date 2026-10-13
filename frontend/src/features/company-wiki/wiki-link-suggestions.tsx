import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { type Editor } from "@tiptap/react";
import {
  ChevronRight,
  File,
  FileText,
  Image,
  LayoutDashboard,
  Monitor,
  type IconComponent,
} from "@/shared/ui/icons";
import type {
  ArtifactRunResult,
  ArtifactType,
  RunOutputItem,
} from "@/features/run-deliverables/model/run-deliverable";
import { useListWikiPagesQuery, type WikiPageSummary } from "./wiki-api";
import { useListCitableArtifactsQuery } from "./wiki-artifacts";

import "./wiki-link-suggestions.css";

type LinkMatch = { from: number; to: number; query: string };
type Scope = "all" | "pages" | "artifacts";
type Choice =
  | { kind: "page"; page: WikiPageSummary }
  | { kind: "artifact"; result: ArtifactRunResult }
  | { kind: "item"; result: ArtifactRunResult; item: RunOutputItem };

const scopes: Scope[] = ["all", "pages", "artifacts"];
const scopeLabels: Record<Scope, string> = {
  all: "All",
  pages: "Pages",
  artifacts: "Artifacts",
};
const typeIcons: Record<ArtifactType, IconComponent> = {
  app: Monitor,
  media: Image,
  text: FileText,
  file: File,
  whiteboard: LayoutDashboard,
};

// `[[a:` (or `[[artifact:`) narrows the picker to Artifacts.
const artifactPrefix = /^a(?:rtifact)?:/i;

function linkMatch(editor: Editor): LinkMatch | null {
  const { selection } = editor.state;
  const { $from } = selection;
  if (
    !selection.empty ||
    $from.parent.type.spec.code ||
    $from.marks().some((mark) => mark.type.spec.code)
  )
    return null;
  const before = $from.parent.textBetween(0, $from.parentOffset, "\n", "￼");
  const match = /\[\[([^\[\]\n￼]{0,200})$/.exec(before);
  return match
    ? {
        from: selection.from - match[0].length,
        to: selection.from,
        query: match[1],
      }
    : null;
}

function choiceKey(choice: Choice) {
  if (choice.kind === "page") return `page:${choice.page.id}`;
  if (choice.kind === "artifact")
    return `artifact:${choice.result.artifact.id}`;
  return `item:${choice.result.artifact.id}:${choice.item.ordinal}`;
}

function artifactMeta(result: ArtifactRunResult) {
  const count = result.artifact.items.length;
  return [
    result.artifact.type[0].toUpperCase() + result.artifact.type.slice(1),
    // A Whiteboard is one live Board, not a set of files.
    ...(result.artifact.type === "whiteboard"
      ? []
      : [`${count} item${count === 1 ? "" : "s"}`]),
    new Date(result.artifact.createdAt).toLocaleDateString(undefined, {
      dateStyle: "medium",
    }),
  ].join(" · ");
}

/**
 * Keep typing in the document while choosing a stable, ID-based page link or
 * an Artifact citation. Tab switches All / Pages / Artifacts; → opens an
 * Artifact's items so one of them can be cited.
 */
export function WikiLinkSuggestions({
  editor,
  companyId,
  pageId,
  children,
}: {
  editor: Editor | null;
  companyId: string;
  pageId: string;
  children: ReactNode;
}) {
  const listId = useId();
  const dismissedFrom = useRef<number | null>(null);
  const [match, setMatch] = useState<LinkMatch | null>(null);
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState(0);
  const [chosenScope, setChosenScope] = useState<Scope>("all");
  const [expanded, setExpanded] = useState<string | null>(null);
  const [position, setPosition] = useState({ left: 0, top: 0 });

  const prefixed = artifactPrefix.test(query);
  const scope: Scope = prefixed ? "artifacts" : chosenScope;
  const search = query.replace(artifactPrefix, "").trim();
  const pageChoices = useListWikiPagesQuery(
    { companyId, status: "active", search },
    { skip: !match || !companyId || scope === "artifacts" },
  );
  const artifactChoices = useListCitableArtifactsQuery(
    { companyId, search },
    { skip: !match || !companyId || scope === "pages" },
  );
  // Pages and Artifacts load and fail independently, so a slow or failing
  // Artifact list never hides page links (and the reverse).
  const stale = query !== match?.query;
  const pagesLoading =
    scope !== "artifacts" && (stale || pageChoices.isFetching);
  const artifactsLoading =
    scope !== "pages" && (stale || artifactChoices.isFetching);
  const pagesFailed =
    scope !== "artifacts" && !pagesLoading && pageChoices.isError;
  const artifactsFailed =
    scope !== "pages" && !artifactsLoading && artifactChoices.isError;
  const pending = pagesLoading || artifactsLoading;

  const pages: Choice[] =
    pagesLoading || pagesFailed
      ? []
      : (pageChoices.currentData?.items
          .filter((page) => page.id !== pageId)
          .map((page) => ({ kind: "page", page })) ?? []);
  const artifacts: Choice[] =
    artifactsLoading || artifactsFailed
      ? []
      : (artifactChoices.currentData?.items.map((result) => ({
          kind: "artifact",
          result,
        })) ?? []);
  const opened = artifactChoices.currentData?.items.find(
    (result) => result.artifact.id === expanded,
  );
  const items: Choice[] = opened
    ? [
        { kind: "artifact", result: opened },
        ...opened.artifact.items.map((item) => ({
          kind: "item" as const,
          result: opened,
          item,
        })),
      ]
    : scope === "pages"
      ? pages.slice(0, 8)
      : scope === "artifacts"
        ? artifacts.slice(0, 8)
        : [...pages.slice(0, 4), ...artifacts.slice(0, 5)];
  const activeIndex = Math.min(selected, Math.max(0, items.length - 1));
  const active = items[activeIndex];

  useEffect(() => {
    if (match && items.length) {
      document.getElementById(`${listId}-${activeIndex}`)?.scrollIntoView({
        block: "nearest",
      });
    }
  }, [match, listId, activeIndex, items.length]);

  useEffect(() => {
    if (!editor) return;
    const sync = () => {
      const next =
        editor.isEditable && editor.isFocused ? linkMatch(editor) : null;
      if (!next) dismissedFrom.current = null;
      setMatch((previous) => {
        if (!next || next.from === dismissedFrom.current) return null;
        return previous?.from === next.from &&
          previous.to === next.to &&
          previous.query === next.query
          ? previous
          : next;
      });
    };
    editor.on("transaction", sync).on("focus", sync).on("blur", sync);
    sync();
    return () => {
      editor.off("transaction", sync).off("focus", sync).off("blur", sync);
    };
  }, [editor]);

  useEffect(() => {
    setSelected(0);
    setExpanded(null);
    const timer = window.setTimeout(() => setQuery(match?.query ?? ""), 180);
    return () => window.clearTimeout(timer);
  }, [match?.query, match?.from]);

  // A new `[[` starts in All again.
  useEffect(() => {
    setChosenScope("all");
  }, [match?.from]);

  useEffect(() => {
    if (!editor || !match) return;
    const place = () => {
      const cursor = editor.view.coordsAtPos(editor.state.selection.from);
      setPosition({
        left: Math.max(8, Math.min(cursor.left, window.innerWidth - 388)),
        top:
          cursor.bottom + 340 > window.innerHeight
            ? Math.max(8, cursor.top - 340)
            : cursor.bottom + 6,
      });
    };
    place();
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    return () => {
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
    };
  }, [editor, match]);

  useEffect(() => {
    if (!editor) return;
    const dom = editor.view.dom;
    if (match) {
      dom.setAttribute("aria-controls", listId);
      dom.setAttribute("aria-autocomplete", "list");
      if (items.length)
        dom.setAttribute("aria-activedescendant", `${listId}-${activeIndex}`);
      else dom.removeAttribute("aria-activedescendant");
    }
    return () => {
      dom.removeAttribute("aria-controls");
      dom.removeAttribute("aria-autocomplete");
      dom.removeAttribute("aria-activedescendant");
    };
  }, [editor, match, listId, activeIndex, items.length]);

  const pick = (choice: Choice) => {
    if (!editor?.isEditable || !match) return;
    // Recheck the cursor range before insertion; a late response must not replace other text.
    const current = linkMatch(editor);
    if (
      !current ||
      current.from !== match.from ||
      current.query !== match.query
    )
      return;
    const after = editor.state.doc.textBetween(
      current.to,
      Math.min(editor.state.doc.content.size, current.to + 2),
    );
    const node =
      choice.kind === "page"
        ? {
            type: "wikiPageLink",
            attrs: { id: choice.page.id, label: choice.page.title },
          }
        : {
            type: "wikiArtifactRef",
            attrs: {
              id: choice.result.artifact.id,
              ordinal: choice.kind === "item" ? choice.item.ordinal : null,
            },
          };
    editor
      .chain()
      .focus()
      .insertContentAt(
        { from: current.from, to: current.to + (after === "]]" ? 2 : 0) },
        node,
      )
      .run();
    setMatch(null);
  };

  const stop = (event: React.KeyboardEvent) => {
    event.preventDefault();
    event.stopPropagation();
  };

  return (
    <div
      onKeyDownCapture={(event) => {
        if (
          !editor?.isEditable ||
          event.nativeEvent.isComposing ||
          editor.view.composing ||
          !editor.view.dom.contains(event.target as Node)
        )
          return;
        if (
          (event.metaKey || event.ctrlKey) &&
          event.shiftKey &&
          event.key.toLowerCase() === "k"
        ) {
          stop(event);
          if (editor.isActive("codeBlock") || editor.isActive("code")) return;
          dismissedFrom.current = null;
          editor.chain().focus().insertContent("[[").run();
          return;
        }
        if (!match) return;
        if (event.key === "Escape") {
          stop(event);
          if (expanded) {
            setExpanded(null);
            return;
          }
          dismissedFrom.current = match.from;
          setMatch(null);
        } else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
          stop(event);
          if (items.length)
            setSelected(
              (activeIndex +
                (event.key === "ArrowDown" ? 1 : -1) +
                items.length) %
                items.length,
            );
        } else if (
          event.key === "ArrowRight" &&
          !expanded &&
          active?.kind === "artifact" &&
          active.result.artifact.items.length > 1
        ) {
          stop(event);
          setExpanded(active.result.artifact.id);
          setSelected(0);
        } else if (event.key === "ArrowLeft" && expanded) {
          stop(event);
          const index = artifacts.findIndex(
            (choice) =>
              choice.kind === "artifact" &&
              choice.result.artifact.id === expanded,
          );
          setExpanded(null);
          setSelected(
            Math.max(
              0,
              index + (scope === "all" ? Math.min(4, pages.length) : 0),
            ),
          );
        } else if (event.key === "Tab") {
          // Tab never leaves the picker (or indents a list item) while it is
          // open; it switches scope where that is possible.
          stop(event);
          if (expanded || prefixed) return;
          setSelected(0);
          setChosenScope(
            (current) =>
              scopes[
                (scopes.indexOf(current) + (event.shiftKey ? 2 : 1)) %
                  scopes.length
              ],
          );
        } else if (event.key === "Enter" && (pending || items.length)) {
          stop(event);
          if (active) pick(active);
        }
      }}
    >
      {children}
      {match && (
        <div
          className="wk-link-suggestions"
          style={position}
          onMouseDown={(event) => event.preventDefault()}
        >
          <div className="wk-link-suggestions__heading">
            {opened ? (
              <span className="block overflow-hidden text-ellipsis whitespace-nowrap">
                {opened.artifact.title}
              </span>
            ) : (
              <div
                className="wk-link-suggestions__scopes flex gap-[2px]"
                role="tablist"
                aria-label="Show"
              >
                {scopes.map((option) => (
                  <button
                    type="button"
                    role="tab"
                    key={option}
                    tabIndex={-1}
                    aria-selected={scope === option}
                    disabled={prefixed && option !== "artifacts"}
                    onClick={() => {
                      setSelected(0);
                      setChosenScope(option);
                    }}
                  >
                    {scopeLabels[option]}
                  </button>
                ))}
              </div>
            )}
          </div>
          <div
            role="listbox"
            aria-label={opened ? "Artifact items" : "Pages and artifacts"}
            id={listId}
            aria-busy={pending}
          >
            {items.map((choice, index) => {
              const Icon =
                choice.kind === "page"
                  ? FileText
                  : typeIcons[choice.result.artifact.type];
              return (
                <button
                  type="button"
                  role="option"
                  aria-selected={index === activeIndex}
                  id={`${listId}-${index}`}
                  key={choiceKey(choice)}
                  tabIndex={-1}
                  className={choice.kind === "item" ? "is-item" : undefined}
                  onClick={() => pick(choice)}
                >
                  <Icon size={16} />
                  {choice.kind === "page" ? (
                    <span>{choice.page.title}</span>
                  ) : choice.kind === "item" ? (
                    <span className="wk-link-suggestions__text">
                      <span>{choice.item.displayName}</span>
                      <small>
                        Item {choice.item.ordinal + 1} · {choice.item.role}
                      </small>
                    </span>
                  ) : (
                    <span className="wk-link-suggestions__text">
                      <span>
                        {opened
                          ? "Whole artifact"
                          : choice.result.artifact.title}
                      </span>
                      <small>{artifactMeta(choice.result)}</small>
                    </span>
                  )}
                  {!opened &&
                    choice.kind === "artifact" &&
                    choice.result.artifact.items.length > 1 && (
                      <ChevronRight
                        size={14}
                        aria-label="Has items"
                        className="wk-link-suggestions__more"
                      />
                    )}
                </button>
              );
            })}
          </div>
          {(pagesFailed || artifactsFailed) && (
            <p role="alert">
              {pagesFailed && artifactsFailed
                ? "Results could not be loaded."
                : pagesFailed
                  ? "Pages could not be loaded."
                  : "Artifacts could not be loaded."}{" "}
              <button
                type="button"
                onClick={() => {
                  if (pagesFailed) void pageChoices.refetch();
                  if (artifactsFailed) void artifactChoices.refetch();
                }}
              >
                Retry
              </button>
            </p>
          )}
          {pending ? (
            <p role="status">Searching…</p>
          ) : pagesFailed || artifactsFailed ? null : items.length === 0 ? (
            <p role="status">
              {scope === "artifacts"
                ? "No artifacts you can open match."
                : scope === "pages"
                  ? "No matching pages."
                  : "No matching pages or artifacts."}
            </p>
          ) : null}
          <div className="wk-link-suggestions__hint">
            {opened
              ? "↑ ↓ choose · Enter cite · ← back · Esc close"
              : prefixed
                ? "↑ ↓ choose · → items · Enter cite · Esc close"
                : "↑ ↓ choose · → items · Tab Pages/Artifacts · Enter insert · Esc close"}
          </div>
        </div>
      )}
    </div>
  );
}
