import type { Node as DocumentNode } from "@tiptap/pm/model";
import type { Transaction } from "@tiptap/pm/state";

function childPosition(node: DocumentNode, index: number, position: number) {
  let offset = position + 1;
  for (let i = 0; i < index; i++) offset += node.child(i).nodeSize;
  return offset;
}

// Keep retained containers and untouched siblings in place. Only moved or new
// children are inserted; removing a child does not recreate its neighbours.
export function reconcileSourceReplacement(
  tr: Transaction,
  before: DocumentNode,
  after: DocumentNode,
  position: number,
) {
  if (before.eq(after)) return;
  if (
    before.type !== after.type ||
    before.attrs.blockId !== after.attrs.blockId
  )
    throw new Error(
      "The preview changed the block identity. Prepare a new preview.",
    );

  let keyed = true;
  before.forEach((child) => {
    if (!child.attrs.blockId) keyed = false;
  });
  after.forEach((child) => {
    if (!child.attrs.blockId) keyed = false;
  });
  const retained = new Set<string>();
  after.forEach((child) => retained.add(child.attrs.blockId));
  let sharedChild = false;
  before.forEach((child) => {
    if (retained.has(child.attrs.blockId)) sharedChild = true;
  });
  if (keyed && sharedChild) {
    // Add new children before pruning, so replacing a required leading child
    // never temporarily empties that slot and recreates retained neighbours.
    after.forEach((desired, _offset, index) => {
      const current = tr.doc.nodeAt(position)!;
      let exists = false;
      current.forEach((child) => {
        if (child.attrs.blockId === desired.attrs.blockId) exists = true;
      });
      if (!exists)
        tr.insert(
          childPosition(current, Math.min(index, current.childCount), position),
          desired,
        );
    });
    // Remove omitted children first, so a deletion does not turn every following
    // retained child into a move with a new CRDT item.
    const populated = tr.doc.nodeAt(position)!;
    for (let i = populated.childCount - 1; i >= 0; i--) {
      const child = populated.child(i);
      if (!retained.has(child.attrs.blockId)) {
        const start = childPosition(populated, i, position);
        tr.delete(start, start + child.nodeSize);
      }
    }
    after.forEach((desired, _offset, index) => {
      let current = tr.doc.nodeAt(position)!;
      let found = -1;
      current.forEach((child, _childOffset, childIndex) => {
        if (child.attrs.blockId === desired.attrs.blockId) found = childIndex;
      });
      if (found !== index) {
        const moving = found >= 0 ? current.child(found) : desired;
        if (found >= 0) {
          const start = childPosition(current, found, position);
          tr.delete(start, start + moving.nodeSize);
          current = tr.doc.nodeAt(position)!;
        }
        tr.insert(childPosition(current, index, position), moving);
      }
      current = tr.doc.nodeAt(position)!;
      reconcileSourceReplacement(
        tr,
        current.child(index),
        desired,
        childPosition(current, index, position),
      );
    });
    // Required child shapes (for example a list item's leading paragraph) can
    // make ProseMirror insert temporary filler during an intermediate deletion.
    // Commit exactly the reviewed content, never that automatically fitted shape.
    const reconciled = tr.doc.nodeAt(position)!;
    if (!reconciled.content.eq(after.content))
      tr.replaceWith(
        position + 1,
        position + reconciled.nodeSize - 1,
        after.content,
      );
  } else if (!before.content.eq(after.content)) {
    tr.replaceWith(position + 1, position + before.nodeSize - 1, after.content);
  }
  tr.setNodeMarkup(position, undefined, after.attrs, after.marks);
}
