import { useEffect, useRef } from "react";
import { useWorkspaceNavigationBlocker } from "@/app/shell/workspace-navigation-guard";
import { useAppDispatch, useAppSelector } from "@/state/store";
import { clearSkillFileDrafts } from "@/state/slice/skill-file-drafts";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";

export function SkillFileDraftGuard() {
  const dispatch = useAppDispatch();
  const dirty = useAppSelector(
    (state) => Object.keys(state.skillFileDrafts).length > 0,
  );
  const blocker = useWorkspaceNavigationBlocker(
    ({ currentLocation, nextLocation }) =>
      dirty && currentLocation.pathname !== nextLocation.pathname,
  );
  const leaving = useRef(false);
  useEffect(() => {
    if (blocker.state === "unblocked") leaving.current = false;
    if (!dirty && blocker.state === "blocked" && !leaving.current) {
      leaving.current = true;
      blocker.proceed();
    }
  }, [dirty, blocker]);
  useEffect(() => {
    if (!dirty) return;
    const protect = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", protect);
    return () => window.removeEventListener("beforeunload", protect);
  }, [dirty]);
  function stay() {
    if (blocker.state === "blocked" && !leaving.current) blocker.reset();
  }
  function leave() {
    if (blocker.state !== "blocked" || leaving.current) return;
    leaving.current = true;
    dispatch(clearSkillFileDrafts());
    blocker.proceed();
  }
  return (
    <Dialog
      open={blocker.state === "blocked"}
      onOpenChange={(open) => {
        if (!open) stay();
      }}
    >
      <DialogContent className="custom-skill-dialog custom-skill-dialog--compact">
        <div className="custom-skill-dialog-header">
          <DialogTitle>Discard unsaved changes?</DialogTitle>
          <DialogDescription>
            Unsaved skill edits and file deletions will be lost.
          </DialogDescription>
        </div>
        <div className="custom-skills-footer">
          <button
            type="button"
            className="custom-skill-button custom-skill-button--secondary"
            onClick={stay}
          >
            Stay
          </button>
          <button
            type="button"
            className="custom-skill-button custom-skill-button--danger"
            onClick={leave}
          >
            Discard and leave
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
