import { fromMarkdown } from "mdast-util-from-markdown";
import { gfmStrikethroughFromMarkdown } from "mdast-util-gfm-strikethrough";
import { gfmStrikethrough } from "micromark-extension-gfm-strikethrough";
import {
  compactNotificationText as compactText,
  truncateNotificationText as truncatePreview,
} from "./notification-text";
import type {
  DiscussionMessage,
  DiscussionSummary,
} from "@/features/discussions/api/discussions-api";

export type DiscussionNotificationContent = {
  conversation: Pick<
    DiscussionSummary,
    "type" | "title" | "memberCount" | "agentCount"
  >;
  message: Pick<
    DiscussionMessage,
    | "author"
    | "body"
    | "attachments"
    | "attachmentsWithheld"
    | "artifacts"
    | "threadRootId"
  >;
};

export type DiscussionNotificationCopy = { title: string; body: string };

// Only call with freshly authorized content. Inbox rows and realtime hints
// deliberately retain their bodyless copy; this preview is transient.
export function discussionNotificationCopy(
  { conversation, message }: DiscussionNotificationContent,
  companyName: string,
): DiscussionNotificationCopy {
  const sender = truncatePreview(
    compactText(message.author.displayName) || "Someone",
    40,
  );
  const company = truncatePreview(
    compactText(companyName) || "Zenith Team",
    24,
  );
  const name = truncatePreview(compactText(conversation.title), 48);
  const context =
    conversation.type === "channel"
      ? name
        ? ` in #${name}`
        : " in a channel"
      : conversation.memberCount + conversation.agentCount > 2
        ? ` in ${name || "a group message"}`
        : "";
  const preview = message.body ? markdownPreview(message.body.markdown) : "";
  const text = preview || attachmentPreview(message);
  return {
    title: truncatePreview(`${sender}${context} (${company})`, 120),
    body: truncatePreview(
      message.threadRootId ? `Replied in thread: ${text}` : text,
      260,
    ),
  };
}

function attachmentPreview(message: DiscussionNotificationContent["message"]) {
  const count = message.attachments.length;
  if (count) {
    const imagesOnly = message.attachments.every((file) =>
      file.contentType.startsWith("image/"),
    );
    const noun = imagesOnly ? "image" : "file";
    return count === 1
      ? imagesOnly
        ? "Sent an image"
        : "Sent a file"
      : `Sent ${count} ${noun}s`;
  }
  if (message.artifacts.length) {
    return message.artifacts.length === 1
      ? "Shared an output"
      : `Shared ${message.artifacts.length} outputs`;
  }
  return message.attachmentsWithheld ? "Sent a file" : "Sent a message";
}

type MarkdownNode = {
  type: string;
  value?: string;
  children?: MarkdownNode[];
};

function markdownPreview(markdown: string) {
  const tree = fromMarkdown(markdown, {
    extensions: [gfmStrikethrough()],
    mdastExtensions: [gfmStrikethroughFromMarkdown()],
  });
  function text(node: MarkdownNode): string {
    if (node.type === "html" || node.type === "definition") return "";
    if (node.type === "break") return " ";
    if (node.value !== undefined) {
      return node.type === "code" ? `${node.value} ` : node.value;
    }
    const contents = node.children?.map(text).join("") ?? "";
    return ["paragraph", "listItem", "blockquote"].includes(node.type)
      ? `${contents} `
      : contents;
  }
  return compactText(text(tree));
}
