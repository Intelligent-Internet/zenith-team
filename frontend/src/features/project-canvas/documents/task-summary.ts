import type { components } from "@/shared/api/generated/api.generated";
import type { TaskDto } from "@/shared/api/types";

// Match the retained OverviewTask projection: the Agent doing the Task is
// distinct from an Agent assisting a human through the Task conversation.
export function taskBlockSummary(
  task: TaskDto,
): components["schemas"]["OverviewTask"] {
  const agent = task.executedBy.kind === "agent" ? task.executedBy : undefined;
  return {
    id: task.id,
    title: task.title,
    status: task.status,
    assigneeUserId: task.assignee.id,
    assigneeName: task.assignee.name,
    agentId: agent?.id ?? null,
    agentName: agent?.name || undefined,
    revision: task.revision,
  };
}
