import { isRecord } from "@/features/chat-panel/model/ceo-discovery-message";
import type { ChatRuntimeActivity } from "@/shared/chat/types";

export const WIKI_SEARCH_TOOL = "wiki_search";
export const WIKI_READ_TOOL = "wiki_read";
export const WIKI_WRITE_TOOL = "wiki_write";

export function isWikiToolActivity(activity: ChatRuntimeActivity) {
  return (
    activity.toolName === WIKI_SEARCH_TOOL ||
    activity.toolName === WIKI_READ_TOOL ||
    activity.toolName === WIKI_WRITE_TOOL
  );
}

export type WikiSearchDetail = { query?: string; resultCount?: number };
export type WikiReadDetail = { title?: string; revision?: number };
/** One page a wiki_write call saved; `pageId` is absent when only the input is known. */
export type WikiWrittenPage = {
  pageId?: string;
  status?: string;
  title: string;
};

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

/**
 * The pages a wiki_write call saved, from its result. A failed call, or a
 * result that saved nothing (every page skipped), lists no page. A stored
 * result can be cut off, so a truncated result, or a call still running,
 * falls back to the page titles in the input, which carry no page id and so
 * cannot be linked.
 */
export function wikiWriteDetail(activity: ChatRuntimeActivity): {
  pages: WikiWrittenPage[];
} {
  if (activity.error || activity.status === "failed") {
    return { pages: [] };
  }
  const output = unwrapPayload(activity.output);
  if (isRecord(output) && Array.isArray(output.pages)) {
    return {
      pages: output.pages.flatMap((item): WikiWrittenPage[] => {
        if (!isRecord(item) || typeof item.title !== "string") {
          return [];
        }
        const title = item.title.trim();
        return title
          ? [
              {
                pageId: wikiPageId(item.pageId),
                status: wikiWriteStatus(item.status),
                title,
              },
            ]
          : [];
      }),
    };
  }
  if (activity.output) {
    const text = outputText(activity.output);
    // Truncated output: recover the complete page objects that precede the cut.
    const pages = [...text.matchAll(wikiWrittenPagePattern)]
      .map((match): WikiWrittenPage | undefined => {
        const title = parseJSONString(match[2]);
        return title
          ? {
              pageId: wikiPageId(match[1]),
              status: wikiWriteStatus(match[3]),
              title,
            }
          : undefined;
      })
      .filter((page) => page !== undefined);
    if (pages.length > 0) {
      return { pages };
    }
    // Anything but a cut-off JSON result (an error message, say) saved nothing.
    if (!text.trimStart().startsWith("{")) {
      return { pages: [] };
    }
  }
  const input = unwrapPayload(activity.input);
  if (!isRecord(input) || !Array.isArray(input.pages)) {
    return { pages: [] };
  }
  return {
    pages: input.pages.flatMap((item): WikiWrittenPage[] => {
      const title =
        isRecord(item) && typeof item.title === "string"
          ? item.title.trim()
          : "";
      return title ? [{ title }] : [];
    }),
  };
}

// A saved page as the server serializes it: key, pageId, title, then the
// optional revision and status.
const wikiWrittenPagePattern =
  /"pageId"\s*:\s*"([0-9a-fA-F-]{36})"\s*,\s*"title"\s*:\s*("(?:[^"\\]|\\.)*")(?:\s*,\s*"revision"\s*:\s*\d+)?(?:\s*,\s*"status"\s*:\s*"([^"]*)")?/g;

function wikiPageId(value: unknown) {
  return typeof value === "string" && /^[0-9a-f-]{36}$/i.test(value)
    ? value
    : undefined;
}

function wikiWriteStatus(value: unknown) {
  return typeof value === "string" ? value.trim() || undefined : undefined;
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
  if (Array.isArray(parsed)) {
    return textBlocks(parsed);
  }
  if (isRecord(parsed) && Array.isArray(parsed.content)) {
    return textBlocks(parsed.content);
  }
  if (parsed === undefined) {
    // A stored copy cut off inside a content block: close its text string.
    const cut = /"text"\s*:\s*("(?:[^"\\]|\\.)*)/.exec(value);
    const text = cut ? parseJSON(`${cut[1]}"`) : undefined;
    if (typeof text === "string") {
      return text;
    }
  }
  return value;
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
