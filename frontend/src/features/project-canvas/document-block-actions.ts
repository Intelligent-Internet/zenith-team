import type { Editor, JSONContent } from "@tiptap/react";
import { NodeSelection } from "@tiptap/pm/state";
import type { ReviewAnchor } from "./annotation-overlay";

export function documentBlockActions(
  editor: Editor,
  options: {
    onComment?: (anchor: ReviewAnchor) => void;
    onError: (message: string) => void;
  },
) {
  const top = () => {
    const selection = editor.state.selection;
    const pos = selection.$from;
    if (pos.depth === 0 && selection instanceof NodeSelection)
      return {
        node: selection.node,
        start: selection.from,
        index: pos.index(0),
      };
    if (pos.depth < 1) return null;
    return { node: pos.node(1), start: pos.before(1), index: pos.index(0) };
  };
  const selectBlock = (blockId?: string) => {
    let from: number | undefined;
    editor.state.doc.forEach((node, pos) => {
      if (node.attrs.blockId === blockId) from = pos;
    });
    if (from === undefined) return false;
    editor.commands.setNodeSelection(from);
    return true;
  };
  const selectedBlockId = (): string | undefined => {
    const selection = editor.state.selection;
    if (selection instanceof NodeSelection) return selection.node.attrs.blockId;
    for (let depth = selection.$from.depth; depth > 0; depth--) {
      const id = selection.$from.node(depth).attrs.blockId;
      if (id) return id;
    }
  };
  const selectionAnchor = (): ReviewAnchor | undefined => {
    const selection = editor.state.selection;
    if (selection.empty || selection instanceof NodeSelection) {
      const blockId = selectedBlockId();
      return blockId ? { type: "block", blockId } : { type: "version" };
    }
    const exact = editor.state.doc
      .textBetween(selection.from, selection.to, " ")
      .trim();
    if (!exact || exact.length > 1000) {
      options.onError("Select up to 1,000 characters.");
      return;
    }
    const depth = selection.$from.sharedDepth(selection.to);
    let blockId: string | undefined;
    for (let current = depth; current > 0; current--) {
      const id = selection.$from.node(current).attrs.blockId;
      if (id) {
        blockId = id;
        break;
      }
    }
    return { type: "textQuote", exact, blockId };
  };
  const commentSelection = () => {
    if (!options.onComment) return;
    const anchor = selectionAnchor();
    if (anchor) options.onComment(anchor);
  };
  const blockAction = (
    blockId: string,
    action: "up" | "down" | "duplicate" | "delete",
  ) => {
    if (!editor.isEditable || !selectBlock(blockId)) return;
    const current = top();
    if (!current) return;
    const { node, start, index } = current;
    const end = start + node.nodeSize;
    const tr = editor.state.tr;
    if (action === "delete") {
      editor.commands.deleteRange({ from: start, to: end });
      return;
    }
    if (action === "duplicate") {
      const clone = node.toJSON() as JSONContent;
      const clear = (n: JSONContent) => {
        if (n.attrs) delete n.attrs.blockId;
        n.content?.forEach(clear);
      };
      clear(clone);
      editor.commands.insertContentAt(end, clone);
      return;
    }
    if (action === "up" && index > 0) {
      const previous = editor.state.doc.child(index - 1);
      tr.delete(start, end).insert(start - previous.nodeSize, node);
    } else if (action === "down" && index < editor.state.doc.childCount - 1) {
      const next = editor.state.doc.child(index + 1);
      tr.delete(start, end).insert(start + next.nodeSize, node);
    }
    if (tr.docChanged) editor.view.dispatch(tr);
  };

  const moveBlock = (from: number, destination: number) => {
    if (!editor.isEditable) return;
    const node = editor.state.doc.nodeAt(from);
    if (!node || (destination >= from && destination <= from + node.nodeSize))
      return;
    editor.view.dispatch(
      editor.state.tr
        .delete(from, from + node.nodeSize)
        .insert(destination - (from < destination ? node.nodeSize : 0), node),
    );
  };
  const dropBlock = (id: string, point: { left: number; top: number }) => {
    if (!editor.isEditable) return;
    const hit = editor.view.posAtCoords({
      left: point.left,
      top: point.top,
    });
    if (!hit) return;
    const target = editor.state.doc.resolve(hit.pos);
    let source: { from: number; size: number } | undefined;
    editor.state.doc.forEach((node, from) => {
      if (node.attrs.blockId === id) source = { from, size: node.nodeSize };
    });
    if (!source) return;
    let destination = target.depth > 0 ? target.before(1) : target.pos;
    const targetElement = editor.view.nodeDOM(destination);
    if (target.depth > 0 && targetElement instanceof HTMLElement) {
      const rect = targetElement.getBoundingClientRect();
      if (point.top >= rect.top + rect.height / 2)
        destination += target.node(1).nodeSize;
    }
    if (destination >= source.from && destination <= source.from + source.size)
      return;
    const node = editor.state.doc.nodeAt(source.from);
    if (!node) return;
    editor.view.dispatch(
      editor.state.tr
        .delete(source.from, source.from + source.size)
        .insert(
          destination - (source.from < destination ? source.size : 0),
          node,
        ),
    );
    return true;
  };
  return {
    top,
    selectBlock,
    selectedBlockId,
    commentSelection,
    selectionAnchor,
    blockAction,
    moveBlock,
    dropBlock,
  };
}
