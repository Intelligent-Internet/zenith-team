import { NodeSelection, Selection, type EditorState } from "@tiptap/pm/state";
import { dropPoint } from "@tiptap/pm/transform";
import { Fragment, Slice, type Node } from "@tiptap/pm/model";

export function hasDocumentBlocks(slice: Slice) {
  let found = false;
  slice.content.descendants((node) => {
    if (Object.prototype.hasOwnProperty.call(node.attrs, "blockId"))
      found = true;
  });
  return found;
}

// IDs come from the owning document's server reservation pool. Allocate the
// entire subtree before insertion so local links can resolve forward targets.
export function copyDocumentSlice(slice: Slice, ids: string[]) {
  let needed = 0;
  const count = (node: Node) => {
    if (Object.prototype.hasOwnProperty.call(node.attrs, "blockId")) needed++;
    node.forEach(count);
  };
  slice.content.forEach(count);
  if (needed > ids.length) return { needed, slice: null };

  // Open slice ancestors may merge into the destination paragraph/container.
  // They are not copied subtrees, so links to them must keep their source target.
  const partialIds = new Set<string>();
  const rememberPartial = (
    node: Node | null,
    depth: number,
    first: boolean,
  ) => {
    for (let level = 0; node && level < depth; level++) {
      if (typeof node.attrs.blockId === "string")
        partialIds.add(node.attrs.blockId);
      node = first ? node.firstChild : node.lastChild;
    }
  };
  rememberPartial(slice.content.firstChild, slice.openStart, true);
  rememberPartial(slice.content.lastChild, slice.openEnd, false);
  const mapped = new Map<string, string>();
  let index = 0;
  const allocate = (node: Node) => {
    if (Object.prototype.hasOwnProperty.call(node.attrs, "blockId")) {
      const id = ids[index++];
      if (
        typeof node.attrs.blockId === "string" &&
        !partialIds.has(node.attrs.blockId) &&
        !mapped.has(node.attrs.blockId)
      )
        mapped.set(node.attrs.blockId, id);
    }
    node.forEach(allocate);
  };
  slice.content.forEach(allocate);
  let copyIndex = 0;
  const copy = (node: Node): Node => {
    const marks = node.marks.map((mark) => {
      const href = mark.attrs.href;
      if (
        mark.type.name !== "link" ||
        typeof href !== "string" ||
        !href.startsWith("#block-")
      )
        return mark;
      const target = mapped.get(href.slice("#block-".length));
      return target
        ? mark.type.create({ ...mark.attrs, href: `#block-${target}` })
        : mark;
    });
    if (node.isText) return node.mark(marks);
    const id = Object.prototype.hasOwnProperty.call(node.attrs, "blockId")
      ? ids[copyIndex++]
      : undefined;
    const children: Node[] = [];
    node.forEach((child) => children.push(copy(child)));
    return node.type.create(
      id ? { ...node.attrs, blockId: id } : node.attrs,
      Fragment.fromArray(children),
      marks,
    );
  };
  const children: Node[] = [];
  slice.content.forEach((node) => children.push(copy(node)));
  const copied = new Slice(
    Fragment.fromArray(children),
    slice.openStart,
    slice.openEnd,
  );
  ids.splice(0, needed);
  return { needed, slice: copied };
}

// Copy drops never delete the drag source. Native move drops stay with the
// editor so their identities, selection semantics and undo behavior survive.
export function copyDocumentDrop(
  state: EditorState,
  slice: Slice,
  position: number,
  ids: string[],
) {
  const available = ids.slice();
  const copied = copyDocumentSlice(slice, available);
  if (!copied.slice) return { needed: copied.needed, transaction: null };
  const at = dropPoint(state.doc, position, copied.slice) ?? position;
  const transaction = state.tr.replaceRange(at, at, copied.slice);
  if (transaction.doc.eq(state.doc)) return { needed: 0, transaction: null };
  const single =
    !copied.slice.openStart &&
    !copied.slice.openEnd &&
    copied.slice.content.childCount === 1;
  const inserted = transaction.doc.resolve(at);
  const first = copied.slice.content.firstChild;
  if (
    single &&
    first &&
    NodeSelection.isSelectable(first) &&
    inserted.nodeAfter?.sameMarkup(first)
  ) {
    transaction.setSelection(new NodeSelection(inserted));
  } else {
    transaction.setSelection(
      Selection.near(transaction.doc.resolve(transaction.mapping.map(at))),
    );
  }
  ids.splice(0, copied.needed);
  return {
    needed: copied.needed,
    transaction: transaction.setMeta("uiEvent", "drop").scrollIntoView(),
  };
}
