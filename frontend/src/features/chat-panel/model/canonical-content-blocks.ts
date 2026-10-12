import type { ChatMessageContentBlock } from "@/shared/chat/types";

type SequencedContentBlock = ChatMessageContentBlock & {
  sequence?: number;
};

export function reserveAssistantTextBlock(
  blocks: ChatMessageContentBlock[] | undefined,
  itemId: string,
  startedSequence: number,
): ChatMessageContentBlock[] {
  const next = blocks ? [...blocks] : [];
  const index = next.findIndex(
    (block) => block.type === "text" && block.itemId === itemId,
  );
  if (index >= 0) {
    next[index] = {
      ...next[index],
      sequence: startedSequence,
    } as SequencedContentBlock;
  } else {
    next.push(
      {
        id: `text:${itemId}`,
        itemId,
        sequence: startedSequence,
        text: "",
        type: "text",
      } as SequencedContentBlock,
    );
  }
  return sortContentBlocks(next);
}

export function appendAssistantTextBlock(
  blocks: ChatMessageContentBlock[] | undefined,
  delta: string,
  itemId: string | undefined,
  startedSequence?: number,
): ChatMessageContentBlock[] {
  const next = blocks ? [...blocks] : [];
  const textIndex = findAssistantTextBlock(next, itemId);

  if (textIndex >= 0) {
    const current = next[textIndex];
    if (current.type === "text") {
      next[textIndex] = {
        ...current,
        ...(itemId && !current.itemId ? { itemId } : {}),
        ...(startedSequence === undefined
          ? {}
          : { sequence: startedSequence }),
        text: `${current.text}${delta}`,
      };
    }
    return sortContentBlocks(next);
  }

  next.push({
    id: itemId ? `text:${itemId}` : `text:stream:${next.length}`,
    ...(itemId ? { itemId } : {}),
    ...(startedSequence === undefined ? {} : { sequence: startedSequence }),
    text: delta,
    type: "text",
  });
  return sortContentBlocks(next);
}

export function completeAssistantTextBlock(
  blocks: ChatMessageContentBlock[] | undefined,
  text: string,
  itemId: string | undefined,
  startedSequence?: number,
): ChatMessageContentBlock[] {
  if (!text) return blocks ?? [];

  const next = blocks ? [...blocks] : [];
  const textIndex = findAssistantTextBlock(next, itemId);
  if (textIndex >= 0) {
    const current = next[textIndex];
    if (current.type === "text") {
      next[textIndex] = {
        ...current,
        ...(itemId && !current.itemId ? { itemId } : {}),
        ...(startedSequence === undefined
          ? {}
          : { sequence: startedSequence }),
        text,
      };
    }
    return sortContentBlocks(next);
  }

  return appendAssistantTextBlock(next, text, itemId, startedSequence);
}

export function reserveReasoningTextBlock(
  blocks: ChatMessageContentBlock[] | undefined,
  itemId: string,
  startedSequence: number,
): ChatMessageContentBlock[] {
  const next = blocks ? [...blocks] : [];
  const index = next.findIndex(
    (block) => block.type === "reasoning" && block.itemId === itemId,
  );
  if (index >= 0) {
    next[index] = {
      ...next[index],
      sequence: startedSequence,
    } as SequencedContentBlock;
  } else {
    next.push(
      {
        id: `reasoning:${itemId}`,
        itemId,
        sequence: startedSequence,
        text: "",
        type: "reasoning",
      } as SequencedContentBlock,
    );
  }
  return sortContentBlocks(next);
}

export function appendReasoningTextBlock(
  blocks: ChatMessageContentBlock[] | undefined,
  delta: string,
  itemId: string | undefined,
  startedSequence?: number,
): ChatMessageContentBlock[] {
  if (!itemId) return blocks ?? [];
  const next = blocks ? [...blocks] : [];
  const reasoningIndex = next.findIndex(
    (block) => block.type === "reasoning" && block.itemId === itemId,
  );
  if (reasoningIndex >= 0) {
    const current = next[reasoningIndex];
    if (current.type === "reasoning") {
      next[reasoningIndex] = {
        ...current,
        ...(startedSequence === undefined
          ? {}
          : { sequence: startedSequence }),
        text: `${current.text}${delta}`,
      } as SequencedContentBlock;
    }
    return sortContentBlocks(next);
  }
  next.push(
    {
      id: `reasoning:${itemId}`,
      itemId,
      ...(startedSequence === undefined ? {} : { sequence: startedSequence }),
      text: delta,
      type: "reasoning",
    } as SequencedContentBlock,
  );
  return sortContentBlocks(next);
}

export function completeReasoningTextBlock(
  blocks: ChatMessageContentBlock[] | undefined,
  text: string,
  itemId: string | undefined,
  startedSequence?: number,
): ChatMessageContentBlock[] {
  if (!itemId || !text) return blocks ?? [];
  const next = blocks ? [...blocks] : [];
  const reasoningIndex = next.findIndex(
    (block) => block.type === "reasoning" && block.itemId === itemId,
  );
  if (reasoningIndex >= 0) {
    const current = next[reasoningIndex];
    if (current.type === "reasoning") {
      next[reasoningIndex] = {
        ...current,
        ...(startedSequence === undefined
          ? {}
          : { sequence: startedSequence }),
        text,
      } as SequencedContentBlock;
    }
    return sortContentBlocks(next);
  }
  return appendReasoningTextBlock(next, text, itemId, startedSequence);
}

export function upsertRuntimeActivityBlock(
  blocks: ChatMessageContentBlock[] | undefined,
  activityId: string,
  previousActivityId?: string,
  startedSequence?: number,
): ChatMessageContentBlock[] {
  const next = blocks ? [...blocks] : [];
  const activityIndex = next.findIndex(
    (block) =>
      block.type === "runtime_activity" &&
      (block.activityId === activityId ||
        (previousActivityId && block.activityId === previousActivityId)),
  );

  if (activityIndex >= 0) {
    const current = next[activityIndex];
    if (current.type === "runtime_activity") {
      next[activityIndex] = {
        ...current,
        activityId,
        id: `activity:${activityId}`,
        ...(startedSequence === undefined
          ? {}
          : { sequence: startedSequence }),
      };
    }
    return sortContentBlocks(next);
  }

  next.push({
    activityId,
    id: `activity:${activityId}`,
    ...(startedSequence === undefined ? {} : { sequence: startedSequence }),
    type: "runtime_activity",
  });
  return sortContentBlocks(next);
}

export function assistantTextFromBlocks(
  blocks: ChatMessageContentBlock[] | undefined,
): string {
  return (blocks ?? [])
    .flatMap((block) => (block.type === "text" ? [block.text] : []))
    .join("");
}

function findAssistantTextBlock(
  blocks: ChatMessageContentBlock[],
  itemId: string | undefined,
): number {
  if (itemId) {
    return blocks.findIndex(
      (block) => block.type === "text" && block.itemId === itemId,
    );
  }
  for (let index = blocks.length - 1; index >= 0; index -= 1) {
    if (blocks[index].type === "text") return index;
  }
  return -1;
}

function sortContentBlocks(
  blocks: ChatMessageContentBlock[],
): ChatMessageContentBlock[] {
  return blocks
    .map((block, index) => ({ block, index }))
    .sort((left, right) => {
      const leftSequence =
        (left.block as SequencedContentBlock).sequence ??
        Number.MAX_SAFE_INTEGER;
      const rightSequence =
        (right.block as SequencedContentBlock).sequence ??
        Number.MAX_SAFE_INTEGER;
      return leftSequence - rightSequence || left.index - right.index;
    })
    .map(({ block }) => block);
}
