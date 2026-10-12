import { skipToken } from "@reduxjs/toolkit/query";
import { useCallback, useEffect, useMemo, useRef } from "react";
import { useLocation, useNavigate } from "react-router";

import type { ConversationArtifactsContext } from "../api/conversation-artifacts-api";
import { conversationArtifactReference } from "../model/conversation-artifact";
import type { useConversationArtifacts } from "./use-conversation-artifacts";
import type { useConversationArtifactsPanel } from "./use-conversation-artifacts-panel";
import { parseChatWorkspaceRoute } from "@/features/chat-workspace/model/chat-workspace-route";
import {
  useGetRunDetailsQuery,
  useGetRunResultQuery,
} from "@/features/run-deliverables/api/run-deliverables-api";

export function useConversationArtifactNavigation(
  context: ConversationArtifactsContext | undefined,
  artifacts: ReturnType<typeof useConversationArtifacts>,
  panel: ReturnType<typeof useConversationArtifactsPanel>,
) {
  const location = useLocation();
  const navigate = useNavigate();
  const route = useMemo(
    () => parseChatWorkspaceRoute(location.search),
    [location.search],
  );
  const matchesConversation = Boolean(
    context &&
    location.pathname === "/app" &&
    route.target?.conversationId === context.conversationId,
  );
  const target = matchesConversation ? route.artifact : undefined;
  const conversationRun = matchesConversation && route.runId && !target;
  const reviewOnly = Boolean(matchesConversation && route.reviewArtifacts);
  const run = useGetRunDetailsQuery(
    context && target
      ? { companyId: context.companyId, runId: target.runId }
      : skipToken,
    { refetchOnMountOrArgChange: true },
  );
  const runMatchesConversation =
    run.currentData?.conversationId === context?.conversationId;
  const result = useGetRunResultQuery(
    context && target && runMatchesConversation && !run.isError
      ? {
          companyId: context.companyId,
          runId: target.runId,
          artifactId: target.artifactId,
        }
      : skipToken,
    { refetchOnMountOrArgChange: true },
  );
  const unavailable = Boolean(
    target &&
    (run.isError ||
      result.isError ||
      (run.currentData && !runMatchesConversation) ||
      (result.currentData &&
        result.currentData.output?.id !== target.artifactId)),
  );
  const artifact =
    target &&
    !unavailable &&
    runMatchesConversation &&
    result.currentData?.output
      ? {
          runId: target.runId,
          runtimeId: result.currentData.runtimeId,
          runStatus: result.currentData.runStatus,
          artifact: result.currentData.output,
          deliverable: result.currentData.deliverable,
          publication: result.currentData.publication,
        }
      : undefined;
  const loading = Boolean(target && !artifact && !unavailable);
  const requestKey = `${context?.companyId}:${location.key}`;
  const opened = useRef("");
  const selected = useRef("");
  const pending = artifacts.items.filter(
    (item) => item.deliverable?.status === "ready_for_review",
  );

  useEffect(() => {
    if (conversationRun && opened.current !== requestKey) {
      opened.current = requestKey;
      panel.back();
      panel.close();
      return;
    }
    if (!target && !reviewOnly) return;
    if (opened.current !== requestKey) {
      opened.current = requestKey;
      panel.back();
      panel.open();
    }
    if (selected.current === requestKey) return;
    if (artifact) {
      selected.current = requestKey;
      panel.select(conversationArtifactReference(artifact));
      return;
    }
    if (
      !reviewOnly ||
      target ||
      artifacts.isLoading ||
      artifacts.isFetching ||
      artifacts.error
    )
      return;
    // Look beyond the first page before treating a single result as the only
    // pending review. Multiple results stay in the picker.
    if (pending.length < 2 && artifacts.hasMore) {
      void artifacts.loadMore();
      return;
    }
    selected.current = requestKey;
    if (pending.length === 1)
      panel.select(conversationArtifactReference(pending[0]));
  }, [
    target,
    conversationRun,
    reviewOnly,
    requestKey,
    artifact,
    artifacts,
    panel,
    pending,
  ]);

  const clear = useCallback(() => {
    if (!matchesConversation || (!target && !reviewOnly)) return;
    const params = new URLSearchParams(location.search);
    for (const key of ["artifactId", "runId", "fileId", "artifacts"])
      params.delete(key);
    navigate(
      { pathname: location.pathname, search: params.toString() },
      {
        replace: true,
        state: location.state,
      },
    );
  }, [location, matchesConversation, navigate, reviewOnly, target]);

  return {
    requestKey:
      target || reviewOnly || conversationRun ? requestKey : undefined,
    artifact,
    target,
    loading,
    unavailable,
    reviewOnly,
    pending,
    clear,
    retry: () => {
      if (run.isError || !runMatchesConversation) void run.refetch();
      else void result.refetch();
    },
  };
}
