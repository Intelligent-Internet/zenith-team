import { skipToken } from "@reduxjs/toolkit/query";
import { useMemo } from "react";

import {
  useGetConversationArtifactsInfiniteQuery,
  type ConversationArtifactsContext,
} from "@/features/chat-panel/api/conversation-artifacts-api";

export function useConversationArtifacts(
  context: ConversationArtifactsContext | undefined,
) {
  const argument = context ?? skipToken;
  const query = useGetConversationArtifactsInfiniteQuery(argument, {
    refetchOnMountOrArgChange: 30,
  });
  // RTK retains the previous data after a failed refetch, including a denial
  // after membership or artifact access was revoked. Never render that copy.
  const pages = query.isError ? undefined : query.currentData?.pages;
  const items = useMemo(
    () => pages?.flatMap((page) => page.items) ?? [],
    [pages],
  );

  return {
    items,
    count: items.length,
    error: query.error ? "Artifacts could not be loaded. Try again." : null,
    hasMore: query.hasNextPage,
    isLoading: !query.currentData && (query.isLoading || query.isFetching),
    isFetching: query.isFetching,
    isLoadingMore: query.isFetchingNextPage,
    loadMore: query.fetchNextPage,
    refresh: query.refetch,
  };
}
