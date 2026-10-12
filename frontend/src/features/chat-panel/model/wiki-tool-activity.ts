import { isRecord } from "@/features/chat-panel/model/ceo-discovery-message";
import type { ChatRuntimeActivity } from "@/shared/chat/types";

export const WIKI_SEARCH_TOOL = "wiki_search";
export const WIKI_READ_TOOL = "wiki_read";

export function isWikiToolActivity(activity: ChatRuntimeActivity) {
  return (
    activity.toolName === WIKI_SEARCH_TOOL ||
    activity.toolName === WIKI_READ_TOOL
  );
}

export type WikiSearchDetail = { query?: string; resultCount?: number };
export type WikiReadDetail = { title?: string; revision?: number };

/** The query the Agent searched for and, once finished, how many pages matched. */
export function wikiSearchDetail(
  activity: ChatRuntimeActivity,
): WikiSearchDetail {
  const input = unwrapPayload(activity.input);
  const query =
    isRecord(input) && typeof input.query === "string"
      ? input.query.trim() || undefined
      : undefined;
  const output = unwrapPayload(activity.output);
  let resultCount: number | undefined;
  if (isRecord(output)) {
    if (typeof output.total === "number") {
      resultCount = output.total;
    } else if (Array.isArray(output.items)) {
      resultCount = output.items.length;
    }
  } else if (activity.output) {
    // Output that is not a single JSON document (e.g. text wrapped around it).
    const total = /"total"\s*:\s*(\d+)/.exec(activity.output);
    if (total) {
      resultCount = Number(total[1]);
    }
  }
  return { query, resultCount };
}

/** The page title and content revision the Agent read, from the tool result. */
export function wikiReadDetail(activity: ChatRuntimeActivity): WikiReadDetail {
  if (!activity.output) {
    return {};
  }
  const output = unwrapPayload(activity.output);
  if (isRecord(output)) {
    return {
      title:
        typeof output.title === "string"
          ? output.title.trim() || undefined
          : undefined,
      revision:
        typeof output.revision === "number" ? output.revision : undefined,
    };
  }
  // wiki_read returns up to 24,000 characters; a stored copy can be cut off
  // mid-content, but title and revision come first.
  const text = outputText(activity.output);
  const title = /"title"\s*:\s*("(?:[^"\\]|\\.)*")/.exec(text);
  const revision = /"revision"\s*:\s*(\d+)/.exec(text);
  return {
    title: title ? parseJSONString(title[1]) : undefined,
    revision: revision ? Number(revision[1]) : undefined,
  };
}

export function wikiSearchResultLabel(count: number | undefined) {
  if (count === undefined) {
    return undefined;
  }
  return count === 1 ? "1 result" : `${count} results`;
}

// MCP results can arrive as the bare JSON object, as a `content` list of text
// blocks, or with `structuredContent`; accept each.
function unwrapPayload(value: string | undefined): unknown {
  if (!value) {
    return undefined;
  }
  const parsed = parseJSON(value);
  if (Array.isArray(parsed)) {
    return parseJSON(textBlocks(parsed));
  }
  if (isRecord(parsed)) {
    if (isRecord(parsed.structuredContent)) {
      return parsed.structuredContent;
    }
    if (Array.isArray(parsed.content)) {
      return parseJSON(textBlocks(parsed.content));
    }
  }
  return parsed;
}

function outputText(value: string) {
  const parsed = parseJSON(value);
  return Array.isArray(parsed) ? textBlocks(parsed) : value;
}

function textBlocks(blocks: unknown[]) {
  return blocks
    .flatMap((block) =>
      isRecord(block) && typeof block.text === "string" ? [block.text] : [],
    )
    .join("");
}

function parseJSON(value: string): unknown {
  try {
    return JSON.parse(value);
  } catch {
    return undefined;
  }
}

function parseJSONString(value: string) {
  const parsed = parseJSON(value);
  return typeof parsed === "string" ? parsed.trim() || undefined : undefined;
}
