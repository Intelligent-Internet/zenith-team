import {
  useId,
  useCallback,
  useLayoutEffect,
  useMemo,
  useState,
  useRef,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import type { Editor } from "@tiptap/react";
import type { components } from "@/shared/api/generated/api.generated";
import { SuggestionVersionPicker } from "./suggestion-version-picker";
import type { Block } from "./block-editor-schema";

import {
  suggestionPointsFor,
  existingSuggestionPoints,
  releaseSuggestionPoints,
  suggestionPlacement,
  type SuggestionVersionState,
  type InlineEntry,
} from "./page-suggestion-points";

type Suggestion = components["schemas"]["PageSuggestion"];

// Preview blocks must not impersonate live blocks for comments or navigation.
export function suggestionPreview(block: Block): Block {
  const { blockId: _id, ...attrs } = block.attrs ?? {};
  return {
    ...block,
    attrs,
    content: block.content?.map(suggestionPreview),
  };
}

/** Previews never change the saved document; merging uses the server review transaction. */
export function InlinePageSuggestion({
  editor,
  suggestion,
  after,
  agentId,
  agentName,
  priority,
  busy,
  children,
}: {
  editor?: Editor | null;
  suggestion: Suggestion;
  after: Block;
  agentId: string;
  agentName: string;
  priority: string;
  busy: boolean;
  children: (originalVisible: boolean) => ReactNode;
}) {
  const id = useId();
  const [isExpanded, setExpanded] = useState(false);
  const [visited, setVisited] = useState(false);
  const setVisible = useCallback((visible: boolean) => {
    setExpanded(visible);
    if (visible) setVisited(true);
  }, []);
  const [version, updateVersion] = useState<SuggestionVersionState>({
    selected: false,
    pickerOwner: false,
    selectedId: "",
    options: [],
  });
  const setVersion = useCallback((value: SuggestionVersionState) => {
    updateVersion((current) =>
      current.selected === value.selected &&
      current.pickerOwner === value.pickerOwner &&
      current.selectedId === value.selectedId &&
      current.options.length === value.options.length &&
      current.options.every((option, index) => {
        const next = value.options[index];
        return (
          option.conflicting === next.conflicting &&
          option.id === next.id &&
          option.agentId === next.agentId &&
          option.agentName === next.agentName &&
          option.number === next.number &&
          option.total === next.total
        );
      })
        ? current
        : value,
    );
  }, []);
  const avatarTrigger = useMemo(() => document.createElement("div"), []);
  const preview = useMemo(() => {
    const element = document.createElement("div");
    element.className = "pc-point-preview";
    return element;
  }, []);
  const [originalVisible, setOriginalVisible] = useState(false);
  const [mounted, setMounted] = useState(false);
  const order = useMemo(() => {
    const ids: string[] = [];
    const visit = (block: Block) => {
      if (block.attrs?.blockId) ids.push(String(block.attrs.blockId));
      block.content?.forEach(visit);
    };
    visit(after);
    const index = ids.indexOf(suggestion.blockId);
    return index < 0 ? suggestion.index : index;
  }, [after, suggestion.blockId, suggestion.index]);
  const entry = useRef<InlineEntry>({
    id,
    agentId,
    agentName,
    setVersion,
    suggestion,
    after,
    priority,
    setVisible,
    order,
    avatarTrigger,
    preview,
  });
  entry.current = {
    id,
    agentId,
    agentName,
    setVersion,
    suggestion,
    after,
    priority,
    setVisible,
    order,
    avatarTrigger,
    preview,
  };
  useLayoutEffect(() => {
    if (!editor || editor.isDestroyed) return;
    const widgets = suggestionPointsFor(editor);
    widgets.entries.set(id, entry.current);
    widgets.refresh();
    setMounted(true);
    return () => {
      widgets.entries.delete(id);
      avatarTrigger.remove();
      preview.remove();
      if (!widgets.entries.size) {
        releaseSuggestionPoints(editor);
      } else widgets.refresh();
    };
  }, [editor, id, avatarTrigger, preview]);
  useLayoutEffect(() => {
    if (!editor || editor.isDestroyed) return;
    const widgets = existingSuggestionPoints(editor);
    if (!widgets) return;
    widgets.entries.set(id, entry.current);
    widgets.refresh();
    const update = () => {
      const { original } = suggestionPlacement(
        editor.state.doc,
        suggestion,
        after,
      );
      setOriginalVisible(!!original && suggestion.status === "pending");
    };
    update();
    editor.on("update", update);
    return () => {
      editor.off("update", update);
    };
  }, [editor, id, suggestion, after, order, priority, agentId, agentName]);
  const selectVersion = (selectedId: string) => {
    if (!editor || editor.isDestroyed) return;
    existingSuggestionPoints(editor)?.select(selectedId);
  };
  if (!mounted) return null;
  return (
    <>
      {version.pickerOwner &&
        createPortal(
          <SuggestionVersionPicker
            options={version.options}
            selectedId={version.selectedId}
            disabled={busy}
            onSelect={selectVersion}
          />,
          avatarTrigger,
        )}
      {createPortal(
        <div id={id} hidden={!isExpanded}>
          {visited && children(originalVisible)}
        </div>,
        preview,
      )}
    </>
  );
}
