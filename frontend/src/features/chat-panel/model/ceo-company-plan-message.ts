import {
  isRecord,
  parseRuntimeTrace,
} from "@/features/chat-panel/model/ceo-discovery-message";
import { CEO_CHAT_TEAM_ID } from "@/shared/chat/chat-team";
import type {
  CeoCompanyPlanMessagePayload,
  CeoCompanyPlanRevision,
  CeoCompanyPlanTask,
  ChatMessage,
  CompanyPlanMode,
} from "@/shared/chat/types";
import type { JsonObject } from "@/shared/api/types";

export type CompanyPlanMessageHandoff = {
  messageId: number;
  payload: CeoCompanyPlanMessagePayload;
};

const COMPANY_PLAN_FALLBACK_TASKS = [
  "Lock MVP scope and matching criteria",
  "Build the first working MVP",
  "Create the first acquisition test",
  "Document the launch operating rhythm",
];
const COMPANY_PLAN_TOOL_NAME = "emit_company_plan_payload";

export function parseCeoCompanyPlanMessagePayload(
  payload: JsonObject,
): CeoCompanyPlanMessagePayload | null {
  if (
    payload.type !== "ceo_company_plan_message" ||
    typeof payload.message !== "string" ||
    (payload.selected_mode !== "light" && payload.selected_mode !== "full") ||
    !isRecord(payload.product_vision_snapshot)
  ) {
    return null;
  }

  const tasks = Array.isArray(payload.tasks)
    ? payload.tasks.flatMap((task) => {
        const parsed = parseCompanyPlanTask(task);
        return parsed ? [parsed] : [];
      })
    : [];

  if (tasks.length === 0) {
    return null;
  }

  return {
    type: "ceo_company_plan_message",
    message: payload.message,
    tasks,
    product_vision_snapshot: payload.product_vision_snapshot,
    revision: parseCompanyPlanRevision(payload.revision),
    runtime_trace: parseRuntimeTrace(payload.runtime_trace),
    selected_mode: payload.selected_mode,
  };
}

export function findLatestCeoCompanyPlanMessage(
  messages: readonly ChatMessage[],
): CompanyPlanMessageHandoff | null {
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index];
    const richContent = message.richContent;

    if (richContent?.type === "ceo_company_plan_message") {
      return {
        messageId: message.id,
        payload: richContent,
      };
    }

    const fallbackPayload = plainCompanyPlanPayloadFromMessage(message);

    if (fallbackPayload) {
      return {
        messageId: message.id,
        payload: fallbackPayload,
      };
    }
  }

  return null;
}

export function plainCompanyPlanPayloadFromMessage(
  message: ChatMessage,
): CeoCompanyPlanMessagePayload | null {
  const text = normalizedString(message.text);

  if (
    message.author !== "team" ||
    message.team !== CEO_CHAT_TEAM_ID ||
    message.isStreaming ||
    message.richContent ||
    !text ||
    !hasCompanyPlanRuntimeTrace(message)
  ) {
    return null;
  }

  const selectedMode = selectedModeFromText(text);
  const productVisionSnapshot = productVisionSnapshotFromPlanText(text);
  const tasks = companyPlanTasksFromText(text);
  const runtimeTrace = runtimeTraceFromPlainCompanyPlanMessage(message);

  return {
    type: "ceo_company_plan_message",
    message: text,
    selected_mode: selectedMode,
    product_vision_snapshot: productVisionSnapshot,
    tasks,
    ...(runtimeTrace ? { runtime_trace: runtimeTrace } : {}),
  };
}

function hasCompanyPlanRuntimeTrace(message: ChatMessage) {
  return (
    message.activeTools?.includes(COMPANY_PLAN_TOOL_NAME) ||
    message.runtimeActivities?.some(
      (activity) => activity.toolName === COMPANY_PLAN_TOOL_NAME,
    )
  );
}

function selectedModeFromText(text: string): CompanyPlanMode {
  const normalized = text.toLowerCase();

  if (
    /\b(?:company\s+full\s+mode|full\s+mode|mode\s*:\s*full)\b/.test(normalized)
  ) {
    return "full";
  }

  return "light";
}

function productVisionSnapshotFromPlanText(
  text: string,
): Record<string, unknown> {
  const product =
    labeledTextValue(text, [
      "Sản phẩm",
      "San pham",
      "Product",
      "Product/service",
    ]) ?? "First MVP";
  const initialCustomer =
    labeledTextValue(text, [
      "Khách hàng đầu tiên",
      "Khach hang dau tien",
      "Target customers",
      "Target audience",
      "First customer",
    ]) ?? "Early customers";

  return {
    initial_customer: initialCustomer,
    open_questions: [],
    product,
  };
}

/**
 * A plain-text plan lists work under headed lines ("Product: a, b"). The
 * heading is a focus area, not an Agent, so parsed tasks carry no assignee.
 */
