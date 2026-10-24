import type { Editor } from "@tiptap/react";

import type { components } from "@/shared/api/generated/api.generated";
import { editorDocumentView, type DocumentView } from "./editor-runtime";
import { createEvaluationClient } from "./evaluation-client";
import { canonicalJSON } from "./expression";
import type { ScopeRuntime } from "./scope-runtime";

type Selections = components["schemas"]["PresentationSelections"];
type Inputs = Selections[string][string];

function scalar(value: unknown): value is string | number | boolean | null {
  return (
    value === null ||
    typeof value === "string" ||
    typeof value === "boolean" ||
    (typeof value === "number" && Number.isFinite(value))
  );
}

// Paths mirror CaptureUI's authored traversal. Repeat rows add the hash of a
// canonical scalar key; their rendered editors never allocate saved identities.
export async function collectCaptureInputs(
  editor: Editor,
  signal?: AbortSignal,
) {
  const selected: Selections = { page: {} };
  const guards: Array<() => boolean> = [];
  const editors = new Set<DocumentView>();
  const runtimes = new Set<ScopeRuntime>();
  const client = createEvaluationClient();
  let nodes = 0;
  let indexedNodes = 0;
  const indexedSources = new WeakMap<object, Map<string, string>>();

  const validate = () => {
    if (signal?.aborted || guards.some((current) => !current()))
      throw new Error(
        "The Page or scenario changed. Prepare the capture again.",
      );
  };
  const observeEditor = (current: DocumentView) => {
    if (editors.has(current)) return;
    if (current.isDisposed())
      throw new Error("This view is no longer available.");
    editors.add(current);
    const document = current.getDocument();
    guards.push(
      () => !current.isDisposed() && current.getDocument() === document,
    );
  };
  const environment = (runtime?: ScopeRuntime) => {
    if (!runtime) return { state: {}, data: {} };
    const snapshot = runtime.getSnapshot();
    if (snapshot.status !== "ready")
      throw new Error(
        "Wait for scenario values and sources to finish updating.",
      );
    if (!runtimes.has(runtime)) {
      runtimes.add(runtime);
      guards.push(() => runtime.getSnapshot() === snapshot);
    }
    return snapshot.environment;
  };
  const walk = async (
    current: DocumentView,
    node: components["schemas"]["CanvasBlock"],
    parent: ScopeRuntime | undefined,
    location: {
      path: string;
      depth: number;
      repeats: number;
      documentKey: string;
      references: number;
      sourcePaths?: { editor: DocumentView; paths: Map<string, string> };
    },
  ): Promise<void> => {
    validate();
    observeEditor(current);
    if (++nodes > 10000 || location.depth > 32)
      throw new Error("This scenario exceeds the capture size limit.");
    const id =
      typeof node.attrs?.blockId === "string" ? node.attrs?.blockId : undefined;
    const path =
      (id && location.sourcePaths?.editor === current
        ? location.sourcePaths.paths.get(id)
        : undefined) ?? location.path + (id ? "/" + id : "");
    const registry = current.registry;
    if (node.type === "uiDocument") {
      const read = id ? registry.getInline(id) : undefined;
      if (!read)
        throw new Error("Wait for this component to render before capturing.");
      const snapshot = read();
      const encoded = JSON.stringify(snapshot);
      if (
        snapshot.source !==
        (node.content ?? []).map((child) => child.text ?? "").join("")
      )
        throw new Error("The component source changed. Prepare a new capture.");
      guards.push(
        () =>
          registry.getInline(id!) === read &&
          JSON.stringify(read()) === encoded,
      );
      const selections = (selected[location.documentKey] ??= {});
      if (selections[path] && selections[path].inlineState !== encoded)
        throw new Error(
          "Included components have different state in separate views.",
        );
      selections[path] = { inlineState: encoded };
      return;
    }
    let runtime = parent;
    if (node.type === "uiScope") {
      runtime = id ? registry.get(id) : undefined;
      if (!runtime)
        throw new Error("A scenario scope is not ready to capture.");
      environment(runtime);
      const inputs = runtime.getInputs();
      const local: Inputs = {};
      for (const name of Object.keys(node.attrs?.initial ?? {})) {
        const value = inputs[name];
        if (!scalar(value)) throw new Error("A scenario input is unavailable.");
        local[name] = value;
      }
      const selections = (selected[location.documentKey] ??= {});
      if (
        selections[path] &&
        canonicalJSON(selections[path]) !== canonicalJSON(local)
      )
        throw new Error(
          "The same included source has different inputs in separate views. Make those inputs agree before capturing.",
        );
      selections[path] = local;
    }
    if (node.type === "artifactReference" && id) {
      const reference = registry.getReference(id);
      guards.push(() => registry.getReference(id) === reference);
      if (reference) {
        if (location.references >= 2)
          throw new Error(
            "This capture includes too many nested source views.",
          );
        const cachedPaths = indexedSources.get(reference.content);
        const paths = cachedPaths ?? new Map<string, string>();
        const indexPaths = (
          block: components["schemas"]["CanvasBlock"],
          parent: string,
          depth: number,
        ) => {
          if (++indexedNodes > 10000 || depth > 32)
            throw new Error(
              "An included source exceeds the capture size limit.",
            );
          const blockId =
            typeof block.attrs?.blockId === "string"
              ? block.attrs.blockId
              : undefined;
          const path = parent + (blockId ? "/" + blockId : "");
          if (blockId) paths.set(blockId, path);
          block.content?.forEach((child, index) =>
            indexPaths(child, path + "/" + index, depth + 1),
          );
        };
        if (!cachedPaths) {
          indexPaths(reference.content, "", 0);
          indexedSources.set(reference.content, paths);
        }
        const referenceView = editorDocumentView(reference.editor);
        await walk(referenceView, referenceView.getDocument(), undefined, {
          path: "",
          depth: 0,
          repeats: 0,
          documentKey: reference.key,
          references: location.references + 1,
          sourcePaths: { editor: referenceView, paths },
        });
      }
    }
    if (node.type === "uiConditional") {
      const visible = await client.evaluate(
        node.attrs?.when,
        environment(runtime),
        signal,
      );
      if (typeof visible !== "boolean")
        throw new Error("A scenario condition is unavailable.");
      if (!visible) return;
    }
    if (node.type === "uiRepeat") {
      if (!id || location.repeats >= 8)
        throw new Error("This repeated scenario cannot be captured.");
      const items = await client.evaluate(
        node.attrs?.items,
        environment(runtime),
        signal,
      );
      if (!Array.isArray(items) || items.length > 1000)
        throw new Error("The repeated items are unavailable.");
      const seen = new Set<string>();
      for (const item of items) {
        const key =
          item !== null && typeof item === "object" && !Array.isArray(item)
            ? item[String(node.attrs?.keyField || "id")]
            : item;
        if (!scalar(key) || key === null)
          throw new Error("Repeated items need stable scalar keys.");
        const identity = canonicalJSON(key);
        if (seen.has(identity))
          throw new Error("Repeated item keys must be unique.");
        seen.add(identity);
        const instance = registry.getInstance(id, identity);
        if (!instance)
          throw new Error("Wait for repeated items to finish rendering.");
        guards.push(() => registry.getInstance(id, identity) === instance);
        const digest = await crypto.subtle.digest(
          "SHA-256",
          new TextEncoder().encode(identity),
        );
        const hash = Array.from(new Uint8Array(digest), (byte) =>
          byte.toString(16).padStart(2, "0"),
        ).join("");
        observeEditor(instance);
        const inherited = instance.registry.getParent();
        environment(inherited);
        for (
          let index = 0;
          index < (instance.getDocument().content?.length ?? 0);
          index++
        ) {
          await walk(
            instance,
            instance.getDocument().content![index],
            inherited,
            {
              ...location,
              path: path + "/item/" + hash + "/" + index,
              depth: location.depth + 1,
              repeats: location.repeats + 1,
            },
          );
        }
      }
      return;
    }
    for (let index = 0; index < (node.content?.length ?? 0); index++)
      await walk(current, node.content![index], runtime, {
        ...location,
        path: path + "/" + index,
        depth: location.depth + 1,
        repeats: location.repeats,
      });
  };
  try {
    const view = editorDocumentView(editor);
    await walk(view, view.getDocument(), undefined, {
      path: "",
      depth: 0,
      repeats: 0,
      documentKey: "page",
      references: 0,
    });
    validate();
    if (new TextEncoder().encode(JSON.stringify(selected)).length > 1048576)
      throw new Error("The selected scenario inputs are too large to capture.");
    return { selected, validate };
  } finally {
    client.dispose();
  }
}
