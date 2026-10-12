import type {
  ApprovalDto,
  ApprovalStatusDto,
  QuestionDto,
  QuestionStatusDto,
} from "@/shared/api/types";

export const approvalStatusFilters: ReadonlyArray<{
  label: string;
  value: ApprovalStatusDto;
}> = [
  { label: "Pending", value: "pending" },
  { label: "Approved", value: "approved" },
  { label: "Rejected", value: "rejected" },
  { label: "Expired", value: "expired" },
  { label: "Cancelled", value: "cancelled" },
];

export const questionStatusFilters: ReadonlyArray<{
  label: string;
  value: QuestionStatusDto;
}> = [
  { label: "Pending", value: "pending" },
  { label: "Answered", value: "answered" },
  { label: "Expired", value: "expired" },
  { label: "Withdrawn", value: "withdrawn" },
];

export function approvalStatusLabel(status: ApprovalStatusDto) {
  return status === "cancelled" ? "Cancelled" : capitalize(status);
}

export function questionStatusLabel(status: QuestionStatusDto) {
  return capitalize(status);
}

export function approvalStatusTone(status: ApprovalStatusDto) {
  if (status === "pending") {
    return "border-transparent bg-[var(--workspace-approval-soft)] text-[var(--workspace-native-approval)]";
  }
  if (status === "approved") {
    return "border-transparent bg-[var(--workspace-success-soft)] text-[var(--workspace-native-success)]";
  }
  if (status === "rejected") {
    return "border-transparent bg-[var(--workspace-danger-soft)] text-[var(--workspace-native-danger)]";
  }
  return "border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] text-[var(--workspace-native-text-muted)]";
}

export function questionStatusTone(status: QuestionStatusDto) {
  if (status === "pending") {
    return "border-transparent bg-[var(--workspace-approval-soft)] text-[var(--workspace-native-approval)]";
  }
  if (status === "answered") {
    return "border-transparent bg-[var(--workspace-success-soft)] text-[var(--workspace-native-success)]";
  }
  return "border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] text-[var(--workspace-native-text-muted)]";
}

export function approvalConversationHref(approval: ApprovalDto) {
  return `/app?${conversationSearch({
    agentId: approval.requestedByAgent.id,
    approvalId: approval.id,
    conversationId: approval.conversationId,
    taskId: approval.taskId,
  }).toString()}`;
}

export function questionConversationHref(question: QuestionDto) {
  return `/app?${conversationSearch({
    agentId: question.askedByAgent.id,
    conversationId: question.conversationId,
    questionId: question.id,
    taskId: question.taskId,
  }).toString()}`;
}

export function formatHitlDate(value: string) {
  const parsed = Date.parse(value);
  return Number.isFinite(parsed)
    ? new Intl.DateTimeFormat(undefined, {
        dateStyle: "medium",
        timeStyle: "short",
      }).format(parsed)
    : value;
}

function conversationSearch({
  agentId,
  approvalId,
  conversationId,
  questionId,
  taskId,
}: {
  agentId: string;
  approvalId?: string;
  conversationId: string;
  questionId?: string;
  taskId: string | null;
}) {
  const search = new URLSearchParams({
    agentId,
    conversationId,
    conversationKind: taskId ? "task" : "direct",
  });
  if (approvalId) search.set("approvalId", approvalId);
  if (questionId) search.set("questionId", questionId);
  if (taskId) search.set("taskId", taskId);
  return search;
}

function capitalize(value: string) {
  return `${value.charAt(0).toUpperCase()}${value.slice(1)}`;
}
