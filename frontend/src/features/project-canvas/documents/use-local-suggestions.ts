import { useEffect, useState } from "react";
import type { Editor } from "@tiptap/react";
import { ySyncPluginKey, yDocToProsemirrorJSON } from "@tiptap/y-tiptap";
import * as Y from "yjs";
import type { components } from "@/shared/api/generated/api.generated";
import type { Block } from "./block-editor-schema";
import { decodeUpdate } from "./document-update";

type Suggestion = components["schemas"]["PageSuggestion"];
function findBlock(root: Block, id: string): Block | undefined {
  if (root.attrs?.blockId === id) return root;
  for (const child of root.content ?? []) {
    const found = findBlock(child, id);
    if (found) return found;
  }
}
function sameContent(a: Block | undefined | null, b: Block | undefined | null) {
  return (
    !!a &&
    !!b &&
    JSON.stringify(a.content) === JSON.stringify(b.content) &&
    a.type === b.type
  );
}

// Speculative updates only ever enter disposable clones. The live document keeps
// receiving human/accepted edits, and no preview is attached to its sync provider.
export function useLocalSuggestions(
  editor: Editor | null | undefined,
  suggestions: Suggestion[] | undefined,
) {
  const [preview, setPreview] = useState<Suggestion[] | undefined>();
  useEffect(() => {
    if (!editor || editor.isDestroyed || !suggestions) {
      setPreview(suggestions);
      return;
    }
    const live = ySyncPluginKey.getState(editor.state)?.doc as
      Y.Doc | undefined;
    if (!live) {
      setPreview(suggestions);
      return;
    }
    let frame = 0;
    const refresh = () => {
      const saved = Y.encodeStateAsUpdate(live);
      const current = yDocToProsemirrorJSON(live, "content") as Block;
      setPreview(
        suggestions.map((suggestion) => {
          if (!suggestion.crdtUpdate || suggestion.status !== "pending")
            return suggestion;
          const target = findBlock(current, suggestion.blockId);
          if (!target)
            return {
              ...suggestion,
              status: "needs_update",
              current: undefined,
            };
          // Independently authored identical edits must not replay an insertion.
          if (sameContent(target, suggestion.after))
            return { ...suggestion, current: target };
          const branch = new Y.Doc();
          try {
            Y.applyUpdate(branch, saved);
            Y.applyUpdate(branch, decodeUpdate(suggestion.crdtUpdate));
            const after = findBlock(
              yDocToProsemirrorJSON(branch, "content") as Block,
              suggestion.blockId,
            );
            if (!after || sameContent(after, target))
              return { ...suggestion, current: target, status: "needs_update" };
            return { ...suggestion, current: target, after };
          } finally {
            branch.destroy();
          }
        }),
      );
    };
    const schedule = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(refresh);
    };
    refresh();
    live.on("update", schedule);
    return () => {
      cancelAnimationFrame(frame);
      live.off("update", schedule);
    };
  }, [editor, suggestions]);
  return preview ?? suggestions ?? [];
}
