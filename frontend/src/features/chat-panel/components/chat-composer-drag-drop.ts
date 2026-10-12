export type FileDragEventLike = {
  dataTransfer?: {
    types?: ArrayLike<string> | Iterable<string> | null;
  } | null;
};

export type DragLeaveEventLike = {
  currentTarget: {
    contains?: (node: Node) => boolean;
  };
  relatedTarget: EventTarget | null;
};

export function dragEventHasFiles(event: FileDragEventLike) {
  return dragTypesIncludeFiles(event.dataTransfer?.types);
}

export function dragTypesIncludeFiles(
  types: ArrayLike<string> | Iterable<string> | null | undefined,
) {
  if (!types) {
    return false;
  }

  return Array.from(types).includes("Files");
}

export function isDragLeavingDropTarget(event: DragLeaveEventLike) {
  if (typeof Node === "undefined") {
    return true;
  }

  return (
    !(event.relatedTarget instanceof Node) ||
    !event.currentTarget.contains?.(event.relatedTarget)
  );
}
