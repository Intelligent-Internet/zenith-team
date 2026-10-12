import { useCallback } from "react";
import { useDiscussionRuntime } from "../components/discussion-provider";
import { discussionsApi } from "../api/discussions-api";
import type { RootState } from "@/state/store";

// A notice never stores a channel's title (C0-CONTRACT §5), so a surface
// outside the chat names the channel from the channel list the viewer can
// see now. These read that list by conversation id and answer undefined
// when it has not loaded the channel, so the caller keeps its own words.

// Every channel list already loaded for the Company, whatever its filter.
export function selectChannelTitle(
  state: RootState,
  companyId: string,
  conversationId: string,
): string | undefined {
  for (const args of discussionsApi.util.selectCachedArgsForQuery(
    state,
    "listChatChannels",
  )) {
    if (args.companyId !== companyId) continue;
    const pages =
      discussionsApi.endpoints.listChatChannels.select(args)(state).data
        ?.pages ?? [];
    for (const page of pages) {
      const channel = page.items.find(
        (item) => item.id === conversationId && item.type === "channel",
      );
      if (channel) return channel.title;
    }
  }
  return undefined;
}

// Loads the channel list the chat sidebar opens with (the same cache entry
// when both are on screen) and names channels from it. Only the current
// Company's list is loaded; a notice from another Company stays unnamed.
export function useChannelTitles() {
  const runtime = useDiscussionRuntime();
  const { companyId } = runtime.session;
  const channels = discussionsApi.useListChatChannelsInfiniteQuery(
    { ...runtime.session, query: "", archived: false, includeHidden: false },
    { skip: !companyId },
  );
  const pages = channels.currentData?.pages;
  return useCallback(
    (
      itemCompanyId: string | null | undefined,
      conversationId: string | null,
    ) =>
      itemCompanyId === companyId && conversationId
        ? pages
            ?.flatMap((page) => page.items)
            .find(
              (item) => item.id === conversationId && item.type === "channel",
            )?.title
        : undefined,
    [companyId, pages],
  );
}
