import { inlineCaptureRegistry } from "./inline-capture-registry";
import type { InlineCaptureReader } from "@/shared/inline-ui/capture";
import { createScopeDefinitionCache } from "./scope-definition";
import { supportsReactiveVersion } from "./versions";
import type { Editor } from "@tiptap/react";

import type { components } from "@/shared/api/generated/api.generated";
import { createScopeRuntime, type ScopeRuntime } from "./scope-runtime";

type Block = components["schemas"]["CanvasBlock"];
const registries = new WeakMap<Editor, ReturnType<typeof createRegistry>>();
export function createRegistry(
  inline = new Map<string, InlineCaptureReader>(),
) {
  const definitions = createScopeDefinitionCache();
  const scopes = new Map<
    string,
    { runtime: ScopeRuntime; parent?: ScopeRuntime; definition: string }
  >();
  const instances = new Map<string, Map<string, DocumentView>>();
  const fallbacks = new Map<string, DocumentView>();
  const references = new Map<
    string,
    { key: string; editor: Editor; content: Block }
  >();
  const listeners = new Set<() => void>();
  let revision = 0;
  let inherited: ScopeRuntime | undefined;
  return {
    getInline: (id: string) => inline.get(id),
    registerInline(id: string, read: InlineCaptureReader) {
      inline.set(id, read);
      return () => {
        if (inline.get(id) === read) inline.delete(id);
      };
    },
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    getSnapshot: () => revision,
    setParent(parent: ScopeRuntime | undefined) {
      inherited = parent;
    },
    get: (id: string) => scopes.get(id)?.runtime,
    getParent: () => inherited,
    getReference: (
      id: string,
    ): { key: string; editor: Editor; content: Block } | undefined =>
      references.get(id) ?? fallbacks.get(id)?.registry.getReference(id),
    registerFallback(id: string, view: DocumentView) {
      fallbacks.set(id, view);
      return () => {
        if (fallbacks.get(id) === view) fallbacks.delete(id);
      };
    },
    registerReference(
      id: string,
      reference: { key: string; editor: Editor; content: Block },
    ) {
      references.set(id, reference);
      return () => {
        if (references.get(id) === reference) references.delete(id);
      };
    },
    getInstance: (repeatId: string, key: string) =>
      instances.get(repeatId)?.get(key),
    registerInstance(repeatId: string, key: string, editor: DocumentView) {
      let rows = instances.get(repeatId);
      if (!rows) {
        rows = new Map();
        instances.set(repeatId, rows);
      }
      rows.set(key, editor);
      return () => {
        if (rows.get(key) === editor) rows.delete(key);
        if (rows.size === 0 && instances.get(repeatId) === rows)
          instances.delete(repeatId);
      };
    },
    update(document: Block) {
      const seen = new Set<string>();
      const walk = (node: Block, parent?: ScopeRuntime) => {
        // Unknown containers do not establish a compatible lexical environment.
        // Omit descendants from seen so previously active runtimes are disposed.
        if (
          node.type?.startsWith("ui") &&
          !supportsReactiveVersion(node.type ?? "", node.attrs ?? {})
        )
          return;
        let runtime = parent;
        if (
          node.type === "uiScope" &&
          typeof node.attrs?.blockId === "string"
        ) {
          const id = node.attrs.blockId;
          seen.add(id);
          const definition = JSON.stringify(node.attrs ?? {});
          const existing = scopes.get(id);
          const block = { type: "uiScope", attrs: node.attrs } as Block;
          try {
            if (!existing || existing.parent !== parent) {
              existing?.runtime.dispose();
              runtime = createScopeRuntime(block, parent, definitions);
              scopes.set(id, { runtime, parent, definition });
            } else {
              runtime = existing.runtime;
              if (existing.definition !== definition) {
                runtime.updateDefinition(block);
                existing.definition = definition;
              }
            }
          } catch {
            existing?.runtime.dispose();
            scopes.delete(id);
            runtime = undefined;
          }
        }
        if (node.type !== "uiRepeat")
          node.content?.forEach((child) => walk(child, runtime));
      };
      walk(document, inherited);
      for (const [id, scope] of scopes)
        if (!seen.has(id)) {
          scope.runtime.dispose();
          scopes.delete(id);
        }
      revision++;
      listeners.forEach((listener) => listener());
    },
    dispose() {
      for (const scope of scopes.values()) scope.runtime.dispose();
      scopes.clear();
      definitions.clear();
      instances.clear();
      fallbacks.clear();
      references.clear();
      inline.clear();
      listeners.clear();
    },
  };
}
export function editorRuntime(editor: Editor) {
  let registry = registries.get(editor);
  if (!registry) {
    registry = createRegistry(inlineCaptureRegistry(editor));
    registries.set(editor, registry);
  }
  return registry;
}
export function disposeEditorRuntime(editor: Editor) {
  registries.get(editor)?.dispose();
  registries.delete(editor);
}

export interface DocumentView {
  getDocument(): Block;
  isDisposed(): boolean;
  registry: ReturnType<typeof createRegistry>;
}
const editorViews = new WeakMap<Editor, DocumentView>();
export function editorDocumentView(editor: Editor): DocumentView {
  let view = editorViews.get(editor);
  if (!view) {
    let document = editor.state.doc;
    let content = document.toJSON() as Block;
    view = {
      registry: editorRuntime(editor),
      isDisposed: () => editor.isDestroyed,
      getDocument() {
        if (document !== editor.state.doc) {
          document = editor.state.doc;
          content = document.toJSON() as Block;
        }
        return content;
      },
    };
    editorViews.set(editor, view);
  }
  return view;
}
