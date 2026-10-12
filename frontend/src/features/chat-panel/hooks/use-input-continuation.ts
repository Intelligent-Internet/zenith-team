import { useCallback, useEffect, useRef, useState } from "react";

import type {
  ApprovalDto,
  ConversationCurrentRun,
  QuestionDto,
} from "@/shared/api/types";
import type { ChatMessage } from "@/shared/chat/types";

type InputReceipt = Pick<
  ApprovalDto | QuestionDto,
  "id" | "conversationId" | "originRunId" | "revision" | "continuationRunId"
>;

export function useInputContinuation({
  approvals,
  conversationId,
  currentRun,
  messages,
  questions,
}: {
  approvals: ApprovalDto[];
  conversationId?: string | null;
  currentRun: ConversationCurrentRun | null;
  messages: ChatMessage[];
  questions: QuestionDto[];
}) {
  const [receipt, setReceipt] = useState<InputReceipt | null>(null);
  const [submitting, setSubmitting] = useState<ReadonlySet<string>>(
    () => new Set(),
  );
  const onSubmittingChange = useCallback(
    (requestId: string, pending: boolean) => {
      const key = `${conversationId}:${requestId}`;
      setSubmitting((current) => {
        const next = new Set(current);
        if (pending) next.add(key);
        else next.delete(key);
        return next;
      });
    },
    [conversationId],
  );
  const isSubmitting = [...submitting].some((key) =>
    key.startsWith(`${conversationId}:`),
  );
  const observedQueuedRunId = useRef<string | null>(null);
  const request = [...approvals, ...questions].find(
    (item) => item.id === receipt?.id,
  );
  const latest =
    request && receipt && request.revision >= receipt.revision
      ? request
      : receipt;
  const runId = latest?.continuationRunId;
  const originIndex = receipt
    ? messages.map((message) => message.runId).lastIndexOf(receipt.originRunId)
    : -1;
  const followingMessages =
    originIndex >= 0 ? messages.slice(originIndex + 1) : [];
  const hasSameTurnContinuation = Boolean(
    originIndex >= 0 &&
    !followingMessages.some((message) => message.author === "user") &&
    (followingMessages.some(
      (message) => message.author === "team" && message.runId,
    ) ||
      (currentRun &&
        currentRun.id !== receipt?.originRunId &&
        (currentRun.status === "dispatched" ||
          currentRun.status === "running"))),
  );
  // The resumed stream can arrive before the resolution event, or after the
  // pending-only query has dropped the resolved request. Its activity is
  // enough to hand the indicator back to the same chat turn.
  const hasRunActivity = Boolean(
    hasSameTurnContinuation ||
    (runId &&
      (messages.some((message) => message.runId === runId) ||
        (currentRun?.id === runId &&
          (currentRun.status === "dispatched" ||
            currentRun.status === "running")))),
  );
  // Delivery or abandonment advances the resolved request's revision. A
  // settled receipt with no Run must not leave a perpetual loading indicator.
  const settledWithoutRun = Boolean(
    request && receipt && request.revision > receipt.revision && !runId,
  );
  const leftQueue = Boolean(
    runId && observedQueuedRunId.current === runId && currentRun?.id !== runId,
  );
  const isPending = Boolean(
    receipt &&
    receipt.conversationId === conversationId &&
    !hasRunActivity &&
    !settledWithoutRun &&
    !leftQueue,
  );

  useEffect(() => {
    if (!receipt) return;
    if (!isPending) {
      setReceipt(null);
      observedQueuedRunId.current = null;
    } else if (runId && currentRun?.id === runId) {
      observedQueuedRunId.current = runId;
    }
  }, [currentRun?.id, isPending, receipt, runId]);

  const onResolved = useCallback((resolved: ApprovalDto | QuestionDto) => {
    if (resolved.status === "pending" || resolved.status === "expired") return;
    observedQueuedRunId.current = null;
    setReceipt({
      id: resolved.id,
      conversationId: resolved.conversationId,
      originRunId: resolved.originRunId,
      revision: resolved.revision,
      continuationRunId: resolved.continuationRunId,
    });
  }, []);

  return {
    isPending: isSubmitting || isPending,
    onResolved,
    onSubmittingChange,
  };
}
