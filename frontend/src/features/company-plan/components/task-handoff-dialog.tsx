import { useState } from "react";

import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import type { TaskDto, TaskUpdateRequest } from "@/shared/api/types";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { Icon } from "@/shared/ui/icon";

// The dialog opens from every screen that shows a Task, so it brings the
// dialog shell it is drawn with.
import "@/features/settings/styles/settings.css";
import "@/features/company-plan/styles/task-handoff.css";

type TaskHandoffDialogProps = {
  onCancel: () => void;
  onConfirm: () => Promise<void>;
  open: boolean;
  // Whether work is in flight right now, which is what confirming cancels.
  runLive: boolean;
  request: TaskUpdateRequest;
  task: TaskDto;
};

// A Task that has run carries files on the computer that ran it. Handing it
// to somebody else, or to another agent, starts the work over from the
// project's main branch, and nothing moves those files (ADR 0014). This
// dialog says so before the handoff, in the words of whoever is losing it.
export function TaskHandoffDialog({
  onCancel,
  onConfirm,
  open,
  runLive,
  request,
  task,
}: TaskHandoffDialogProps) {
  const [confirming, setConfirming] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // The dialog has no trigger of its own, so on closing Radix would leave
  // focus on the page. It goes back to where it was when the dialog opened,
  // such as the picker that asked for the handoff.
  const [returnFocusTo] = useState(() => document.activeElement);
  const viewerUserId = useSessionSnapshot().userId;
  // Whose computer the work stays on: the viewer's own, or the member's
  // who is losing the Task.
  const whose =
    task.assignee.id === viewerUserId ? "your" : `${task.assignee.name}'s`;
  const movingToAnotherMember =
    request.assignee !== undefined && request.assignee.id !== task.assignee.id;

  return (
    <Dialog open={open} onOpenChange={(next) => (next ? null : onCancel())}>
      <DialogContent
        className="profile-delete-dialog runtime-dialog"
        overlayClassName="profile-delete-dialog__overlay"
        showCloseButton={false}
        onCloseAutoFocus={(event) => {
          if (
            returnFocusTo instanceof HTMLElement &&
            returnFocusTo.isConnected
          ) {
            event.preventDefault();
            returnFocusTo.focus();
          }
        }}
      >
        <div className="profile-delete-dialog__surface">
          <DialogHeader className="profile-delete-dialog__header">
            <span className="profile-delete-dialog__icon runtime-dialog__icon">
              <Icon name="alert" />
            </span>
            <DialogTitle>
              {movingToAnotherMember
                ? "Hand this task over?"
                : "Start it over?"}
            </DialogTitle>
            <DialogDescription>
              {runLive
                ? `A run is in progress on ${whose} computer. It will be cancelled, and the work starts fresh from the project's main branch.`
                : `This task has already run on ${whose} computer. The work starts fresh from the project's main branch.`}
            </DialogDescription>
          </DialogHeader>

          <p className="m-[16px_0_0] text-workspace-muted text-workspace-caption leading-workspace-caption">
            Files that were not merged stay on {whose} computer. Nothing moves
            them, and nobody else can reach them. Messages already in the task
            chat are kept: the next run reads them.
          </p>

          {error ? (
            <p className="profile-delete-dialog__error" role="alert">
              {error}
            </p>
          ) : null}

          <DialogFooter className="profile-delete-dialog__footer">
            <button
              className="profile-delete-dialog__button profile-delete-dialog__button--cancel"
              disabled={confirming}
              onClick={onCancel}
              type="button"
            >
              Keep it as it is
            </button>
            <button
              className="profile-delete-dialog__button runtime-dialog__submit"
              disabled={confirming}
              onClick={() => {
                setConfirming(true);
                setError(null);
                void onConfirm()
                  .catch(() => setError("The task could not be handed over."))
                  .finally(() => setConfirming(false));
              }}
              type="button"
            >
              {confirming
                ? "Handing over…"
                : runLive
                  ? "Cancel the run and hand it over"
                  : "Hand it over"}
            </button>
          </DialogFooter>
        </div>
      </DialogContent>
    </Dialog>
  );
}
