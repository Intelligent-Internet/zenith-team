import type { ApprovalDto, QuestionDto } from "@/shared/api/types";
import type { ChatMessage } from "@/shared/chat/types";

type ConversationRequest =
  | { kind: "question"; request: QuestionDto }
  | { kind: "approval"; request: ApprovalDto };

export function createConversationRequestTimeline({
  approvals,
  messages,
  questions,
}: {
  approvals: ApprovalDto[];
  messages: ChatMessage[];
  questions: QuestionDto[];
}) {
  const requests: ConversationRequest[] = [
    ...questions.map((request) => ({ kind: "question" as const, request })),
    ...approvals.map((request) => ({ kind: "approval" as const, request })),
  ];
  requests.sort(
    (left, right) =>
      Date.parse(left.request.createdAt) -
        Date.parse(right.request.createdAt) ||
      left.request.id.localeCompare(right.request.id),
  );

  return requests.flatMap((item) => {
    const { request } = item;
    let originIndex = -1;
    for (let index = messages.length - 1; index >= 0; index -= 1) {
      if (messages[index].runId === request.originRunId) {
        originIndex = index;
        break;
      }
    }
    let nextIndex = originIndex >= 0 ? originIndex + 1 : -1;
    if (nextIndex < 0 && request.continuationRunId !== null) {
      nextIndex = messages.findIndex(
        (message) => message.runId === request.continuationRunId,
      );
    }
    if (nextIndex < 0) {
      const createdAt = Date.parse(request.createdAt);
      const firstMessageCreatedAt = messages.find(
        (message) => message.createdAt,
      )?.createdAt;
      // Older resolved requests enter the timeline when their history loads.
      if (
        request.status !== "pending" &&
        (!firstMessageCreatedAt ||
          createdAt < Date.parse(firstMessageCreatedAt))
      ) {
        return [];
      }
      nextIndex = messages.findIndex(
        (message) =>
          message.createdAt && Date.parse(message.createdAt) > createdAt,
      );
    }
    return [
      {
        ...item,
        afterMessageId: messages[originIndex]?.id,
        beforeMessageId: messages[nextIndex]?.id,
      },
    ];
  });
}
