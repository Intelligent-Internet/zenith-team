import { useCallback, useEffect, useRef } from "react";
import { useNavigate } from "react-router";
import { useAppDispatch } from "@/state/store";
import { taskApi } from "@/shared/api/task-api";
import { pageApi } from "@/features/project-canvas/api/page-api";
import { wikiApi } from "@/features/company-wiki/wiki-api";

// Source identifiers never grant authority: resolve through the existing API
// before navigating, using the current host's Company and Project scope.
export function useInlineEntityAction(companyId?: string, projectId?: string) {
  const dispatch = useAppDispatch();
  const navigate = useNavigate();
  const current = useRef({ companyId, projectId, mounted: true });
  current.current.companyId = companyId;
  current.current.projectId = projectId;
  useEffect(() => {
    current.current.mounted = true;
    return () => {
      current.current.mounted = false;
    };
  }, []);
  return useCallback(
    async (value: unknown) => {
      const entity =
        value && typeof value === "object"
          ? (value as Record<string, unknown>)
          : {};
      // Final-guide ref form: openEntityDetail({category, refId}).
      const id = entity.refId;
      if (
        !companyId ||
        typeof id !== "string" ||
        !/^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i.test(id)
      )
        throw new Error("A real Zenith Task or Page identifier is required.");
      const valid = () =>
        current.current.mounted &&
        current.current.companyId === companyId &&
        current.current.projectId === projectId;
      try {
        if (entity.category === "task") {
          const task = await dispatch(
            taskApi.endpoints.getWorkspaceTask.initiate(
              { companyId, taskId: id },
              { subscribe: false, forceRefetch: true },
            ),
          ).unwrap();
          if (valid())
            navigate(
              `/projects/${encodeURIComponent(task.projectId)}?taskId=${encodeURIComponent(task.id)}`,
            );
        } else if (entity.category === "page") {
          if (projectId) {
            await dispatch(
              pageApi.endpoints.projectPage.initiate(
                { companyId, projectId, pageId: id },
                { subscribe: false, forceRefetch: true },
              ),
            ).unwrap();
            if (valid())
              navigate(
                `/projects/${encodeURIComponent(projectId)}/pages?pageId=${encodeURIComponent(id)}`,
              );
          } else {
            await dispatch(
              wikiApi.endpoints.getWikiPage.initiate(
                { companyId, pageId: id },
                { subscribe: false, forceRefetch: true },
              ),
            ).unwrap();
            if (valid()) navigate(`/wiki/pages/${encodeURIComponent(id)}`);
          }
        } else
          throw new Error(
            "This entity category is not connected in Zenith Team.",
          );
      } catch {
        throw new Error("This item is unavailable or your access has changed.");
      }
    },
    [companyId, projectId, dispatch, navigate],
  );
}
