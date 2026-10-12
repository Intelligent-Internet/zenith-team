import { NodeViewWrapper, type NodeViewProps } from "@tiptap/react";
import { WikiArtifactChip } from "./wiki-artifacts";

/** Citations open their source in both reading and editing views. */
export function WikiArtifactRefView({ node, selected }: NodeViewProps) {
  return (
    <NodeViewWrapper
      as="span"
      className={`wk-artifact-ref${selected ? " is-selected" : ""}`}
    >
      <WikiArtifactChip
        id={String(node.attrs.id)}
        ordinal={
          typeof node.attrs.ordinal === "number" ? node.attrs.ordinal : null
        }
        interactive
      />
    </NodeViewWrapper>
  );
}
