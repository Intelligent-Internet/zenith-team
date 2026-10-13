import { Node, type Editor, type JSONContent } from "@tiptap/react";
import type { DiscussionMessage } from "../api/discussions-api";

type References = NonNullable<DiscussionMessage["body"]>["references"];

// Mentions are indivisible editor nodes. Their identity never comes from the
// display name; the wire still carries references into the serialized Markdown.
export const DiscussionMention = Node.create({
  name: "discussionMention",
  group: "inline",
  inline: true,
  atom: true,
  selectable: false,
  marks: "",
  addAttributes() {
    return {
      label: { default: "" },
      reference: { default: null },
      token: { default: "" },
    };
  },
  renderHTML({ node }) {
    return [
      "span",
      { class: "discussion-mention", contenteditable: "false" },
      node.attrs.label,
    ];
  },
  renderText({ node }) {
    return node.attrs.label;
  },
  renderMarkdown(node) {
    return node.attrs?.token ?? "";
  },
});

export function richDiscussionDocument(
  editor: Editor,
  text: string,
  references: References,
) {
  const prefix = `ZENITHMENTION${crypto.randomUUID().replaceAll("-", "")}TOKEN`;
  const mentions = references.filter((ref) =>
    ["user", "agent", "broadcast"].includes(ref.type),
  );
  const tokens = new Map<string, JSONContent>();
  let markdown = text;
  for (const [index, reference] of [...mentions]
    .sort((a, b) => b.start - a.start)
    .entries()) {
    const token = `${prefix}${index}END`;
    tokens.set(token, {
      type: "discussionMention",
      attrs: { label: text.slice(reference.start, reference.end), reference },
    });
    markdown =
      markdown.slice(0, reference.start) +
      token +
      markdown.slice(reference.end);
  }
  const doc = editor.markdown!.parse(markdown);
  const pattern = new RegExp(`${prefix}\\d+END`, "g");
  const visit = (node: JSONContent): JSONContent => ({
    ...node,
    ...(node.content
      ? {
          content: node.content.flatMap((child) => {
            if (child.type !== "text" || !child.text) return [visit(child)];
            const parts: JSONContent[] = [];
            let cursor = 0;
            for (const match of child.text.matchAll(pattern)) {
              if (match.index > cursor)
                parts.push({
                  ...child,
                  text: child.text.slice(cursor, match.index),
                });
              parts.push(tokens.get(match[0])!);
              cursor = match.index + match[0].length;
            }
            if (cursor < child.text.length)
              parts.push({ ...child, text: child.text.slice(cursor) });
            return parts;
          }),
        }
      : {}),
  });
  return visit(doc);
}

export function serializeDiscussionEditor(editor: Editor) {
  const prefix = `ZENITHMENTION${crypto.randomUUID().replaceAll("-", "")}TOKEN`;
  const mentions: { label: string; reference: References[number] }[] = [];
  const visit = (node: JSONContent): JSONContent => {
    if (node.type === "discussionMention") {
      const index = mentions.length;
      mentions.push({
        label: node.attrs!.label,
        reference: node.attrs!.reference,
      });
      return {
        ...node,
        attrs: { ...node.attrs, token: `${prefix}${index}END` },
      };
    }
    return {
      ...node,
      ...(node.content ? { content: node.content.map(visit) } : {}),
    };
  };
  const markdown = editor.markdown!.serialize(visit(editor.getJSON()));
  const references: References = [];
  let text = "";
  let cursor = 0;
  for (const match of markdown.matchAll(
    new RegExp(`${prefix}(\\d+)END`, "g"),
  )) {
    text += markdown.slice(cursor, match.index);
    const mention = mentions[Number(match[1])];
    const start = text.length;
    text += mention.label;
    references.push({ ...mention.reference, start, end: text.length });
    cursor = match.index + match[0].length;
  }
  text += markdown.slice(cursor);
  return { text, references };
}
