import { useCallback, useState } from "react";

import { commerceService } from "@/features/revenue/api/commerce.service";
import type { CommerceEnvironment } from "@/features/revenue/model/commerce";

type CommerceOnboardingOperation =
  "account-session" | "onboarding-link" | "dashboard-link";

export function useCommerceOnboardingActions(companyId: string) {
  const [pendingOperation, setPendingOperation] =
    useState<CommerceOnboardingOperation | null>(null);

  const run = useCallback(
    async <Result>(
      operation: CommerceOnboardingOperation,
      request: () => Promise<Result>,
    ) => {
      setPendingOperation(operation);
      try {
        return await request();
      } finally {
        setPendingOperation((current) =>
          current === operation ? null : current,
        );
      }
    },
    [],
  );

  const createAccountSession = useCallback(
    (environment: CommerceEnvironment) =>
      run("account-session", () =>
        commerceService.createAccountSession(companyId, environment, {
          schemaVersion: 1,
        }),
      ),
    [companyId, run],
  );

  const createOnboardingLink = useCallback(
    (environment: CommerceEnvironment) =>
      run("onboarding-link", () =>
        commerceService.createOnboardingLink(companyId, environment, {
          schemaVersion: 1,
        }),
      ),
    [companyId, run],
  );

  const createDashboardLink = useCallback(
    (environment: CommerceEnvironment) =>
      run("dashboard-link", () =>
        commerceService.createDashboardLink(companyId, environment, {
          schemaVersion: 1,
        }),
      ),
    [companyId, run],
  );

  return {
    createAccountSession,
    createDashboardLink,
    createOnboardingLink,
    pendingOperation,
  };
}
