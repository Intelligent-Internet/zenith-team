// Page references and native capture bookkeeping are separate from inline UI.
// The inline document node never reads or mutates this registry.
import { Extension } from "@tiptap/react";
import { Plugin } from "@tiptap/pm/state";
import { editorRuntime, disposeEditorRuntime } from "./editor-runtime";
import type { ScopeRuntime } from "./scope-runtime";

export function captureRuntimeExtension(parent?: ScopeRuntime) {
  return Extension.create({
    name: "documentCaptureRuntime",
    addProseMirrorPlugins() {
      const editor = this.editor;
      return [
        new Plugin({
          view(view) {
            const registry = editorRuntime(editor);
            registry.setParent(parent);
            registry.update(view.state.doc.toJSON());
            return {
              update(next, previous) {
                if (!next.state.doc.eq(previous.doc))
                  registry.update(next.state.doc.toJSON());
              },
              destroy() {
                disposeEditorRuntime(editor);
              },
            };
          },
        }),
      ];
    },
  });
}
