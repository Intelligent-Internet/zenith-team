import { useCallback } from "react";
import { useChannelTitles } from "@/features/discussions/model/channel-titles";
import { notificationConversationId } from "@/features/notifications/lib/notification-navigation";
import {
  type InboxItem,
  namedDiscussionNotice,
} from "@/features/notifications/model/notification";

// The body an Inbox row shows. A channel notice names its channel from the
// viewer's channel list; every other notice shows its stored words.
export function useNoticeBody() {
  const channelTitle = useChannelTitles();
  return useCallback(
    (item: InboxItem) =>
      item.kind === "discussion_mention"
        ? namedDiscussionNotice(
            item.body,
            channelTitle(item.companyId, notificationConversationId(item)),
          )
        : item.body,
    [channelTitle],
  );
}
