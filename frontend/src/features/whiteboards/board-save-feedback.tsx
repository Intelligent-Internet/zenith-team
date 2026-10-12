import { useCallback, useEffect, useRef, useState } from "react";
import { useWorkspaceNavigationBlocker } from "@/app/shell/workspace-navigation-guard";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";

export function useBoardSaveFeedback({
  editable,
  pending,
  hasPendingChanges,
  flush,
}: {
  editable: boolean;
  pending: boolean;
  hasPendingChanges: () => boolean;
  flush: () => Promise<void>;
}) {
  const [feedback, setFeedback] = useState<
    "saving" | "saved" | "failed" | null
  >(null);
  const [leaveError, setLeaveError] = useState("");
  const activeSave = useRef<Promise<void> | null>(null);
  const savedTimeout = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined,
  );
  const mounted = useRef(true);
  const leaveAttempt = useRef(0);
  const blocker = useWorkspaceNavigationBlocker(
    ({ currentLocation, nextLocation }) => {
      const leavingBoard =
        currentLocation.pathname !== nextLocation.pathname ||
        new URLSearchParams(currentLocation.search).get("companyId") !==
          new URLSearchParams(nextLocation.search).get("companyId");
      if (!leavingBoard) return false;
      if (document.activeElement instanceof HTMLElement)
        document.activeElement.blur();
      return hasPendingChanges();
    },
  );

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      leaveAttempt.current++;
      clearTimeout(savedTimeout.current);
    };
  }, []);

  const save = useCallback(() => {
    if (activeSave.current) return activeSave.current;
    clearTimeout(savedTimeout.current);
    setFeedback("saving");
    const request = flush()
      .then(() => {
        if (!mounted.current) return;
        setFeedback("saved");
        savedTimeout.current = setTimeout(() => setFeedback(null), 2000);
      })
      .catch((error: unknown) => {
        if (mounted.current) setFeedback("failed");
        throw error;
      })
      .finally(() => {
        if (activeSave.current === request) activeSave.current = null;
      });
    activeSave.current = request;
    return request;
  }, [flush]);

  useEffect(() => {
    if (!pending && feedback === "failed") setFeedback(null);
    if (pending && feedback === "saved") {
      clearTimeout(savedTimeout.current);
      setFeedback(null);
    }
  }, [pending, feedback]);

  useEffect(() => {
    const keydown = (event: KeyboardEvent) => {
      if (
        !(event.metaKey || event.ctrlKey) ||
        event.altKey ||
        event.shiftKey ||
        event.key.toLowerCase() !== "s"
      )
        return;
      event.preventDefault();
      if (editable && !event.repeat) {
        if (
          event.isComposing &&
          document.activeElement instanceof HTMLElement
        ) {
          const field = document.activeElement;
          field.blur();
          field.focus({ preventScroll: true });
        }
        void save().catch(() => {});
      }
    };
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if (document.activeElement instanceof HTMLElement)
        document.activeElement.blur();
      if (!hasPendingChanges()) return;
      event.preventDefault();
      event.returnValue = "";
      void save().catch(() => {});
    };
    window.addEventListener("keydown", keydown, true);
    window.addEventListener("beforeunload", beforeUnload);
    return () => {
      window.removeEventListener("keydown", keydown, true);
      window.removeEventListener("beforeunload", beforeUnload);
    };
  }, [editable, hasPendingChanges, save]);

  const saveBeforeLeaving = useCallback(() => {
    if (blocker.state !== "blocked") return;
    const attempt = ++leaveAttempt.current;
    setLeaveError("");
    void save()
      .then(() => {
        if (
          mounted.current &&
          leaveAttempt.current === attempt &&
          !hasPendingChanges()
        )
          blocker.proceed();
      })
      .catch((error: unknown) => {
        if (mounted.current && leaveAttempt.current === attempt)
          setLeaveError(getBoardlyQueryErrorMessage(error));
      });
  }, [blocker, hasPendingChanges, save]);

  useEffect(() => {
    saveBeforeLeaving();
    return () => {
      leaveAttempt.current++;
    };
  }, [saveBeforeLeaving]);

  useEffect(() => {
    if (blocker.state === "blocked" && !pending && leaveError)
      blocker.proceed();
  }, [blocker, pending, leaveError]);

  const stay = () => {
    leaveAttempt.current++;
    if (blocker.state === "blocked") blocker.reset();
  };

  const leaveDialog = (
    <Dialog
      open={blocker.state === "blocked"}
      onOpenChange={(open) => {
        if (!open) stay();
      }}
    >
      <DialogContent
        className="wb-dialog wb-leave-dialog"
        overlayClassName="wb-details-overlay"
        onInteractOutside={(event) => event.preventDefault()}
      >
        <DialogTitle>
          {leaveError ? "Changes haven't saved yet" : "Saving before leaving…"}
        </DialogTitle>
        <DialogDescription>
          {leaveError ||
            "Your changes are still saving. We'll open the next page when they're saved."}
        </DialogDescription>
        <footer className="wb-details-footer">
          <button type="button" onClick={stay}>
            Stay on board
          </button>
          {leaveError && (
            <button
              type="button"
              className="wb-primary"
              onClick={saveBeforeLeaving}
            >
              Retry save
            </button>
          )}
        </footer>
      </DialogContent>
    </Dialog>
  );
  return { feedback, leaveDialog };
}
