import { Circle, RadioIndicator } from "@/shared/ui/icons";
import { useMemberPresence } from "../model/use-member-presence";

export function DiscussionMemberPresence({ userId }: { userId: string }) {
  const presence = useMemberPresence([userId]);
  const member = presence.currentData?.items.find(
    (item) => item.userId === userId,
  );
  // Loading or failed presence is unknown, never evidence that someone is offline.
  if (!member) return null;
  const label = member.online
    ? "Active"
    : member.manualAway
      ? "Away"
      : "Offline";
  const StatusIcon = member.online ? RadioIndicator : Circle;
  return (
    <span
      role="img"
      aria-label={label}
      title={label}
      style={{
        display: "inline-flex",
        flexShrink: 0,
        color: member.online
          ? "var(--workspace-native-success)"
          : "var(--workspace-native-text-muted)",
      }}
    >
      <StatusIcon size={member.online ? 10 : 14} aria-hidden="true" />
    </span>
  );
}
