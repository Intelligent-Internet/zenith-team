import { useEffect, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router";
import { confirmProjectAccess } from "@/app/shell/realtime-sync/project-access-cache-updater";
import { isProjectAccessLost } from "@/features/company-plan/model/project-access-lost";
import store, { useAppDispatch } from "@/state/store";

/**
 * Whether a read of this Project answered 404 and the Project is being read
 * again to tell lost access from a failed load. While it is, the screen
 * shows a neutral state, never a red error or an empty board. The check is
 * the one `project.access_changed` runs (`confirmProjectAccess`), so a 404
 * that arrives without that frame, say after a missed frame, still takes
 * the viewer to Projects. When the Project still reads, the 404 is an
 * ordinary load error again and this answers false.
 */
export function useProjectAccessLoss(
  companyId: string,
  projectId: string,
  error: unknown,
): boolean {
  const dispatch = useAppDispatch();
  const navigate = useNavigate();
  const location = useLocation();
  const live = useRef({ location, navigate });
  live.current = { location, navigate };
  const lost = isProjectAccessLost(error);
  // The 404 the Project's own read answered as still visible.
  const [kept, setKept] = useState<unknown>();

  useEffect(() => {
    if (!lost || kept === error) return undefined;
    let current = true;
    void confirmProjectAccess(
      {
        companyId,
        dispatch,
        getState: store.getState,
        pathname: live.current.location.pathname,
        requestRefresh: () => undefined,
        leave: (destination) => {
          const to = destination({
            pathname: live.current.location.pathname,
            search: live.current.location.search,
          });
          if (!to) return false;
          live.current.navigate(to, { replace: true });
          return true;
        },
      },
      projectId,
    ).then((visible) => {
      if (current && visible) setKept(error);
    });
    return () => {
      current = false;
    };
  }, [companyId, dispatch, error, kept, lost, projectId]);

  return lost && kept !== error;
}
