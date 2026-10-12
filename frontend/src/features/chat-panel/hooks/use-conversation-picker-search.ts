import { useEffect } from "react";
import { useLazySearchWorkspaceConversationsQuery } from "@/shared/api/workspace-api";
import type {
  CompanyConversationSearchResult,
  CompanyTaskSearchResult,
} from "@/shared/api/types";

type ConversationPickerResult =
  CompanyConversationSearchResult | CompanyTaskSearchResult;

export function useConversationPickerSearch({
  companyId,
  isOpen,
  query,
}: {
  companyId: string;
  isOpen: boolean;
  query: string;
}) {
  const [search, searchResult] = useLazySearchWorkspaceConversationsQuery();
  const normalizedQuery = query.trim();
  const canSearch = [...normalizedQuery].length >= 2;
  const isCurrentResult =
    searchResult.originalArgs?.companyId === companyId &&
    searchResult.originalArgs.request.query === normalizedQuery;

  useEffect(() => {
    if (!companyId || !isOpen || !canSearch) {
      return;
    }

    const timeout = window.setTimeout(() => {
      void search({
        companyId,
        request: {
          includeArchived: false,
          kinds: ["task", "conversation"],
          limit: 20,
          query: normalizedQuery,
        },
      });
    }, 250);

    return () => window.clearTimeout(timeout);
  }, [canSearch, companyId, isOpen, normalizedQuery, search]);

  const results = isCurrentResult
    ? ((searchResult.currentData?.items ?? []).filter(
        (result): result is ConversationPickerResult =>
          result.kind === "task" || result.kind === "conversation",
      ) as ConversationPickerResult[])
    : [];

  return {
    canSearch,
    error:
      isCurrentResult && searchResult.isError
        ? "Chats could not be searched. Try again."
        : null,
    isLoading:
      Boolean(companyId && isOpen && canSearch) &&
      (!isCurrentResult || searchResult.isFetching),
    results,
  };
}
