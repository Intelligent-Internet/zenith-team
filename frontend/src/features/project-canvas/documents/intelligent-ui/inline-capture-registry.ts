import type { Editor } from "@tiptap/react";
import type { InlineCaptureReader } from "@/shared/inline-ui/capture";

const captures = new WeakMap<Editor, Map<string, InlineCaptureReader>>();
export function inlineCaptureRegistry(editor: Editor) {
  let registry = captures.get(editor);
  if (!registry) {
    registry = new Map<string, InlineCaptureReader>();
    captures.set(editor, registry);
  }
  return registry;
}
export function registerInlineCapture(
  editor: Editor,
  id: string,
  read: InlineCaptureReader,
) {
  const registry = inlineCaptureRegistry(editor);
  registry.set(id, read);
  return () => {
    if (registry.get(id) === read) registry.delete(id);
  };
}
