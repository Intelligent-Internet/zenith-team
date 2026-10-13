import type { Editor } from "@tiptap/react";
import { Plugin, PluginKey } from "@tiptap/pm/state";
import { Decoration, DecorationSet } from "@tiptap/pm/view";
import type { Node as ProseMirrorNode } from "@tiptap/pm/model";
import type { components } from "@/shared/api/generated/api.generated";
import type { Block } from "./block-editor-schema";

type Suggestion = components["schemas"]["PageSuggestion"];

export function suggestionPlacement(
  doc: ProseMirrorNode,
  suggestion: Suggestion,
  after: Block,
) {
  const blocks = new Map<string, { from: number; to: number }>();
  doc.descendants((node, from) => {
    if (node.attrs.blockId)
      blocks.set(node.attrs.blockId, { from, to: from + node.nodeSize });
  });
  const original = blocks.get(suggestion.blockId);
  if (original && suggestion.type !== "move")
    return { position: original.to, original };

  // Insertions and moves use the proposal's sibling order, never text matching.
  const locate = (parent: Block): number | undefined => {
    const siblings = parent.content ?? [];
    const index = siblings.findIndex(
      (block) => block.attrs?.blockId === suggestion.blockId,
    );
    if (index >= 0) {
      for (let i = index - 1; i >= 0; i--) {
        const previous = blocks.get(String(siblings[i].attrs?.blockId));
        if (previous) return previous.to;
      }
      for (let i = index + 1; i < siblings.length; i++) {
        const next = blocks.get(String(siblings[i].attrs?.blockId));
        if (next) return next.from;
      }
      const container = blocks.get(String(parent.attrs?.blockId));
      if (container) return container.from + 1;
      if (parent.type === "doc") return doc.content.size;
    }
    for (const child of siblings) {
      const position = locate(child);
      if (position !== undefined) return position;
    }
  };
  return { position: locate(after) ?? doc.content.size, original };
}

function widgetPosition(doc: ProseMirrorNode, position: number) {
  let resolved = doc.resolve(position);
  // Keep a list-item or row's preview at that item/cell, not after the
  // entire list/table. Widgets must live inside valid block containers.
  while (
    resolved.depth &&
    ["bulletList", "orderedList", "taskList", "table", "tableRow"].includes(
      resolved.parent.type.name,
    )
  ) {
    if (resolved.nodeBefore && !resolved.nodeBefore.isLeaf) position -= 1;
    else if (resolved.nodeAfter && !resolved.nodeAfter.isLeaf) position += 1;
    else position = resolved.after();
    resolved = doc.resolve(position);
  }
  return position;
}

export type SuggestionVersionOption = {
  conflicting: boolean;
  id: string;
  agentId: string;
  agentName: string;
  number: number;
  total: number;
};
export type SuggestionVersionState = {
  selected: boolean;
  pickerOwner: boolean;
  selectedId: string;
  options: SuggestionVersionOption[];
};

export type InlineEntry = {
  id: string;
  agentId: string;
  agentName: string;
  setVersion: (value: SuggestionVersionState) => void;
  suggestion: Suggestion;
  after: Block;
  priority: string;
  setVisible: (value: boolean) => void;
  order: number;
  avatarTrigger: HTMLDivElement;
  preview: HTMLDivElement;
};

type SuggestionBlock = {
  key: string;
  position: number;
  readingPosition: number;
  entries: InlineEntry[];
  order: number;
};

function blocksInDocumentOrder(
  doc: ProseMirrorNode,
  entries: Iterable<InlineEntry>,
) {
  const blocks = new Map<string, SuggestionBlock>();
  for (const entry of entries) {
    const { position, original } = suggestionPlacement(
      doc,
      entry.suggestion,
      entry.after,
    );
    const point = widgetPosition(doc, position);
    // Insertions can share an anchor without being alternatives for the same block.
    const blockKey = entry.suggestion.blockId || entry.suggestion.type;
    const block = blocks.get(blockKey) ?? {
      key: blockKey,
      position: point,
      readingPosition:
        original && entry.suggestion.type !== "move" ? original.from : position,
      entries: [],
      order: entry.order,
    };
    block.entries.push(entry);
    block.order = Math.min(block.order, entry.order);
    blocks.set(blockKey, block);
  }
  return [...blocks.values()].sort(
    (a, b) => a.readingPosition - b.readingPosition || a.order - b.order,
  );
}

type ReviewWidgets = {
  entries: Map<string, InlineEntry>;
  key: PluginKey;
  refresh: () => void;
  select: (id: string) => void;
};
const reviewWidgets = new WeakMap<Editor, ReviewWidgets>();

