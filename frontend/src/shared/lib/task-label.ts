import { useGetWorkspaceCompanyQuery } from "@/shared/api/company-api";
import { useCallback } from "react";
import { formatTaskLabel, type LabelTask } from "./task-label-format";

export { formatTaskLabel, type LabelTask } from "./task-label-format";

/**
 * The label formatter bound to a company: it reads the company's prefix
 * once and hands back a stable function, so a surface that shows task
 * labels learns nothing about where the prefix comes from. Pass the
 * company id, or undefined before it is known.
 */
export function useTaskLabel(companyId: string | undefined): LabelTask {
  const company = useGetWorkspaceCompanyQuery(companyId ?? "", {
    skip: !companyId,
  });
  const prefix = company.currentData?.taskPrefix;
  return useCallback<LabelTask>(
    (task) => formatTaskLabel(task, prefix),
    [prefix],
  );
}
