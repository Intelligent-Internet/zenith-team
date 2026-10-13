import { skipToken } from "@reduxjs/toolkit/query";
import { useCallback, useEffect } from "react";

import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import {
  agentRuntimeApi,
  getCeoAgentApiErrorMessage,
} from "@/shared/api/agent-runtime-api";
import type {
  RuntimeDto,
  RuntimeModel,
  RuntimeModelDiscoveryRequest,
} from "@/shared/api/types";

export type ModelDiscoveryStatus =
  RuntimeModelDiscoveryRequest["status"] | "idle" | "unavailable";

const emptyModels: RuntimeModel[] = [];

/** Shares a runtime's model catalogue across chat, setup and Agent settings. */
export function useRuntimeModelDiscovery({
  companyId,
  runtime,
  requireFreshRequest = false,
}: {
  companyId: string;
  runtime: RuntimeDto | null;
  requireFreshRequest?: boolean;
}): {
  message: string;
  requestId?: string;
  models: RuntimeModel[];
  refresh: () => void;
  status: ModelDiscoveryStatus;
} {
  const session = useSessionSnapshot();
  const args =
    companyId && runtime && session.userId
      ? {
          companyId,
          runtimeId: runtime.id,
          userId: session.userId,
          provider: runtime.provider,
          providerVersion: runtime.providerVersion,
          signInState: runtime.signIn?.state ?? ("unknown" as const),
        }
      : skipToken;
  const enabled = args !== skipToken && runtime?.status === "online";
  const { refetch } =
    agentRuntimeApi.endpoints.getRuntimeModelCatalog.useQuerySubscription(
      args,
      {
        skip: !enabled,
        // Onboarding submits the discovery receipt; the server retains it for
        // only two minutes. Ordinary pickers can reuse model labels for longer.
        refetchOnMountOrArgChange: requireFreshRequest ? 60 : 300,
        pollingInterval: requireFreshRequest ? 60_000 : 0,
      },
    );
  // Reading separately preserves the last catalogue while the runtime is
  // offline, without starting discovery or borrowing another runtime's data.
  const query =
    agentRuntimeApi.endpoints.getRuntimeModelCatalog.useQueryState(args);
  const receiptExpired =
    requireFreshRequest &&
    query.fulfilledTimeStamp !== undefined &&
    Date.now() - query.fulfilledTimeStamp >= 60_000;
  useEffect(() => {
    if (enabled && receiptExpired && !query.isFetching && !query.isError)
      void refetch();
  }, [enabled, receiptExpired, query.isFetching, query.isError, refetch]);
  const refresh = useCallback(() => {
    if (enabled) void refetch();
  }, [enabled, refetch]);
  const error = query.error;
  const denied =
    error &&
    "status" in error &&
    (error.status === 403 || error.status === 404);
  const code = error && "code" in error ? error.code : undefined;
  const status: ModelDiscoveryStatus = !enabled
    ? "idle"
    : query.isFetching ||
        query.isUninitialized ||
        (receiptExpired && !query.isError)
      ? "pending"
      : query.isError
        ? code === "discovery_failed"
          ? "failed"
          : code === "pending_timeout" ||
              code === "running_timeout" ||
              code === "model_discovery_timeout"
            ? "timeout"
            : "unavailable"
        : "completed";

  return {
    message:
      enabled && query.isError
        ? getCeoAgentApiErrorMessage(
            error,
            "Runtime models could not be loaded.",
          )
        : "",
    requestId:
      status === "completed" ? query.currentData?.requestId : undefined,
    models: denied ? emptyModels : (query.currentData?.models ?? emptyModels),
    refresh,
    status,
  };
}
