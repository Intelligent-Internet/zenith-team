import type { DesktopContextMenuPayload } from "../../../electron/shared/desktop-bridge.types";
import { useEffect } from "react";
import {
  shouldUseDesktopHashRouter,
  showDesktopContextMenu,
} from "@/shared/desktop/desktop-environment.service";

const DESKTOP_CONTEXT_SELECTOR = "[data-zenith-team-desktop-context-kind]";

export function DesktopNativeContextMenuController() {
  useEffect(() => {
    if (!shouldUseDesktopHashRouter()) {
      return undefined;
    }

    function handleContextMenu(event: MouseEvent) {
      const target = event.target;

      if (!(target instanceof Element)) {
        return;
      }

      const contextElement = target.closest(DESKTOP_CONTEXT_SELECTOR);
      const payload = createDesktopContextMenuPayload(contextElement, event);

      if (!payload) {
        return;
      }

      event.preventDefault();
      void showDesktopContextMenu(payload);
    }

    document.addEventListener("contextmenu", handleContextMenu);

    return () => {
      document.removeEventListener("contextmenu", handleContextMenu);
    };
  }, []);

  return null;
}

export function createDesktopContextMenuPayload(
  element: Element | null,
  event: Pick<MouseEvent, "clientX" | "clientY">,
): DesktopContextMenuPayload | null {
  if (!(element instanceof HTMLElement)) {
    return null;
  }

  return createDesktopContextMenuPayloadFromDataset(element.dataset, event);
}

export function createDesktopContextMenuPayloadFromDataset(
  dataset: Partial<Record<DesktopContextDatasetKey, string>>,
  event: Pick<MouseEvent, "clientX" | "clientY">,
): DesktopContextMenuPayload | null {
  const kind = dataset.zenithTeamDesktopContextKind;
  if (!isDesktopContextKind(kind)) {
    return null;
  }

  const label = dataset.zenithTeamDesktopContextLabel?.trim();
  if (!label) {
    return null;
  }

  return {
    copyText: dataset.zenithTeamDesktopContextCopy,
    detailText: dataset.zenithTeamDesktopContextDetail,
    kind,
    label,
    routePath: dataset.zenithTeamDesktopContextRoute,
    shareUrl: dataset.zenithTeamDesktopContextShareUrl,
    x: event.clientX,
    y: event.clientY,
  };
}

type DesktopContextDatasetKey =
  | "zenithTeamDesktopContextCopy"
  | "zenithTeamDesktopContextDetail"
  | "zenithTeamDesktopContextKind"
  | "zenithTeamDesktopContextLabel"
  | "zenithTeamDesktopContextRoute"
  | "zenithTeamDesktopContextShareUrl";

function isDesktopContextKind(
  value: string | undefined,
): value is DesktopContextMenuPayload["kind"] {
  return (
    value === "agent" ||
    value === "approval" ||
    value === "message" ||
    value === "resource" ||
    value === "task"
  );
}
