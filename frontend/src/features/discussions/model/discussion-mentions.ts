import type { Element, Root, RootContent } from "hast";
import type { DiscussionMessage } from "../api/discussions-api";

type Body = NonNullable<DiscussionMessage["body"]>;

export function discussionMentionRanges(references: Body["references"]) {
  return references
    .filter((reference) =>
      ["user", "agent", "broadcast"].includes(reference.type),
    )
    .sort((a, b) => a.start - b.start);
}

// The Agents a draft tags, once each, in the order they appear. A name comes
// from the chat's Agents; a tag of an Agent no longer listed keeps the words
// the person picked, without the "@".
export function discussionTaggedAgentNames(
  text: string,
  references: Body["references"],
  agents: readonly { agentId: string; name: string }[],
) {
  const names = new Map<string, string>();
  for (const { type, agentId, start, end } of discussionMentionRanges(
    references,
  )) {
    if (type !== "agent" || !agentId || names.has(agentId)) continue;
    names.set(
      agentId,
      agents.find((agent) => agent.agentId === agentId)?.name ??
        text.slice(start, end).replace(/^@/, ""),
    );
  }
  return [...names.values()];
}

// The line above the message box while a draft tags an Agent (Access
// DESIGN 12). A tag runs the Agent with the tagger's own access, and
// everyone in the chat reads its answer. A chat with no channel name is a
// DM or group DM. Undefined when the draft tags no Agent.
export function discussionAgentHint(
  agentNames: readonly string[],
  channelName?: string,
) {
  const [first, second] = agentNames;
  if (first === undefined) return undefined;
  const place = channelName ? `#${channelName}` : "this chat";
  if (agentNames.length === 1)
    return `${first} works with your access. Everyone in ${place} will see its answer.`;
  const who =
    agentNames.length === 2
      ? `${first} and ${second}`
      : `${first} and ${agentNames.length - 1} other agents`;
  return `${who} work with your access. Everyone in ${place} will see their answers.`;
}

// Offsets refer to the original Markdown, not the rendered text. Only decorate
// literal text nodes so escapes, links and code cannot accidentally become tags.
export function discussionMentionHighlightPlugin(
  body: DiscussionMessage["body"],
) {
  const ranges = discussionMentionRanges(body?.references ?? []);
  return (tree: Root) => {
    const visit = (parent: Root | Element) => {
      parent.children = parent.children.flatMap((node): RootContent[] => {
        if (node.type === "element") {
          if (!["a", "code", "pre", "blockquote"].includes(node.tagName))
            visit(node);
          return [node];
        }
        const start = node.position?.start.offset;
        const end = node.position?.end.offset;
        if (
          node.type !== "text" ||
          start === undefined ||
          end === undefined ||
          body?.markdown.slice(start, end) !== node.value
        )
          return [node];
        const matches = ranges.filter(
          (range) =>
            range.start >= start && range.end <= end && range.end > range.start,
        );
        const result: RootContent[] = [];
        let cursor = 0;
        for (const range of matches) {
          const from = range.start - start;
          const to = range.end - start;
          if (from < cursor) continue;
          result.push({ type: "text", value: node.value.slice(cursor, from) });
          result.push({
            type: "element",
            tagName: "mark",
            properties: { className: ["discussion-mention"] },
            children: [{ type: "text", value: node.value.slice(from, to) }],
          });
          cursor = to;
        }
        result.push({ type: "text", value: node.value.slice(cursor) });
        return result;
      });
    };
    visit(tree);
  };
}

export function remapDiscussionReferences(
  previous: string,
  next: string,
  references: Body["references"],
) {
  let start = 0;
  while (
    start < previous.length &&
    start < next.length &&
    previous[start] === next[start]
  )
    start++;
  let oldEnd = previous.length,
    newEnd = next.length;
  while (
    oldEnd > start &&
    newEnd > start &&
    previous[oldEnd - 1] === next[newEnd - 1]
  ) {
    oldEnd--;
    newEnd--;
  }
  const delta = newEnd - oldEnd;
  return references.flatMap((reference) => {
    if (reference.end <= start) return [reference];
    if (reference.start >= oldEnd)
      return [
        {
          ...reference,
          start: reference.start + delta,
          end: reference.end + delta,
        },
      ];
    return [];
  });
}
