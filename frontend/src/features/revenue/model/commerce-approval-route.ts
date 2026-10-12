import type { ApprovalDto } from "@/shared/api/types";

const UUID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export type CommerceApprovalReturnContext = {
  agentId: string;
  approvalId: string;
  conversationId: string;
  taskId: string;
};

export type CommerceApprovalRoute = {
  manifestId: string | null;
  mode: CommerceManifestRouteMode | null;
  returnContext: CommerceApprovalReturnContext | null;
};

type CommerceManifestRouteMode = "publish" | "view";

export function commerceManifestApprovalSubjectId(approval: ApprovalDto) {
  const subjectId = approval.subjectId;
  return typeof subjectId === "string" && UUID_PATTERN.test(subjectId)
    ? subjectId
    : null;
}

export function commerceManifestReviewHref(
  manifestId: string,
  returnContext: CommerceApprovalReturnContext,
) {
  const search = new URLSearchParams({ manifestId, mode: "publish" });
  search.set("agentId", returnContext.agentId);
  search.set("approvalId", returnContext.approvalId);
  search.set("conversationId", returnContext.conversationId);
  search.set("taskId", returnContext.taskId);
  return `/revenue?${search.toString()}`;
}

export function commerceManifestViewHref(manifestId: string) {
  return `/revenue?${new URLSearchParams({ manifestId, mode: "view" }).toString()}`;
}

export function commerceApprovalReturnContext(
  approval: ApprovalDto,
): CommerceApprovalReturnContext | null {
  if (!approval.taskId) return null;
  return {
    agentId: approval.requestedByAgent.id,
    approvalId: approval.id,
    conversationId: approval.conversationId,
    taskId: approval.taskId,
  };
}

export function parseCommerceApprovalRoute(searchValue: string) {
  const search = new URLSearchParams(searchValue);
  const manifestId = validUUID(search.get("manifestId"));
  const agentId = validUUID(search.get("agentId"));
  const approvalId = validUUID(search.get("approvalId"));
  const conversationId = validUUID(search.get("conversationId"));
  const taskId = validUUID(search.get("taskId"));
  const returnContext =
    agentId && approvalId && conversationId && taskId
      ? { agentId, approvalId, conversationId, taskId }
      : null;
  const mode = parseCommerceManifestRouteMode(
    manifestId,
    search.get("mode"),
    returnContext,
  );

  return {
    manifestId,
    mode,
    returnContext: mode === "publish" ? returnContext : null,
  } satisfies CommerceApprovalRoute;
}

export function commerceApprovalConversationHref(
  context: CommerceApprovalReturnContext,
) {
  return `/app?${new URLSearchParams({
    agentId: context.agentId,
    approvalId: context.approvalId,
    conversationId: context.conversationId,
    conversationKind: "task",
    taskId: context.taskId,
  }).toString()}`;
}

export function isCommerceApprovalReturnContext(
  value: unknown,
): value is CommerceApprovalReturnContext {
  if (!value || typeof value !== "object") return false;
  const context = value as Partial<CommerceApprovalReturnContext>;
  return (
    validUUID(context.agentId) !== null &&
    validUUID(context.approvalId) !== null &&
    validUUID(context.conversationId) !== null &&
    validUUID(context.taskId) !== null
  );
}

function validUUID(value: unknown) {
  return typeof value === "string" && UUID_PATTERN.test(value) ? value : null;
}

function parseCommerceManifestRouteMode(
  manifestId: string | null,
  requestedMode: string | null,
  returnContext: CommerceApprovalReturnContext | null,
): CommerceManifestRouteMode | null {
  if (!manifestId) return null;
  if (requestedMode === "view") return "view";
  if (
    returnContext &&
    (requestedMode === "publish" || requestedMode === null)
  ) {
    return "publish";
  }
  return requestedMode === null ? "view" : null;
}
