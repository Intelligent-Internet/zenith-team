import { createChatWorkspaceHref } from "@/features/chat-workspace/model/chat-workspace-route";
import type { ArtifactDetail } from "./run-deliverable";

export function artifactSourceHref(detail: ArtifactDetail, artifactId: string) {
  const source = detail.source;
  const target =
    source?.type === "channel" || source?.type === "human_direct"
      ? {
          kind: "discussion" as const,
          discussionType: source.type,
          conversationId: source.conversationId,
        }
      : source?.type === "task" && source.taskId
        ? {
            kind: "task" as const,
            taskId: source.taskId,
            conversationId: source.conversationId,
          }
        : source?.type === "direct" && source.agentId
          ? {
              kind: "direct" as const,
              agentId: source.agentId,
              conversationId: source.conversationId,
            }
          : undefined;
  return target
    ? createChatWorkspaceHref(target, {
        ...(detail.runId
          ? { artifact: { artifactId, runId: detail.runId } }
          : {}),
      })
    : undefined;
}
