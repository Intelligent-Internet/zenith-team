import { Extension } from "@tiptap/core";
import { Plugin } from "@tiptap/pm/state";
import type { Node as ProseMirrorNode } from "@tiptap/pm/model";
import { ySyncPluginKey } from "@tiptap/y-tiptap";
import { supportsDocumentBlock, type DocumentScope } from "./document-scope";

// Guard local transactions as well as menus: pasted HTML can contain our nodes.
// Remote updates remain authoritative and must not be filtered out of Yjs.
export function documentCapabilities(
  scope: DocumentScope,
  onRejected: (message: string) => void,
) {
  return Extension.create({
    name: "documentCapabilities",
    priority: 1100,
    addProseMirrorPlugins() {
      const editor = this.editor;
      return [
        new Plugin({
          filterTransaction(transaction) {
            if (
              !transaction.docChanged ||
              transaction.getMeta(ySyncPluginKey)?.isChangeOrigin
            )
              return true;
            if (!editor.isEditable) return false;
            // Older recovery records may already contain rejected blocks. Allow
            // removing them individually without allowing new unsupported nodes.
            const retained: ProseMirrorNode[] = [];
            transaction.before.descendants((node) => {
              if (!supportsDocumentBlock(scope, node.type.name))
                retained.push(node);
            });
            let supported = true;
            transaction.doc.descendants((node) => {
              if (supportsDocumentBlock(scope, node.type.name)) return;
              const existing = retained.findIndex((previous) =>
                previous.eq(node),
              );
              if (existing < 0) supported = false;
              else retained.splice(existing, 1);
            });
            if (!supported)
              onRejected(
                "This content includes Project-only blocks that cannot be saved in Wiki. Insert it in a Project Page instead.",
              );
            return supported;
          },
        }),
      ];
    },
  });
}
