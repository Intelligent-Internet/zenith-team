import { useEffect, useRef, useState } from "react";
import {
  EditorContent,
  useEditor,
  type Editor,
  type JSONContent,
} from "@tiptap/react";
import StarterKit from "@tiptap/starter-kit";
import type { Node as ProseMirrorNode } from "@tiptap/pm/model";
import type { components } from "@/shared/api/generated/api.generated";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { BlockDocument } from "./block-editor-schema";
import { suggestionPreview } from "./inline-page-suggestion";
import {
  usePageChangeDetailQuery,
  useReviewPageChangeMutation,
  type PageScope,
} from "./page-api";

type Suggestion = components["schemas"]["PageSuggestion"];
type Detail = components["schemas"]["PageChangeDetail"];
type ReviewInput = components["schemas"]["PageChangeReviewInput"];

function currentBlock(editor: Editor | null | undefined, blockId: string) {
  let result: { node: ProseMirrorNode; from: number; to: number } | undefined;
  if (!editor || editor.isDestroyed) return result;
  editor.state.doc.descendants((node, from) => {
    if (node.attrs.blockId === blockId)
      result = { node, from, to: from + node.nodeSize };
  });
  return result;
}

function editableText(block: JSONContent | undefined): boolean {
  return (
    !!block &&
    ["paragraph", "heading", "codeBlock"].includes(block.type ?? "") &&
    (block.content ?? []).every(
      (node) =>
        ["text", "hardBreak"].includes(node.type ?? "") &&
        (node.marks ?? []).every((mark) =>
          ["bold", "italic", "strike", "code", "underline", "link"].includes(
            mark.type,
          ),
        ),
    )
  );
}

