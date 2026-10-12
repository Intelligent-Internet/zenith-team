import type {
  ActivityDto,
  ActivityEventSummary,
  AgentSummary,
  CommonStatus,
  CompanyResourceDto,
  ResourceSummary,
  TaskDto,
  TaskSummary,
  AgentDto,
} from "@/shared/api/types";
import { activityActorLabel } from "@/shared/lib/activity-actor";

/** The root Agent of a Company: the one that reports to nobody. */
export function isRootAgent(agent: Pick<AgentDto, "reportsTo">) {
  return agent.reportsTo === null;
}

export function findRootAgent<
  T extends Pick<AgentDto, "reportsTo" | "archivedAt">,
>(agents: readonly T[]): T | undefined {
  return (
    agents.find((agent) => !agent.archivedAt && isRootAgent(agent)) ??
    agents.find(isRootAgent)
  );
}

export function agentToAgentSummary(agent: AgentDto): AgentSummary {
  return {
    agent_role: isRootAgent(agent) ? "ceo" : "agent",
    display_name: agent.name,
    id: agent.id,
    profile: agent.instructions || agent.title,
    status: agentStatus(agent),
  };
}

export function taskToTaskSummary(task: TaskDto): TaskSummary {
  return {
    created_at: task.createdAt,
    description: task.description || null,
    id: task.number,
    idempotency_key: null,
    owner_agent_id:
      task.executedBy.kind === "agent" ? task.executedBy.id : null,
    priority: task.priority,
    source_refs: {
      conversation_id: task.conversationId,
      row_version: task.revision,
    },
    status: taskStatus(task.status),
    task_status: task.status,
    task_id: task.id,
    key: task.key,
    title: task.title,
    updated_at: task.updatedAt,
    version: task.revision,
  };
}

export function activityToActivityEvent(
  activity: ActivityDto,
): ActivityEventSummary {
  const summary = activitySummary(activity);
  return {
    action: activity.action,
    actor: activity.actor,
    on_behalf_of: activity.onBehalfOf,
    run_id: activity.runId,
    created_at: activity.createdAt,
    data: activity.details,
    revision: activity.revision,
    entity: activity.entity,
    event_id: activity.id,
    summary,
  };
}

function activitySummary(activity: ActivityDto) {
  const entityLabel =
    activity.entity.title ??
    activity.entity.key ??
    groupRowName(activity) ??
    humanizeEventType(activity.entity.type);
  const action = activity.action
    .slice(activity.action.indexOf(".") + 1)
    .replaceAll("_", " ");
  return `${activityActorLabel(activity)}: ${entityLabel} — ${action}`;
}

// A Group row (group.created and the rest, Access TECH D3) carries the
// Group's name as it was then; it has no entity title of its own.
function groupRowName(activity: ActivityDto): string | null {
  if (activity.entity.type !== "group") return null;
  const payload = (activity.details as { payload?: unknown }).payload;
  const name =
    payload && typeof payload === "object"
      ? (payload as { name?: unknown }).name
      : undefined;
  return typeof name === "string" && name ? name : null;
}

export function projectResourceToResourceSummary(
  resource: CompanyResourceDto,
): ResourceSummary {
  return {
    agent_id: null,
    content: {},
    content_url: null,
    created_at: resource.createdAt,
    description: `${resource.resourceRef.daemonName} local directory connection`,
    file_size: null,
    file_type: "Local directory",
    id: resource.id,
    kind: "tool",
    metadata: {
      resource_type: resource.resourceType,
      row_version: resource.revision,
      daemon_status: resource.daemonStatus,
      daemon_last_seen_at: resource.daemonLastSeenAt,
      created_by_id: resource.createdBy.id,
      created_by_name: resource.createdBy.name,
      ...resource.metadata,
      ...resource.resourceRef,
    },
    mime_type: "application/vnd.boardly.resource",
    preview_kind: "unknown",
    preview_status: "unsupported",
    preview_url: null,
    resource_id: resource.id,
    source_conversation_id: null,
    source_message_id: null,
    size_bytes: 0,
    source_type: "system",
    source_tool_call_id: null,
    status: resource.daemonStatus === "online" ? "active" : "pending",
    thumbnail_url: null,
    title: resource.label,
    updated_at: resource.updatedAt,
    version: resource.revision,
  };
}

function agentStatus(agent: AgentDto): CommonStatus {
  if (agent.archivedAt) return "archived";
  return agent.runtimeAvailable ? "active" : "inactive";
}

function taskStatus(status: TaskDto["status"]): TaskSummary["status"] {
  switch (status) {
    case "todo":
      return "pending";
    case "in_progress":
      return "active";
    case "in_review":
      return "pending";
    case "done":
      return "completed";
    case "blocked":
      return "blocked";
    case "failed":
    case "error":
      return "failed";
    case "cancelled":
      return "canceled";
    case "backlog":
      return "draft";
  }
}

function humanizeEventType(value: string) {
  return value
    .replace(/[._:-]+/gu, " ")
    .replace(/\b\p{L}/gu, (character) => character.toUpperCase());
}
