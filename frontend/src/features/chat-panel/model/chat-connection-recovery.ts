export type AgentRealtimeConnectionStatus =
  | "connected"
  | "connecting"
  | "error"
  | "idle";

export type ChatConnectionRecoveryNotice =
  | {
      visible: false;
    }
  | {
      detail: string;
      label: string;
      role: "alert" | "status";
      tone: "checking" | "danger";
      visible: true;
    };

export function createChatConnectionRecoveryNotice(
  status: AgentRealtimeConnectionStatus,
): ChatConnectionRecoveryNotice {
  if (status === "connecting") {
    return {
      detail: "Restoring live updates for this conversation.",
      label: "Reconnecting conversation",
      role: "status",
      tone: "checking",
      visible: true,
    };
  }

  if (status === "error") {
    return {
      detail: "Zenith Team will recover when the connection is available again.",
      label: "Conversation disconnected",
      role: "alert",
      tone: "danger",
      visible: true,
    };
  }

  return { visible: false };
}
