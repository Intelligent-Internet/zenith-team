import { useGetWorkspaceTaskQuery } from "@/shared/api/task-api";

import {
  useTaskAccess,
  type ProjectAccessView,
} from "./use-project-permissions";

/**
 * The Task access for something that names a Task by id (an Approval, a
 * Question). `known` is false while the Task is loading or when it is gone;
 * the server still decides every action, so callers keep controls enabled
 * then and let it answer. Kept out of the package index so the permission
 * module never imports the API layer that imports it.
 */
export function useTaskAccessById(
  companyId: string | null | undefined,
  taskId: string | null | undefined,
): ProjectAccessView & { known: boolean } {
  const query = useGetWorkspaceTaskQuery(
    { companyId: companyId ?? "", taskId: taskId ?? "" },
    { skip: !companyId || !taskId },
  );
  const access = useTaskAccess(query.currentData);
  return { ...access, known: Boolean(query.currentData?.access) };
}
