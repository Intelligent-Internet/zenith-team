import type { DesktopUpdateState } from "../../../electron/shared/desktop-bridge.types";
import { useEffect, useState } from "react";

import {
  checkDesktopUpdates,
  downloadDesktopUpdate,
  getDesktopUpdateState,
  restartDesktopUpdate,
  setDesktopAutomaticUpdates,
  subscribeToDesktopUpdateDialogRequests,
  subscribeToDesktopUpdateState,
  WEB_DESKTOP_UPDATE_STATE_FALLBACK,
} from "@/shared/desktop/desktop-environment.service";
import { Button } from "@/shared/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { Icon } from "@/shared/ui/icon";
import { ProgressBar } from "@/shared/ui/progress-bar";

import "./desktop-update-dialog.css";

export function DesktopUpdateDialog() {
  const [open, setOpen] = useState(false);
  const [updateState, setUpdateState] = useState<DesktopUpdateState>(
    WEB_DESKTOP_UPDATE_STATE_FALLBACK,
  );
  const [actionError, setActionError] = useState<string | null>(null);
  const [isActing, setIsActing] = useState(false);
  const [isSavingPreference, setIsSavingPreference] = useState(false);

  useEffect(() => {
    let cancelled = false;

    const receiveState = (state: DesktopUpdateState) => {
      if (cancelled) return;
      setUpdateState(state);

      if (isActiveDownload(state) || isReadyToInstall(state)) {
        setOpen(true);
      }
    };

    void getDesktopUpdateState().then(receiveState);
    const unsubscribeFromState = subscribeToDesktopUpdateState(receiveState);
    const unsubscribeFromRequests = subscribeToDesktopUpdateDialogRequests(
      () => {
        setActionError(null);
        setOpen(true);
        void getDesktopUpdateState().then(receiveState);
      },
    );

    return () => {
      cancelled = true;
      unsubscribeFromRequests();
      unsubscribeFromState();
    };
  }, []);

  const busy =
    isActiveDownload(updateState) || updateState.status === "checking";
  const presentation = getUpdatePresentation(updateState);

  const runAction = async () => {
    setActionError(null);
    setIsActing(true);

    try {
      const result = isReadyToInstall(updateState)
        ? await restartDesktopUpdate()
        : updateState.status === "available"
          ? await downloadDesktopUpdate()
          : await checkDesktopUpdates();

      if (!result.ok) {
        setActionError(result.reason);
      }
    } catch {
      setActionError("The update action could not be completed. Try again.");
    } finally {
      setIsActing(false);
    }
  };

  const saveAutomaticUpdatePreference = async (enabled: boolean) => {
    setIsSavingPreference(true);
    setActionError(null);

    try {
      const result = await setDesktopAutomaticUpdates(enabled);
      if (result.state) {
        setUpdateState(result.state);
      }
      if (!result.ok) {
        setActionError(result.reason);
      }
    } catch {
      setActionError("The automatic update preference could not be saved.");
    } finally {
      setIsSavingPreference(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        if (nextOpen || !busy) {
          setOpen(nextOpen);
        }
      }}
    >
      <DialogContent
        className="desktop-update-dialog gap-0 overflow-hidden p-0 shadow-2xl"
        onEscapeKeyDown={(event) => {
          if (busy) event.preventDefault();
        }}
        onPointerDownOutside={(event) => {
          if (busy) event.preventDefault();
        }}
        showCloseButton={!busy}
      >
        <div className="p-6 pb-5">
          <DialogHeader className="pr-8 text-left">
            <div
              className="mb-2 grid size-10 place-items-center rounded-full bg-brand-primary/10 text-brand-primary"
              aria-hidden="true"
            >
              <Icon
                className={
                  updateState.status === "checking"
                    ? "size-5 animate-spin motion-reduce:animate-none"
                    : "size-5"
                }
                name={presentation.icon}
              />
            </div>
            <DialogTitle>{presentation.title}</DialogTitle>
            <DialogDescription className="text-secondary leading-5">
              {presentation.description}
            </DialogDescription>
          </DialogHeader>

          {isActiveDownload(updateState) || isReadyToInstall(updateState) ? (
            <div className="mt-6" aria-live="polite">
              <div className="mb-2 flex items-center justify-between gap-4 text-sm">
                <span className="font-medium text-primary">
                  {isReadyToInstall(updateState)
                    ? "Download complete"
                    : updateState.downloadPercent === 99
                      ? "Preparing update…"
                      : "Downloading…"}
                </span>
                <span className="text-secondary tabular-nums">
                  {updateState.downloadPercent === null
                    ? "Calculating…"
                    : `${updateState.downloadPercent}%`}
                </span>
              </div>
              <ProgressBar
                className="h-2.5 bg-brand-primary/10"
                indicatorClassName="duration-300 ease-[cubic-bezier(0.2,0,0,1)] motion-reduce:transition-none"
                value={updateState.downloadPercent ?? 0}
              />
              <p className="text-secondary mt-3 text-xs leading-5">
                {isReadyToInstall(updateState)
                  ? "Zenith Team will reopen after the update is installed."
                  : "Keep Zenith Team open while the update downloads."}
              </p>
            </div>
          ) : null}

          {updateState.status === "available" ? (
            <label className="text-secondary mt-5 flex items-center gap-2.5 text-sm">
              <input
                checked={updateState.automaticUpdatesEnabled}
                className="size-4 accent-brand-primary"
                disabled={isSavingPreference}
                type="checkbox"
                onChange={(event) =>
                  void saveAutomaticUpdatePreference(event.target.checked)
                }
              />
              Check for updates automatically
            </label>
          ) : null}

          {actionError || updateState.errorMessage ? (
            <p className="mt-4 text-sm leading-5 text-red-2" role="alert">
              {actionError ?? updateState.errorMessage}
            </p>
          ) : null}
        </div>

        {!busy ? (
          <DialogFooter className="desktop-update-dialog__footer px-6 py-4 sm:justify-between">
            <Button
              disabled={isActing}
              type="button"
              variant="ghost"
              onClick={() => setOpen(false)}
            >
              {updateState.status === "available" ||
              isReadyToInstall(updateState)
                ? "Later"
                : "Close"}
            </Button>
            {presentation.actionLabel ? (
              <Button
                disabled={isActing}
                type="button"
                variant="default"
                onClick={() => void runAction()}
              >
                {isActing ? presentation.actingLabel : presentation.actionLabel}
              </Button>
            ) : null}
          </DialogFooter>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function isActiveDownload(state: DesktopUpdateState) {
  return state.status === "downloading";
}

function isReadyToInstall(state: DesktopUpdateState) {
  return state.status === "downloaded" || state.status === "restart-required";
}

function getUpdatePresentation(state: DesktopUpdateState) {
  const version = state.availableVersion ? ` v${state.availableVersion}` : "";

  if (isReadyToInstall(state)) {
    return {
      actionLabel: "Install and restart",
      actingLabel: "Restarting…",
      description: `Zenith Team${version} is downloaded and ready to install.`,
      icon: "tick-circle",
      title: "Update ready",
    };
  }

  switch (state.status) {
    case "available":
      return {
        actionLabel: "Download update",
        actingLabel: "Starting download…",
        description: `Zenith Team${version} is available. Download it now to install the update.`,
        icon: "download",
        title: "A new update is available",
      };
    case "checking":
      return {
        actionLabel: null,
        actingLabel: "Checking…",
        description: "Looking for the latest Zenith Team release.",
        icon: "refresh",
        title: "Checking for updates",
      };
    case "downloading":
      return {
        actionLabel: null,
        actingLabel: "Downloading…",
        description: `Zenith Team${version} is downloading securely.`,
        icon: "download",
        title: "Downloading update",
      };
    case "failed":
      return {
        actionLabel: "Try again",
        actingLabel: "Checking…",
        description:
          "Zenith Team could not complete the update check or download.",
        icon: "danger",
        title: "Update failed",
      };
    case "up-to-date":
      return {
        actionLabel: null,
        actingLabel: "Checking…",
        description: `Zenith Team v${state.installedVersion ?? "current"} is the latest available version.`,
        icon: "tick-circle",
        title: "Zenith Team is up to date",
      };
    default:
      return {
        actionLabel: "Check again",
        actingLabel: "Checking…",
        description: "Check whether a newer Zenith Team release is available.",
        icon: "refresh",
        title: "Zenith Team update",
      };
  }
}
