import { useCallback, useEffect, useRef } from "react";

import { readSessionSnapshot } from "@/app/session/session";
import { useGetWorkspaceActivityInsightQuery } from "@/shared/api/workspace-api";

const activityInsightRefetchDelayMs = 250;
export function useActivityInsight() {
  const companyId = readSessionSnapshot().company?.id;
  const enabled = Boolean(companyId);
  const query = useGetWorkspaceActivityInsightQuery(companyId ?? "", {
    refetchOnMountOrArgChange: 60,
    skip: !enabled,
  });
  const refetchRef = useRef(query.refetch);
  const scheduledRefetchRef = useRef<number | undefined>(undefined);
  refetchRef.current = query.refetch;

  const scheduleCloudRefetch = useCallback(() => {
    if (!enabled || scheduledRefetchRef.current !== undefined) return;
    scheduledRefetchRef.current = window.setTimeout(() => {
      scheduledRefetchRef.current = undefined;
      void refetchRef.current();
    }, activityInsightRefetchDelayMs);
  }, [enabled]);

  const retry = useCallback(() => {
    scheduleCloudRefetch();
  }, [scheduleCloudRefetch]);

  useEffect(() => {
    return () => {
      if (scheduledRefetchRef.current !== undefined) {
        window.clearTimeout(scheduledRefetchRef.current);
        scheduledRefetchRef.current = undefined;
      }
    };
  }, []);

  return {
    data: query.data,
    isError: query.isError,
    isLoading: query.isLoading && query.data === undefined,
    retry,
  };
}
