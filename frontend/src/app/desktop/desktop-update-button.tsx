import type { DesktopUpdateState } from "../../../electron/shared/desktop-bridge.types";
import { useEffect, useState } from "react";

import {
  downloadDesktopUpdate,
  getDesktopUpdateState,
  restartDesktopUpdate,
  subscribeToDesktopUpdateState,
  WEB_DESKTOP_UPDATE_STATE_FALLBACK,
} from "@/shared/desktop/desktop-environment.service";
import { Icon } from "@/shared/ui/icon";
import { Tooltip } from "@/shared/ui/tooltip";

import "./desktop-update-button.css";

export function DesktopUpdateButton() {
  const [updateState, setUpdateState] = useState<DesktopUpdateState>(
    WEB_DESKTOP_UPDATE_STATE_FALLBACK,
  );

  useEffect(() => {
    let cancelled = false;
    void getDesktopUpdateState().then((state) => {
      if (!cancelled) {
        setUpdateState(state);
      }
    });
    const unsubscribe = subscribeToDesktopUpdateState(setUpdateState);

    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, []);

  const isReadyToRestart =
    updateState.status === "downloaded" ||
    updateState.status === "restart-required";
  const isDownloading = updateState.status === "downloading";
  const isAvailable = updateState.status === "available";

  if (!isAvailable && !isDownloading && !isReadyToRestart) {
    return null;
  }

  const version = updateState.availableVersion
    ? ` to v${updateState.availableVersion}`
    : "";
  const progress =
    updateState.downloadPercent === null
      ? ""
      : ` (${updateState.downloadPercent}%)`;
  const label = isReadyToRestart
    ? `Restart Zenith Team to install update${version}`
    : isDownloading
      ? `Downloading Zenith Team update${version}${progress}`
      : `Download Zenith Team update${version}`;

  return (
    <Tooltip label={label}>
      <button
        aria-busy={isDownloading || undefined}
        aria-label={label}
        className="desktop-update-button"
        disabled={isDownloading}
        type="button"
        onClick={() =>
          void (isReadyToRestart
            ? restartDesktopUpdate()
            : downloadDesktopUpdate())
        }
      >
        <Icon
          aria-hidden="true"
          className={
            isDownloading
              ? "animate-spin motion-reduce:animate-none"
              : undefined
          }
          name={isReadyToRestart || isDownloading ? "refresh" : "download"}
          size={18}
        />
      </button>
    </Tooltip>
  );
}
