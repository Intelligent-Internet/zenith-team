import { analyticsApi } from "@/shared/api/analytics-api";
import { useEffect, useMemo } from "react";
import { createSelector } from "@reduxjs/toolkit";

import { companyService } from "@/shared/api/company.service";
import { useAppDispatch, useAppSelector } from "@/state";

export type AnalyticsSeriesQuery = Parameters<
  typeof companyService.getAnalyticsSeries
>[1] & { companyId: string };

// Subscribe to at most five existing, company-scoped series queries. RTK Query
// keeps ownership of the cache and Run-completion invalidation.
export function useAgentSeries(queries: AnalyticsSeriesQuery[]) {
  const dispatch = useAppDispatch();
  const key = JSON.stringify(queries);
  const args = useMemo(() => JSON.parse(key) as AnalyticsSeriesQuery[], [key]);
  const endpoint = analyticsApi.endpoints.getWorkspaceAnalyticsSeries;
  const selector = useMemo(
    () =>
      createSelector(
        args.map((arg) => endpoint.select(arg)),
        (...results) => results,
      ),
    [args, endpoint],
  );
  const results = useAppSelector(selector);
  useEffect(() => {
    const subscriptions = args.map((arg) => dispatch(endpoint.initiate(arg)));
    return () =>
      subscriptions.forEach((subscription) => subscription.unsubscribe());
  }, [args, dispatch, endpoint]);
  return {
    results,
    retry: () =>
      args.forEach((arg) => {
        void dispatch(
          endpoint.initiate(arg, { subscribe: false, forceRefetch: true }),
        );
      }),
  };
}
