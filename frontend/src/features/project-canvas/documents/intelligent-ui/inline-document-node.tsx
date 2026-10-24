import { InlineArtifactReference } from "@/features/common/components/inline-artifact-reference";
import { registerInlineCapture } from "./inline-capture-registry";
import {
  isInlineCapture,
  type InlineCaptureReader,
} from "@/shared/inline-ui/capture";
import { DocumentViewMode } from "../document-view-mode";
import { useInlineEntityAction } from "@/features/common/hooks/use-inline-entity-action";
import { InlineDocument } from "@/shared/inline-ui/inline-document";
import { NodeViewWrapper } from "./render-surface";
import {
  IntelligentDocumentContext,
  IntelligentPreviewInteractionContext,
  InlineStreamingContext,
} from "./document-context";
import { allowsLocalInteraction } from "./interaction-policy";
import { supportsReactiveVersion } from "./versions";
import { intelligentDefinitions } from "./catalog.generated";
import { useCallback, useContext, useEffect, useState } from "react";
import {
  Node,
  NodeViewContent,
  ReactNodeViewRenderer,
  type NodeViewProps,
} from "@tiptap/react";

const definitions = intelligentDefinitions;
export const inlineDocumentBlockTypes = Object.keys(definitions);
function InlineDocumentNodeView(props: NodeViewProps) {
  const [editing, setEditing] = useState(false);
  const { editor, getPos } = props;
  const blockId = String(props.node.attrs.blockId);
  const registerCapture = useCallback(
    (read: InlineCaptureReader) => registerInlineCapture(editor, blockId, read),
    [editor, blockId],
  );
  useEffect(() => {
    const revealSourceSelection = () => {
      const position = getPos();
      if (!editor.isEditable || position === undefined) return;
      const { from, to } = editor.state.selection;
      const node = editor.state.doc.nodeAt(position);
      if (node && from > position && to < position + node.nodeSize)
        setEditing(true);
    };
    editor.on("selectionUpdate", revealSourceSelection);
    return () => {
      editor.off("selectionUpdate", revealSourceSelection);
    };
  }, [editor, getPos]);
  const toggleSource = () => {
    if (editing) {
      const position = getPos();
      if (position !== undefined) editor.commands.setNodeSelection(position);
    }
    setEditing(!editing);
  };
  return (
    <NodeViewWrapper className="my-3 min-w-0">
      {props.editor.isEditable && (
        <div contentEditable={false} className="mb-2 flex justify-end">
          <button
            type="button"
            onClick={toggleSource}
            aria-expanded={editing}
            className="rounded-workspace-control px-2 py-1 text-sm text-workspace-muted focus-visible:outline-2"
          >
            {editing ? "Show preview" : "Edit source"}
          </button>
        </div>
      )}
      <div
        hidden={!editing}
        className="rounded-workspace-control border border-border bg-background p-3 font-mono text-sm whitespace-pre-wrap"
      >
        <NodeViewContent aria-label="Interactive component source" />
      </div>
      <div hidden={editing} contentEditable={false}>
        <InlineDocumentBlockView
          type={props.node.type.name}
          attrs={props.node.attrs}
          registerCapture={registerCapture}
          source={props.node.textContent}
        />
      </div>
    </NodeViewWrapper>
  );
}
export function InlineDocumentBlockView({
  type,
  attrs,
  source,
  registerCapture,
}: {
  type: string;
  attrs: Record<string, unknown>;
  source: string;
  registerCapture?: (read: InlineCaptureReader) => () => void;
}) {
  const document = useContext(IntelligentDocumentContext);
  const openEntity = useInlineEntityAction(
    document?.companyId,
    document && "projectId" in document ? document.projectId : undefined,
  );
  const mode = useContext(DocumentViewMode);
  const preview = useContext(IntelligentPreviewInteractionContext);
  const streaming = useContext(InlineStreamingContext);
  const capture = isInlineCapture(attrs.inlineCapture, source)
    ? attrs.inlineCapture
    : undefined;
  const supportedCapture =
    attrs.inlineCapture === undefined || capture !== undefined;
  return (
    <div className="min-w-0">
      {type === "uiDocument" &&
      supportsReactiveVersion(type, attrs) &&
      supportedCapture ? (
        <InlineDocument
          renderReference={(name, props, interactive) => (
            <InlineArtifactReference
              companyId={document?.companyId}
              name={name}
              props={props}
              interactive={interactive}
            />
          )}
          key={JSON.stringify(attrs.inlineCapture ?? null)}
          source={source}
          capture={capture}
          registerCapture={registerCapture}
          streaming={streaming}
          openEntity={openEntity}
          readOnly={!allowsLocalInteraction(mode, preview)}
        />
      ) : (
        <p role="status" className="text-sm text-workspace-muted">
          This component is not supported by the inline renderer.
        </p>
      )}
    </div>
  );
}

export function inlineDocumentExtensions() {
  return Object.entries(definitions).map(([name, definition]) =>
    Node.create({
      name,
      group: definition.inline ? "inline" : "block",
      inline: !!definition.inline,
      atom: !definition.children,
      content:
        name === "uiDocument"
          ? "text*"
          : definition.children
            ? "block+"
            : undefined,
      code: name === "uiDocument",
      whitespace: name === "uiDocument" ? "pre" : "normal",
      defining: true,
      marks: "",
      draggable: !definition.inline,
      addAttributes() {
        return Object.fromEntries(
          Object.entries({
            catalogVersion: definition.version,
            ...definition.attrs,
          }).map(([key, value]) => [key, { default: value, rendered: false }]),
        );
      },
      parseHTML() {
        return [
          {
            tag: `${definition.inline ? "span" : "div"}[data-ui-type="${name}"]`,
            getAttrs(element) {
              try {
                const value = JSON.parse(
                  element.getAttribute("data-ui-definition") ?? "{}",
                );
                if (!value || typeof value !== "object" || Array.isArray(value))
                  return false;
                return Object.fromEntries(
                  Object.keys({
                    catalogVersion: definition.version,
                    ...definition.attrs,
                  })
                    .filter((key) =>
                      Object.prototype.hasOwnProperty.call(value, key),
                    )
                    .map((key) => [key, value[key]]),
                );
              } catch {
                return false;
              }
            },
          },
        ];
      },
      renderHTML({ node, HTMLAttributes }) {
        HTMLAttributes = {
          ...HTMLAttributes,
          "data-ui-definition": JSON.stringify(
            Object.fromEntries(
              Object.keys({
                catalogVersion: definition.version,
                ...definition.attrs,
              }).map((key) => [key, node.attrs[key]]),
            ),
          ),
        };
        return definition.children
          ? ["div", { ...HTMLAttributes, "data-ui-type": name }, 0]
          : [
              definition.inline ? "span" : "div",
              { ...HTMLAttributes, "data-ui-type": name },
            ];
      },
      addNodeView() {
        return ReactNodeViewRenderer(InlineDocumentNodeView);
      },
    }),
  );
}
