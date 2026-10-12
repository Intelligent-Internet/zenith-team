import type { Element, Root, Text } from "hast";

import {
  findChatMentionRanges,
  type ChatMentionMember,
} from "@/features/chat-panel/model/chat-mentions";

export function rehypeChatMentions({
  members,
}: {
  members: readonly Pick<ChatMentionMember, "name">[];
}) {
  function highlightMentions(node: Root | Element) {
    for (let index = 0; index < node.children.length; index += 1) {
      const child = node.children[index];
      if (child.type === "element") {
        if (!["a", "code", "pre", "kbd", "math"].includes(child.tagName)) {
          highlightMentions(child);
        }
        continue;
      }
      if (child.type !== "text") continue;

      const ranges = findChatMentionRanges(child.value, members);
      if (!ranges.length) continue;

      const replacement: Array<Element | Text> = [];
      let offset = 0;
      for (const { start, end } of ranges) {
        if (start > offset) {
          replacement.push({
            type: "text",
            value: child.value.slice(offset, start),
          });
        }
        replacement.push({
          type: "element",
          tagName: "span",
          properties: { className: ["chat-message-mention"] },
          children: [{ type: "text", value: child.value.slice(start, end) }],
        });
        offset = end;
      }
      if (offset < child.value.length) {
        replacement.push({ type: "text", value: child.value.slice(offset) });
      }
      node.children.splice(index, 1, ...replacement);
      index += replacement.length - 1;
    }
  }

  return highlightMentions;
}