function companyPlanTasksFromText(text: string): CeoCompanyPlanTask[] {
  const parsedTasks = planTasksFromText(text);
  const tasks = (parsedTasks.length ? parsedTasks : COMPANY_PLAN_FALLBACK_TASKS)
    .map((task) => normalizedString(task))
    .filter((task): task is string => Boolean(task))
    .slice(0, 24);

  return tasks.map((task, index) => ({
    agentId: "",
    id: `plan-task-${index + 1}`,
    outcome: task,
    title: task,
  }));
}

function planTasksFromText(text: string): string[] {
  const normalizedText = text
    .replace(/\s[-•]\s+([A-Za-z][A-Za-z ]{1,40})\s*:/g, "\n$1:")
    .replace(/^\s*[-•]\s+([A-Za-z][A-Za-z ]{1,40})\s*:/gm, "$1:");
  const tasks: string[] = [];

  normalizedText.split(/\r?\n/).forEach((line) => {
    const match = line.match(/^([A-Za-z][A-Za-z ]{1,40})\s*:\s*(.+)$/);

    if (!match) {
      return;
    }

    tasks.push(...splitPlanTasks(match[2]));
  });

  return tasks;
}

function splitPlanTasks(value: string): string[] {
  const withoutFollowingSections = value
    .split(/\s+(?:Nếu muốn|Neu muon|Next step|Bước tiếp theo)\b/i)[0]
    .trim();

  return withoutFollowingSections
    .split(/\s+\+\s+|[,;]\s*/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function labeledTextValue(
  text: string,
  labels: readonly string[],
): string | undefined {
  const escapedLabels = labels.map(escapeRegExp).join("|");
  const match = text.match(
    new RegExp(
      `(?:^|[-•\\n]\\s*)(${escapedLabels})\\s*:\\s*([^\\n-]+(?:\\s(?![-•]\\s*[A-Za-zÀ-ỹ /]+\\s*:)[^\\n-]+)*)`,
      "i",
    ),
  );

  return normalizedString(match?.[2]);
}

function runtimeTraceFromPlainCompanyPlanMessage(
  message: ChatMessage,
): CeoCompanyPlanMessagePayload["runtime_trace"] {
  const activeSkills = uniqueStrings(message.activeSkills);
  const usedTools = uniqueStrings(message.activeTools);
  const toolActivities = message.runtimeActivities?.map((activity) => ({
    ...(activity.callId ? { call_id: activity.callId } : {}),
    ...(activity.input ? { input: activity.input } : {}),
    ...(activity.itemId ? { item_id: activity.itemId } : {}),
    ...(activity.output ? { output: activity.output } : {}),
    ...(activity.status ? { status: activity.status } : {}),
    tool_name: activity.toolName,
  }));

  if (!activeSkills.length && !usedTools.length && !toolActivities?.length) {
    return undefined;
  }

  return {
    active_skills: activeSkills,
    ...(toolActivities?.length ? { tool_activities: toolActivities } : {}),
    used_tools: usedTools,
  };
}

function uniqueStrings(values: string[] | undefined) {
  return [...new Set(values?.filter(Boolean) ?? [])];
}

function escapeRegExp(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function parseCompanyPlanTask(value: unknown): CeoCompanyPlanTask | null {
  if (!isRecord(value)) {
    return null;
  }

  const id = normalizedString(value.id);
  const title = normalizedString(value.title);
  const outcome = normalizedString(value.outcome);

  if (!id || !title || !outcome) {
    return null;
  }

  return {
    agentId: normalizedString(value.agentId) ?? "",
    id,
    outcome,
    title,
  };
}

function parseCompanyPlanRevision(
  value: unknown,
): CeoCompanyPlanRevision | null | undefined {
  if (value === null || value === undefined) {
    return value;
  }

  if (!isRecord(value)) {
    return undefined;
  }

  const userFeedback = normalizedString(value.user_feedback);
  const changeSummary = stringList(value.change_summary);

  if (!userFeedback || changeSummary.length === 0) {
    return undefined;
  }

  return {
    change_summary: changeSummary,
    revision_of_item_id: optionalString(value.revision_of_item_id),
    user_feedback: userFeedback,
  };
}

function stringList(value: unknown) {
  if (!Array.isArray(value)) {
    return [];
  }

  const seen = new Set<string>();
  const items: string[] = [];
  value.forEach((item) => {
    const text = normalizedString(item);
    if (!text || seen.has(text)) {
      return;
    }
    seen.add(text);
    items.push(text);
  });
  return items;
}

function normalizedString(value: unknown) {
  if (typeof value !== "string") {
    return undefined;
  }

  const normalized = value.trim();
  return normalized || undefined;
}

function optionalString(value: unknown) {
  return value === null ? null : normalizedString(value);
}
