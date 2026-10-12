import { useEffect, useState } from "react";

import {
  getDesktopDaemonStatus,
  startDesktopDaemon,
  stopDesktopDaemon,
  subscribeToDesktopDaemonStatus,
  type DesktopDaemonStatus,
} from "@/app/desktop/desktop-environment.service";
import { isComputerHandoverPending } from "@/features/settings/model/runtime-management";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { Monitor } from "@/shared/ui/icons";

import "@/features/settings/styles/settings.css";
import "@/features/settings/styles/computer-use-dialog.css";

type HandoverAnswer = "start" | "stop";

// Asks the person signed in now before the desktop's agent service starts
// for them on a computer somebody else used last. "Use this computer" is the
// agent service's Start; "Not now" (or closing the dialog) is its Stop, which
// leaves it stopped until they select this computer again or start it in Settings.
export function ComputerHandoverDialog({
  requested,
  onClose,
}: {
  requested: boolean;
  onClose: () => void;
}) {
  const [status, setStatus] = useState<DesktopDaemonStatus | null>(null);

  useEffect(() => {
    let cancelled = false;
    let pushed = false;
    void getDesktopDaemonStatus().then((next) => {
      if (!cancelled && !pushed) setStatus(next);
    });
    const unsubscribe = subscribeToDesktopDaemonStatus((next) => {
      pushed = true;
      setStatus(next);
    });
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, []);

  return (
    <ComputerUseDialog
      open={requested || (status !== null && isComputerHandoverPending(status))}
      onClose={() => {
        setStatus(null);
        onClose();
      }}
    />
  );
}

function ComputerUseDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const [answer, setAnswer] = useState<HandoverAnswer | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) setError(null);
  }, [open]);

  const respond = async (next: HandoverAnswer) => {
    if (answer !== null) return;
    setAnswer(next);
    setError(null);
    const result =
      next === "start" ? await startDesktopDaemon() : await stopDesktopDaemon();
    if (!result.ok) {
      setError(result.reason);
    } else {
      onClose();
    }
    setAnswer(null);
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(open) => {
        if (!open) void respond("stop");
      }}
    >
      <DialogContent
        className="computer-use-dialog"
        overlayClassName="computer-use-dialog__overlay"
        showCloseButton={false}
      >
        <div className="computer-use-dialog__body">
          <DialogHeader className="computer-use-dialog__header">
            <span className="computer-use-dialog__icon" aria-hidden="true">
              <Monitor size={24} />
            </span>
            <DialogTitle>Let your agents work here</DialogTitle>
            <DialogDescription>
              Connect this computer to run your agents with the providers you’ve
              installed. You can manage this connection in Settings.
            </DialogDescription>
          </DialogHeader>

          {error ? (
            <p className="profile-delete-dialog__error" role="alert">
              {error}
            </p>
          ) : null}
        </div>
        <DialogFooter className="computer-use-dialog__footer">
          <button
            className="computer-use-dialog__button computer-use-dialog__button--cancel"
            disabled={answer !== null}
            type="button"
            onClick={() => void respond("stop")}
          >
            Not now
          </button>
          <button
            className="computer-use-dialog__button computer-use-dialog__button--confirm"
            disabled={answer !== null}
            type="button"
            onClick={() => void respond("start")}
          >
            {answer === "start" ? "Starting…" : "Use this computer"}
          </button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
