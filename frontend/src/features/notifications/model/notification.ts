import type {
  components as ApiComponents,
  operations as ApiOperations,
} from "@/shared/api/generated/api.generated";

type InboxListQuery = NonNullable<
  ApiOperations["listInbox"]["parameters"]["query"]
>;

export type InboxItem = ApiComponents["schemas"]["InboxItem"];
export type InboxCounts = ApiComponents["schemas"]["InboxCounts"];
export type InboxPage =
  ApiOperations["listInbox"]["responses"][200]["content"]["application/json"];
export type InboxMutationResult =
  ApiOperations["mutateInboxItem"]["responses"][200]["content"]["application/json"];
export type InboxReadAllResult =
  ApiOperations["readAllInbox"]["responses"][200]["content"]["application/json"];
export type InboxListOptions = InboxListQuery;
export type InboxFilterKind = NonNullable<InboxListQuery["kind"]>;
export type NotificationStatus = NonNullable<InboxListQuery["status"]>;
export type NotificationMutation =
  ApiOperations["mutateInboxItem"]["parameters"]["path"]["mutation"];

export type NotificationKind = InboxItem["kind"];

// Empty Company scope addresses the authenticated account Inbox.
export const ACCOUNT_INBOX_SCOPE = "";

export const notificationKinds = [
  "calendar_reminder",
  "calendar_update",
  "approval_request",
  "question_request",
  "task_assigned",
  "blocker",
  "run_completed",
  "run_failed",
  "runtime_offline",
  "invitation_renewal",
  "discussion_mention",
  "task_run_requested",
  "task_handoff",
  "analytics_budget",
  "huddle_missed",
  "project_access_granted",
] as const satisfies readonly NotificationKind[];

export const notificationKindLabels: Record<NotificationKind, string> = {
  calendar_reminder: "Calendar reminder",
  calendar_update: "Calendar update",
  approval_request: "Approval",
  blocker: "Blocker",
  question_request: "Question",
  run_completed: "Completed",
  run_failed: "Failed",
  runtime_offline: "Machine offline",
  invitation_renewal: "Invitation",
  task_assigned: "Assigned",
  discussion_mention: "Chat activity",
  task_run_requested: "Waiting for you",
  task_handoff: "Handed over",
  analytics_budget: "Usage budget",
  huddle_missed: "Missed Huddle",
  project_access_granted: "Access",
};

export function isNotificationKind(value: unknown): value is NotificationKind {
  return (
    typeof value === "string" &&
    (notificationKinds as readonly string[]).includes(value)
  );
}

// Notices that report something good rather than ask for attention: an
// unread one is tinted neutral grey, from the theme's muted text color so it
// shows in light and dark, not with the alert red.
export function neutralNotice(kind: NotificationKind): boolean {
  return kind === "project_access_granted";
}

// A channel notice is stored naming only who wrote — "Mai Owner wrote in a
// channel." — because a notice never keeps a channel's title. The app names
// the channel when it shows the notice, from the channel
// list the viewer can see now; without a title the stored words stand.
export function namedDiscussionNotice(
  body: string,
  channelTitle: string | undefined,
): string {
  if (!channelTitle) return body;
  if (body.endsWith(" in a channel.")) {
    return `${body.slice(0, -" a channel.".length)} #${channelTitle}.`;
  }
  if (body.endsWith(" in a thread.")) {
    return `${body.slice(0, -".".length)} in #${channelTitle}.`;
  }
  return body;
}
