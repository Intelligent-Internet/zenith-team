import type { ChatTeamId } from "@/shared/chat/chat-team";

export type TeamNodeStatus = "active" | "idle";

/** One Agent as the chat surfaces present it: its lane, identity and look. */
export type TeamNodeData = {
  accentColor?: string;
  agentId?: string;
  label: string;
  agentRole: string;
  currentWork: string;
  status: TeamNodeStatus;
  notificationCount: number;
  teamId: ChatTeamId;
  iconName?: string;
  iconSrc: string;
  href: string;
  available?: boolean;
  disabled?: boolean;
};
