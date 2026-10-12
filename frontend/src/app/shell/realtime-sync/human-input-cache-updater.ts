import { approvalsApi } from "@/features/approvals/api/approvals-api";
import { boardlyApi } from "@/shared/api/boardly-api";
import type {
  ApprovalDto,
  ApprovalListQuery,
  QuestionDto,
  QuestionListQuery,
} from "@/shared/api/types";
import type { BoardlyRealtimeEvent } from "@/shared/realtime/realtime-client.service";
import {
  removeVersioned,
  replaceVersioned,
  type RealtimeSyncContext,
} from "@/app/shell/realtime-sync/realtime-sync-context";

const approvalEvents = new Set([
  "approval.requested",
  "approval.resolved",
  "approval.expired",
]);
const questionEvents = new Set([
  "question.asked",
  "question.resolved",
  "question.expired",
]);

// The server embeds the whole entity on every one of these frames, so the
// cache is written from the payload and only the counted rollup is re-read.
export function applyHumanInputCacheEvent(
  context: RealtimeSyncContext,
  event: BoardlyRealtimeEvent,
) {
  if (approvalEvents.has(event.type)) {
    if (isCompanyEntity(event.payload, context.companyId)) {
      updateApproval(context, event.payload as unknown as ApprovalDto);
      // The stats rollup is counted server-side, and this is the only event
      // that moves it.
      context.requestRefresh("approvalStats");
    }
    return true;
  }
  if (questionEvents.has(event.type)) {
    if (isCompanyEntity(event.payload, context.companyId)) {
      updateQuestion(context, event.payload as unknown as QuestionDto);
    }
    return true;
  }
  return false;
}

function isCompanyEntity(payload: unknown, companyId: string) {
  const entity = payload as Partial<ApprovalDto> | undefined;
  return (
    entity?.companyId === companyId &&
    typeof entity.id === "string" &&
    typeof entity.revision === "number"
  );
}

function updateApproval(context: RealtimeSyncContext, approval: ApprovalDto) {
  let pendingRead =
    approvalsApi.endpoints.getApproval.select({
      approvalId: approval.id,
      companyId: context.companyId,
    })(context.getState()).status === "pending";
  for (const argument of approvalsApi.util.selectCachedArgsForQuery(
    context.getState(),
    "listApprovals",
  )) {
    if (argument.companyId !== context.companyId) continue;
    let unknownToDecideView = false;
    pendingRead ||=
      approvalsApi.endpoints.listApprovals.select(argument)(context.getState())
        .status === "pending";
    context.dispatch(
      approvalsApi.util.updateQueryData("listApprovals", argument, (page) => {
        unknownToDecideView = !applyToPage(
          page.items,
          approval,
          argument,
          approvalMatches(approval, argument),
        );
      }),
    );
    if (unknownToDecideView) {
      void context.dispatch(
        approvalsApi.endpoints.listApprovals.initiate(argument, {
          forceRefetch: true,
          subscribe: false,
        }),
      );
    }
  }

  context.dispatch(
    approvalsApi.util.updateQueryData(
      "getApproval",
      { approvalId: approval.id, companyId: context.companyId },
      (current) => {
        if (current.revision <= approval.revision) {
          Object.assign(current, approval);
        }
      },
    ),
  );
  // A response started before this event can overwrite the optimistic patch.
  // Delayed invalidation waits for that response before taking a fresh snapshot.
  if (pendingRead)
    context.dispatch(
      boardlyApi.util.invalidateTags([
        { type: "Approval", id: context.companyId },
        { type: "Approval", id: `${context.companyId}:item:${approval.id}` },
      ]),
    );
}

function updateQuestion(context: RealtimeSyncContext, question: QuestionDto) {
  let pendingRead = false;
  for (const argument of approvalsApi.util.selectCachedArgsForQuery(
    context.getState(),
    "listQuestions",
  )) {
    if (argument.companyId !== context.companyId) continue;
    let unknownToDecideView = false;
    pendingRead ||=
      approvalsApi.endpoints.listQuestions.select(argument)(context.getState())
        .status === "pending";
    context.dispatch(
      approvalsApi.util.updateQueryData("listQuestions", argument, (page) => {
        unknownToDecideView = !applyToPage(
          page.items,
          question,
          argument,
          questionMatches(question, argument),
        );
      }),
    );
    if (unknownToDecideView) {
      void context.dispatch(
        approvalsApi.endpoints.listQuestions.initiate(argument, {
          forceRefetch: true,
          subscribe: false,
        }),
      );
    }
  }
  if (pendingRead)
    context.dispatch(
      boardlyApi.util.invalidateTags([
        { type: "Question", id: context.companyId },
      ]),
    );
}

/**
 * Writes one frame's entity into a cached page. Answers false only for a
 * `view: "decide"` page the entity is new to: these frames reach every
 * reader of the Project, not only those who may decide, so the page must
 * ask the server again instead of taking the row from the frame.
 */
function applyToPage<
  T extends { createdAt: string; id: string; revision: number },
>(
  items: T[],
  entity: T,
  argument: {
    allPages?: boolean;
    cursor?: string;
    limit?: number;
    view?: string;
  },
  matches: boolean,
): boolean {
  if (!matches) {
    removeVersioned(items, entity.id, entity.revision);
    return true;
  }
  const known = items.some((candidate) => candidate.id === entity.id);
  // A row that is not already on a continuation page belongs to some earlier
  // page, not this one.
  if (argument.cursor && !known) return true;
  if (argument.view === "decide" && !known) return false;
  replaceVersioned(items, entity);
  items.sort(
    (left, right) => Date.parse(right.createdAt) - Date.parse(left.createdAt),
  );
  if (!argument.allPages) items.splice(argument.limit ?? 50);
  return true;
}

function approvalMatches(approval: ApprovalDto, query: ApprovalListQuery) {
  if (query.status?.length && !query.status.includes(approval.status)) {
    return false;
  }
  if (query.agent_id && approval.requestedByAgent.id !== query.agent_id) {
    return false;
  }
  return !query.task_id || approval.taskId === query.task_id;
}

function questionMatches(question: QuestionDto, query: QuestionListQuery) {
  if (query.status?.length && !query.status.includes(question.status)) {
    return false;
  }
  if (query.agent_id && question.askedByAgent.id !== query.agent_id) {
    return false;
  }
  return !query.task_id || question.taskId === query.task_id;
}