// One selected version per change point, including multiple versions by one Agent.
export function suggestionPointsFor(editor: Editor): ReviewWidgets {
  const existing = reviewWidgets.get(editor);
  if (existing) return existing;
  const entries = new Map<string, InlineEntry>();
  const hosts = new Map<string, HTMLDivElement>();
  const selectedVersions = new Map<string, string>();
  const entryBlocks = new Map<string, string>();
  const key = new PluginKey("pageSuggestionPoints");
  const widgets: ReviewWidgets = {
    select: (id) => {
      const blockKey = entryBlocks.get(id);
      if (!blockKey) return;
      selectedVersions.set(blockKey, id);
      widgets.refresh();
    },
    entries,
    key,
    refresh: () => {
      if (!editor.isDestroyed)
        editor.view.dispatch(editor.state.tr.setMeta(key, true));
    },
  };
  reviewWidgets.set(editor, widgets);
  editor.registerPlugin(
    new Plugin({
      key,
      props: {
        decorations(state) {
          const decorations: Decoration[] = [];
          const used = new Set<string>();
          const orderedBlocks = blocksInDocumentOrder(
            state.doc,
            entries.values(),
          );
          entryBlocks.clear();
          const activeBlocks = new Set(orderedBlocks.map((block) => block.key));
          for (const blockKey of selectedVersions.keys()) {
            if (!activeBlocks.has(blockKey)) selectedVersions.delete(blockKey);
          }
          for (const [blockIndex, block] of orderedBlocks.entries()) {
            const { position, entries: group } = block;
            group.sort(
              (a, b) =>
                a.priority.localeCompare(b.priority) ||
                a.order - b.order ||
                a.id.localeCompare(b.id),
            );
            const agents = new Map<string, InlineEntry[]>();
            for (const entry of group) {
              const changes = agents.get(entry.agentId) ?? [];
              changes.push(entry);
              agents.set(entry.agentId, changes);
            }
            const selectedId = group.some(
              (entry) => entry.id === selectedVersions.get(block.key),
            )
              ? selectedVersions.get(block.key)!
              : group[0].id;
            selectedVersions.set(block.key, selectedId);
            const options = group.map((entry) => {
              const versions = agents.get(entry.agentId)!;
              return {
                conflicting: entry.suggestion.competingChangeIds.length > 0,
                id: entry.id,
                agentId: entry.agentId,
                agentName: entry.agentName,
                number: versions.indexOf(entry) + 1,
                total: versions.length,
              };
            });
            group.forEach((entry) => {
              entryBlocks.set(entry.id, block.key);
              const version = {
                selected: entry.id === selectedId,
                pickerOwner: entry === group[0],
                selectedId,
                options,
              };
              const visible = version.selected;
              queueMicrotask(() => {
                // A refresh can outlive a React unmount or a replacement entry.
                if (entries.get(entry.id) !== entry) return;
                entry.setVersion(version);
                entry.setVisible(visible);
              });
              const { original } = suggestionPlacement(
                state.doc,
                entry.suggestion,
                entry.after,
              );
              if (
                visible &&
                original &&
                entry.suggestion.status === "pending"
              ) {
                const className = ["remove", "update"].includes(
                  entry.suggestion.type,
                )
                  ? "pc-suggestion-removed"
                  : "pc-suggestion-moved";
                const originalNode = state.doc.nodeAt(original.from);
                if (
                  originalNode &&
                  !originalNode.isTextblock &&
                  !originalNode.isLeaf
                ) {
                  // Nested review controls must not inherit a container's deletion style.
                  originalNode.descendants((node, offset) => {
                    if (node.isBlock && (node.isTextblock || node.isLeaf)) {
                      const from = original.from + 1 + offset;
                      decorations.push(
                        Decoration.node(from, from + node.nodeSize, {
                          class: className,
                        }),
                      );
                      return false;
                    }
                  });
                } else {
                  decorations.push(
                    Decoration.node(original.from, original.to, {
                      class: className,
                    }),
                  );
                }
              }
            });
            const groupKey = group
              .map((entry) => entry.id)
              .sort()
              .join(":");
            used.add(groupKey);
            let host = hosts.get(groupKey);
            if (!host) {
              host = document.createElement("div");
              host.className = "pc-inline-suggestion";
              host.contentEditable = "false";
              host.setAttribute("data-suggestion-preview", "true");
              const avatars = document.createElement("div");
              avatars.className = "pc-diff-avatars";
              avatars.setAttribute("role", "group");
              avatars.setAttribute("aria-label", "Suggestion versions");
              host.append(avatars);
              hosts.set(groupKey, host);
            }
            const avatars =
              host.querySelector<HTMLDivElement>(".pc-diff-avatars")!;
            if (avatars.firstChild !== group[0].avatarTrigger)
              avatars.replaceChildren(group[0].avatarTrigger);
            for (const entry of group) {
              if (entry.preview.parentElement !== host)
                host.append(entry.preview);
            }
            decorations.push(
              Decoration.widget(position, host, {
                side: blockIndex + 1,
                key: groupKey,
                stopEvent: () => true,
                ignoreSelection: true,
              }),
            );
          }
          for (const key of hosts.keys()) {
            if (!used.has(key)) {
              hosts.delete(key);
            }
          }
          return DecorationSet.create(state.doc, decorations);
        },
      },
    }),
  );
  return widgets;
}

export function existingSuggestionPoints(editor: Editor) {
  return reviewWidgets.get(editor);
}

export function releaseSuggestionPoints(editor: Editor) {
  const points = reviewWidgets.get(editor);
  if (!points || points.entries.size) return;
  if (!editor.isDestroyed) editor.unregisterPlugin(points.key);
  reviewWidgets.delete(editor);
}
