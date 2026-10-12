import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { useCompanyPermissions } from "@/shared/permissions";
import { discussionsApi, type DiscussionSummary } from "../api/discussions-api";
import { useDiscussionRuntime } from "../components/discussion-provider";

export type DiscussionSelection = {
  conversationId: string;
  type: DiscussionSummary["type"];
  manage?: boolean;
  messageId?: string;
  threadRootId?: string;
};

export function useDiscussionNavigation(
  query: string,
  showArchivedDirectMessages = false,
) {
  const runtime = useDiscussionRuntime();
  const { can } = useCompanyPermissions();
  const [search, setSearch] = useState("");
  const [archived, setArchived] = useState(false);
  const [includeHidden, setIncludeHidden] = useState(false);
  const [manage, setManage] = useState(false);
  const canManage = can("channels.manage");
  const managing = manage && canManage;
  const searching = Boolean(query.trim());
  const [updatePersonalState] =
    discussionsApi.useUpdateDiscussionPersonalStateMutation();
  useEffect(() => {
    const timer = setTimeout(() => setSearch(query.trim()), 250);
    return () => clearTimeout(timer);
  }, [query]);
  const args = useMemo(
    () => ({ ...runtime.session, query: search, archived, includeHidden }),
    [runtime, search, archived, includeHidden],
  );
  const people = discussionsApi.useListHumanDiscussionsInfiniteQuery({
    ...runtime.session,
    includeHidden: includeHidden || showArchivedDirectMessages || searching,
  });
  useEffect(() => {
    // Name search also covers older DMs outside the loaded sidebar pages.
    if (
      searching &&
      search &&
      people.hasNextPage &&
      !people.isFetching &&
      !people.isError
    )
      void people.fetchNextPage();
  }, [
    search,
    searching,
    people.hasNextPage,
    people.isFetching,
    people.isError,
    people.fetchNextPage,
  ]);
  const groups = discussionsApi.useListChatChannelsInfiniteQuery(args, {
    skip: managing,
  });
  const management = discussionsApi.useListManagedChannelsInfiniteQuery(args, {
    skip: !managing,
  });
  const dms = people.currentData?.pages.flatMap((page) => page.items) ?? [];
  const channels =
    groups.currentData?.pages.flatMap((page) => page.items) ?? [];
  const ids = [...dms, ...channels]
    .map((item) => item.id)
    .sort()
    .join();
  // Typing changes the runtime draft; the navigator only observes access changes.
  const revokedIds = useSyncExternalStore(
    runtime.subscribe,
    () =>
      ids
        .split(",")
        .filter((id) => runtime.status(id)?.phase === "revoked")
        .join(),
    () => "",
  );
  const revoked = new Set(revokedIds.split(","));
  const visible = (item: DiscussionSummary) =>
    (includeHidden || !item.userState?.hiddenAt) && !revoked.has(item.id);
  useEffect(() => {
    runtime.setVisibleIds(ids ? ids.split(",") : []);
    return () => runtime.setVisibleIds([]);
  }, [ids, runtime]);

  return {
    archived,
    canManage,
    includeHidden,
    managing,
    setArchived,
    setIncludeHidden,
    setManage,
    people,
    groupQuery: managing ? management : groups,
    dms: dms.filter(
      (item) =>
        (searching || includeHidden || !item.userState?.hiddenAt) &&
        !revoked.has(item.id),
    ),
    archivedDms: dms.filter(
      (item) => item.userState?.hiddenAt && !revoked.has(item.id),
    ),
    channels: managing ? [] : channels.filter(visible),
    managed: managing
      ? (management.currentData?.pages.flatMap((page) => page.items) ?? [])
      : [],
    async togglePin(conversationId: string, pinned: boolean) {
      const item = [...dms, ...channels].find(
        (item) => item.id === conversationId,
      );
      if (!item || !runtime.active) return false;
      await updatePersonalState({
        ...runtime.session,
        conversationId,
        view: "root",
        body: { expectedRevision: item.userState?.revision ?? 0, pinned },
      }).unwrap();
      runtime.schedule(true);
      return runtime.active;
    },
    async toggleArchive(conversationId: string, hidden: boolean) {
      const item = dms.find((item) => item.id === conversationId);
      if (!item || !runtime.active) return false;
      await updatePersonalState({
        ...runtime.session,
        conversationId,
        view: "root",
        body: {
          expectedRevision: Math.max(
            item.userState?.revision ?? 0,
            runtime.state(conversationId)?.userState?.revision ?? 0,
          ),
          hidden,
        },
      }).unwrap();
      runtime.schedule(true);
      return runtime.active;
    },
  };
}

export type DiscussionNavigation = ReturnType<typeof useDiscussionNavigation>;
