import * as Y from "yjs";

export function preserveBlockIdentityOnUndo(manager: Y.UndoManager) {
  const deleteFilter = manager.deleteFilter;
  // The binding can retain a paragraph containing another author's text during
  // undo. Keep its ID too. Deleting a whole block still deletes its attributes.
  manager.deleteFilter = (item) =>
    !(item.parent instanceof Y.XmlElement && item.parentSub === "blockId") &&
    deleteFilter(item);
  return () => {
    manager.deleteFilter = deleteFilter;
  };
}

// Repair drafts cached by older clients using the CRDT element's identity, never
// its position or text. Restoring a known ID preserves comments and references;
// a new or unrecognized element must go through normal server ID reservation.
export function restoreDocumentIdentities(doc: Y.Doc, saved: Y.Doc) {
  const savedIds = new Map<string, string>();
  const usedIds = new Set<string>();
  const missing: Y.XmlElement[] = [];
  const key = (element: Y.XmlElement) => {
    const id = Y.createRelativePositionFromTypeIndex(element, 0).type;
    return id ? `${id.client}:${id.clock}` : "";
  };
  for (const node of saved
    .getXmlFragment("content")
    .createTreeWalker((node) => node instanceof Y.XmlElement)) {
    if (!(node instanceof Y.XmlElement)) continue;
    const id = node.getAttribute("blockId");
    if (id) savedIds.set(key(node), id);
  }
  for (const node of doc
    .getXmlFragment("content")
    .createTreeWalker((node) => node instanceof Y.XmlElement)) {
    if (!(node instanceof Y.XmlElement)) continue;
    const id = node.getAttribute("blockId");
    if (id) usedIds.add(id);
    else missing.push(node);
  }
  doc.transact(() => {
    for (const node of missing) {
      const id = savedIds.get(key(node));
      if (!id || usedIds.has(id)) continue;
      node.setAttribute("blockId", id);
      usedIds.add(id);
    }
  }, restoreDocumentIdentities);
}
