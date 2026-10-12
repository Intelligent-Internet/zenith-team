import { getBoardlyDesktopBridge } from "@/app/desktop/desktop-environment.service";

export function prepareHuddleWindow(): Window | null {
  if (getBoardlyDesktopBridge()?.openHuddleWindow) return null;
  const popup = window.open(
    "",
    "zenith-huddle",
    "popup,width=1120,height=780,resizable=yes",
  );
  if (popup) popup.focus();
  return popup;
}

export function openHuddleWindow(
  huddleId: string,
  popup?: Window | null,
): Promise<boolean> {
  const desktop = getBoardlyDesktopBridge();
  if (desktop?.openHuddleWindow) {
    return desktop.openHuddleWindow(huddleId).then((result) => result.ok);
  }
  const url = `${window.location.origin}/huddles/${encodeURIComponent(huddleId)}`;
  const target = popup && !popup.closed ? popup : prepareHuddleWindow();
  if (!target) return Promise.resolve(false);
  target.location.href = url;
  target.focus();
  return Promise.resolve(true);
}

// openHuddleStartWindow opens the lobby for a new call in a conversation; the
// Huddle is created only when the person presses Start there.
export function openHuddleStartWindow(
  conversationId: string,
  popup?: Window | null,
): Promise<boolean> {
  const desktop = getBoardlyDesktopBridge();
  if (desktop?.openHuddleStartWindow) {
    return desktop
      .openHuddleStartWindow(conversationId)
      .then((result) => result.ok);
  }
  const url = `${window.location.origin}/huddles/start/${encodeURIComponent(conversationId)}`;
  const target = popup && !popup.closed ? popup : prepareHuddleWindow();
  if (!target) return Promise.resolve(false);
  target.location.href = url;
  target.focus();
  return Promise.resolve(true);
}