export function PageConflictEditor({
  initialMode,
  scope,
  detail,
  suggestion,
  editor,
  dirty,
  busy,
  onBusyChange,
  onClose,
  onEditPage,
}: {
  initialMode: "accept" | "merge";
  scope: PageScope;
  detail: Detail;
  suggestion: Suggestion;
  editor?: Editor | null;
  dirty: boolean;
  busy: boolean;
  onBusyChange: (id: string) => void;
  onClose: () => void;
  onEditPage?: () => void;
}) {
  const panel = useRef<HTMLElement>(null);
  useEffect(() => panel.current?.focus({ preventScroll: true }), []);
  const [choice, setChoice] = useState<{
    detail: Detail;
    suggestion: Suggestion;
  } | null>({ detail, suggestion });
  const [editing, setEditing] = useState(
    () =>
      initialMode === "merge" &&
      editableText(currentBlock(editor, suggestion.blockId)?.node.toJSON()) &&
      editableText(suggestion.after),
  );
  const [error, setError] = useState("");
  const [request, setRequest] = useState<{
    id: string;
    input: ReviewInput;
  } | null>(null);
  const [review, reviewing] = useReviewPageChangeMutation();
  const [original] = useState(
    () =>
      currentBlock(editor, suggestion.blockId)?.node.toJSON() as
        JSONContent | undefined,
  );
  const textEditable = editableText(original);
  const draft = useEditor({
    extensions: [StarterKit.configure({ link: { openOnClick: false } })],
    content: {
      type: "doc",
      content:
        editing && suggestion.after
          ? [suggestion.after]
          : textEditable && original
            ? [original]
            : [{ type: "paragraph" }],
    },
    immediatelyRender: false,
    editorProps: {
      attributes: { "aria-label": "Final content", spellcheck: "true" },
    },
  });
  const blocked = dirty || busy || reviewing.isLoading;
  const applyChoice = async (retry = false, keepCurrent = false) => {
    if (blocked || (!choice && !retry && !keepCurrent)) return;
    const next = retry
      ? request
      : {
          id: keepCurrent ? detail.id : choice!.detail.id,
          input: {
            requestKey: crypto.randomUUID(),
            fingerprint: keepCurrent
              ? detail.fingerprint
              : choice!.detail.fingerprint,
            action: keepCurrent ? ("reject" as const) : ("apply" as const),
            indexes: keepCurrent ? suggestion.group : choice!.suggestion.group,
          },
        };
    if (!next) return;
    setRequest(next);
    setError("");
    onBusyChange(next.id);
    try {
      await review({ ...scope, ...next }).unwrap();
      onClose();
    } catch {
      setError(
        "The page changed or the merge couldn’t be confirmed. Your selection is preserved.",
      );
    } finally {
      onBusyChange("");
    }
  };
  const applyEdit = () => {
    if (blocked || !draft || !editor || editor.isDestroyed) return;
    if (!editor.isEditable) {
      onEditPage?.();
      return;
    }
    const current = currentBlock(editor, suggestion.blockId);
    if (
      !current ||
      JSON.stringify(current.node.toJSON()) !== JSON.stringify(original)
    ) {
      setError(
        "This section changed while you were editing. Close and reopen the editor to review the latest content.",
      );
      return;
    }
    const content = draft.getJSON().content;
    if (
      content?.length !== 1 ||
      !["paragraph", "heading", "codeBlock"].includes(content[0].type ?? "")
    ) {
      setError(
        "Keep this edit within one text block. Use Edit page for changes spanning several blocks.",
      );
      return;
    }
    try {
      const replacement = editor.schema.nodeFromJSON({
        ...original,
        content: content[0].content ?? [],
      });
      replacement.check();
      editor.view.dispatch(
        editor.state.tr.replaceWith(current.from, current.to, replacement),
      );
      onClose();
    } catch {
      setError(
        "This edit cannot be applied to the current block. Use Edit page to make the change.",
      );
    }
  };
  return (
    <section
      className="pc-conflict-editor"
      ref={panel}
      tabIndex={-1}
      aria-label="Resolve competing suggestions"
    >
      <header>
        <strong>
          {initialMode === "merge" ? "Merge versions" : "Review this version"}
        </strong>
        <button type="button" disabled={busy} onClick={onClose}>
          Cancel
        </button>
      </header>
      <div
        className="pc-conflict-choices"
        role="group"
        aria-label="Choose a proposal"
      >
        <button
          type="button"
          aria-pressed={!choice && !editing}
          disabled={blocked || !!request}
          onClick={() => {
            setChoice(null);
            setEditing(false);
          }}
        >
          Keep current
        </button>
        {[
          detail.id,
          ...suggestion.competingChangeIds.filter((id) => id !== detail.id),
        ].map((id) => (
          <ConflictChoice
            key={id}
            id={id}
            scope={scope}
            blockId={suggestion.blockId}
            sourceId={detail.id}
            initial={id === detail.id ? { detail, suggestion } : undefined}
            selected={!editing && choice?.detail.id === id}
            disabled={blocked || !!request}
            onSelect={(next) => {
              setChoice(next);
              setEditing(false);
            }}
          />
        ))}
        {textEditable &&
          (!choice?.suggestion.after ||
            editableText(choice.suggestion.after)) && (
            <button
              type="button"
              aria-pressed={editing}
              disabled={blocked || !!request}
              onClick={() => {
                const proposed = choice?.suggestion.after ?? original;
                if (
                  proposed &&
                  ["paragraph", "heading", "codeBlock"].includes(
                    proposed.type ?? "",
                  )
                )
                  draft?.commands.setContent({
                    type: "doc",
                    content: [proposed],
                  });
                setEditing(true);
              }}
            >
              Edit result
            </button>
          )}
      </div>
      {editing ? (
        <div className="pc-conflict-draft">
          <EditorContent editor={draft} />
          <small>
            Your edit uses the page’s normal save flow. Other proposals remain
            available for review.
          </small>
        </div>
      ) : choice ? (
        <div className="pc-conflict-preview">
          {choice.detail.suggestions
            .filter((item) => choice.suggestion.group.includes(item.index))
            .map((item) => (
              <div key={item.index}>
                {item.after ? (
                  <BlockDocument
                    scope={scope}
                    content={{
                      type: "doc",
                      content: [suggestionPreview(item.after)],
                    }}
                    mode="comparison"
                  />
                ) : (
                  <p>
                    {item.type === "remove"
                      ? "Remove this content."
                      : item.type === "theme"
                        ? `Theme: ${choice.detail.beforeTheme} → ${choice.detail.afterTheme}`
                        : "Move this content to the proposed position."}
                  </p>
                )}
              </div>
            ))}
        </div>
      ) : original ? (
        <BlockDocument
          scope={scope}
          content={{
            type: "doc",
            content: [
              suggestionPreview(
                original as components["schemas"]["CanvasBlock"],
              ),
            ],
          }}
          mode="comparison"
        />
      ) : (
        <p>Keep the page as it is.</p>
      )}
      {error && (
        <p role="alert" className="pc-review-error">
          {error}
        </p>
      )}
      <footer>
        {onEditPage && (
          <button
            type="button"
            disabled={blocked}
            onClick={() => {
              onEditPage();
              onClose();
            }}
          >
            Edit page
          </button>
        )}
        {request && error ? (
          <button
            type="button"
            disabled={blocked}
            onClick={() => void applyChoice(true)}
          >
            Retry merge
          </button>
        ) : editing ? (
          <button
            type="button"
            className="pc-review-merge"
            disabled={blocked || (!editor?.isEditable && !onEditPage)}
            onClick={applyEdit}
          >
            {editor?.isEditable ? "Apply edit" : "Enable page editing"}
          </button>
        ) : choice ? (
          <button
            type="button"
            className="pc-review-merge"
            disabled={
              blocked ||
              !choice.suggestion.group.every((index) =>
                choice.detail.suggestions.some(
                  (item) => item.index === index && item.status === "pending",
                ),
              )
            }
            onClick={() => void applyChoice()}
          >
            Accept selected version
          </button>
        ) : (
          <button
            type="button"
            disabled={blocked}
            onClick={() => void applyChoice(false, true)}
          >
            Keep current · Reject this version
          </button>
        )}
      </footer>
    </section>
  );
}

function ConflictChoice({
  id,
  scope,
  blockId,
  sourceId,
  initial,
  selected,
  disabled,
  onSelect,
}: {
  id: string;
  scope: PageScope;
  blockId: string;
  sourceId: string;
  initial?: { detail: Detail; suggestion: Suggestion };
  selected: boolean;
  disabled: boolean;
  onSelect: (choice: { detail: Detail; suggestion: Suggestion }) => void;
}) {
  const query = usePageChangeDetailQuery({ ...scope, id }, { skip: !!initial });
  const detail = initial?.detail ?? query.currentData;
  const suggestion =
    initial?.suggestion ??
    detail?.suggestions.find(
      (item) =>
        (item.blockId === blockId ||
          item.competingChangeIds.includes(sourceId)) &&
        ["pending", "needs_update", "already_reflected"].includes(item.status),
    );
  if (query.isError)
    return (
      <button type="button" onClick={() => void query.refetch()}>
        Retry proposal
      </button>
    );
  return (
    <button
      type="button"
      disabled={disabled || !detail || !suggestion}
      aria-pressed={selected}
      onClick={() => detail && suggestion && onSelect({ detail, suggestion })}
    >
      {detail ? (
        <>
          <FoldAvatar
            agentId={detail.agentId || detail.agentName || "agent"}
            size={24}
            motion="still"
          />
          {detail.agentName || "Agent"}
          {suggestion?.status === "needs_update" ? " · Needs revision" : ""}
        </>
      ) : (
        "Loading…"
      )}
    </button>
  );
}
