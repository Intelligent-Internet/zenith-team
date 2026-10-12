import type { InboxItem } from "@/features/notifications/model/notification";

export function desktopNotificationCopy(
  item: Pick<InboxItem, "kind" | "title" | "body">,
  companyName: string,
) {
  // Company budget titles already name their owner. Agent budget titles still
  // need the Company prefix to identify the workspace.
  const namesCompany =
    item.kind === "analytics_budget" &&
    item.title.startsWith(`${companyName} `);
  return {
    title: namesCompany ? item.title : `${companyName} · ${item.title}`,
    body: item.body,
  };
}
