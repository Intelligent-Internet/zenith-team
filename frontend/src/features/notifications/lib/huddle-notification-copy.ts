import type { HuddleInvitation } from "@/features/discussions/api/huddle-api";
import {
  compactNotificationText,
  truncateNotificationText,
} from "./notification-text";

export type HuddleNotificationContent = Pick<
  HuddleInvitation,
  "inviterName" | "sourceConversation"
>;

// The recipient REST read omits the source when its discussion is inaccessible.
export function huddleNotificationCopy(
  invitation: HuddleNotificationContent,
  companyName: string,
  missed = false,
) {
  const sender = truncateNotificationText(
    compactNotificationText(invitation.inviterName ?? "") || "A teammate",
    40,
  );
  const company = truncateNotificationText(
    compactNotificationText(companyName) || "Zenith Team",
    24,
  );
  const source = invitation.sourceConversation;
  const name = truncateNotificationText(
    compactNotificationText(source?.title ?? ""),
    60,
  );
  const context = source
    ? source.type === "channel"
      ? name
        ? `#${name}`
        : "Channel"
      : name || "Direct message"
    : "";
  const location = [context, company].filter(Boolean).join(" · ");
  return {
    title: missed
      ? `Missed Huddle from ${sender}`
      : `${sender} invited you to a Huddle`,
    body: missed ? `${location} · Open Chat to follow up.` : location,
  };
}
